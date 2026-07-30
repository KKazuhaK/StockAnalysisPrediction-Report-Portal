package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// One username namespace, case-folded.
//
// userPrincipal folds case, because a read principal has to be stable. The account namespace did
// not, and the two disagreeing is the bug: `Alice` and `alice` were two accounts sharing the single
// principal `u:alice`, so each read the reports granted to the other and deleting either wiped both
// their viewer rows.
//
// The fix is the one the Passwall panel gets for free — MySQL's default collation makes its `upn`
// UNIQUE index case-insensitive. SQLite's TEXT PRIMARY KEY is BINARY and Postgres' text is
// case-sensitive, so here it has to be written down: fold at every creation path and compare
// case-insensitively at every guard, which makes the folded principal correct by construction.

func userAddReq(t *testing.T, s *Server, username, password string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"username":"` + username + `","password":"` + password + `","role":"user"}`
	s.apiUserAdd(rec, httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(body)), "admin")
	return rec.Code
}

// TestAdminCreateFoldsTheUsername pins the stored form. An admin who types a capital gets the
// canonical account, so every later exact-match lookup — login included — agrees with the principal.
func TestAdminCreateFoldsTheUsername(t *testing.T) {
	s := tenancyServer(t)
	if code := userAddReq(t, s, "  Alice@Corp.Example  ", "a-long-enough-password"); code != http.StatusOK {
		t.Fatalf("create → %d", code)
	}
	if s.st.GetUser("alice@corp.example") == nil {
		t.Error("the account was not stored under its folded name")
	}
	if s.st.GetUser("  Alice@Corp.Example  ") != nil {
		t.Error("the account was stored under the raw typed name")
	}
}

// TestCaseVariantAccountsCannotCoexist is the security property. Two accounts differing only by case
// would collapse onto one read principal, so the namespace must refuse the second one.
func TestCaseVariantAccountsCannotCoexist(t *testing.T) {
	s := tenancyServer(t)
	if code := userAddReq(t, s, "alice", "a-long-enough-password"); code != http.StatusOK {
		t.Fatalf("first create → %d", code)
	}
	if code := userAddReq(t, s, "Alice", "a-long-enough-password"); code == http.StatusOK {
		t.Error("a case-variant of an existing username was accepted")
	}
	if code := userAddReq(t, s, "ALICE", "a-long-enough-password"); code == http.StatusOK {
		t.Error("an upper-case variant of an existing username was accepted")
	}
	var n int
	s.st.queryRow("SELECT COUNT(*) FROM users WHERE LOWER(username)=?", "alice").Scan(&n)
	if n != 1 {
		t.Errorf("%d accounts share the principal %q", n, userPrincipal("alice"))
	}
}

// TestSSOJITRefusesACaseVariantLocalAccount closes the path the collision guard missed.
// sanitizeSSOUsername already folds, so a federated login mapping to "alice.wang" met a
// case-sensitive GetUser check that a local "Alice.Wang" did not trip — and the two accounts then
// shared one principal. Refusing is the only safe answer: auto-linking would let anyone who can make
// their IdP assert a matching UPN take over a password account.
func TestSSOJITRefusesACaseVariantLocalAccount(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "Alice.Wang", PasswordHash: "local-password", Role: "user"})

	// No UPN attribute mapped, so the subject is what sanitizeSSOUsername folds into the name.
	p := SSOProvider{Kind: "oidc", Slug: "idp", Provisioning: "jit"}
	id := ssoIdentity{Provider: "oidc", Issuer: "https://idp", Subject: "Alice.Wang"}
	if name, _ := sanitizeSSOUsername(id.Subject); name != "alice.wang" {
		t.Fatalf("the mapped name is %q, so this test is not exercising the collision", name)
	}
	_, created, err := s.resolveSSOAccount(p, id)
	if err == nil {
		t.Fatal("JIT created an account despite a case-variant local one already holding the name")
	}
	if created {
		t.Error("a refused resolution must not leave an account behind")
	}
	// The same provider must still be able to provision a name nobody holds.
	if _, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "oidc", Issuer: "https://idp",
		Subject: "Bob.Chen"}); err != nil {
		t.Errorf("JIT refused an uncontested name: %v", err)
	}
}

// TestPreexistingCaseVariantsAreReported covers the database that already has the pair — folding new
// writes cannot retrofit an old collision, and a silent one is worse than a loud one.
func TestPreexistingCaseVariantsAreReported(t *testing.T) {
	st := newTestStore(t)
	st.exec("INSERT INTO users(username,password_hash,role) VALUES(?,?,?)", "alice", "h", "user")
	st.exec("INSERT INTO users(username,password_hash,role) VALUES(?,?,?)", "Alice", "h", "user")

	got := st.CaseVariantUsernames()
	if len(got) != 1 || !strings.EqualFold(got[0], "alice") {
		t.Fatalf("CaseVariantUsernames() = %v, want one entry for alice", got)
	}
	if clean := newTestStore(t).CaseVariantUsernames(); len(clean) != 0 {
		t.Errorf("a portal with no collisions reported %v", clean)
	}
}
