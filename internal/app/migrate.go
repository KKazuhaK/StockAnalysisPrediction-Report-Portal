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
