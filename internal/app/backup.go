package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/version"
)

// Backup and restore for the whole database, as two CLI subcommands.
//
// Everything the portal owns lives in the database — accounts, groups, reports, apps, tokens,
// settings — and until now there was no way to take a copy of it or put one back. The Docker stack
// keeps Postgres in a named volume, so `docker compose down -v` (the documented way to reset a
// stack) deletes it, and `pg_dump` is not reachable from the portal's own image.
//
// It is deliberately ONE format for both drivers rather than a wrapper around pg_dump/`.dump`:
//   - the same command works whichever driver a deployment runs, so the runbook has one line in it;
//   - a dump is portable BETWEEN drivers, which makes "outgrew SQLite, move to Postgres" a supported
//     move rather than a rewrite;
//   - it needs no client binaries in the image (the release image carries the portal and nothing else).
//
// The cost is that it is a logical dump, not a physical one: it restores into the schema the running
// binary creates, so a backup taken by a NEWER build can carry a column this build has never heard
// of. That case is a hard error naming the column, never a silent drop — see restoreStream.
//
// The file is JSON Lines: one header object, then, per table, a `table` object followed by its
// `row` objects. Line-oriented so a dump streams in constant memory in both directions and stays
// greppable; `-` means stdin/stdout, so `backup - | gzip` and `zcat … | restore -` compose.

const (
	backupFormat        = "report-portal-backup"
	backupFormatVersion = 1
)

// backupHeader is the first line of a dump. Driver and app version are recorded for the operator
// reading a file months later, not for validation: a dump restores across drivers by design.
type backupHeader struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// SchemaVersion is the database's generation marker (ADR 0013), and it is in the HEADER rather
	// than left to be discovered among the `meta` rows for a reason: a restore has to be able to
	// refuse a dump BEFORE it deletes anything, and `meta` sorts into the middle of the file.
	SchemaVersion int      `json:"schema_version"`
	CreatedAt     string   `json:"created_at"`
	Driver        string   `json:"driver"`
	AppVersion    string   `json:"app_version"`
	Tables        []string `json:"tables"`
}

// backupSection introduces one table. Columns are recorded per dump rather than assumed, so a file
// stays readable after the schema grows a column.
type backupSection struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// b64Key tags a binary value inside a row. Only app_files.content is binary today; the tag exists
// so that stays true by construction rather than by luck.
const b64Key = "$b64"

// backupTables lists every table in the current schema, from the schema declaration itself so a new
// table is included the day it is declared and nobody has to remember a second list.
func (s *Store) backupTables() []string {
	seen := map[string]bool{}
	var out []string
	for _, stmt := range s.baseSchemaStmts() {
		t, _, ok := parseCreateTable(stmt)
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out) // stable across builds, so two dumps of the same data diff cleanly
	return out
}

// Backup writes a full dump of the configured database to path ("-" for stdout).
func Backup(cfgPath, path string) (tables int, rows int64, err error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, 0, fmt.Errorf("config: %w", err)
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, 0, err
	}
	defer st.Close()

	out := io.Writer(os.Stdout)
	if path != "-" {
		// 0600: a dump carries password hashes, API token hashes and the sealed SSO keyring. The
		// SSO secrets are useless without config secret_key, which is NOT in here — but the rest is
		// plenty, so the file must not be readable by anyone else.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return 0, 0, err
		}
		defer f.Close()
		// The mode above applies only when O_CREATE actually creates the file. Overwriting an
		// existing one keeps whatever mode it already had — which is precisely the shape of a
		// nightly script writing to the same path forever, so the guarantee has to be made again
		// here or it is a guarantee about the first run only.
		//
		// Fatal rather than a warning: the promise is that this file is not readable by others, and
		// a backup tool that quietly writes password hashes world-readable because a chmod failed
		// has broken the one property it was asked for. A warning in a cron job is not read.
		if err := f.Chmod(0o600); err != nil {
			return 0, 0, fmt.Errorf("cannot make %s readable only by you (it carries password and "+
				"token hashes): %w", path, err)
		}
		out = f
	}
	w := bufio.NewWriterSize(out, 1<<16)
	tables, rows, err = st.dumpTo(w)
	if err != nil {
		return tables, rows, err
	}
	return tables, rows, w.Flush()
}

// dumpTo writes the whole database as JSON Lines inside ONE read transaction, so the dump is a
// single point in time rather than a table-by-table sample of a moving database.
func (s *Store) dumpTo(w io.Writer) (int, int64, error) {
	names := s.backupTables()
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only: rollback is the only ending

	enc := json.NewEncoder(w)
	if err := enc.Encode(backupHeader{
		Format:        backupFormat,
		Version:       backupFormatVersion,
		SchemaVersion: schemaVersionTx(tx),
		CreatedAt:     time.Now().Format(time.RFC3339),
		Driver:        s.driver,
		AppVersion:    version.Version,
		Tables:        names,
	}); err != nil {
		return 0, 0, err
	}

	var total int64
	for _, table := range names {
		n, err := dumpTable(tx, enc, table)
		if err != nil {
			return 0, total, fmt.Errorf("dump %s: %w", table, err)
		}
		total += n
	}
	return len(names), total, nil
}

// schemaVersionTx reads the generation marker ON THE TRANSACTION, not through the pool.
//
// Store.schemaVersion() would be the obvious call and it deadlocks: on SQLite the pool is a single
// connection, this function's own transaction is holding it, and a pooled read would wait for a
// connection it can never get. Reading it here also makes the header describe the same snapshot as
// the rows underneath it, which is what a dump is for.
func schemaVersionTx(tx *sql.Tx) int {
	var v sql.NullString
	if err := tx.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v); err != nil {
		return 0 // unknown rather than guessed; the restore treats 0 as "not recorded"
	}
	if n, err := strconv.Atoi(v.String); err == nil && n > 0 {
		return n
	}
	return 0
}

// dumpTable streams one table. Every statement goes through tx, never the pool: on SQLite the pool
// is one connection, so a pooled read while this transaction is open would wait for a connection
// the transaction itself is holding, and hang for ever.
func dumpTable(tx *sql.Tx, enc *json.Encoder, table string) (int64, error) {
	// Table names come from the schema declaration, never from input, so interpolating is safe —
	// and neither driver accepts an identifier as a bind parameter.
	rows, err := tx.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	binary := make([]bool, len(cols))
	for i, ct := range types {
		switch strings.ToUpper(ct.DatabaseTypeName()) {
		case "BLOB", "BYTEA":
			binary[i] = true
		}
	}
	if err := enc.Encode(backupSection{Table: table, Columns: cols}); err != nil {
		return 0, err
	}

	scan := make([]any, len(cols))
	holders := make([]any, len(cols))
	for i := range scan {
		holders[i] = &scan[i]
	}
	var n int64
	line := struct {
		Row []any `json:"row"`
	}{Row: make([]any, len(cols))}
	for rows.Next() {
		if err := rows.Scan(holders...); err != nil {
			return n, err
		}
		for i, v := range scan {
			line.Row[i] = backupValue(v, binary[i])
		}
		if err := enc.Encode(line); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// backupValue makes one scanned column JSON-representable without losing what it was.
//
// The binary flag comes from the column's declared type, not from the Go type that came back: both
// drivers hand back []byte for real binary, but a text column that ever arrives as []byte must go
// back as text, or restoring it into SQLite would turn a string into a blob.
func backupValue(v any, binary bool) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if binary {
			return map[string]string{b64Key: base64.StdEncoding.EncodeToString(t)}
		}
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	default:
		return v // int64, float64, bool, string — all round-trip through JSON as themselves
	}
}

// restoreValue reverses backupValue. Numbers arrive as json.Number (the decoder is told to keep
// them) because a report id past 2^53 would otherwise come back as a different number through
// float64 — silent, and exactly the kind of corruption a restore must not introduce.
func restoreValue(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		return t.Float64()
	case map[string]any:
		enc, ok := t[b64Key].(string)
		if !ok {
			return nil, fmt.Errorf("unrecognised object value %v", t)
		}
		return base64.StdEncoding.DecodeString(enc)
	default:
		return v, nil // string, bool
	}
}

// RestoreReport is what a restore did, or (without force) what it would do.
type RestoreReport struct {
	Header   backupHeader
	Rows     map[string]int64 // rows read from the dump, per table
	Existing map[string]int64 // rows already in the target, per table — all of them are replaced
	Total    int64
	Replaced int64
	Applied  bool
	SkipCols map[string][]string // columns the target has and the dump does not: left at their default
}

// Restore loads a dump into the configured database, replacing everything in it.
//
// Without force it is a dry run: the file is parsed and validated end to end and the counts are
// reported, but nothing is written. That makes "is this backup actually readable?" a question an
// operator can answer without a spare database, and it makes the destructive form explicit rather
// than a flag people learn about afterwards.
func Restore(cfgPath, path string, force bool) (*RestoreReport, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return nil, err
	}
	defer st.Close()

	in := io.Reader(os.Stdin)
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	return st.restoreFrom(bufio.NewReaderSize(in, 1<<16), force)
}

func (s *Store) restoreFrom(r io.Reader, force bool) (*RestoreReport, error) {
	// Every pooled read happens HERE, before any transaction is opened: on SQLite the pool is a
	// single connection, and a pooled query issued while the restore transaction is open would
	// block on the connection that transaction is holding.
	known := map[string]map[string]bool{}
	for _, t := range s.backupTables() {
		known[t] = map[string]bool{}
	}
	if err := s.collectColumns(known); err != nil {
		return nil, err
	}
	existing, err := s.rowCounts(s.backupTables())
	if err != nil {
		return nil, err
	}
	identity, err := s.identityColumns()
	if err != nil {
		return nil, err
	}

	rep := &RestoreReport{
		Rows:     map[string]int64{},
		Existing: existing,
		SkipCols: map[string][]string{},
	}
	for _, n := range existing {
		rep.Replaced += n
	}

	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&rep.Header); err != nil {
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if rep.Header.Format != backupFormat {
		return nil, fmt.Errorf("not a report-portal backup (format %q)", rep.Header.Format)
	}
	if rep.Header.Version > backupFormatVersion {
		return nil, fmt.Errorf("backup format v%d is newer than this build understands (v%d): upgrade the portal first",
			rep.Header.Version, backupFormatVersion)
	}
	if err := checkSchemaGeneration(rep.Header.SchemaVersion); err != nil {
		return nil, err
	}

	var tx *sql.Tx
	if force {
		tx, err = s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback() //nolint:errcheck // no-op once committed
		// Empty EVERY table in the schema, not only the ones the dump carries: a table the dump
		// omits must end up empty too, or the result is a mix of two databases that never existed.
		for _, t := range s.backupTables() {
			if _, err := tx.Exec("DELETE FROM " + t); err != nil {
				return nil, fmt.Errorf("clear %s: %w", t, err)
			}
		}
	}
	if err := s.restoreStream(dec, tx, known, identity, rep); err != nil {
		return nil, err
	}
	if !force {
		return rep, nil
	}
	if err := s.resetIdentities(tx, identity, rep.Rows); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rep.Applied = true
	return rep, nil
}

// restoreStream reads the dump body. With tx == nil it validates and counts without writing, which
// is the dry run; the parsing and the column checks are identical either way, so a dry run that
// passes is real evidence about the file.
func (s *Store) restoreStream(dec *json.Decoder, tx *sql.Tx, known map[string]map[string]bool, identity map[string]string, rep *RestoreReport) error {
	var (
		table  string
		cols   []string
		stmt   *sql.Stmt
		values []any
	)
	closeStmt := func() error {
		if stmt == nil {
			return nil
		}
		err := stmt.Close()
		stmt = nil
		return err
	}
	defer closeStmt() //nolint:errcheck

	for {
		var line struct {
			Table   string   `json:"table"`
			Columns []string `json:"columns"`
			Row     []any    `json:"row"`
		}
		if err := dec.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("read backup: %w", err)
		}
		if line.Table != "" {
			if err := closeStmt(); err != nil {
				return err
			}
			table, cols = line.Table, line.Columns
			cset, ok := known[table]
			if !ok {
				return fmt.Errorf("the backup contains table %q, which this build has no schema for: "+
					"restore it with the version that wrote it (%s), or upgrade this one", table, rep.Header.AppVersion)
			}
			for _, c := range cols {
				if !cset[c] {
					return fmt.Errorf("the backup's %s.%s does not exist in this build's schema: "+
						"restoring would silently drop it — upgrade the portal to at least %s first",
						table, c, firstNonEmpty(rep.Header.AppVersion, "the version that wrote the backup"))
				}
			}
			var missing []string
			for c := range cset {
				if !containsString(cols, c) {
					missing = append(missing, c)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				rep.SkipCols[table] = missing // older dump, newer schema: these keep their defaults
			}
			values = make([]any, len(cols))
			if tx != nil {
				var err error
				if stmt, err = s.prepareInsert(tx, table, cols, identity); err != nil {
					return fmt.Errorf("prepare insert into %s: %w", table, err)
				}
			}
			continue
		}
		if table == "" {
			return fmt.Errorf("backup has a row before any table header")
		}
		if len(line.Row) != len(cols) {
			return fmt.Errorf("%s: row has %d values for %d columns", table, len(line.Row), len(cols))
		}
		for i, v := range line.Row {
			conv, err := restoreValue(v)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", table, cols[i], err)
			}
			values[i] = conv
		}
		if stmt != nil {
			if _, err := stmt.Exec(values...); err != nil {
				return fmt.Errorf("insert into %s: %w", table, err)
			}
		}
		rep.Rows[table]++
		rep.Total++
	}
	return closeStmt()
}

// prepareInsert builds the per-table insert. On Postgres an identity column rejects an explicit
// value unless the statement says OVERRIDING SYSTEM VALUE — and a restore must keep the original
// ids, because half the database refers to reports and groups by them.
func (s *Store) prepareInsert(tx *sql.Tx, table string, cols []string, identity map[string]string) (*sql.Stmt, error) {
	marks := make([]string, len(cols))
	for i := range marks {
		marks[i] = "?"
	}
	override := ""
	if id, ok := identity[table]; ok && containsString(cols, id) {
		override = " OVERRIDING SYSTEM VALUE"
	}
	q := fmt.Sprintf("INSERT INTO %s (%s)%s VALUES (%s)", table, strings.Join(cols, ","), override, strings.Join(marks, ","))
	return tx.Prepare(s.bind(q))
}

// identityColumns maps table -> identity column for Postgres. SQLite's AUTOINCREMENT accepts explicit
// ids and advances its own counter on insert, so it needs neither the override nor the reset below.
func (s *Store) identityColumns() (map[string]string, error) {
	out := map[string]string{}
	if s.driver != "postgres" {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema='public' AND is_identity='YES'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		out[t] = c
	}
	return out, rows.Err()
}

// resetIdentities moves each Postgres identity sequence past the ids just restored. Without it the
// sequence still sits at 1 and the very next insert collides with a restored row.
func (s *Store) resetIdentities(tx *sql.Tx, identity map[string]string, restored map[string]int64) error {
	for table, col := range identity {
		if restored[table] == 0 {
			continue
		}
		q := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','%s'), COALESCE((SELECT MAX(%s) FROM %s),0)+1, false)`,
			table, col, col, table)
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("reset %s.%s sequence: %w", table, col, err)
		}
	}
	return nil
}

// collectColumns fills in the real column set of every table, which is what decides whether a dump
// can be loaded without dropping anything.
func (s *Store) collectColumns(into map[string]map[string]bool) error {
	for table := range into {
		rows, err := s.db.Query("SELECT * FROM " + table + " WHERE 1=0")
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		cols, err := rows.Columns()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range cols {
			into[table][c] = true
		}
	}
	return nil
}

func (s *Store) rowCounts(tables []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range tables {
		var n int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			out[t] = n
		}
	}
	return out, nil
}

// checkSchemaGeneration refuses a dump this build cannot represent.
//
// It runs before the transaction is opened. That is cheapness, not safety — the rollback is what
// keeps a failed restore from leaving a partial database, whatever the reason — but there is no
// sense emptying 36 tables to then throw the work away.
//
// Without it a cross-generation restore "succeeds": the tables were created by the running binary in
// its own shape, the rows arrive from an older or newer one, and the dump's schema_version lands in
// `meta`. Nothing complains until the NEXT boot, where requireSchemaBaseline finally refuses — a
// delayed failure after a destructive operation, and the worst possible order for the two.
// requireSchemaBaseline itself cannot catch this: it runs at open time against the TARGET database,
// which at that moment is empty or current, and it never sees the dump at all.
//
// 0 means a dump written before the header carried the field. Allowed rather than refused: the
// alternative is rejecting a backup over a question it cannot answer, and this format has not shipped
// outside the branch that introduced it.
func checkSchemaGeneration(dump int) error {
	switch {
	case dump == 0 || dump == schemaBaseline:
		return nil
	case dump < schemaBaseline:
		return fmt.Errorf("this backup is from schema generation %d and the portal now requires %d: "+
			"restore it with the release that wrote it and let that release upgrade the database, "+
			"then back it up again", dump, schemaBaseline)
	default:
		return fmt.Errorf("this backup is from schema generation %d, which is newer than this build's %d: "+
			"upgrade the portal before restoring", dump, schemaBaseline)
	}
}
