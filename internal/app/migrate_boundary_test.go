package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The database compatibility boundary (ADR 0034). The accepted baseline is the v0.4.72 shape: a
// database that satisfies it starts with no DDL and no writes at all, and everything older is
// refused BEFORE any statement runs. The refusal has to be provable, so every rejection test
// compares a full picture of the database before and after — an error string alone cannot show that
// nothing was written, and "it errored" is not the same claim as "it did not touch my data".

// rawStoreDB is an in-memory sqlite handle with no init() run over it.
func rawStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // share the one in-memory connection, matching OpenStore
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

// describe is a canonical, order-stable picture of every table's columns and row count.
func describe(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for _, name := range names {
		colRows, err := db.Query(`PRAGMA table_info(` + quoteIdent(name) + `)`)
		if err != nil {
			t.Fatal(err)
		}
		cols := 0
		for colRows.Next() {
			cols++
		}
		colRows.Close()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(name)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		// newline-separated and sorted, so the comparison is over the whole picture and not a prefix
		b.WriteString(name)
		b.WriteString(" cols=")
		b.WriteString(strconv.Itoa(cols))
		b.WriteString(" rows=")
		b.WriteString(strconv.Itoa(n))
		b.WriteString("\n")
	}
	return b.String()
}

// fileStore creates a database at a temp path with the real binary and returns the path, so the
// shape on disk can be edited (a renamed column, an added leftover column) before reopening.
func fileStore(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mutateFile(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execAll(t, db, stmts...)
}

// ---------- the accepted baseline ----------

func TestFreshStoreIsStampedAtCurrentBaseline(t *testing.T) {
	st := newTestStore(t)
	if got := st.schemaVersion(); got != schemaBaseline {
		t.Fatalf("fresh schema version = %d, want %d", got, schemaBaseline)
	}
}

// TestReopeningTheAcceptedShapeWritesNothing is the "seamless" guarantee: a database already at the
// v0.4.72 shape is read, accepted, and left byte-identical. The failure this guards is not a crash
// but erosion — an ADD COLUMN here, a re-stamp there, each invisible until a rollback to the old
// binary meets a database the new one quietly reshaped.
func TestReopeningTheAcceptedShapeWritesNothing(t *testing.T) {
	path := fileStore(t, "accepted.db")
	mutateFile(t, path, `INSERT INTO reports(symbol,rdate,rtype,title,body_md) VALUES('600519','2026-07-01','投资决策','T','body')`,
		`INSERT INTO meta(k,v) VALUES('panel_note','untouched')`)

	before := describeFile(t, path)
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening the accepted shape failed: %v", err)
	}
	defer st.Close()
	after := describeFile(t, path)

	if before != after {
		t.Errorf("reopening the accepted shape changed the database:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
	var note string
	st.queryRow(`SELECT v FROM meta WHERE k='panel_note'`).Scan(&note)
	if note != "untouched" {
		t.Errorf("existing meta row = %q, want it untouched", note)
	}
	if got := st.schemaVersion(); got != schemaBaseline {
		t.Errorf("schema version after reopen = %d, want %d", got, schemaBaseline)
	}
}

// A leftover column from a feature that shipped and was withdrawn is still a v0.4.72 database. The
// boundary is "does it satisfy the baseline", not "does it match exactly" — refusing a superset
// would reject the databases the old binary itself produced.
func TestASupersetShapeIsAccepted(t *testing.T) {
	path := fileStore(t, "superset.db")
	mutateFile(t, path, `ALTER TABLE reports ADD COLUMN legacy_leftover TEXT DEFAULT ''`)

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("a database with a leftover column must still open: %v", err)
	}
	defer st.Close()
	if !st.columnExists("reports", "legacy_leftover") {
		t.Error("opening the database dropped the leftover column")
	}
}

// ---------- rejections ----------

// TestLegacyShapesAreRejectedBeforeMutation drives every state the boundary has to refuse, and
// checks the same two things each time: the message names the v0.4.72 bridge, and the database is
// exactly as it was.
func TestLegacyShapesAreRejectedBeforeMutation(t *testing.T) {
	cases := []struct {
		name  string
		seed  []string
		wants string
	}{
		{
			name: "pre-v0.2 has no meta table at all",
			seed: []string{
				`CREATE TABLE users(username TEXT PRIMARY KEY, password_hash TEXT, role TEXT)`,
				`INSERT INTO users(username,password_hash,role) VALUES('alice','kept','admin')`,
			},
			wants: "v0.4.72",
		},
		{
			name: "generation 1",
			seed: []string{
				`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
				`INSERT INTO meta(k,v) VALUES('schema_version','1')`,
			},
			wants: "v0.4.72",
		},
		{
			name: "a v0.4.1 database",
			seed: []string{
				`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
				`INSERT INTO meta(k,v) VALUES('schema_version','2')`,
				`CREATE TABLE sso_keyring(id INTEGER PRIMARY KEY, name TEXT)`,
				`INSERT INTO sso_keyring(name) VALUES('data-key')`,
			},
			wants: "v0.4.1",
		},
		{
			name: "a newer schema generation",
			seed: []string{
				`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
				`INSERT INTO meta(k,v) VALUES('schema_version','3')`,
			},
			wants: "newer",
		},
		{
			name: "a damaged marker",
			seed: []string{
				`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
				`INSERT INTO meta(k,v) VALUES('schema_version','two')`,
			},
			wants: "schema_version",
		},
		{
			name: "meta with no marker, holding data",
			seed: []string{
				`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
				`CREATE TABLE reports(symbol TEXT, rdate TEXT, rtype TEXT, title TEXT)`,
				`INSERT INTO reports(symbol,rdate,rtype,title) VALUES('600519','2026-07-01','投资决策','T')`,
			},
			wants: "v0.4.72",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := rawStoreDB(t)
			execAll(t, db, tc.seed...)
			before := describe(t, db)

			err := (&Store{db: db, driver: "sqlite"}).init()
			if err == nil {
				t.Fatalf("init accepted a database it must refuse")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not mention %q", err, tc.wants)
			}
			if after := describe(t, db); after != before {
				t.Errorf("a refused database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
			}
			// The specific failure the pre-v0.2 case is really about: the legacy row must survive.
			if tc.name == "pre-v0.2 has no meta table at all" {
				var hash string
				if err := db.QueryRow(`SELECT password_hash FROM users WHERE username='alice'`).Scan(&hash); err != nil {
					t.Fatal(err)
				}
				if hash != "kept" {
					t.Errorf("legacy row changed to %q", hash)
				}
			}
		})
	}
}

// A database at generation 2 whose tables are an older release's tables is still an older release's
// database. The generation marker is not evidence on its own — this is the case a marker-only check
// waves through, and the reports.identity change (v0.3.0, 626 reports merged) is what that costs.
func TestAMissingColumnIsRejected(t *testing.T) {
	path := fileStore(t, "older.db")
	// Rename rather than drop: it leaves the rest of the table, and any index, exactly as they were,
	// so the only difference from the accepted shape is the missing column.
	mutateFile(t, path, `ALTER TABLE cleanup_runs RENAME COLUMN bytes_reclaimed TO bytes_reclaimed_old`)

	before := describeFile(t, path)
	_, err := OpenStore("sqlite", path)
	if err == nil {
		t.Fatal("init accepted a database missing a declared column")
	}
	if !strings.Contains(err.Error(), "bytes_reclaimed") {
		t.Errorf("error %q does not name the missing column", err)
	}
	if !strings.Contains(err.Error(), "v0.4.72") {
		t.Errorf("error %q does not name the bridge release", err)
	}
	if after := describeFile(t, path); after != before {
		t.Errorf("a refused database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

func TestAMissingTableIsRejected(t *testing.T) {
	path := fileStore(t, "notable.db")
	mutateFile(t, path, `ALTER TABLE announcements RENAME TO announcements_old`)

	_, err := OpenStore("sqlite", path)
	if err == nil {
		t.Fatal("init accepted a database missing a declared table")
	}
	if !strings.Contains(err.Error(), "announcements") {
		t.Errorf("error %q does not name the missing table", err)
	}
}

// The real thing: testdata/schema_v0.3.10.sql is the schema a v0.3.10 database actually has, dumped
// from one the v0.3.10 binary created. It is the shape production was on, and this release reads
// none of it — so the fixture now proves the refusal rather than an upgrade.
func TestTheV0310ShapeIsRejected(t *testing.T) {
	path, err := buildFromFixture(t, "schema_v0.3.10.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := describeFile(t, path)

	_, err = OpenStore("sqlite", path)
	if err == nil {
		t.Fatal("init accepted a real v0.3.10 database")
	}
	if !strings.Contains(err.Error(), "v0.4.72") {
		t.Errorf("error %q does not name the bridge release", err)
	}
	if after := describeFile(t, path); after != before {
		t.Errorf("the v0.3.10 database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

// ---------- interrupted fresh initialization ----------

// TestInterruptedFreshInitializationIsResumed covers the one state that is neither a legacy database
// nor a finished one: the process died between creating `meta` and stamping it. Every statement in
// the create path is IF NOT EXISTS, so finishing it is safe — but only while it is indistinguishable
// from a schema nobody has written to yet, which is why a table with the wrong shape, or any row at
// all, turns it back into a refusal.
func TestInterruptedFreshInitializationIsResumed(t *testing.T) {
	db := rawStoreDB(t)
	st := &Store{db: db, driver: "sqlite"}
	// A genuine prefix of the create path — every table, before the indexes and before the stamp —
	// which is where a first run that dies leaves the database. Built from the declarations
	// themselves so the fixture cannot drift from the schema it is imitating.
	for _, stmt := range st.baseSchemaStmts() {
		if isIndexDDL(stmt) {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding an interrupted create: %v", err)
		}
	}

	if err := st.init(); err != nil {
		t.Fatalf("an interrupted fresh initialization must be resumable: %v", err)
	}
	if got := st.schemaVersion(); got != schemaBaseline {
		t.Errorf("resumed database is stamped %d, want %d", got, schemaBaseline)
	}
	if !st.indexExists("idx_reports_ident") {
		t.Error("the resumed initialization did not finish the indexes")
	}
	if _, created, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-01", RType: "投资决策", Title: "T"}); err != nil || !created {
		t.Errorf("the resumed database is not usable: created=%v err=%v", created, err)
	}
}

// A table left by an OLDER release at that point is not a half-created current table, even though
// both have no rows and no marker. Only the columns tell them apart.
func TestAnEmptyTableFromAnOlderShapeIsStillRejected(t *testing.T) {
	db := rawStoreDB(t)
	execAll(t, db,
		`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE reports(id INTEGER PRIMARY KEY AUTOINCREMENT, symbol TEXT, rdate TEXT,
			rtype TEXT, title TEXT)`,
	)
	before := describe(t, db)

	err := (&Store{db: db, driver: "sqlite"}).init()
	if err == nil {
		t.Fatal("an empty table from an older shape must not be resumed")
	}
	if !strings.Contains(err.Error(), "v0.4.72") {
		t.Errorf("error %q does not name the bridge release", err)
	}
	if after := describe(t, db); after != before {
		t.Errorf("a refused database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

// ---------- fixture helpers ----------

func buildFromFixture(t *testing.T, name string) (string, error) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		return "", err
	}
	path := filepath.Join(t.TempDir(), strings.TrimSuffix(name, ".sql")+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec(string(schema)); err != nil {
		return "", err
	}
	return path, nil
}

func describeFile(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	return describe(t, db)
}
