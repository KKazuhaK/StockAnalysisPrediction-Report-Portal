package app

import (
	"fmt"
	"net/http"
	"testing"
)

// apiUserSave is the shared partial-update endpoint behind the edit form and the password-reset
// modal: only the fields present in the body are applied, with guards for the last admin and the
// acting account itself.
func TestUserSavePartialUpdateAndGuards(t *testing.T) {
	s := userAdminServer(t)
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}

	if code, _ := callPath(t, s.apiUserSave, http.MethodPost, `{}`, map[string]string{"name": "nobody"}, "admin"); code != http.StatusNotFound {
		t.Fatalf("unknown user → %d, want 404", code)
	}

	// Role + profile + deactivation in one save; strings are trimmed.
	code, _ := callPath(t, s.apiUserSave, http.MethodPost,
		`{"role":"operator","display_name":" Alice A ","email":" a@x.com ","active":false}`,
		map[string]string{"name": "alice"}, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiUserSave → %d", code)
	}
	u := s.st.GetUser("alice")
	if u.Role != "operator" || u.DisplayName != "Alice A" || u.Email != "a@x.com" || u.Active {
		t.Fatalf("saved user = %+v", u)
	}

	// A too-short password is refused by the shared policy before any write.
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"password":"short"}`,
		map[string]string{"name": "alice"}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("short password → %d, want 400", code)
	}

	// Malformed expiry is refused; a valid one is stored.
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"expires_at":"07/01/2026"}`,
		map[string]string{"name": "alice"}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("bad expiry → %d, want 400", code)
	}
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"expires_at":"2099-01-01"}`,
		map[string]string{"name": "alice"}, "admin"); code != http.StatusOK {
		t.Fatalf("valid expiry → %d", code)
	}
	if u = s.st.GetUser("alice"); u.ExpiresAt != "2099-01-01" {
		t.Fatalf("expiry = %q, want 2099-01-01", u.ExpiresAt)
	}

	// The sole admin survives a demote + deactivate attempt.
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"role":"user","active":false}`,
		map[string]string{"name": "admin"}, "admin"); code != http.StatusOK {
		t.Fatalf("last-admin save → %d", code)
	}
	if u = s.st.GetUser("admin"); u.Role != "admin" || !u.Active {
		t.Fatalf("last admin = %+v, want role and active kept", u)
	}

	// With a second admin, the acting account still cannot deactivate itself.
	if err := s.st.UpsertUser(User{Username: "root", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"active":false}`,
		map[string]string{"name": "admin"}, "admin"); code != http.StatusOK {
		t.Fatalf("self-deactivate save → %d", code)
	}
	if u = s.st.GetUser("admin"); !u.Active {
		t.Fatal("acting admin deactivated themselves")
	}

	// Nor may they set an already-passed expiry on themselves.
	if code, _ = callPath(t, s.apiUserSave, http.MethodPost, `{"expires_at":"2000-01-01"}`,
		map[string]string{"name": "admin"}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("passed self-expiry → %d, want 400", code)
	}
}

// apiUserDelete refuses deleting yourself and the last admin, deletes anyone else, and records the
// removal only when it actually happened.
func TestUserDeleteGuardsAndAudit(t *testing.T) {
	s := userAdminServer(t)
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}

	// Self-delete: nothing happens.
	if code, _ := callPath(t, s.apiUserDelete, http.MethodPost, ``, map[string]string{"name": "admin"}, "admin"); code != http.StatusOK {
		t.Fatalf("apiUserDelete self → %d", code)
	}
	if s.st.GetUser("admin") == nil {
		t.Fatal("acting admin deleted themselves")
	}
	// The last admin cannot be deleted by someone else either.
	if code, _ := callPath(t, s.apiUserDelete, http.MethodPost, ``, map[string]string{"name": "admin"}, "alice"); code != http.StatusOK {
		t.Fatalf("apiUserDelete last admin → %d", code)
	}
	if s.st.GetUser("admin") == nil {
		t.Fatal("last admin was deleted")
	}

	// A plain account is deletable, and the audit row outlives it.
	if code, _ := callPath(t, s.apiUserDelete, http.MethodPost, ``, map[string]string{"name": "alice"}, "admin"); code != http.StatusOK {
		t.Fatalf("apiUserDelete → %d", code)
	}
	if s.st.GetUser("alice") != nil {
		t.Fatal("alice survived delete")
	}
	if _, total := s.st.ListAudit(AuditFilter{Action: AuditUserDelete}); total != 1 {
		t.Fatalf("delete audit rows = %d, want 1", total)
	}
	// The refused attempts above recorded nothing.
	if _, total := s.st.ListAudit(AuditFilter{TargetType: "user", TargetID: "admin"}); total != 0 {
		t.Fatalf("refused deletes were audited: %d rows", total)
	}
}

// apiTokenDelete removes a token by id and audits the removal; an unknown id is a harmless no-op.
func TestTokenDeleteHandler(t *testing.T) {
	s := userAdminServer(t)
	if err := s.st.CreateToken("tok-secret", "dify", "query", ""); err != nil {
		t.Fatal(err)
	}
	ts := s.st.ListTokens()
	if len(ts) != 1 {
		t.Fatalf("tokens = %d, want 1", len(ts))
	}
	id := fmt.Sprint(ts[0].ID)

	if code, _ := callPath(t, s.apiTokenDelete, http.MethodPost, ``, map[string]string{"id": id}, "admin"); code != http.StatusOK {
		t.Fatalf("apiTokenDelete → %d", code)
	}
	if len(s.st.ListTokens()) != 0 {
		t.Fatal("token survived delete")
	}
	if _, total := s.st.ListAudit(AuditFilter{Action: AuditTokenDelete}); total != 1 {
		t.Fatalf("token delete audit rows = %d, want 1", total)
	}
	// Deleting the same id again answers ok rather than erroring.
	if code, _ := callPath(t, s.apiTokenDelete, http.MethodPost, ``, map[string]string{"id": id}, "admin"); code != http.StatusOK {
		t.Fatalf("repeat apiTokenDelete → %d", code)
	}
}
