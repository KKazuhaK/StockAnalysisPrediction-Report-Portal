package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// TestIdentityLinkRoundTrip locks the account-linking store: an identity resolves to its account by
// (provider, issuer, subject) and by nothing else — in particular a same-subject value under a
// DIFFERENT issuer is a different person, because `sub` is only unique within an issuer.
func TestIdentityLinkRoundTrip(t *testing.T) {
	st := newTestStore(t)
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	if _, ok := st.FindIdentity("oidc", "https://idp.example", "sub-1"); ok {
		t.Fatal("an unlinked identity must not resolve")
	}
	if err := st.LinkIdentity(Identity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Username: "alice", ProviderSlug: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.FindIdentity("oidc", "https://idp.example", "sub-1")
	if !ok || got != "alice" {
		t.Fatalf("FindIdentity = %q/%v, want alice", got, ok)
	}
	// Same subject, different issuer → a different identity, never the same account.
	if _, ok := st.FindIdentity("oidc", "https://evil.example", "sub-1"); ok {
		t.Error("a subject from another issuer must not resolve to the same account")
	}
	// Same subject, different protocol → also distinct.
	if _, ok := st.FindIdentity("saml", "https://idp.example", "sub-1"); ok {
		t.Error("a subject from another protocol must not resolve to the same account")
	}
	// Re-linking the same identity is idempotent (a repeat login), not a duplicate-key error.
	if err := st.LinkIdentity(Identity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1", Username: "alice",
	}); err != nil {
		t.Errorf("re-link on a repeat login must be idempotent: %v", err)
	}
}

// TestFindUserByExternalID covers adoption: an account pre-created by an admin (or later by SCIM)
// carrying the IdP's immutable object id is found on first SSO login, so it is linked rather than
// duplicated. The lookup is scoped per provider, so two IdPs cannot collide on the same id.
func TestFindUserByExternalID(t *testing.T) {
	st := newTestStore(t)
	st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	if err := st.SetUserExternalID("bob", "acme", "objid-42"); err != nil {
		t.Fatal(err)
	}
	if got, ok := st.FindUserByExternalID("acme", "objid-42"); !ok || got != "bob" {
		t.Fatalf("FindUserByExternalID = %q/%v, want bob", got, ok)
	}
	if _, ok := st.FindUserByExternalID("other", "objid-42"); ok {
		t.Error("external ids must be scoped to their provider")
	}
	if _, ok := st.FindUserByExternalID("acme", ""); ok {
		t.Error("an empty external id must never match")
	}
}

// TestSSOUsernameSanitizer proves a mapped UPN is reduced to a safe, bounded username, and that
// anything with no usable characters is rejected rather than silently becoming something odd.
func TestSSOUsernameSanitizer(t *testing.T) {
	cases := map[string]string{
		"alice@acme.com":         "alice",
		"Alice.Smith@x.io":       "alice.smith",
		"  bob  ":                "bob",
		"weird//name":            "weird-name",
		"ACME\\jdoe":             "acme-jdoe",
		strings.Repeat("a", 200): strings.Repeat("a", 64),
	}
	for in, want := range cases {
		if got, ok := sanitizeSSOUsername(in); !ok || got != want {
			t.Errorf("sanitizeSSOUsername(%q) = %q/%v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "   ", "@@@", "///"} {
		if got, ok := sanitizeSSOUsername(bad); ok {
			t.Errorf("sanitizeSSOUsername(%q) = %q, want rejection", bad, got)
		}
	}
}

// TestApiLoginRejectsFederatedAccount is a security invariant, not a nicety: an SSO-owned account
// has no local password, so the password path must refuse it BEFORE bcrypt — otherwise the row is
// exposed to password guessing and to bcrypt CPU burn, and today it only fails by the accident that
// bcrypt rejects an empty hash.
func TestApiLoginRejectsFederatedAccount(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	h, _ := bcrypt.GenerateFromPassword([]byte("correct-horse-battery"), bcrypt.MinCost)
	st.UpsertUser(User{Username: "ext", PasswordHash: string(h), Role: "user"})

	login := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "ext", "password": "correct-horse-battery"})
		rec := httptest.NewRecorder()
		s.apiLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body))))
		return rec
	}
	if rec := login(); rec.Code != http.StatusOK {
		t.Fatalf("a local account must still log in: %d %s", rec.Code, rec.Body.String())
	}
	// Mark it federated — even with a valid stored hash, the password path must now refuse.
	if err := st.SetUserSource("ext", "jit", "acme"); err != nil {
		t.Fatal(err)
	}
	rec := login()
	if rec.Code == http.StatusOK {
		t.Fatal("a federated account must not be reachable through password login")
	}
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("federated password login → %d, want 401/403", rec.Code)
	}
}

// TestApplyAssignmentBumpsSessionOnChange proves a re-scope takes effect immediately: changing an
// SSO user's role or OU invalidates their existing cookie, so a downgrade cannot be outrun by a
// still-valid 7-day session.
func TestApplyAssignmentBumpsSessionOnChange(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	root := st.EnsureDefaultGroup()
	ou, _ := st.CreateUserGroup("ext", "", 0)
	st.SetGroupParent(ou, root)
	st.UpsertUser(User{Username: "u", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("u", root)

	before := st.GetUser("u").SessionRev
	// A no-op assignment must NOT churn the session — otherwise every login logs the user out.
	if err := s.applyAssignment("u", "user", root); err != nil {
		t.Fatal(err)
	}
	if got := st.GetUser("u").SessionRev; got != before {
		t.Errorf("an unchanged assignment bumped session_rev (%d → %d)", before, got)
	}
	// A real change must invalidate the old cookie.
	if err := s.applyAssignment("u", "operator", ou); err != nil {
		t.Fatal(err)
	}
	u := st.GetUser("u")
	if u.Role != "operator" {
		t.Errorf("role = %q, want operator", u.Role)
	}
	if st.PrimaryGroupOf("u") != ou {
		t.Error("the OU was not applied")
	}
	if u.SessionRev == before {
		t.Error("a role/OU change must bump session_rev so existing sessions die")
	}
}
