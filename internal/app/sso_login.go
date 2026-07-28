package app

import (
	"log"
	"net/http"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/ssorules"
)

// The shared tail of every SSO login (ADR 0023). Both protocols hand a verified identity to
// completeSSOLogin, so account resolution, rule evaluation and session issuance exist exactly once
// and cannot drift between SAML and OIDC — or, later, between login and a SCIM sync.

// completeSSOLogin resolves the identity to a local account, applies the group rules, and issues a
// portal session. Every refusal produces the same generic outcome: unknown subject, not
// provisioned, inactive, expired and no-rule-match must be indistinguishable, or the login page
// becomes a user-enumeration oracle against the IdP.
func (s *Server) completeSSOLogin(w http.ResponseWriter, r *http.Request, p SSOProvider, id ssoIdentity, target string) {
	username, err := s.resolveSSOAccount(p, id)
	if err != nil {
		log.Printf("sso: %s/%s login refused for subject %q: %v", p.Kind, p.Slug, id.Subject, err)
		s.ssoFail(w, r, "not_provisioned")
		return
	}

	// Rules decide the role AND the OU. In this portal the OU carries report visibility, the run
	// allow-list and the daily quota (ADR 0022), so this is the actual permission decision.
	cur := Current(s, username)
	out := ssorules.Resolve(s.ssoRules(p.ID), ssorules.Facts{
		Groups: id.groups(p.AttrGroups),
	}, cur, ssorules.Defaults{
		Role: firstNonEmpty(p.DefaultRole, "user"), Group: p.DefaultGroup,
		AllowAdminRole: p.AllowAdminRole, PrivilegedRoles: privilegedRoles(),
	})
	if out.Deny {
		log.Printf("sso: %s/%s denied %q: %s", p.Kind, p.Slug, username, out.DenyReason)
		s.ssoFail(w, r, "not_provisioned")
		return
	}
	if out.AdminBlocked {
		log.Printf("sso: %s/%s refused an admin elevation for %q (allow_admin_role is off)", p.Kind, p.Slug, username)
	}
	if err := s.applyAssignment(username, out.Role, out.Group); err != nil {
		log.Printf("sso: %s/%s could not apply the assignment for %q: %v", p.Kind, p.Slug, username, err)
		s.ssoFail(w, r, "internal")
		return
	}

	u := s.st.GetUser(username)
	// Re-check the account gates AFTER assignment, so a rule that just deactivated or re-scoped
	// someone cannot be outrun by this very login.
	if u == nil || !u.Active || s.accountExpired(u) {
		s.ssoFail(w, r, "not_provisioned")
		return
	}
	if err := s.st.LinkIdentity(Identity{
		Provider: id.Provider, Issuer: id.Issuer, Subject: id.Subject,
		Username: username, ProviderSlug: p.Slug, Attrs: id.attrs(),
	}); err != nil {
		log.Printf("sso: %s/%s could not link identity for %q: %v", p.Kind, p.Slug, username, err)
	}
	s.st.TouchLastLogin(username)
	s.issueSession(w, r, *u, p)
	log.Printf("sso login %s via %s/%s", username, p.Kind, p.Slug)
	http.Redirect(w, r, safeReturnPath(target), http.StatusFound)
}

// resolveSSOAccount finds — or, when the provider allows it, creates — the local account behind an
// external identity. The order matters and is the whole of the linking policy.
func (s *Server) resolveSSOAccount(p SSOProvider, id ssoIdentity) (string, error) {
	// 1. An existing link. This is the only lookup that runs on every login, and it is keyed on
	// (provider, issuer, subject) — never on email.
	if u, ok := s.st.FindIdentity(id.Provider, id.Issuer, id.Subject); ok {
		return u, nil
	}
	// 2. Adoption: an account an admin pre-created (or SCIM will later create) carrying this IdP's
	// immutable object id. This is why external_id ships with SSO rather than with SCIM.
	if ext := id.claim(p.AttrExternalID); ext != "" {
		if u, ok := s.st.FindUserByExternalID(p.Slug, ext); ok {
			return u, nil
		}
	}
	// 3. Otherwise the account must be created — and only if this provider is allowed to.
	if p.Provisioning != "jit" {
		return "", errNotProvisioned
	}
	upn := firstNonEmpty(id.claim(p.AttrUPN), id.Subject)
	name, ok := sanitizeSSOUsername(upn)
	if !ok {
		return "", errUnusableUsername
	}
	// A collision with a LOCAL account is never auto-linked: that would let anyone who can make
	// their IdP assert a matching UPN take over a password account. It needs an admin.
	if existing := s.st.GetUser(name); existing != nil {
		return "", errUsernameTaken
	}
	if err := s.st.UpsertUser(User{Username: name, PasswordHash: "", Role: firstNonEmpty(p.DefaultRole, "user")}); err != nil {
		return "", err
	}
	s.st.SetUserProfile(name, id.claim(p.AttrDisplay), id.claim(p.AttrEmail))
	s.st.SetUserSource(name, "jit", p.Slug)
	if ext := id.claim(p.AttrExternalID); ext != "" {
		s.st.SetUserExternalID(name, p.Slug, ext)
	}
	if p.DefaultExpiryDays > 0 {
		s.st.SetUserExpiry(name, time.Now().In(s.panelLocation()).
			AddDate(0, 0, p.DefaultExpiryDays).Format("2006-01-02"))
	}
	return name, nil
}

// issueSession mints the portal session cookie for an SSO login, reusing the same signed-cookie
// path as password login so expiry and session_rev invalidation behave identically. A provider may
// shorten the lifetime: until SCIM exists an IdP-side disable is invisible to us, so a shorter
// session is the honest partial answer to deprovisioning.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u User, p SSOProvider) {
	maxAge := 7 * 24 * 3600
	if p.SessionHours > 0 {
		maxAge = p.SessionHours * 3600
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.signUser(u), Path: "/",
		HttpOnly: true, Secure: requestIsHTTPS(r, s.trustedNets),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// Current snapshots the account as the rule engine needs to see it.
func Current(s *Server, username string) ssorules.Current {
	u := s.st.GetUser(username)
	if u == nil {
		return ssorules.Current{}
	}
	return ssorules.Current{Exists: true, Role: u.EffRole(), Group: s.st.PrimaryGroupOf(username)}
}

// privilegedRoles is the set of roles that confer admin rights, derived from the role registry so
// adding a privileged role cannot silently become SSO-grantable.
func privilegedRoles() map[string]bool {
	out := map[string]bool{}
	for _, r := range roleRegistry {
		if r.Perms[PermManage] {
			out[r.Code] = true
		}
	}
	return out
}

// ssoRules loads the ordered rules that apply to a provider: its own, plus any global ones.
func (s *Server) ssoRules(providerID int64) []ssorules.Rule {
	rows, err := s.st.query(`SELECT id,COALESCE(ord,0),COALESCE(enabled,1),COALESCE(attr,''),COALESCE(value,''),
		COALESCE(target_role,''),COALESCE(target_group,0),COALESCE(keep_on_miss,0),COALESCE(ci,0),COALESCE(note,'')
		FROM sso_group_rules WHERE provider_id=? OR provider_id IS NULL ORDER BY ord, id`, providerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ssorules.Rule
	for rows.Next() {
		var r ssorules.Rule
		var enabled, keep, ci int
		if err := rows.Scan(&r.ID, &r.Ord, &enabled, &r.Attr, &r.Value,
			&r.TargetRole, &r.TargetGroup, &keep, &ci, &r.Note); err != nil {
			continue
		}
		r.Enabled, r.KeepOnMiss, r.CaseInsensitive = enabled != 0, keep != 0, ci != 0
		out = append(out, r)
	}
	return out
}

// Refusal reasons, kept internal: the caller maps all of them to one generic outcome so the login
// page cannot be used to probe which accounts exist or how they are provisioned.
var (
	errNotProvisioned   = ssoError("no account for this identity and this provider does not create them")
	errUnusableUsername = ssoError("the mapped username has no usable characters")
	errUsernameTaken    = ssoError("a local account already owns that username; link it explicitly")
)

type ssoError string

func (e ssoError) Error() string { return string(e) }
