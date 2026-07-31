package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Seeing and revoking an account's identity-provider binding.
//
// The store has been able to do both since SSO shipped, and nothing could reach either — so an
// admin could not answer "which IdP account is this person?" and could not cut a stale link. The
// only remedy was to delete the account. Revoking matters on its own: a link survives the person
// leaving the IdP, and while it stands, whoever the IdP later issues that subject to signs in as
// this account.

func identityAdminServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.LinkIdentity(Identity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Username: "alice", ProviderSlug: "corp"})
	return s
}

func TestAdminSeesTheIdentityBehindAnAccount(t *testing.T) {
	s := identityAdminServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/users/alice/identity", nil)
	r.SetPathValue("name", "alice")
	s.apiAdminUserIdentity(rec, r, "admin")

	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET identity → %d %s", rec.Code, rec.Body.String())
	}
	id, _ := out["identity"].(map[string]any)
	if id == nil || id["subject"] != "sub-1" || id["provider"] != "oidc" {
		t.Errorf("identity = %v, want the linked oidc subject", out["identity"])
	}
	// An account with no link says so, rather than 404ing or inventing one.
	s.st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/admin/users/bob/identity", nil)
	r.SetPathValue("name", "bob")
	s.apiAdminUserIdentity(rec, r, "admin")
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["identity"] != nil {
		t.Errorf("an unlinked account → %d %v", rec.Code, out["identity"])
	}
}

func TestAdminCanRevokeAnIdentityWithoutDeletingTheAccount(t *testing.T) {
	s := identityAdminServer(t)
	if _, ok := s.st.FindIdentity("oidc", "https://idp.example", "sub-1"); !ok {
		t.Fatal("failed to seed the link")
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/users/alice/identity", nil)
	r.SetPathValue("name", "alice")
	s.apiAdminUserUnlink(rec, r, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE identity → %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.st.FindIdentity("oidc", "https://idp.example", "sub-1"); ok {
		t.Error("the binding still resolves after being revoked")
	}
	// The ACCOUNT survives — that is the whole point of revoking rather than deleting.
	if u := s.st.GetUser("alice"); u == nil {
		t.Fatal("revoking the link deleted the account")
	} else if u.IsFederated() {
		t.Error("the account still reads as federated, so it can never use a local password again")
	}
	// Revoking again is a no-op rather than an error, so a double click cannot 500.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/api/admin/users/alice/identity", nil)
	r.SetPathValue("name", "alice")
	s.apiAdminUserUnlink(rec, r, "admin")
	if rec.Code != http.StatusOK {
		t.Errorf("revoking an already-revoked link → %d", rec.Code)
	}
}
