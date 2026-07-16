package app

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Schema-reconciliation machinery lives outside the core store. Versioned data-move/drop steps
// are intentionally absent at the v0.3 boundary; the complete v0.2 result is now the base schema.
// See docs/adr/0013-v2-schema-consolidation.md.

const schemaBaseline = 2

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

// requireSchemaBaseline enforces the major-boundary upgrade contract before any schema DDL runs.
// An empty database is allowed and stamped after creation. A v0.1 database must first run the last
// v0.2 release, which owns the now-squashed data movement; v0.3 never guesses or rewrites that data.
func (s *Store) requireSchemaBaseline() (fresh bool, err error) {
	if !s.tableExists("meta") {
		for _, table := range []string{"reports", "users", "links", "batch_jobs"} {
			if s.tableExists(table) {
				return false, fmt.Errorf("unsupported pre-v0.2 database: first run v0.2.26 successfully, then upgrade to v0.3.0")
			}
		}
		return true, nil
	}
	if version := s.schemaVersion(); version < schemaBaseline {
		return false, fmt.Errorf("unsupported schema generation %d: first run v0.2.26 successfully, then upgrade to v0.3.0", version)
	}
	return false, nil
}

// tableExists reports whether a table is present.
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
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil && name == col {
			found = true
			break
		}
	}
	if rows.Err() != nil {
		return false // PRAGMA iteration failed; treat the column as absent (the guarded ADD COLUMN is idempotent)
	}
	return found
}

// duplicateColumnErr reports whether an ADD COLUMN failed only because the column already exists
// (the idempotency signal for additive reconciliation, on both SQLite and Postgres).
func duplicateColumnErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

// ensureColumns auto-reconciles pure-additive columns: for every column declared in the base
// schema (baseSchemaStmts — the single source of truth) that an older database is missing, it runs
// a plain ADD COLUMN. Adding a column carries no data, so it needs no versioned migration step —
// declare the column in createBaseSchema and existing databases pick it up on the next startup.
// At a major boundary, databases must already satisfy the release-line baseline before this runs.
// A column a plain ALTER cannot add (a new PK/UNIQUE, or one needing a backfill) surfaces as a hard
// error — the signal that it needs an explicit design decision in a later release line.
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
