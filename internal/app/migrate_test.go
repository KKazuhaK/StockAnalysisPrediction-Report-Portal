package app

import (
	"reflect"
	"testing"
)

func colNames(cols []schemaCol) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.name
	}
	return out
}

// TestParseCreateTable locks the base-schema column extractor. It drives the shape check and the
// backup table walk, so a column it quietly drops is a column neither of them can refuse: it pulls
// plain columns, skips the primary-key column and table-level constraints, is paren-aware for a
// composite key, and ignores non-table statements.
func TestParseCreateTable(t *testing.T) {
	table, cols, ok := parseCreateTable(`CREATE TABLE IF NOT EXISTS link_groups(
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT DEFAULT '', mode TEXT DEFAULT 'row',
		show_label INTEGER DEFAULT 1, icon TEXT DEFAULT '', ord INTEGER DEFAULT 0)`)
	if !ok || table != "link_groups" {
		t.Fatalf("parse link_groups: ok=%v table=%q", ok, table)
	}
	if got, want := colNames(cols), []string{"name", "mode", "show_label", "icon", "ord"}; !reflect.DeepEqual(got, want) {
		t.Errorf("columns = %v, want %v (id PK skipped)", got, want)
	}

	// A composite table-level PRIMARY KEY(...) is skipped, and its inner comma must not split a
	// column — the paren-aware splitter guards that.
	_, cols2, ok2 := parseCreateTable(`CREATE TABLE IF NOT EXISTS app_files(
		app_id TEXT, path TEXT, ctype TEXT, content BLOB, PRIMARY KEY(app_id, path))`)
	if !ok2 {
		t.Fatal("parse app_files: ok=false")
	}
	if got, want := colNames(cols2), []string{"app_id", "path", "ctype", "content"}; !reflect.DeepEqual(got, want) {
		t.Errorf("app_files columns = %v, want %v", got, want)
	}

	if _, _, ok := parseCreateTable(`CREATE INDEX IF NOT EXISTS idx_x ON t(a, b)`); ok {
		t.Error("a CREATE INDEX statement should not parse as a table")
	}
}

// TestParseIndexName covers the other half of the shape check: the name a CREATE INDEX declares is
// what has to exist, and a name the parser gets wrong is an index the boundary silently stops
// checking — which is how an index quietly goes missing and takes a uniqueness guarantee with it.
func TestParseIndexName(t *testing.T) {
	cases := []struct {
		stmt string
		want string
		ok   bool
	}{
		{"CREATE INDEX IF NOT EXISTS idx_batch_jobs_run_at ON batch_jobs(run_at)", "idx_batch_jobs_run_at", true},
		{"CREATE UNIQUE INDEX IF NOT EXISTS idx_reports_ident ON reports(symbol, rdate)", "idx_reports_ident", true},
		{"CREATE INDEX idx_plain ON t(a)", "idx_plain", true},
		{"CREATE UNIQUE INDEX idx_u ON t(a)", "idx_u", true},
		{"CREATE TABLE IF NOT EXISTS reports(id INTEGER PRIMARY KEY)", "", false},
		{"CREATE VIEW v AS SELECT 1", "", false},
	}
	for _, tc := range cases {
		if tc.stmt[0:6] != "CREATE" {
			t.Fatalf("unusable case %q", tc.stmt)
		}
		got, ok := parseIndexName(tc.stmt)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseIndexName(%q) = (%q, %v), want (%q, %v)", tc.stmt, got, ok, tc.want, tc.ok)
		}
	}

	// Every index the base schema declares must be parsed, or checkBaseShape skips it: the two
	// halves of this test have to agree about which statements are indexes at all.
	declared := 0
	for _, stmt := range (&Store{driver: "sqlite"}).baseSchemaStmts() {
		if !isIndexDDL(stmt) {
			continue
		}
		declared++
		if name, ok := parseIndexName(stmt); !ok || name == "" {
			t.Errorf("isIndexDDL accepts a statement parseIndexName cannot read: %q", stmt)
		}
	}
	if declared == 0 {
		t.Fatal("the base schema declares no indexes — the check above proved nothing")
	}
}
