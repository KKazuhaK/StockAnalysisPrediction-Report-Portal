package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedForBackup fills a store with one row of every shape a dump has to survive: a big explicit id,
// unicode text, an empty string, a NULL, and real binary content.
func seedForBackup(t *testing.T, st *Store) {
	t.Helper()
	const bigID = int64(1) << 60 // past float64's exact-integer range, which JSON numbers otherwise land in
	mustExec(t, st, `INSERT INTO reports(id,title,symbol,name,rtype,rdate,kind,body_md,version,author,owner_group)
		VALUES(?,?,?,?,?,?,?,?,?,?,NULL)`,
		bigID, "深度分析 · 一季度", "002594", "比亚迪", "股权分析", "2026-03-31", "分析", "# 正文\n\n包含「引号」与 emoji 🚗", "", "")
	mustExec(t, st, `INSERT INTO reports(id,title,symbol,rtype,rdate,body_md,version)
		VALUES(?,?,?,?,?,?,?)`, int64(2), "第二篇", "600519", "估值分析", "2026-04-01", "body", "manual")
	mustExec(t, st, `INSERT INTO users(username,password_hash,role) VALUES(?,?,?)`, "alice", "$2a$12$fakehash", "admin")
	mustExec(t, st, `INSERT INTO apps(id,name,version) VALUES(?,?,?)`, "demo", "Demo", "1.0.0")
	mustExec(t, st, `INSERT INTO app_files(app_id,path,ctype,content) VALUES(?,?,?,?)`,
		"demo", "index.html", "text/html", []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i'})
	mustExec(t, st, `INSERT INTO meta(k,v) VALUES(?,?)`, "site_title", "报告门户")
}

func mustExec(t *testing.T, st *Store, q string, args ...any) {
	t.Helper()
	if _, err := st.exec(q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

func dumpOf(t *testing.T, st *Store) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, _, err := st.dumpTo(&buf); err != nil {
		t.Fatalf("dumpTo: %v", err)
	}
	return buf.Bytes()
}

func scalar[T any](t *testing.T, st *Store, q string, args ...any) T {
	t.Helper()
	var v T
	if err := st.queryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return v
}

// TestBackupRestoreRoundTrip is the whole promise: a dump taken from one database loads into an
// empty one and every value comes back as itself — the big id intact rather than rounded through a
// float, the unicode unmangled, the binary still binary, and NULL still NULL rather than "".
func TestBackupRestoreRoundTrip(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	rep, err := dst.restoreFrom(bytes.NewReader(dump), true)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !rep.Applied {
		t.Fatal("a forced restore must report itself as applied")
	}
	if rep.Rows["reports"] != 2 || rep.Rows["users"] != 1 || rep.Rows["app_files"] != 1 {
		t.Fatalf("restored row counts are wrong: %v", rep.Rows)
	}

	const bigID = int64(1) << 60
	if got := scalar[string](t, dst, `SELECT title FROM reports WHERE id=?`, bigID); got != "深度分析 · 一季度" {
		t.Errorf("a 2^60 id must survive JSON: title at that id = %q", got)
	}
	if got := scalar[string](t, dst, `SELECT body_md FROM reports WHERE id=?`, bigID); !strings.Contains(got, "🚗") {
		t.Errorf("body did not survive: %q", got)
	}
	// owner_group was NULL, and NULL is not the same answer as 0 or "" anywhere it is read.
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM reports WHERE owner_group IS NULL`); n != 2 {
		t.Errorf("NULL columns must restore as NULL, not as a zero value; got %d of 2", n)
	}
	if got := scalar[string](t, dst, `SELECT version FROM reports WHERE id=?`, int64(2)); got != "manual" {
		t.Errorf("version = %q; want manual", got)
	}
	blob := scalar[[]byte](t, dst, `SELECT content FROM app_files WHERE app_id='demo' AND path='index.html'`)
	if !bytes.Equal(blob, []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i'}) {
		t.Errorf("binary content came back as %v", blob)
	}
	if got := scalar[string](t, dst, `SELECT v FROM meta WHERE k='site_title'`); got != "报告门户" {
		t.Errorf("settings must restore too; site_title = %q", got)
	}

	// And the dump of the restored database equals the dump of the original — the strongest
	// statement available that nothing was lost, added, or reordered.
	if !bytes.Equal(stripHeader(dump), stripHeader(dumpOf(t, dst))) {
		t.Error("re-dumping the restored database must produce the same body as the original dump")
	}
}

// stripHeader drops the first line, which legitimately differs between two dumps (a timestamp).
func stripHeader(dump []byte) []byte {
	if i := bytes.IndexByte(dump, '\n'); i >= 0 {
		return dump[i+1:]
	}
	return dump
}

// TestRestoreDryRunWritesNothing covers the default form of the command. It must parse and validate
// the whole file — that is what makes it useful as a "can this backup be read?" check — while
// leaving the target exactly as it was, including the rows it would have deleted.
func TestRestoreDryRunWritesNothing(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	mustExec(t, dst, `INSERT INTO reports(id,title,symbol,rtype,rdate,version) VALUES(?,?,?,?,?,?)`,
		int64(77), "已经在库里的", "000001", "股权分析", "2026-01-01", "")
	before := dumpOf(t, dst)

	rep, err := dst.restoreFrom(bytes.NewReader(dump), false)
	if err != nil {
		t.Fatalf("dry run must succeed on a valid backup: %v", err)
	}
	if rep.Applied {
		t.Error("a dry run must not report itself as applied")
	}
	if want := int64(bytes.Count(dump, []byte("\n")) - 1 - strings.Count(string(dump), `{"table":`)); rep.Total != want {
		t.Errorf("the dry run must still count every row it read; got %d, want %d", rep.Total, want)
	}
	if rep.Existing["reports"] != 1 {
		t.Errorf("the dry run must report what it WOULD delete; existing=%v", rep.Existing)
	}
	var wantReplaced int64
	for _, n := range rep.Existing {
		wantReplaced += n
	}
	if rep.Replaced != wantReplaced || rep.Replaced == 0 {
		t.Errorf("replaced = %d; want the sum of every existing table (%d)", rep.Replaced, wantReplaced)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, dst))) {
		t.Error("a dry run must leave the database byte-identical")
	}
}

// TestRestoreReplacesEverything proves a restore is a replacement rather than a merge: a row the
// target has and the dump does not must be gone, or the result is a database that never existed.
func TestRestoreReplacesEverything(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	mustExec(t, dst, `INSERT INTO reports(id,title,symbol,rtype,rdate,version) VALUES(?,?,?,?,?,?)`,
		int64(77), "不该留下的", "000001", "股权分析", "2026-01-01", "")
	mustExec(t, dst, `INSERT INTO links(label,url) VALUES(?,?)`, "旧入口", "https://example.com")

	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM reports WHERE id=77`); n != 0 {
		t.Error("a row absent from the backup must not survive the restore")
	}
	// links appears in the dump with zero rows, so it must be emptied as well.
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM links`); n != 0 {
		t.Errorf("a table the backup has empty must end up empty; got %d rows", n)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM reports`); n != 2 {
		t.Errorf("reports = %d; want the backup's 2", n)
	}
}

// TestRestoreRefusesWhatItCannotStore covers the one way a logical dump can lose data: a backup
// written by a NEWER build, carrying a table or column this one has no schema for. Dropping it
// silently is the failure mode that matters, so both must be hard errors that name the thing.
func TestRestoreRefusesWhatItCannotStore(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := string(dumpOf(t, src))

	cases := []struct {
		name, from, to, want string
	}{
		{"unknown column", `{"table":"reports","columns":["id",`, `{"table":"reports","columns":["id","invented_by_a_newer_build",`, "invented_by_a_newer_build"},
		{"unknown table", `{"table":"reports",`, `{"table":"reports_from_the_future",`, "reports_from_the_future"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mutated := strings.Replace(dump, c.from, c.to, 1)
			if mutated == dump {
				t.Fatalf("the dump did not contain %q — fix the test, not the code", c.from)
			}
			dst := newTestStore(t)
			_, err := dst.restoreFrom(strings.NewReader(mutated), true)
			if err == nil {
				t.Fatal("restoring something this build cannot store must fail, not drop it")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error must name %q; got: %v", c.want, err)
			}
			if n := scalar[int](t, dst, `SELECT COUNT(*) FROM reports`); n != 0 {
				t.Errorf("a refused restore must not leave rows behind; got %d", n)
			}
		})
	}
}

// TestRestoreRejectsForeignFiles keeps the command from half-loading something that is not a dump —
// a truncated download, or the wrong file entirely.
func TestRestoreRejectsForeignFiles(t *testing.T) {
	for _, in := range []string{
		``,
		`{"hello":"world"}`,
		`{"format":"report-portal-backup","version":99}`,
		"not json at all",
	} {
		dst := newTestStore(t)
		if _, err := dst.restoreFrom(strings.NewReader(in), true); err == nil {
			t.Errorf("input %q must be refused", in)
		}
	}
}

// TestRestoreAcceptsAnOlderDump is the upgrade path: a dump written before a column existed must
// still load, with the new column left at its default, and the restore must say which columns those
// were rather than leaving the operator to guess.
func TestRestoreAcceptsAnOlderDump(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := string(dumpOf(t, src))

	// Drop `author` from the reports section and from every reports row: exactly the shape of a dump
	// taken by a build from before ADR 0026 added the column.
	older := dropColumn(t, dump, "reports", "author")

	dst := newTestStore(t)
	rep, err := dst.restoreFrom(strings.NewReader(older), true)
	if err != nil {
		t.Fatalf("an older dump must still restore: %v", err)
	}
	if rep.Rows["reports"] != 2 {
		t.Fatalf("reports rows = %d; want 2", rep.Rows["reports"])
	}
	if !containsString(rep.SkipCols["reports"], "author") {
		t.Errorf("the restore must report author as left-at-default; got %v", rep.SkipCols["reports"])
	}
	if got := scalar[string](t, dst, `SELECT COALESCE(author,'') FROM reports WHERE id=2`); got != "" {
		t.Errorf("a column the dump lacks must take its default; author = %q", got)
	}
}

// TestBackupCoversEveryTable guards the one silent way a backup goes wrong: a table declared in the
// schema that the dump never visits. The list comes from the schema itself, so this stays true as
// tables are added — but only as long as nobody replaces that with a hand-kept list.
func TestBackupCoversEveryTable(t *testing.T) {
	st := newTestStore(t)
	dumped := map[string]bool{}
	for _, line := range strings.Split(string(dumpOf(t, st)), "\n") {
		if strings.HasPrefix(line, `{"table":`) {
			dumped[line[len(`{"table":"`):strings.Index(line[len(`{"table":"`):], `"`)+len(`{"table":"`)]] = true
		}
	}
	for _, stmt := range st.baseSchemaStmts() {
		table, _, ok := parseCreateTable(stmt)
		if ok && !dumped[table] {
			t.Errorf("table %q is in the schema but not in the dump — a backup that omits it is worse than none", table)
		}
	}
	if len(dumped) < 30 {
		t.Errorf("only %d tables were dumped; the schema has far more, so the walk is broken", len(dumped))
	}
}

// dropColumn rewrites a dump as if `col` had never existed on `table`, which is how an older
// build's dump differs from this one's.
func dropColumn(t *testing.T, dump, table, col string) string {
	t.Helper()
	var out []string
	idx := -1
	inTable := false
	for _, line := range strings.Split(dump, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `{"table":`) {
			var sec backupSection
			mustUnmarshal(t, line, &sec)
			inTable = sec.Table == table
			if inTable {
				idx = -1
				var kept []string
				for i, c := range sec.Columns {
					if c == col {
						idx = i
						continue
					}
					kept = append(kept, c)
				}
				if idx < 0 {
					t.Fatalf("%s has no column %s in the dump", table, col)
				}
				sec.Columns = kept
				out = append(out, mustMarshal(t, sec))
				continue
			}
		} else if inTable {
			var row struct {
				Row []any `json:"row"`
			}
			mustUnmarshal(t, line, &row)
			row.Row = append(row.Row[:idx], row.Row[idx+1:]...)
			out = append(out, mustMarshal(t, row))
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

func mustUnmarshal(t *testing.T, line string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(line), v); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTruncatedRestoreLeavesNothingBehind is the failure a restore must never half-do. A dump cut
// short — an interrupted download, a full disk on the way out — has to leave the target exactly as
// it was, because the alternative is a database holding the first third of someone else's data with
// its own already deleted.
func TestTruncatedRestoreLeavesNothingBehind(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	full := string(dumpOf(t, src))

	// Cut inside the reports section, after at least one row has been inserted.
	lines := strings.Split(full, "\n")
	cut := 0
	for i, l := range lines {
		if strings.HasPrefix(l, `{"table":"reports"`) {
			cut = i + 2 // the header, one row, then nothing
			break
		}
	}
	if cut == 0 || cut >= len(lines) {
		t.Fatal("could not find the reports section to truncate")
	}
	truncated := strings.Join(lines[:cut], "\n") + "\n{\"row\":[1,2"

	dst := newTestStore(t)
	mustExec(t, dst, `INSERT INTO reports(id,title,symbol,rtype,rdate,version) VALUES(?,?,?,?,?,?)`,
		int64(9), "本来就在库里", "000001", "股权分析", "2026-01-01", "")
	before := dumpOf(t, dst)

	if _, err := dst.restoreFrom(strings.NewReader(truncated), true); err == nil {
		t.Fatal("a truncated dump must fail")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, dst))) {
		t.Error("a failed restore must roll back completely — the target is byte-identical or it is corrupt")
	}
}

// TestRestoreRefusesAnotherSchemaGeneration is the failure the ADR claimed requireSchemaBaseline
// already handled, and it did not: that guard runs at open time against the TARGET database — empty
// or current at that moment — and never sees the dump. So a cross-generation backup restored
// "successfully" and only broke on the NEXT boot, which is a delayed failure after a destructive
// operation, in the worst possible order.
func TestRestoreRefusesAnotherSchemaGeneration(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := string(dumpOf(t, src))

	if !strings.Contains(dump, `"schema_version":`) {
		t.Fatal("the dump must record its schema generation in the HEADER, before any row")
	}

	for _, c := range []struct{ name, from, to, want string }{
		{"older than this build", `"schema_version":2`, `"schema_version":1`, "restore it with the release that wrote it"},
		{"newer than this build", `"schema_version":2`, `"schema_version":9`, "upgrade the portal before restoring"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mutated := strings.Replace(dump, c.from, c.to, 1)
			if mutated == dump {
				t.Fatalf("the dump did not contain %q — fix the test, not the code", c.from)
			}
			dst := newTestStore(t)
			mustExec(t, dst, `INSERT INTO reports(id,title,symbol,rtype,rdate,version) VALUES(?,?,?,?,?,?)`,
				int64(5), "本来就在库里", "000001", "深度分析", "2026-01-01", "")
			before := dumpOf(t, dst)

			_, err := dst.restoreFrom(strings.NewReader(mutated), true)
			if err == nil {
				t.Fatal("a dump from another schema generation must be refused")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error must name the remedy %q; got: %v", c.want, err)
			}
			// And the target survives it. Moving the check after the DELETE would still leave the
			// target intact — the rollback does that, and TestTruncatedRestoreLeavesNothingBehind is
			// what proves it — so this asserts the outcome, not the ordering. The ordering is a
			// cheapness property: refuse before opening a transaction and emptying 36 tables to
			// throw the work away.
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, dst))) {
				t.Error("a refused restore must leave the target exactly as it was")
			}
		})
	}
}

// TestRestoreAcceptsADumpWithoutTheField keeps the check from rejecting a backup over a question it
// cannot answer: a dump written before the header carried the generation.
func TestRestoreAcceptsADumpWithoutTheField(t *testing.T) {
	src := newTestStore(t)
	seedForBackup(t, src)
	dump := strings.Replace(string(dumpOf(t, src)), `"schema_version":2,`, "", 1)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(strings.NewReader(dump), true); err != nil {
		t.Fatalf("a dump with no recorded generation must still restore: %v", err)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM reports`); n != 2 {
		t.Errorf("reports = %d; want 2", n)
	}
}

// TestBackupFileIsNotReadableByOthers pins a promise the ADR makes and the code only half kept: a
// dump carries bcrypt password hashes and API token hashes, and the 0600 passed to OpenFile applies
// only when O_CREATE actually creates the file. Overwriting an existing one kept whatever mode it
// already had — which is exactly the shape of a nightly script writing to the same path forever, so
// the guarantee held for the first run and no other.
func TestBackupFileIsNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("secret_key: \"0123456789abcdef0123456789abcdef\"\n"+
		"db_driver: \"sqlite\"\ndb_path: \""+filepath.Join(dir, "portal.db")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		pre  os.FileMode // 0 = the file does not exist yet
	}{
		{"a new file", 0},
		{"overwriting a group-readable one", 0o644},
		{"overwriting a world-writable one", 0o666},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "dump-"+strings.ReplaceAll(c.name, " ", "-")+".jsonl")
			if c.pre != 0 {
				if err := os.WriteFile(path, []byte("stale\n"), c.pre); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, c.pre); err != nil { // WriteFile's mode is umask-masked
					t.Fatal(err)
				}
			}
			if _, _, err := Backup(cfg, path); err != nil {
				t.Fatalf("Backup: %v", err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := fi.Mode().Perm(); perm != 0o600 {
				t.Errorf("dump is mode %o; it carries password hashes and must be 0600", perm)
			}
			// And it really does carry them, so the assertion above is about something.
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "password_hash") {
				t.Error("the fixture dumped no users, so this test is not guarding what it claims")
			}
		})
	}
}
