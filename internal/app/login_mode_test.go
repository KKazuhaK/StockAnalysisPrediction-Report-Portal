package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Login modes, on two axes.
//
// login_mode decides what the login PAGE offers. sso_only decides what the login ENDPOINT accepts.
// Keeping them apart is what makes "force SSO" a real control instead of a cosmetic one: a mode is
// only ever a rendering hint, and anyone can POST to /api/login regardless of what the page drew.
//
// Both degrade to safety when no identity provider is enabled. A portal cannot be redirected to an
// IdP that does not exist, and refusing local passwords with nothing to fall back on would lock out
// every non-admin at once.

func loginModeServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("a-long-enough-password"), 4)
	s.st.UpsertUser(User{Username: "boss", PasswordHash: string(hash), Role: "admin"})
	s.st.UpsertUser(User{Username: "worker", PasswordHash: string(hash), Role: "user"})
	return s
}

func withProvider(s *Server) {
	s.st.SaveSSOProvider(SSOProvider{Slug: "acme", Kind: "oidc", Enabled: true,
		Issuer: "https://idp.example", Provisioning: "jit"})
}

func doLogin(t *testing.T, s *Server, user string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"`+user+`","password":"a-long-enough-password"}`)))
	return rec.Code
}

func TestLoginModeDefaultsToDual(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	if got := s.loginMode(); got != loginDual {
		t.Errorf("a fresh portal is in %q mode, want %q", got, loginDual)
	}
}

// TestLoginModeDegradesWithoutAProvider is the lockout guard. Every SSO-flavoured mode is a promise
// the portal cannot keep with no IdP configured — auto-redirecting to nowhere would be unrecoverable
// through the product.
func TestLoginModeDegradesWithoutAProvider(t *testing.T) {
	s := loginModeServer(t)
	for _, mode := range []string{loginDual, loginSSOFirst, loginSSORedirect} {
		s.st.SetSetting(setLoginMode, mode)
		if got := s.loginMode(); got != loginLocalOnly {
			t.Errorf("mode %q with no provider resolved to %q, want %q", mode, got, loginLocalOnly)
		}
	}
	// With one enabled, the stored mode is honoured again.
	withProvider(s)
	s.st.SetSetting(setLoginMode, loginSSORedirect)
	if got := s.loginMode(); got != loginSSORedirect {
		t.Errorf("mode with a provider = %q, want %q", got, loginSSORedirect)
	}
	// A disabled provider does not count.
	s.st.SaveSSOProvider(SSOProvider{Slug: "acme", Kind: "oidc", Enabled: false, Issuer: "https://idp.example"})
	if got := s.loginMode(); got != loginLocalOnly {
		t.Errorf("mode with only a DISABLED provider = %q, want %q", got, loginLocalOnly)
	}
}

func TestLoginModeRejectsAnUnknownStoredValue(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	s.st.SetSetting(setLoginMode, "sso_redirekt") // a typo, or a hand-edited row
	if got := s.loginMode(); got != loginDual {
		t.Errorf("an unrecognized mode resolved to %q, want the default %q", got, loginDual)
	}
}

// TestSSOOnlyRefusesUsersButNeverAdmins is the enforcement half, and the admin exemption is the
// break-glass: an IdP that breaks must never cost an operator the ability to sign in and fix it.
func TestSSOOnlyRefusesUsersButNeverAdmins(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	s.st.SetSetting(setSSOOnly, "1")

	if code := doLogin(t, s, "worker"); code != http.StatusForbidden {
		t.Errorf("a non-admin password login under sso_only → %d, want 403", code)
	}
	if code := doLogin(t, s, "boss"); code != http.StatusOK {
		t.Errorf("an admin must always keep the local path → %d, want 200", code)
	}
}

// TestSSOOnlyIsInertWithoutAProvider — refusing local passwords while offering no alternative locks
// out every non-admin at once, so the policy only bites when there is something to fall back to.
func TestSSOOnlyIsInertWithoutAProvider(t *testing.T) {
	s := loginModeServer(t)
	s.st.SetSetting(setSSOOnly, "1")
	if s.ssoOnlyActive() {
		t.Error("sso_only must not bite with no enabled provider")
	}
	if code := doLogin(t, s, "worker"); code != http.StatusOK {
		t.Errorf("a non-admin login with no provider → %d, want 200", code)
	}
}

// TestSSOOnlyIsIndependentOfTheMode proves the two axes really are separate: the page can show a
// password form while the endpoint refuses it, and the reverse.
func TestSSOOnlyIsIndependentOfTheMode(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)

	s.st.SetSetting(setLoginMode, loginSSORedirect) // the page hides local…
	if s.ssoOnlyActive() {
		t.Error("a presentation mode must not enable the hard policy by itself")
	}
	if code := doLogin(t, s, "worker"); code != http.StatusOK {
		t.Errorf("force-SSO alone must not refuse a password login → %d", code)
	}

	s.st.SetSetting(setLoginMode, loginDual) // …and the page can show local while the endpoint refuses
	s.st.SetSetting(setSSOOnly, "1")
	if code := doLogin(t, s, "worker"); code != http.StatusForbidden {
		t.Errorf("sso_only must bite in dual mode too → %d", code)
	}
}

// TestAuthMethodsEndpointDrivesTheLoginPage: the page renders from resolved booleans rather than
// re-deriving the rules, so there is one source of truth for what is offered.
func TestAuthMethodsEndpointDrivesTheLoginPage(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)

	get := func() map[string]any {
		rec := httptest.NewRecorder()
		s.apiSSOProviders(rec, httptest.NewRequest(http.MethodGet, "/api/sso/providers", nil))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	for _, tc := range []struct {
		mode       string
		local, sso bool
	}{
		{loginDual, true, true},
		{loginSSOFirst, true, true},
		{loginSSORedirect, false, true}, // the redirect target must stay reachable
		{loginLocalOnly, true, false},
	} {
		s.st.SetSetting(setLoginMode, tc.mode)
		got := get()
		if got["login_mode"] != tc.mode {
			t.Errorf("%s: login_mode = %v", tc.mode, got["login_mode"])
		}
		if got["local"] != tc.local || got["sso"] != tc.sso {
			t.Errorf("%s: local=%v sso=%v, want local=%v sso=%v", tc.mode, got["local"], got["sso"], tc.local, tc.sso)
		}
	}

	// The endpoint stays minimal about the provider itself — no issuer, no client id.
	s.st.SetSetting(setLoginMode, loginDual)
	provs, _ := get()["providers"].([]any)
	if len(provs) != 1 {
		t.Fatalf("providers = %v", provs)
	}
	p, _ := provs[0].(map[string]any)
	for _, leaked := range []string{"issuer", "client_id", "client_secret"} {
		if _, ok := p[leaked]; ok {
			t.Errorf("the public endpoint leaks %q", leaked)
		}
	}
}

// TestSSOOnlyIsInertWhenThePageOffersNoSSO closes a lockout the first version of this feature had.
// The inertness check asked whether a provider was ENABLED, not whether the login page actually
// offers it — so local_only + sso_only left every non-admin staring at a password form that always
// returns 403, with no provider button anywhere on the page. The route still existed, but nothing
// in the product led to it.
func TestSSOOnlyIsInertWhenThePageOffersNoSSO(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	s.st.SetSetting(setSSOOnly, "1")
	s.st.SetSetting(setLoginMode, loginLocalOnly)

	if s.ssoOnlyActive() {
		t.Error("refusing passwords while the page offers no SSO leaves non-admins no way in")
	}
	if code := doLogin(t, s, "worker"); code != http.StatusOK {
		t.Errorf("a non-admin under local_only+sso_only → %d, want 200", code)
	}
	// It must still bite in every mode that DOES surface the provider.
	for _, mode := range []string{loginDual, loginSSOFirst, loginSSORedirect} {
		s.st.SetSetting(setLoginMode, mode)
		if code := doLogin(t, s, "worker"); code != http.StatusForbidden {
			t.Errorf("mode %q with sso_only → %d, want 403", mode, code)
		}
	}
}

// TestSSOOnlyNeedsALocalAdminToFallBackOn guards the guarantee the admin UI prints. The exemption
// only ever protects an admin with a LOCAL password: apiLogin nils out a federated account before
// bcrypt, so the refusal is never even reached for one. An organisation whose admins are all
// federated therefore has no browser path in at all the moment its IdP breaks.
func TestSSOOnlyNeedsALocalAdminToFallBackOn(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	// Make every admin federated, the way a JIT provider with an admin-granting rule ends up:
	// SetUserSource is what resolveSSOAccount calls, and IsFederated reads that column.
	s.st.SetUserSource("boss", "jit", "acme")
	if u := s.st.GetUser("boss"); u == nil || !u.IsFederated() {
		t.Fatal("failed to federate the only admin")
	}

	if s.localAdminExists() {
		t.Error("a federated admin is not a fallback: apiLogin refuses them before the exemption")
	}
	// Turning the policy on must be refused rather than silently locking the portal.
	rec := httptest.NewRecorder()
	s.apiAdminSecuritySave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/security",
		strings.NewReader(`{"login":{"mode":"dual","sso_only":true}}`)), "boss")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("enabling sso_only with no local admin → %d, want 400", rec.Code)
	}
	if s.st.GetSetting(setSSOOnly, "") == "1" {
		t.Error("the policy was stored despite being refused")
	}

	// With a local admin present it saves normally.
	s.st.UpsertUser(User{Username: "root", PasswordHash: "$2a$04$abcdefghijklmnopqrstuv", Role: "admin"})
	if !s.localAdminExists() {
		t.Fatal("a local admin was not recognised")
	}
	rec = httptest.NewRecorder()
	s.apiAdminSecuritySave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/security",
		strings.NewReader(`{"login":{"mode":"dual","sso_only":true}}`)), "boss")
	if rec.Code != http.StatusOK {
		t.Errorf("enabling sso_only with a local admin → %d, want 200", rec.Code)
	}
}

// TestSSOOnlyAndSelfRegistrationAreRefusedTogether closes a configuration trap. Neither setting is
// wrong on its own, but together they mint accounts nobody can use: registration creates a local
// password account, and sso_only then refuses exactly that at the login form. The registrant gets
// a confirmation email, follows it, and is turned away by a portal that told them to sign up.
func TestSSOOnlyAndSelfRegistrationAreRefusedTogether(t *testing.T) {
	s := loginModeServer(t)
	withProvider(s)
	s.st.UpsertUser(User{Username: "root", PasswordHash: "$2a$04$abcdefghijklmnopqrstuv", Role: "admin"})
	s.st.SetSetting(setRegEnabled, "1")

	post := func(body string) int {
		rec := httptest.NewRecorder()
		s.apiAdminSecuritySave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/security",
			strings.NewReader(body)), "boss")
		return rec.Code
	}
	if code := post(`{"login":{"mode":"dual","sso_only":true},"registration":{"enabled":true}}`); code != http.StatusBadRequest {
		t.Errorf("sso_only alongside open registration → %d, want 400", code)
	}
	if s.st.GetSetting(setSSOOnly, "") == "1" {
		t.Error("the pair was stored despite being refused")
	}
	// Turning registration off in the same request is a coherent state and must be accepted.
	if code := post(`{"login":{"mode":"dual","sso_only":true},"registration":{"enabled":false}}`); code != http.StatusOK {
		t.Errorf("sso_only with registration closed → %d, want 200", code)
	}
}
