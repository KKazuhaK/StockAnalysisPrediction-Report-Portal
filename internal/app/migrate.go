package app

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Schema-migration machinery, kept OUT of the core store (store.go) so the transitional,
// per-version migration steps (migrate_v1_to_v2.go, and future migrate_vN_to_vM.go) can be
// added and deleted without touching the main program. This file is permanent; the individual
// migration-step files are disposable. See docs/adr/0013-v2-schema-consolidation.md.

// schemaVersion reads the internal schema-generation marker from meta. Absent (or unparseable)
// means generation 1 — the pre-v0.2 shape that predates this marker. The generation is
// decoupled from the release tag: generation 2 ships in release v0.2.0.
func (s *Store) schemaVersion() int {
	var v sql.NullString
	s.queryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v)
	if n, err := strconv.Atoi(v.String); err == nil && n > 0 {
		return n
	}
	return 1
}

func (s *Store) setSchemaVersion(n int) error {
	_, err := s.exec(`INSERT INTO meta(k,v) VALUES('schema_version',?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.Itoa(n))
	return err
}

// migrate upgrades an existing database to the current schema generation, then stamps it. A fresh
// database (born at the base schema) runs the pending step as a guarded no-op and is stamped
// straight to the current generation; a genuine older database is folded up. Add a new step here
// (and its migrate_vN_to_vM.go file) per boundary; delete the retired step's file at each major.
func (s *Store) migrate() error {
	cur := s.schemaVersion()
	if cur >= 2 {
		return nil // already current — stay quiet on a normal startup
	}
	log.Printf("schema migration: database at generation %d, upgrading to 2 (%s)", cur, s.driver)
	if err := s.migrateV1toV2(); err != nil {
		return fmt.Errorf("migrate schema v1->v2: %w", err)
	}
	if err := s.setSchemaVersion(2); err != nil {
		return err
	}
	log.Printf("schema migration: complete, now at generation 2")
	return nil
}

// tableExists reports whether a table is present (guards the fold-copy steps in migration).
// The Postgres path assumes the default `public` schema — consistent with the rest of the
// store, which issues only unqualified DDL/DML and never sets a custom search_path.
func (s *Store) tableExists(name string) bool {
	var n int
	if s.driver == "postgres" {
		s.queryRow(`SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema='public' AND table_name=?`, name).Scan(&n)
	} else {
		s.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	}
	return n > 0
}

// columnExists reports whether table.col is present. Only ever called with hardcoded internal
// identifiers, so the SQLite PRAGMA path inlines the table name safely (no user input).
func (s *Store) columnExists(table, col string) bool {
	if s.driver == "postgres" {
		var n int
		s.queryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name=? AND column_name=?`, table, col).Scan(&n)
		return n > 0
	}
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil && name == col {
			return true
		}
	}
	return false
}

// duplicateColumnErr reports whether an ADD COLUMN failed only because the column already exists
// (the idempotency signal for guarded migrations, on both SQLite and Postgres).
func duplicateColumnErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

// ensureColumns auto-reconciles pure-additive columns: for every column declared in the base
// schema (baseSchemaStmts — the single source of truth) that an older database is missing, it runs
// a plain ADD COLUMN. Adding a column carries no data, so it needs no versioned migration step —
// declare the column in createBaseSchema and existing databases pick it up on the next startup.
// Only data MOVES (folding a side table into a column) and DROPS still need an explicit,
// generation-gated step in migrate(). Runs after migrate() so it sees the post-fold shape; a
// column a plain ALTER can't add (a new PK/UNIQUE, or one needing a backfill) surfaces as a hard
// error here — the signal that it genuinely needs a migration instead.
func (s *Store) ensureColumns() error {
	for _, stmt := range s.baseSchemaStmts() {
		table, cols, ok := parseCreateTable(stmt)
		if !ok {
			continue // not a CREATE TABLE (e.g. a CREATE INDEX statement)
		}
		for _, c := range cols {
			if s.columnExists(table, c.name) {
				continue
			}
			ddl := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, c.def)
			if _, err := s.exec(ddl); err != nil && !duplicateColumnErr(err) {
				return fmt.Errorf("ensure column %s.%s [%s]: %w", table, c.name, ddl, err)
			}
		}
	}
	return nil
}

// schemaCol is one parsed column: its name and its full definition (name + type + constraints),
// the latter reused verbatim as the ADD COLUMN body.
type schemaCol struct{ name, def string }

// parseCreateTable pulls the table name and its plain column definitions out of a
// "CREATE TABLE IF NOT EXISTS name(...)" statement. ok=false for anything else (CREATE INDEX).
// Table-level constraints (PRIMARY KEY(...)/UNIQUE(...)/FOREIGN/CHECK/CONSTRAINT) and the primary
// -key column itself are skipped: reconcile only ever adds a plain column, never a key.
func parseCreateTable(stmt string) (table string, cols []schemaCol, ok bool) {
	norm := strings.Join(strings.Fields(stmt), " ") // collapse the multi-line literal to one line
	const prefix = "CREATE TABLE IF NOT EXISTS "
	if !strings.HasPrefix(norm, prefix) {
		return "", nil, false
	}
	rest := norm[len(prefix):]
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return "", nil, false
	}
	table = strings.TrimSpace(rest[:open])
	inner := rest[open+1:]
	if i := strings.LastIndexByte(inner, ')'); i >= 0 {
		inner = inner[:i] // drop the matching outer ')'
	}
	for _, item := range splitTopLevel(inner) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name := strings.Fields(item)[0]
		switch strings.ToUpper(name) { // a table-level constraint, not a column
		case "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "CONSTRAINT":
			continue
		}
		if strings.Contains(strings.ToUpper(item), "PRIMARY KEY") {
			continue // the PK column: can't be ALTER-added and is always present
		}
		cols = append(cols, schemaCol{name: name, def: item})
	}
	return table, cols, true
}

// splitTopLevel splits a comma-separated list, ignoring commas nested inside parentheses (e.g. a
// composite "PRIMARY KEY(app_id, path)" constraint) so each column definition stays intact.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
