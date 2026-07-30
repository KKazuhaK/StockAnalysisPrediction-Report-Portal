package app

import (
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The keyring is one salt and one wrapped key, held as two rows in `meta` — the shape every other
// setting already uses. Rotating the portal's secret_key re-wraps that one value rather than
// re-encrypting every secret sealed under it.

func TestKeyringRoundTrip(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}

	sealed, err := s.sealSecret("corp", "client_secret", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.openSecret("corp", "client_secret", sealed); err != nil || got != "hunter2" {
		t.Fatalf("round trip = %q %v", got, err)
	}
	salt, wrapped, ok := st.Keyring()
	if !ok || salt == "" || wrapped == "" {
		t.Fatal("the keyring must be stored after first use")
	}
	// It lives in meta, not in a table of its own.
	if st.tableExists("sso_keyring") {
		t.Error("the single-row keyring table must not be created")
	}
	// Saving again must NOT replace it: a new DEK would orphan every secret sealed under the old.
	if err := st.SaveKeyring("different-salt", "different-key"); err != nil {
		t.Fatal(err)
	}
	if s2, w2, _ := st.Keyring(); s2 != salt || w2 != wrapped {
		t.Error("an existing keyring must never be overwritten")
	}
}

// TestDeletingAProviderDropsItsRules proves a removed provider does not leave rules that a later
// provider reusing its id would inherit.
func TestDeletingAProviderDropsItsRules(t *testing.T) {
	st := newTestStore(t)
	st.SaveSSORules([]storedRule{
		{ProviderID: 1, Value: "pinned"},
		{ProviderID: 0, Value: "global"},
	})
	if err := st.DeleteRulesOfProvider(1); err != nil {
		t.Fatal(err)
	}
	got := st.SSORules()
	if len(got) != 1 || got[0].Value != "global" {
		t.Errorf("after deleting provider 1: %+v, want only the global rule", got)
	}
}
