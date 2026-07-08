package app

import (
	"database/sql"
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

// TestParseCreateTable locks the base-schema column extractor that drives ensureColumns: it pulls
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

// TestEnsureColumnsBackfillsAdditive proves the reconciler adds a base-schema column an existing
// table is missing (link_groups.icon) with no hand-written migration, preserving existing rows —
// the mechanism that lets a new additive column be declared once in createBaseSchema.
func TestEnsureColumnsBackfillsAdditive(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1) // share the one in-memory connection (matches OpenStore)
	t.Cleanup(func() { _ = db.Close() })

	// link_groups in the pre-icon shape, already holding a row.
	if _, err := db.Exec(`CREATE TABLE link_groups(id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT DEFAULT '', mode TEXT DEFAULT 'row', show_label INTEGER DEFAULT 1, ord INTEGER DEFAULT 0)`); err != nil {
		t.Fatalf("seed link_groups: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO link_groups(name,mode) VALUES('legacy','popover')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	st := &Store{db: db, driver: "sqlite"}
	if err := st.init(); err != nil {
		t.Fatalf("init (should auto-reconcile the missing column): %v", err)
	}
	if !st.columnExists("link_groups", "icon") {
		t.Fatal("ensureColumns did not add link_groups.icon")
	}
	gs := st.LinkGroups()
	if len(gs) != 1 || gs[0].Name != "legacy" || gs[0].Icon != "" {
		t.Fatalf("row after reconcile = %+v, want the legacy row preserved with empty icon", gs)
	}
}
