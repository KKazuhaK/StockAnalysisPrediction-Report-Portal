package app

import (
	"errors"
	"log"
	"net/http"
	"strings"
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
	username, created, err := s.resolveSSOAccount(p, id)
	if err != nil {
		log.Printf("sso: %s/%s login refused for subject %q: %v", p.Kind, p.Slug, id.Subject, err)
		s.ssoFail(w, r, resolveFailCode(err))
		return
	}

	// A refusal from here on must not leave a just-created account behind. A leftover row is not
	// inert: the next attempt adopts it as pre-existing, so the engine takes the keep-on-miss branch
	// instead of the new-user branch and the very deny that protected the first attempt no longer
	// applies. Provisioning is therefore all-or-nothing across the whole decision.
	refuse := func(reason string) {
		if created {
			if err := s.st.DeleteUser(username); err != nil {
				log.Printf("sso: could not roll back the provisioned account %q: %v", username, err)
			}
		}
		s.ssoFail(w, r, reason)
	}

	// Rules decide the role AND the OU. In this portal the OU carries report visibility, the run
	// allow-list and the daily quota (ADR 0022), so this is the actual permission decision.
	//
	// A just-created account must NOT look "existing" here. It was written to the users table a few
	// lines ago with no group, so a keep-on-miss rule would otherwise take the leave-them-as-they-are
	// branch and leave a brand-new external user in group 0 — i.e. unrestricted, seeing everything.
	// A fresh account takes the new-user path: provider defaults, or deny.
	cur := ssorules.Current{}
	if !created {
		cur = Current(s, username)
	}
	out := ssorules.Resolve(s.ssoRules(p.ID), ssorules.Facts{
		Groups: id.groups(p.AttrGroups),
		// Attribute-named rules match against this. Without it every rule naming an attribute
		// would silently never fire, which looks to an admin like the rule was ignored.
		Attrs: id.attrMap(),
	}, cur, ssorules.Defaults{
		Role: firstNonEmpty(p.DefaultRole, "user"), Group: p.DefaultGroup,
		AllowAdminRole: p.AllowAdminRole, PrivilegedRoles: privilegedRoles(),
	})
	if out.Deny {
		log.Printf("sso: %s/%s denied %q: %s", p.Kind, p.Slug, username, out.DenyReason)
		refuse("not_provisioned")
		return
	}
	if out.AdminBlocked {
		log.Printf("sso: %s/%s refused an admin elevation for %q (allow_admin_role is off)", p.Kind, p.Slug, username)
	}
	if err := s.applyAssignment(username, out.Role, out.Group); err != nil {
		log.Printf("sso: %s/%s could not apply the assignment for %q: %v", p.Kind, p.Slug, username, err)
		refuse("internal")
		return
	}

	u := s.st.GetUser(username)
	// Re-check the account gates AFTER assignment, so a rule that just deactivated or re-scoped
	// someone cannot be outrun by this very login.
	if u == nil || !u.Active || s.accountExpired(u) {
		refuse("not_provisioned")
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
// How an unlinked login may be matched onto an account that already exists.
//
// The default refuses, and that is not timidity: adopting on a name the IdP asserts hands account
// takeover to anyone who can make it assert one. The other two are an admin declaring that this IdP
// IS authoritative for this portal's accounts — true for an internal deployment behind one company
// IdP, and false for anything federated with the outside. It cannot be inferred, so it is chosen.
const (
	LinkBySubject  = ""         // identity link + external_id only; a collision is refused
	LinkByUsername = "username" // the mapped username names the account
	LinkByEmail    = "email"    // the mapped email names the account
)

func validLinkBy(v string) bool {
	switch v {
	case LinkBySubject, LinkByUsername, LinkByEmail:
		return true
	}
	return false
}

func (s *Server) resolveSSOAccount(p SSOProvider, id ssoIdentity) (username string, created bool, err error) {
	// 1. An existing link. This is the only lookup that runs on every login, and it is keyed on
	// (provider, issuer, subject) — never on email.
	if u, ok := s.st.FindIdentity(id.Provider, id.Issuer, id.Subject); ok {
		return u, false, nil
	}
	// 2. Adoption: an account an admin pre-created (or SCIM will later create) carrying this IdP's
	// immutable object id. This is why external_id ships with SSO rather than with SCIM.
	if ext := id.claim(p.AttrExternalID); ext != "" {
		if u, ok := s.st.FindUserByExternalID(p.Slug, ext); ok {
			return u, false, nil
		}
	}
	// 2b. Adoption by a field the admin has declared this IdP authoritative for. Off by default;
	// see the LinkBy constants for why it has to be a decision rather than a default.
	if p.LinkBy != LinkBySubject {
		u, err := s.matchExistingAccount(p, id)
		if err != nil {
			return "", false, err
		}
		if u != "" {
			// Bind it, so every later login takes step 1 and never matches on a mutable field again.
			if err := s.st.LinkIdentity(Identity{
				Username: u, Provider: id.Provider, Issuer: id.Issuer, Subject: id.Subject,
				ProviderSlug: p.Slug, Attrs: id.attrs(),
			}); err != nil {
				return "", false, err
			}
			return u, false, nil
		}
	}
	// 3. Otherwise the account must be created — and only if this provider is allowed to.
	if p.Provisioning != "jit" {
		return "", false, errNotProvisioned
	}
	upn := firstNonEmpty(id.claim(p.AttrUPN), id.Subject)
	name, ok := sanitizeSSOUsername(upn)
	if !ok {
		return "", false, errUnusableUsername
	}
	// A collision with a LOCAL account is never auto-linked: that would let anyone who can make
	// their IdP assert a matching UPN take over a password account. It needs an admin.
	//
	// Ignoring case is load-bearing, not tidiness. sanitizeSSOUsername folds, so an assertion for
	// "Alice.Wang" arrives here as "alice.wang"; an exact-match guard would sail past a local
	// "Alice.Wang" and provision a second account — and the two would then share the single read
	// principal u:alice.wang, which is the takeover this check exists to prevent.
	if s.st.UsernameTaken(name) {
		return "", false, errUsernameTaken
	}
	// Created with the baseline role, NOT the provider default. The default is a privileged role in
	// some configurations, and the rule engine is the one place allowed to grant one — it drops the
	// elevation when allow_admin_role is off. Writing the default here would hand out admin behind
	// the engine's back; the assignment a few lines later sets the real role either way.
	if err := s.st.UpsertUser(User{Username: name, PasswordHash: "", Role: "user"}); err != nil {
		return "", false, err
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
	return name, true, nil
}

// issueSession mints the portal session cookie for an SSO login, reusing the same signed-cookie
// path as password login so expiry and session_rev invalidation behave identically. A provider may
// shorten the lifetime: until SCIM exists an IdP-side disable is invisible to us, so a shorter
// session is the honest partial answer to deprovisioning.
// The shortened lifetime is stamped into the SIGNED token, not only into the cookie's MaxAge.
// MaxAge is a browser-side hint that anyone actually holding the cookie value simply ignores, so a
// limit expressed only there would be no limit at all against the threat it exists for.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u User, p SSOProvider) {
	ttl := sessionTTL
	if p.SessionHours > 0 {
		ttl = time.Duration(p.SessionHours) * time.Hour
	}
	s.setSessionCookieFor(w, r, u, ttl)
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
func (s *Server) ssoRules(providerID int64) []ssorules.Rule { return s.st.rulesFor(providerID) }

// Refusal reasons, kept internal: the caller maps all of them to one generic outcome so the login
// page cannot be used to probe which accounts exist or how they are provisioned.
// matchExistingAccount resolves p.LinkBy against the accounts that exist. An empty match is not an
// error — the caller falls through to provisioning — but an AMBIGUOUS one is: users.email carries
// no unique index, so two accounts can share an address, and choosing between them would be
// authenticating someone as whichever row came back first.
func (s *Server) matchExistingAccount(p SSOProvider, id ssoIdentity) (string, error) {
	var candidate string
	switch p.LinkBy {
	case LinkByUsername:
		// Folded, like every other username path: sanitizeSSOUsername lowercases, so an exact-match
		// lookup here would miss the very collision the default refuses.
		name, ok := sanitizeSSOUsername(firstNonEmpty(id.claim(p.AttrUPN), id.Subject))
		if !ok {
			return "", errUnusableUsername
		}
		u := s.st.GetUser(name)
		if u == nil {
			return "", nil
		}
		candidate = u.Username
	case LinkByEmail:
		email := strings.TrimSpace(id.claim(p.AttrEmail))
		if email == "" {
			// No claim is no match. Falling through to "any account with an empty email" would
			// adopt an unrelated account on a missing attribute.
			return "", errNoEmailToMatch
		}
		names := s.st.UsersByEmail(email)
		if len(names) == 0 {
			return "", nil
		}
		if len(names) > 1 {
			return "", ssoError("more than one account uses " + email + "; an SSO login cannot choose between them")
		}
		candidate = names[0]
	default:
		return "", nil
	}
	u := s.st.GetUser(candidate)
	if u == nil || !u.Active {
		return "", errInactiveAccount
	}
	return u.Username, nil
}

// resolveFailCode decides what the login page is told.
//
// Almost everything collapses to not_provisioned on purpose: unknown subject, a name a local
// account already holds, disabled, expired, no rule matched — telling those apart would turn the
// login page into a user-enumeration oracle against the IdP, which is the whole reason this
// function is deliberately blunt.
//
// The exception is a refusal decided BEFORE anything is looked up. "This provider has no email
// claim mapped, so email matching cannot run" is a fact about the configuration, not about any
// person; it reveals nothing an attacker could not read off the login page anyway, and it is the
// only way an admin will ever find out — the generic answer names a cause that is not theirs.
func resolveFailCode(err error) string {
	if errors.Is(err, errNoEmailToMatch) {
		return "sso_no_email_claim"
	}
	return "not_provisioned"
}

var (
	errNoEmailToMatch  = ssoError("the assertion carried no email to match an account with")
	errInactiveAccount = ssoError("that account is disabled")
)

var (
	errNotProvisioned   = ssoError("no account for this identity and this provider does not create them")
	errUnusableUsername = ssoError("the mapped username has no usable characters")
	errUsernameTaken    = ssoError("a local account already owns that username; link it explicitly")
)

type ssoError string

func (e ssoError) Error() string { return string(e) }
