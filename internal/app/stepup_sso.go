package app

import (
	"net/http"
	"strings"
	"time"
)

// Re-proving who you are, when your password is not how you get in.
//
// The step-up in totp.go accepts a password or a live 2FA code. That covers a local account and
// says nothing about the rest, which leaves two holes:
//
//   - Somebody who signs in through an IdP every day is asked for a password they never type.
//   - Under force-SSO the portal has stopped ACCEPTING that password at /api/login, yet the dialog
//     still demands it — so re-proving your identity is weaker than signing in. That is backwards.
//
// The third channel is a full round-trip to the identity provider, and it only counts if the IdP
// actually re-authenticates: SAML ForceAuthn, OIDC prompt=login. Without that the IdP answers from
// its own session and bounces back instantly, which proves nothing — the case this whole mechanism
// exists for is an attacker holding the browser, and the IdP session lives in that same browser.
//
// The proof is a single-use HttpOnly cookie, never a token in the URL and never a value JavaScript
// handles. The page just retries the request it was blocked on and the cookie rides along. A URL
// would be written into every reverse-proxy log, kept in history, and leaked in the Referer of any
// subresource — the same reasoning that put the existing proof in a header rather than a query.

const (
	// stepUpCookie carries the proof of a completed SSO step-up back to the blocked request.
	stepUpCookie = "rp_stepup"
	// Long enough to finish the action that was interrupted, short enough that a browser left
	// unattended afterwards is not a standing authorisation.
	stepUpProofTTL = 5 * time.Minute
)

// stepUpPolicy is what a given account may re-prove itself with, and is what the dialog renders
// from. It is computed on the server because the answer depends on the login mode, which is a
// deployment policy the page has no business deciding.
type stepUpPolicy struct {
	Password  bool        `json:"password"` // password or a live 2FA/recovery code
	SSO       bool        `json:"sso"`
	Providers []ssoChoice `json:"providers,omitempty"`
	Reason    string      `json:"reason,omitempty"` // why the password channel is closed, if it is
}

type ssoChoice struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// The admin-configured icon, so this button looks like the same provider the login page
	// offers. A second, hard-coded glyph would read as a different thing entirely.
	Icon string `json:"icon,omitempty"`
}

// stepUpPolicyFor decides the channels.
//
// Force-SSO closes the password channel because that mode's whole meaning is "a password is not a
// valid way into this portal" — leaving a password door open here would make re-proving identity
// weaker than the front door it guards.
//
// It stays open for an admin, matching the exemption sso_only already carries: an IdP that breaks
// must not cost an operator the ability to sign in and fix it. Extending an existing exemption
// beats inventing a second, differently-shaped one.
func (s *Server) stepUpPolicyFor(user string) stepUpPolicy {
	u := s.st.GetUser(user)
	if u == nil {
		return stepUpPolicy{}
	}
	pol := stepUpPolicy{Password: u.TOTPEnabled || (!u.IsFederated() && u.PasswordHash != "")}

	for _, p := range s.st.EnabledSSOProviders() {
		pol.Providers = append(pol.Providers, ssoChoice{Slug: p.Slug, Kind: p.Kind, Name: p.Name, Icon: p.Icon})
	}
	pol.SSO = len(pol.Providers) > 0

	if pol.SSO && s.loginMode() == loginSSORedirect && !s.isAdmin(user) {
		pol.Password = false
		pol.Reason = "sso_required"
	}
	return pol
}

// apiStepUpPolicy tells the dialog what to offer. Nothing here is a secret: it is this caller's own
// account and the same provider list the login page already publishes.
func (s *Server) apiStepUpPolicy(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, s.stepUpPolicyFor(user))
}

// stepUpIntent reads a start request's purpose. It returns ok=false only after answering the
// request itself.
//
// A step-up start is the one SSO entry point that REQUIRES an existing session: it is not a way in,
// it is a way to re-prove the way you already came in.
func (s *Server) stepUpIntent(w http.ResponseWriter, r *http.Request) (purpose, forUser string, ok bool) {
	if r.URL.Query().Get("purpose") != authPurposeStepUp {
		return "", "", true
	}
	user := s.currentActiveUser(r)
	if user == "" {
		s.ssoFail(w, r, "not_signed_in")
		return "", "", false
	}
	if !s.stepUpPolicyFor(user).SSO {
		s.ssoFail(w, r, "step_up_unavailable")
		return "", "", false
	}
	return authPurposeStepUp, user, true
}

// completeSSOStepUp finishes a re-authentication round-trip. It issues NO session: the caller is
// already signed in, and minting one here would turn a re-prove into a login that skipped every
// check completeSSOLogin makes.
func (s *Server) completeSSOStepUp(w http.ResponseWriter, r *http.Request, p SSOProvider, id ssoIdentity, req AuthRequest) {
	// The assertion must resolve to the account that ASKED. Matching only — never provisioning:
	// a step-up that could create an account would be a sign-up path behind a dialog that says
	// "confirm your identity".
	username, err := s.matchExistingAccount(p, id)
	if err != nil || username == "" || !strings.EqualFold(username, req.Username) {
		s.recordAuth(r, AuditLoginFailed, "", req.Username, map[string]any{
			"reason": "step_up_mismatch", "provider": p.Kind, "slug": p.Slug})
		s.ssoFail(w, r, "step_up_mismatch")
		return
	}

	// The proof is a fresh single-use row of its own rather than the one just consumed: the flow
	// row's token was the SAML RelayState and the OIDC state, values that have travelled through
	// the IdP and a redirect chain. A proof must be a secret this browser and this server have
	// only ever shared with each other.
	token, terr := newAuthToken()
	if terr != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, ProviderID: p.ID, Kind: p.Kind, Username: username, Purpose: authPurposeProved,
	}, time.Now().Add(stepUpProofTTL)); err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	http.SetCookie(w, s.stepUpProofCookie(r, token, int(stepUpProofTTL.Seconds())))
	s.recordAuth(r, AuditStepUp, username, username, map[string]any{"via": p.Kind, "slug": p.Slug})
	http.Redirect(w, r, safeReturnPath(req.Target), http.StatusFound)
}

func (s *Server) stepUpProofCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: stepUpCookie, Value: value, Path: "/",
		HttpOnly: true, Secure: requestIsHTTPS(r, s.trustedNets),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
}

// stepUpCookieProof consumes a completed SSO step-up, if this browser is carrying one.
//
// Single use: the row is deleted by the read. Bound to a username, so a proof minted for one
// account cannot be spent on another even inside one browser.
func (s *Server) stepUpCookieProof(r *http.Request, user string) bool {
	c, err := r.Cookie(stepUpCookie)
	if err != nil || c.Value == "" {
		return false
	}
	req, ok := s.st.ConsumeAuthRequest(c.Value, time.Now())
	return ok && req.Purpose == authPurposeProved && strings.EqualFold(req.Username, user)
}
