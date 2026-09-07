package app

import (
	"database/sql"
	"testing"
)

// The pure-additive upgrade path (ADR 0013): a column declared once in baseSchemaStmts and reconciled
// onto an existing database by ensureColumns, with no hand-written migration.
//
// Every other test creates the table with the column already in it, so ensureColumns is a no-op and a
// column it cannot parse or ALTER never shows itself — except in production, at startup, on somebody
// else's database. These start from the older shape instead.

// TestBytesReclaimedReconcilesOnAnOlderDatabase walks the path every existing deployment takes on
// its next boot, which the ordinary tests never do: they create the table with the column already in
// it, so ensureColumns is a no-op and a column it cannot parse or ALTER would never show itself here
// — only in production, at startup, on somebody else's database.
//
// The release note says this column is "picked up automatically". This is what that has to mean.
func TestBytesReclaimedReconcilesOnAnOlderDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1) // share the one in-memory connection (matches OpenStore)
	t.Cleanup(func() { _ = db.Close() })

	// cleanup_runs in its pre-bytes_reclaimed shape, already holding a recorded pass.
	if _, err := db.Exec(`CREATE TABLE cleanup_runs(id INTEGER PRIMARY KEY AUTOINCREMENT,
		ran_at TEXT, trigger TEXT, dry_run INTEGER DEFAULT 0, ok INTEGER DEFAULT 1, error TEXT DEFAULT '',
		batch_deleted INTEGER DEFAULT 0, tokens_deleted INTEGER DEFAULT 0, reports_deleted INTEGER DEFAULT 0,
		duration_ms INTEGER DEFAULT 0, audit_deleted INTEGER DEFAULT 0, revisions_deleted INTEGER DEFAULT 0)`); err != nil {
		t.Fatalf("seed cleanup_runs: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`); err != nil {
		t.Fatalf("seed schema baseline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleanup_runs(ran_at,trigger,reports_deleted,duration_ms)
		VALUES('2026-08-01 03:00:00','schedule',7,120)`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	st := &Store{db: db, driver: "sqlite"}
	if err := st.init(); err != nil {
		t.Fatalf("init (should auto-reconcile bytes_reclaimed): %v", err)
	}
	if !st.columnExists("cleanup_runs", "bytes_reclaimed") {
		t.Fatal("ensureColumns did not add cleanup_runs.bytes_reclaimed")
	}

	// The history a deployment already had must still read, with the new column at its default
	// rather than a NULL the scan would refuse.
	runs, err := st.ListCleanupRuns(5)
	if err != nil {
		t.Fatalf("ListCleanupRuns over a reconciled table: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d history rows; want the one that was already there", len(runs))
	}
	if runs[0].ReportsDeleted != 7 || runs[0].BytesReclaimed != 0 {
		t.Errorf("row = %+v; want the old counts preserved and bytes_reclaimed defaulted to 0", runs[0])
	}

	// And a new pass can write the column on the reconciled table.
	if _, err := st.InsertCleanupRun(CleanupRun{RanAt: "2026-09-04 03:00:00", Trigger: "manual",
		ReportsDeleted: 3, BytesReclaimed: 4096}); err != nil {
		t.Fatalf("InsertCleanupRun after reconcile: %v", err)
	}
	runs, err = st.ListCleanupRuns(5)
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListCleanupRuns = %d rows, %v", len(runs), err)
	}
	if runs[0].BytesReclaimed != 4096 {
		t.Errorf("newest run bytes_reclaimed = %d; want 4096", runs[0].BytesReclaimed)
	}
}

// TestAuthorAndRevisionsReconcileOnAnOlderDatabase covers the same upgrade path for the hand-written
// report work (ADR 0026): reports.author, and the report_revisions table beside it.
//
// Worth pinning precisely because that work is not released yet — v0.4.42 and v0.4.43 are on main
// and untagged — so this path has never run against a real database. Its first execution will be on
// every existing deployment at once, at startup, before anything is serving.
func TestAuthorAndRevisionsReconcileOnAnOlderDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// reports as v0.4.41 left it: version present (it shipped in v0.4.39), author absent.
	if _, err := db.Exec(`CREATE TABLE reports(id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT, kind TEXT, run_id TEXT,
		owner_group BIGINT, source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT,
		version TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("seed reports: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`); err != nil {
		t.Fatalf("seed schema baseline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reports(symbol,rdate,rtype,title,body_md,version,sent_at)
		VALUES('600519','2026-07-23','深度分析','工作流写的','正文','','2026-07-23T09:00:00Z')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	st := &Store{db: db, driver: "sqlite"}
	if err := st.init(); err != nil {
		t.Fatalf("init (should reconcile author + report_revisions): %v", err)
	}
	if !st.columnExists("reports", "author") {
		t.Fatal("ensureColumns did not add reports.author")
	}
	if !st.tableExists("report_revisions") {
		t.Fatal("report_revisions was not created")
	}

	// The pre-existing row survives, keeps its words, and gains the default version rather than the
	// empty string — the reconcile ADR 0024 depends on, since a NULL or '' compares DISTINCT inside
	// the unique identity index and would admit the duplicates that index exists to forbid.
	rep, _ := st.GetNew(1, nil)
	if rep == nil {
		t.Fatal("the pre-existing report did not survive the upgrade")
	}
	if rep.MD != "正文" || rep.Title != "工作流写的" {
		t.Errorf("row not preserved: %+v", rep)
	}
	if rep.Version != defaultVersionName {
		t.Errorf("version = %q; every pre-version row must resolve to the default", rep.Version)
	}
	if rep.Author != "" {
		t.Errorf("author = %q; a workflow's report has no byline and there is no backfill", rep.Author)
	}

	// And the manual version is seeded, which is what makes a hand-written report expressible at all
	// on a database that predates it.
	if _, ok := st.Version(manualVersionName); !ok {
		t.Error("the manual version was not seeded on the upgraded database")
	}
}
