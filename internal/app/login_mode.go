package app

// How the portal offers login, on two independent axes.
//
// `login_mode` decides what the login PAGE shows. `sso_only` decides what the login ENDPOINT
// accepts. They are deliberately separate, and the separation is the whole point: a mode is a
// rendering hint, and anyone can POST to /api/login whatever the page happened to draw. A "force
// SSO" that only rearranges the page is not a control, it is decoration. The Passwall panel reaches
// the same conclusion from the same place — its four-way login_mode is presentation, and a separate
// disallow_user_local_login is documented as applying "regardless of LoginMode".
//
// Both axes fail safe when no identity provider is enabled:
//
//   - a mode that promises SSO degrades to local_only, because a portal cannot redirect to an IdP
//     that does not exist and an admin could not undo it through the product;
//   - sso_only goes inert, because refusing local passwords while offering no alternative locks out
//     every non-admin at once.
//
// And sso_only never applies to an admin. An IdP that breaks must not cost an operator the ability
// to sign in and fix it. That exemption is the break-glass path, in place of a secret bypass URL
// that has to be remembered, documented and kept out of the wrong hands.

const (
	setLoginMode = "login_mode"
	setSSOOnly   = "sso_only"
)

// The four presentation modes an admin can choose.
const (
	loginDual        = "dual"         // password form and SSO buttons together (the default)
	loginSSOFirst    = "sso_first"    // SSO up front, password behind a deliberate click
	loginSSORedirect = "sso_redirect" // no password form; go straight to the provider
	loginLocalOnly   = "local_only"   // password only, even if a provider is configured
)

func validLoginMode(m string) bool {
	switch m {
	case loginDual, loginSSOFirst, loginSSORedirect, loginLocalOnly:
		return true
	}
	return false
}

// ssoAvailable reports whether there is an enabled provider to send anyone to. Every safety
// degradation below hangs off this one question.
func (s *Server) ssoAvailable() bool { return len(s.st.EnabledSSOProviders()) > 0 }

// loginMode resolves the effective presentation mode. An unrecognized stored value falls back to
// the default rather than to something stricter: a typo in a settings row must not lock a portal
// into a mode nobody chose.
func (s *Server) loginMode() string {
	mode := s.st.GetSetting(setLoginMode, loginDual)
	if !validLoginMode(mode) {
		mode = loginDual
	}
	if mode != loginLocalOnly && !s.ssoAvailable() {
		return loginLocalOnly
	}
	return mode
}

// ssoOnlyActive reports whether local password login is refused for ordinary accounts.
//
// It asks whether the login page actually OFFERS SSO, not merely whether a provider is enabled.
// Those differ under local_only, and the difference was a lockout: the page hid every provider
// button while the endpoint refused every password, so a non-admin got a form that always returned
// 403 and no alternative anywhere in the product. Refusing passwords only means something when
// there is a visible way in that is not a password.
func (s *Server) ssoOnlyActive() bool {
	if s.st.GetSetting(setSSOOnly, "") != "1" {
		return false
	}
	_, _, sso := s.loginOffers()
	return sso
}

// localLoginRefused decides one account's fate under the hard policy. Admins are always allowed
// through — see the break-glass note above.
func (s *Server) localLoginRefused(u *User) bool {
	if u == nil || !s.ssoOnlyActive() {
		return false
	}
	// Derived from the role registry, not from role == "admin", so adding a privileged role cannot
	// silently strip its holders of the break-glass path.
	return !can(u.EffRole(), PermManage)
}

// loginOffers resolves what the login page may render, so the page never re-derives these rules.
//
// Under sso_redirect the local form is hidden but SSO stays exposed — that is the redirect target,
// and hiding it would leave the page with nothing at all.
func (s *Server) loginOffers() (mode string, local, sso bool) {
	mode = s.loginMode()
	return mode, mode != loginSSORedirect, mode != loginLocalOnly && s.ssoAvailable()
}
