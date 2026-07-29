package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The upgrade path that has to work is v0.3.10 → here. Production runs v0.3.10; v0.4.0 was tagged
// but never deployed to anything, so no database anywhere is in the v0.4.0 shape.
//
// testdata/schema_v0.3.10.sql is the schema a v0.3.10 database ACTUALLY has, dumped from one created
// by the v0.3.10 binary itself. Testing against a hand-written approximation of the old shape would
// only prove the migration survives my memory of it, which is precisely the assumption that makes
// migrations dangerous.

// v0310Store creates a database with the real v0.3.10 schema, with no init() run over it.
func v0310Store(t *testing.T) (path string, st *Store) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("testdata", "schema_v0.3.10.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "v0310.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("applying the v0.3.10 schema: %v", err)
	}
	return path, &Store{db: db, driver: "sqlite"}
}

// TestUpgradeFromV0310 is the migration assertion that matters. The failure mode it guards is the
// v0.3.0 one, where an identity change silently merged 626 distinct reports: the count must not
// move, and in particular the two thematic reports that only their titles tell apart must stay two.
func TestUpgradeFromV0310(t *testing.T) {
	path, old := v0310Store(t)
	old.exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`)

	rows := [][]string{
		{"600519", "2026-07-01", "投资决策", "茅台投资决策"},
		{"600519", "2026-07-01", "估值分析", "茅台估值"},   // same code+date, different subtype
		{"600519", "2026-07-02", "投资决策", "茅台投资决策"}, // same code+subtype, different date
		{"", "2026-07-01", "主题研究", "白酒行业展望"},       // thematic: no code, told apart by title
		{"", "2026-07-01", "主题研究", "新能源行业展望"},      // ditto — these two MUST stay distinct
	}
	for _, r := range rows {
		if _, err := old.exec(`INSERT INTO reports(symbol,rdate,rtype,title,body_md) VALUES(?,?,?,?,?)`,
			r[0], r[1], r[2], r[3], "body of "+r[3]); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	// Open with the CURRENT code, which runs init() and therefore the migration.
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("upgrading a real v0.3.10 database failed: %v", err)
	}
	defer st.Close()

	var n int
	st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	if n != len(rows) {
		t.Errorf("row count = %d after upgrade, want %d — an identity change must never merge reports", n, len(rows))
	}
	var offDefault int
	st.queryRow("SELECT COUNT(*) FROM reports WHERE version <> ?", st.DefaultVersion()).Scan(&offDefault)
	if offDefault != 0 {
		t.Errorf("%d rows are not on the default version", offDefault)
	}
	var idx string
	st.queryRow(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND name='idx_reports_ident'`).Scan(&idx)
	if !strings.Contains(idx, "version") {
		t.Errorf("identity index after upgrade = %q, want it to cover version", idx)
	}
	// The v0.4.0 columns this database never had must be present too — the upgrade skips a release.
	for _, col := range []string{"owner_group", "version"} {
		if !st.columnExists("reports", col) {
			t.Errorf("reports.%s missing after upgrade", col)
		}
	}

	// Pre-existing rows still behave: a version-less re-ingest overwrites in place.
	before := n
	if _, created, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-01", RType: "投资决策",
		Title: "茅台投资决策", MD: "rewritten"}); err != nil || created {
		t.Errorf("re-ingesting a migrated row: created=%v err=%v, want an in-place overwrite", created, err)
	}
	st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	if n != before {
		t.Errorf("a re-ingest added a row (%d → %d)", before, n)
	}
	st.queryRow(`SELECT COUNT(*) FROM reports WHERE symbol='' AND rdate='2026-07-01' AND rtype='主题研究'`).Scan(&n)
	if n != 2 {
		t.Errorf("thematic reports sharing (code,date,subtype) = %d rows, want 2 kept distinct by title", n)
	}
	// And a second version of a migrated report is a NEW row, not an overwrite of it — the whole
	// reason version joined the identity tuple.
	pub, created, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-01", RType: "投资决策",
		Title: "茅台投资决策", Version: "对外版", MD: "public"})
	if err != nil || !created {
		t.Fatalf("publishing a second version: created=%v err=%v", created, err)
	}
	if r, _ := st.GetNew(pub, nil); r == nil || r.MD != "public" {
		t.Error("the second version must be its own row")
	}
	st.queryRow(`SELECT COUNT(*) FROM reports WHERE symbol='600519' AND rdate='2026-07-01' AND rtype='投资决策'`).Scan(&n)
	if n != 2 {
		t.Errorf("after publishing one more version: %d rows, want 2", n)
	}
}

// TestUpgradeIsIdempotentAndCheap proves the migration bills itself to the upgrade run and not to
// every restart after it — an unguarded full-table backfill cost 706ms on every boot at 200k
// reports before this was caught.
func TestUpgradeIsIdempotentAndCheap(t *testing.T) {
	path, old := v0310Store(t)
	old.exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`)
	old.exec(`INSERT INTO reports(symbol,rdate,rtype,title) VALUES('600519','2026-07-01','投资决策','T')`)
	old.Close()

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A second init must be a no-op: the identity index already covers version, which is only
	// possible if the backfill completed, so there is nothing left to scan for.
	if err := st.init(); err != nil {
		t.Fatalf("a second init failed: %v", err)
	}
	if !st.identIndexCoversVersion() {
		t.Error("the identity index must still cover version after a second init")
	}
	var n int
	st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	if n != 1 {
		t.Errorf("row count = %d after a second init, want 1", n)
	}
}
