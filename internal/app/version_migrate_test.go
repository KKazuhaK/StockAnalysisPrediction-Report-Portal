package app

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpgradeOfAPreVersionDatabase is the migration assertion that matters, run against a database
// built by the OLD schema: four-column identity index, no version column. It must come out with
// every row intact, every row on the default version, and a five-column index — and crucially the
// same NUMBER of rows, because the failure mode this guards is the v0.3.0 one where an identity
// change silently merged 626 distinct reports.
func TestUpgradeOfAPreVersionDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// A raw handle, so init() does NOT run and we can lay down the pre-version shape by hand.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db, driver: "sqlite"}
	// Build the pre-version shape by hand: this is what a v0.4.0 database looks like.
	old.exec(`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`)
	old.exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`)
	old.exec(`CREATE TABLE reports(id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT, kind TEXT, run_id TEXT,
		owner_group BIGINT, source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT)`)
	old.exec(`CREATE UNIQUE INDEX idx_reports_ident ON reports(symbol, rdate, rtype, title)`)
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

	// Now open it with the CURRENT code, which runs init() and therefore the migration.
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("upgrading a pre-version database failed: %v", err)
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
	if idx == "" || !strings.Contains(idx, "version") {
		t.Errorf("identity index after upgrade = %q, want it to cover version", idx)
	}
	// The pre-existing rows still behave: a version-less re-ingest overwrites in place.
	before := n
	if _, created, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-01", RType: "投资决策",
		Title: "茅台投资决策", MD: "rewritten"}); err != nil || created {
		t.Errorf("re-ingesting a migrated row: created=%v err=%v, want an in-place overwrite", created, err)
	}
	st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	if n != before {
		t.Errorf("a re-ingest added a row (%d → %d)", before, n)
	}
	// And the two thematic reports that only their titles tell apart are still two reports.
	st.queryRow(`SELECT COUNT(*) FROM reports WHERE symbol='' AND rdate='2026-07-01' AND rtype='主题研究'`).Scan(&n)
	if n != 2 {
		t.Errorf("thematic reports sharing (code,date,subtype) = %d rows, want 2 kept distinct by title", n)
	}
	// Running init twice must not rebuild the index or disturb anything (idempotence).
	if err := st.init(); err != nil {
		t.Errorf("a second init failed: %v", err)
	}
}
