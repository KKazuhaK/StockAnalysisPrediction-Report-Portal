package app

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// The database compatibility boundary. This release reads exactly one shape — the v0.4.72 schema,
// which is what baseSchemaStmts declares — and converts nothing: no ADD COLUMN, no backfill, no
// adoption step, no stamping of a database that already exists. A database that does not already
// satisfy the baseline is refused before any statement runs, with the release it has to be taken
// through named in the message.
// See docs/adr/0034-calver-baseline-and-database-compatibility-reset.md.

const schemaBaseline = 2

// legacyBridge is the last release of the pre-CalVer line. It accumulated every migration the older
// lines had, so it is the single step a database below the baseline has to take before this release
// will read it. Nothing in this binary performs that step: the bridge is that release's binary.
const legacyBridge = "v0.4.72"

// schemaState is the read-only verdict on the database in front of us. A refused database is
// reported as an error, so the state is only meaningful when classifySchema returns nil.
type schemaState int

const (
	// schemaEmpty — nothing here yet: a database this binary creates from scratch.
	schemaEmpty schemaState = iota
	// schemaResume — an interrupted first run of this binary. Every create statement is
	// IF NOT EXISTS, so finishing it is safe.
	schemaResume
	// schemaCurrent — the accepted baseline. Verify and start; write nothing.
	schemaCurrent
)

// schemaVersion reads the generation marker. Absent (or unparseable) reads as generation 1, the
// pre-v0.2 shape that predates the marker. The generation is decoupled from the release tag:
// generation 2 shipped in release v0.2.0.
func (s *Store) schemaVersion() int {
	raw, ok := s.rawSchemaVersion()
	if !ok {
		return 1
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return 1
}

// rawSchemaVersion returns the marker verbatim, and whether it is present and non-empty. The
// distinction is load-bearing: an absent marker is a state this release can resume, while a marker
// that is present and nonsense is one it refuses rather than interprets.
func (s *Store) rawSchemaVersion() (string, bool) {
	var v sql.NullString
	if err := s.queryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v); err != nil {
		return "", false
	}
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return "", false
	}
	return v.String, true
}

func (s *Store) setSchemaVersion(n int) error {
	_, err := s.exec(`INSERT INTO meta(k,v) VALUES('schema_version',?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.Itoa(n))
	return err
}

// classifySchema decides what the database is, and refuses everything that is not the accepted
// baseline. It is read-only, and init calls it before any DDL: a refused database is therefore never
// partially created, partially altered or partially seeded. That ordering is the fix, not a detail —
// the create path used to run first, so a database that was about to be rejected had already had
// this release's tables added to it by the time anything complained.
//
// The generation marker narrows the field; the SHAPE decides. A marker alone never certifies a
// database, which is exactly the case it would otherwise wave through: generation 2 carrying an
// older release's tables.
func (s *Store) classifySchema() (schemaState, error) {
	// Generation 1 predates the meta table entirely, so "no meta" is either an empty database or a
	// pre-v0.2 one — and the marker tables are what tell them apart.
	if !s.tableExists("meta") {
		for _, table := range []string{"reports", "users", "links", "batch_jobs"} {
			if s.tableExists(table) {
				return schemaEmpty, fmt.Errorf("unsupported pre-v0.2 database (found table %q): take it "+
					"through the legacy releases — v0.2.26 first, then each line's last release — until "+
					"%s has started it once, then start this release. This release reads only the %s "+
					"schema and converts nothing", table, legacyBridge, legacyBridge)
			}
		}
		return schemaEmpty, nil
	}

	// v0.4.1's tables. Its adoption steps were removed in v0.4.3, and booting anyway is the
	// dangerous outcome, not a merciful one: the keyring is now read from `meta`, so the portal
	// would mint a SECOND data key and every secret sealed under the first — SSO client secrets,
	// the SAML SP key — becomes undecryptable at runtime with no error; and the group rules would
	// read as empty, silently re-placing every federated user into their provider's default OU at
	// the next login. Refusing turns silent loss into a message.
	for _, table := range []string{"sso_keyring", "sso_group_rules", "sso_auth_requests"} {
		if s.tableExists(table) {
			return schemaEmpty, fmt.Errorf("this database was written by v0.4.1, whose upgrade path is "+
				"no longer supported (found table %q): recreate it, or run v0.4.2 once and then take it "+
				"to %s", table, legacyBridge)
		}
	}

	raw, ok := s.rawSchemaVersion()
	if !ok {
		// No marker and a meta table: a generation-1 database has no meta at all, so this is either a
		// first run that died between creating `meta` and stamping it, or something this release
		// cannot name. Resuming is safe only while the database is indistinguishable from one nobody
		// has written to — so no table may hold a row, and every table that exists must already match
		// the declared shape. The second half is what separates a half-created current schema from an
		// older release's empty tables, which look identical by row count alone.
		empty, err := s.noRowsAnywhere()
		if err != nil {
			return schemaEmpty, err
		}
		if !empty {
			return schemaEmpty, fmt.Errorf("unsupported database: it has a meta table but no "+
				"schema_version marker and it holds data, so this release cannot tell which release "+
				"wrote it. If that release is older than %s, take the database through %s first",
				legacyBridge, legacyBridge)
		}
		if err := s.checkBaseShape(false); err != nil {
			return schemaEmpty, err
		}
		return schemaResume, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return schemaEmpty, fmt.Errorf("this database has a damaged schema_version marker (%q): "+
			"refusing to guess what wrote it", raw)
	}
	if n > schemaBaseline {
		return schemaEmpty, fmt.Errorf("this database was written by a newer release (schema generation "+
			"%d; this build reads %d): upgrade the portal before starting it", n, schemaBaseline)
	}
	if n < schemaBaseline {
		return schemaEmpty, fmt.Errorf("unsupported schema generation %d: take the database through the "+
			"legacy releases until %s has started it once, then start this release. This release reads "+
			"only the %s schema and converts nothing", n, legacyBridge, legacyBridge)
	}
	return schemaCurrent, nil
}

// ensureColumns is the additive-column step of the upgrade ladder: the place an ALTER TABLE ADD
// COLUMN is written when a release adds a column and has to carry an older database forward.
//
// The step is EMPTY, and that is the design rather than an oversight. The runtime sits at a database
// baseline and converts nothing, so there is no column for it to add: a database missing one is
// refused by verifyBaseSchema above, and the release notes name the version it has to be taken
// through instead. The ladder below (and upgrade_v04.go's shape, had it survived) is what stays —
// entry points that a major change writes a step into and clears again, so that adding one is a
// function body and a call, never a new startup path.
func (s *Store) ensureColumns() error { return nil }

// duplicateColumnErr reports whether an ADD COLUMN failed only because the column already exists —
// the idempotency signal an additive step needs, since the same column may be added on a database
// that already has it.
//
// Unused today because every additive step is empty. Kept, with its linter exemption, because it
// belongs to those steps and not to this release: writing one back is meant to be a body and a call
// rather than a re-derivation of how to detect a duplicate column on two drivers.
//
//lint:ignore U1000 the additive steps of the upgrade ladder are empty for now
func duplicateColumnErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

// verifyBaseSchema is the accepted-baseline check: every table, every column and every index
// baseSchemaStmts declares must already exist. It is what replaced the ADD COLUMN reconciliation that
// used to run at this point, and the name says what it does rather than what it used to do.
func (s *Store) verifyBaseSchema() error { return s.checkBaseShape(true) }

// checkBaseShape verifies a database against baseSchemaStmts — the same declaration list that
// creates it, so the two cannot drift. This is where the old reconciliation used to run ADD COLUMN
// for every column an older database lacked; it now refuses instead, because a column set this
// release would silently add is a column set the release it replaced might not understand.
//
// A superset is accepted: a leftover column from a withdrawn feature is still a v0.4.72 database,
// and refusing one would reject databases the old binary itself produced.
//
// requireEverything distinguishes the two callers. The accepted baseline must have all of it. A
// mid-create database has only a prefix, so there only what EXISTS has to match.
func (s *Store) checkBaseShape(requireEverything bool) error {
	for _, stmt := range s.baseSchemaStmts() {
		if isIndexDDL(stmt) {
			name, ok := parseIndexName(stmt)
			if !ok || s.indexExists(name) || !requireEverything {
				continue
			}
			return s.shapeError("index " + name)
		}
		table, cols, ok := parseCreateTable(stmt)
		if !ok {
			continue
		}
		if !s.tableExists(table) {
			if !requireEverything {
				continue
			}
			return s.shapeError("table " + table)
		}
		for _, c := range cols {
			if !s.columnExists(table, c.name) {
				return s.shapeError("column " + table + "." + c.name)
			}
		}
	}
	return nil
}

func (s *Store) shapeError(what string) error {
	return fmt.Errorf("unsupported database: this release needs the %s schema and %s is missing, so the "+
		"database predates %s. Start it once with %s to finish its upgrades, back it up, then start this "+
		"release — this release converts nothing itself", legacyBridge, what, legacyBridge, legacyBridge)
}

// noRowsAnywhere reports whether every table in the database is empty.
func (s *Store) noRowsAnywhere() (bool, error) {
	tables, err := s.allTables()
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		var n int
		if err := s.queryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&n); err != nil {
			return false, fmt.Errorf("counting rows in %s: %w", table, err)
		}
		if n > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) allTables() ([]string, error) {
	q := `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	if s.driver == "postgres" {
		q = `SELECT table_name FROM information_schema.tables
			WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY table_name`
	}
	rows, err := s.query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// quoteIdent quotes a SQL identifier for both drivers. Every name that reaches it comes from the
// database's own catalog, but it is quoted rather than trusted: a database can hold a table somebody
// else created, and this is the one place a name is pasted into a statement instead of bound.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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

func (s *Store) indexExists(name string) bool {
	var n int
	if s.driver == "postgres" {
		s.queryRow(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname=?`, name).Scan(&n)
	} else {
		s.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n)
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
		return false
	}
	return found
}

// schemaCol is one parsed column: its name and its full definition.
type schemaCol struct{ name, def string }

// parseCreateTable pulls the table name and its plain column definitions out of a
// "CREATE TABLE IF NOT EXISTS name(...)" statement. ok=false for anything else (CREATE INDEX).
// Table-level constraints (PRIMARY KEY(...)/UNIQUE(...)/FOREIGN/CHECK/CONSTRAINT) and the primary
// -key column itself are skipped: it is shared with the backup table walk (see backupTables), and
// neither the walk nor the shape check has anything to say about a key.
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
			continue // the PK column
		}
		cols = append(cols, schemaCol{name: name, def: item})
	}
	return table, cols, true
}

// parseIndexName pulls the index name out of a CREATE [UNIQUE] INDEX statement, with or without the
// IF NOT EXISTS that every index in baseSchemaStmts carries.
func parseIndexName(stmt string) (string, bool) {
	norm := strings.Join(strings.Fields(stmt), " ")
	if !strings.HasPrefix(norm, "CREATE ") {
		return "", false
	}
	rest := norm[len("CREATE "):]
	for _, opt := range []string{"UNIQUE ", "UNIQUE INDEX "} {
		if strings.HasPrefix(rest, opt) {
			rest = rest[len(opt):]
			break
		}
	}
	if !strings.HasPrefix(rest, "INDEX ") {
		return "", false
	}
	rest = rest[len("INDEX "):]
	rest = strings.TrimPrefix(rest, "IF NOT EXISTS ")
	if i := strings.IndexByte(rest, ' '); i > 0 {
		return rest[:i], true
	}
	return rest, rest != ""
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
