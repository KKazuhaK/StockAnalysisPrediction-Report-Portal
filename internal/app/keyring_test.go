package app

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The keyring is one salt and one wrapped key. It used to be a single-row table; two rows in `meta`
// say the same thing in the shape every other setting already uses.
//
// The migration is the part that matters. A keyring left behind in the old table would be invisible
// to the new lookup, the portal would mint a SECOND one, and every secret sealed under the first
// would become permanently unreadable — with no error, because minting a fresh keyring is the
// ordinary first-run path.

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

// TestKeyringAdoptedFromTheOldTable proves a database that ran v0.4.0/v0.4.1 keeps reading the
// secrets it already sealed.
func TestKeyringAdoptedFromTheOldTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	sealed, err := s.sealSecret("corp", "client_secret", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	salt, wrapped, _ := st.Keyring()

	// Put it back the way v0.4.1 stored it, and clear the new location.
	st.exec(`CREATE TABLE IF NOT EXISTS sso_keyring(id INTEGER PRIMARY KEY, salt TEXT,
		wrapped_dek TEXT, kek_version INTEGER, created_at TEXT)`)
	st.exec(`INSERT INTO sso_keyring(id,salt,wrapped_dek,kek_version,created_at) VALUES(1,?,?,1,?)`,
		salt, wrapped, nowStr())
	st.exec(`DELETE FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK)
	if _, _, ok := st.Keyring(); ok {
		t.Fatal("the new location must be empty for this test to mean anything")
	}
	st.Close()

	// Reopen with the current code: init() adopts it.
	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2 := &Server{st: st2, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}

	gotSalt, gotWrapped, ok := st2.Keyring()
	if !ok || gotSalt != salt || gotWrapped != wrapped {
		t.Fatalf("keyring after upgrade = %q/%q, want the original", gotSalt, gotWrapped)
	}
	if st2.tableExists("sso_keyring") {
		t.Error("the old table must be dropped once its contents are adopted")
	}
	// The whole point: a secret sealed before the move still opens.
	if got, err := s2.openSecret("corp", "client_secret", sealed); err != nil || got != "hunter2" {
		t.Errorf("a secret sealed before the migration = %q %v, want it to still open", got, err)
	}
	// Idempotent.
	if err := st2.init(); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if got, _ := s2.openSecret("corp", "client_secret", sealed); got != "hunter2" {
		t.Error("a second init disturbed the keyring")
	}
	var n sql.NullString
	_ = n
}

// TestSSORulesAdoptedFromTheOldTable proves an admin's configured rules survive the move out of the
// table. Losing them would not error — the login would simply stop matching and start denying, which
// looks like an IdP problem rather than a lost setting.
func TestSSORulesAdoptedFromTheOldTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the table the way v0.4.1 had it and put two rules in, in a deliberate order.
	st.exec(`CREATE TABLE IF NOT EXISTS sso_group_rules(id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider_id BIGINT, ord INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, attr TEXT, value TEXT,
		target_role TEXT, target_group BIGINT, keep_on_miss INTEGER DEFAULT 0, ci INTEGER DEFAULT 0,
		note TEXT, created_at TEXT)`)
	st.exec(`INSERT INTO sso_group_rules(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, 7, 1, 1, "department", "contractor", "user", 42, 1, 1, "second")
	st.exec(`INSERT INTO sso_group_rules(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, 0, 0, 1, "", "staff", "", 9, 0, 0, "first, and global")
	st.Close()

	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	got := st2.SSORules()
	if len(got) != 2 {
		t.Fatalf("adopted %d rules, want 2: %+v", len(got), got)
	}
	if got[0].Note != "first, and global" || got[1].Note != "second" {
		t.Errorf("order was not preserved: %q then %q", got[0].Note, got[1].Note)
	}
	// Every field, because a silently-dropped one changes who gets what.
	r := got[1]
	if r.ProviderID != 7 || r.Attr != "department" || r.Value != "contractor" ||
		r.TargetRole != "user" || r.TargetGroup != 42 || !r.KeepOnMiss || !r.CI || !r.Enabled {
		t.Errorf("adopted rule = %+v, want every field carried over", r)
	}
	if st2.tableExists("sso_group_rules") {
		t.Error("the old table must be dropped once its contents are adopted")
	}
	// A global rule applies to any provider; a pinned one only to its own.
	if n := len(st2.rulesFor(7)); n != 2 {
		t.Errorf("provider 7 sees %d rules, want its own plus the global one", n)
	}
	if n := len(st2.rulesFor(8)); n != 1 {
		t.Errorf("provider 8 sees %d rules, want only the global one", n)
	}
	// Adoption must not overwrite edits made after the move.
	st2.SaveSSORules([]storedRule{{Value: "edited"}})
	if err := st2.init(); err != nil {
		t.Fatal(err)
	}
	if got := st2.SSORules(); len(got) != 1 || got[0].Value != "edited" {
		t.Errorf("a later edit was clobbered by re-running the adoption: %+v", got)
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

// TestAuthRequestsAdoptedFromTheOldName proves a pending registration link survives the rename.
// The other kinds are seconds-long and could be dropped, but a verification email is valid for a
// day and is already in someone's inbox.
func TestAuthRequestsAdoptedFromTheOldName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authreq.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	st.exec(`ALTER TABLE auth_requests RENAME TO sso_auth_requests`)
	st.exec(`INSERT INTO sso_auth_requests(token,provider_id,kind,req_id,nonce,verifier,username,target,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, "tok-verify", 0, "verify", "", "", "", "newbie@example.com", "",
		nowStr(), time.Now().Add(24*time.Hour).Unix())
	st.Close()

	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.tableExists("sso_auth_requests") {
		t.Error("the old name must be gone once its rows are adopted")
	}
	req, ok := st2.ConsumeAuthRequest("tok-verify", time.Now())
	if !ok || req.Kind != "verify" || req.Username != "newbie@example.com" {
		t.Errorf("a pending verification did not survive the rename: %+v ok=%v", req, ok)
	}
}
