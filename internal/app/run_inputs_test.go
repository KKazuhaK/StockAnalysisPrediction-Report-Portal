package app

import (
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// TestRunInputsInjectsOwnerTokenForRestrictedOnly proves the owner-attribution token is added to a
// run's Dify inputs ONLY when the submitting user's OU is restricted — internal runs get their inputs
// back untouched (byte-for-byte), so an undeclared-variable-strict workflow is never affected and the
// money-path is unchanged for existing internal usage (ADR 0022 R1).
func TestRunInputsInjectsOwnerTokenForRestrictedOnly(t *testing.T) {
	st := newTestStore(t)
	root := st.EnsureDefaultGroup()
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}

	// Internal user (Default group) → no injection, and the caller's map is returned unchanged.
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"})
	base := map[string]string{"symbol": "600519"}
	got := s.runInputs(BatchJob{CreatedBy: "staff"}, base)
	if _, ok := got[ownerTokenInput]; ok {
		t.Error("internal run must not carry an owner token")
	}
	if len(got) != 1 {
		t.Errorf("internal inputs = %v, want unchanged", got)
	}

	// Restricted external user → a valid token for their OU is injected, without mutating the input.
	org, _ := st.CreateUserGroup("ext-org", "", 0)
	st.SetGroupParent(org, root)
	st.SetGroupRestricted(org, true)
	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", org)

	got = s.runInputs(BatchJob{CreatedBy: "ext"}, base)
	tok := got[ownerTokenInput]
	if tok == "" {
		t.Fatal("restricted run must carry an owner token")
	}
	if ou, who, ok := s.ownerFromToken(tok); !ok || ou != org || who == "" {
		t.Fatalf("injected token OU = %d (ok %v), want %d", ou, ok, org)
	}
	if got["symbol"] != "600519" {
		t.Errorf("existing inputs must be preserved, got %v", got)
	}
	if _, ok := base[ownerTokenInput]; ok {
		t.Error("runInputs must not mutate the caller's inputs map")
	}
}
