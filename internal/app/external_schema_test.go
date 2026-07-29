package app

import (
	"database/sql"
	"testing"
)

// TestExternalUserSchemaBaseline locks the additive schema for external-user access (ADR 0022).
// Every new column/table/index is declared once in baseSchemaStmts, so it is present on a fresh
// store here — and, because ensureColumns reads the same statements, auto-added to an older
// database with no hand-written migration.
func TestExternalUserSchemaBaseline(t *testing.T) {
	st := newTestStore(t)

	cols := []struct{ table, col string }{
		{"user_groups", "parent_id"},       // OU tree
		{"user_groups", "restricted"},      // internal/external switch
		{"user_groups", "daily_run_quota"}, // R2 per-day run cap
		{"reports", "owner_group"},         // R1 attribution (OU that generated the report)
		{"users", "expires_at"},            // R4 account validity
		{"group_targets", "group_id"},      // R3 allow-list
		{"group_targets", "target_id"},
		{"group_targets", "surfaces"},
	}
	for _, c := range cols {
		if !st.columnExists(c.table, c.col) {
			t.Errorf("missing column %s.%s", c.table, c.col)
		}
	}
	if !st.tableExists("group_targets") {
		t.Error("missing table group_targets")
	}
	// owner_group deliberately has NO index (ADR 0024). It served the ADR 0022 read filter, which
	// version grants and report_viewers replaced; the column survives only as attribution, written
	// by a by-id UPDATE that uses the primary key. Asserted as an absence because an index nothing
	// reads is pure write amplification on every ingest — measured at 13% — and this table has
	// already lost two indexes for exactly that reason.
	var n int
	st.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, "idx_reports_owner").Scan(&n)
	if n != 0 {
		t.Error("idx_reports_owner is back: nothing reads owner_group, so it only costs writes")
	}
}

// TestOwnerGroupReconcilesOnOldReports proves an existing reports row survives the addition of
// owner_group: ensureColumns adds it as NULL (= internal/legacy/unattributed) with no backfill and
// without disturbing the row — the pure-additive upgrade path the design depends on.
func TestOwnerGroupReconcilesOnOldReports(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1) // share the one in-memory connection (matches OpenStore)
	t.Cleanup(func() { _ = db.Close() })

	// reports in the pre-owner_group shape, already holding a row.
	if _, err := db.Exec(`CREATE TABLE reports(id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT, kind TEXT, run_id TEXT,
		source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT)`); err != nil {
		t.Fatalf("seed reports: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`); err != nil {
		t.Fatalf("seed schema baseline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reports(symbol,rdate,rtype,title) VALUES('600519','2026-07-23','val','legacy')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	st := &Store{db: db, driver: "sqlite"}
	if err := st.init(); err != nil {
		t.Fatalf("init (should auto-reconcile owner_group): %v", err)
	}
	if !st.columnExists("reports", "owner_group") {
		t.Fatal("ensureColumns did not add reports.owner_group")
	}
	var title string
	var owner sql.NullInt64
	if err := st.queryRow(`SELECT title, owner_group FROM reports WHERE symbol='600519'`).Scan(&title, &owner); err != nil {
		t.Fatalf("read reconciled row: %v", err)
	}
	if title != "legacy" {
		t.Errorf("row not preserved: title=%q, want legacy", title)
	}
	if owner.Valid {
		t.Errorf("owner_group should be NULL on a reconciled legacy row, got %d", owner.Int64)
	}
}
