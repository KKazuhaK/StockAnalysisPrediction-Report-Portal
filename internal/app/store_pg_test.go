package app

import (
	"os"
	"testing"
)

// likeOp must yield the case-insensitive operator on Postgres (whose LIKE is
// case-sensitive, unlike SQLite's) so name/keyword search keeps working there.
func TestLikeOpPerDriver(t *testing.T) {
	if op := (&Store{driver: "postgres"}).likeOp(); op != "ILIKE" {
		t.Errorf("postgres likeOp = %q, want ILIKE", op)
	}
	for _, d := range []string{"sqlite", ""} {
		if op := (&Store{driver: d}).likeOp(); op != "LIKE" {
			t.Errorf("driver %q likeOp = %q, want LIKE", d, op)
		}
	}
}

// groupConcatDistinct must emit a Postgres-valid aggregate (STRING_AGG); Postgres
// has no GROUP_CONCAT, which is the one hard breakage in ListRuns.
func TestGroupConcatDistinctPerDriver(t *testing.T) {
	if got := (&Store{driver: "postgres"}).groupConcatDistinct("rtype"); got != "STRING_AGG(DISTINCT rtype, ',' ORDER BY rtype)" {
		t.Errorf("postgres groupConcatDistinct = %q", got)
	}
	if got := (&Store{driver: "sqlite"}).groupConcatDistinct("rtype"); got != "GROUP_CONCAT(DISTINCT rtype)" {
		t.Errorf("sqlite groupConcatDistinct = %q", got)
	}
}

// TestPostgresQueries runs the real dialect-sensitive queries against a live
// Postgres. It is skipped unless TEST_POSTGRES_DSN is set (CI provides a pg
// service; local dev without pg just skips). This proves STRING_AGG grouping and
// ILIKE case-insensitive search actually execute on Postgres.
func TestPostgresQueries(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	st, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("OpenStore postgres: %v", err)
	}
	for _, tbl := range []string{"reports", "stocks", "api_tokens"} {
		if _, err := st.exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	ins := "INSERT INTO reports(title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html) VALUES(?,?,?,?,?,?,?,?,?,?,?)"
	if _, err := st.exec(ins, "T1", "600160", "Apple Inc", "交易分析", "2026-07-01", "重组决策", "run1", "dify", nowStr(), "body one", ""); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := st.exec(ins, "T2", "600160", "Apple Inc", "舆情分析", "2026-07-01", "重组决策", "run1", "dify", nowStr(), "body two", ""); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	// ListRuns must fold both subtypes into one run (STRING_AGG must not error).
	runs := st.ListRuns("600160", "", nil)
	if len(runs) != 1 {
		t.Fatalf("ListRuns len = %d, want 1", len(runs))
	}
	if len(runs[0].Subtypes) != 2 {
		t.Errorf("run subtypes = %v, want 2 distinct", runs[0].Subtypes)
	}
	// Case-insensitive name search: lowercase query, mixed-case stored name.
	if _, err := st.exec("INSERT INTO stocks(code,name,updated_at) VALUES(?,?,?)", "600160", "Apple Inc", nowStr()); err != nil {
		t.Fatalf("insert stock: %v", err)
	}
	reps, err := st.SearchNew(Filters{Q: "apple"}, nil)
	if err != nil {
		t.Fatalf("SearchNew: %v", err)
	}
	if len(reps) == 0 {
		t.Error("case-insensitive search for 'apple' found nothing; ILIKE not applied on Postgres")
	}

	// FreezeReportNames: the correlated-subquery UPDATE must run on Postgres and only
	// touch un-named rows whose symbol is known.
	if _, err := st.exec(ins, "T3", "600161", "", "交易分析", "2026-07-01", "重组决策", "run2", "dify", nowStr(), "body three", ""); err != nil {
		t.Fatalf("insert 3: %v", err)
	}
	if _, err := st.exec("INSERT INTO stocks(code,name,updated_at) VALUES(?,?,?)", "600161", "Frozen Co", nowStr()); err != nil {
		t.Fatalf("insert stock 2: %v", err)
	}
	n, err := st.FreezeReportNames()
	if err != nil {
		t.Fatalf("FreezeReportNames: %v", err)
	}
	if n != 1 {
		t.Fatalf("frozen rows = %d, want 1 (only the un-named u3)", n)
	}
	if r := repByIdent(t, st, "600161", "2026-07-01", "交易分析"); r == nil || r.Name != "Frozen Co" {
		t.Fatalf("T3 name = %v, want Frozen Co", r)
	}
	if r := repByIdent(t, st, "600160", "2026-07-01", "交易分析"); r == nil || r.Name != "Apple Inc" {
		t.Fatalf("T1 name = %v, want Apple Inc (already-named row must be untouched)", r)
	}

	// Generation-3 credential storage and report indexes must use Postgres-valid SQL too.
	const token = "postgres-token-secret"
	if err := st.CreateToken(token, "pg", "query", ""); err != nil {
		t.Fatalf("CreateToken postgres: %v", err)
	}
	if !st.TokenValid(token, "query") {
		t.Fatal("digest-backed Postgres token was rejected")
	}
	var plaintext string
	if err := st.queryRow(`SELECT COALESCE(token,'') FROM api_tokens WHERE name='pg'`).Scan(&plaintext); err != nil || plaintext != "" {
		t.Fatalf("Postgres plaintext token = %q, err=%v", plaintext, err)
	}
	for _, index := range []string{"idx_api_tokens_hash", "idx_reports_symbol_date_time", "idx_reports_date_time"} {
		var n int
		if err := st.queryRow(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname=?`, index).Scan(&n); err != nil || n != 1 {
			t.Errorf("Postgres index %s count=%d err=%v", index, n, err)
		}
	}
}

// pgStore opens the integration database and empties the tables a test is about to use. Shared by
// the tests below so each states only what it needs cleaned.
func pgStore(t *testing.T, tables ...string) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	st, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("OpenStore postgres: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	for _, tbl := range tables {
		if _, err := st.exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	return st
}

// TestPostgresVersionIdentity covers the ADR 0024 identity change on the driver production actually
// runs. The SQLite path is exercised by every other test; this one exists because the parts that
// differ between drivers are exactly the parts that would fail silently — the five-column unique
// index and the ON CONFLICT target inferred from it.
func TestPostgresVersionIdentity(t *testing.T) {
	st := pgStore(t, "reports")

	base := Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策", Title: "投资决策 600519"}
	internal := base
	internal.MD = "scoring table"
	full, created, err := st.UpsertReport(internal)
	if err != nil || !created {
		t.Fatalf("first ingest: created=%v err=%v", created, err)
	}
	pub := base
	pub.Version, pub.MD = "对外版", "conclusion"
	other, created, err := st.UpsertReport(pub)
	if err != nil || !created {
		t.Fatalf("second version: created=%v err=%v", created, err)
	}
	if full == other {
		t.Fatal("two versions collapsed into one row on Postgres")
	}
	// Re-ingest still overwrites in place — the ON CONFLICT target must match the index exactly, or
	// Postgres raises "no unique or exclusion constraint matching the ON CONFLICT specification".
	again := pub
	again.MD = "revised"
	if id, created, err := st.UpsertReport(again); err != nil || created || id != other {
		t.Errorf("re-ingest: id=%d created=%v err=%v, want an in-place overwrite of %d", id, created, err, other)
	}
	if r, _ := st.GetNew(other, nil); r == nil || r.MD != "revised" || r.Version != "对外版" {
		t.Errorf("read back = %+v", r)
	}
	// The migration guard must recognise the index on this driver too, or every start would drop
	// and rebuild a unique index over the whole reports table.
	if !st.identIndexCoversVersion() {
		t.Error("identIndexCoversVersion must see the Postgres index")
	}
}

// TestPostgresScopedReads covers the ADR 0024 read predicate on Postgres: an EXISTS subquery and up
// to three IN-lists, assembled in Go and then rewritten to $N placeholders. Argument order is the
// thing that breaks, and it breaks into a wrong answer rather than an error.
func TestPostgresScopedReads(t *testing.T) {
	st := pgStore(t, "reports", "report_viewers", "version_grants", "report_versions", "users", "user_groups")
	root := st.EnsureDefaultGroup()
	ou, err := st.CreateUserGroup("pg-clients", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	st.SetGroupParent(ou, root)
	st.SetGroupRestricted(ou, true)
	st.UpsertUser(User{Username: "pg-ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("pg-ext", ou)
	st.ensureDefaultVersion()
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	st.SaveVersion(ReportVersion{Name: "客户版", Ord: 2, Visibility: VisibilityAll})
	st.SetVersionGrants("对外版", []string{groupPrincipal(ou)})
	st.SetVersionGrants("客户版", []string{groupPrincipal(ou)})

	mk := func(version, title string) int64 {
		id, _, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
			Title: title, Version: version, MD: "body of " + title})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	hidden := mk(st.DefaultVersion(), "内部") // ungranted version
	mine := mk("对外版", "我申请的")               // granted, owner visibility
	theirs := mk("对外版", "别人申请的")            // granted, owner visibility, not mine
	library := mk("客户版", "公开库")             // granted, visible to all
	st.AddReportViewer(mine, "2026-07-28", "pg-ext", ou)

	// Built by hand rather than through viewerScope, so the test needs no Server and states the
	// scope it is exercising.
	sc := &ownerScope{
		self:       userPrincipal("pg-ext"),
		principals: []string{userPrincipal("pg-ext"), groupPrincipal(ou), groupPrincipal(root)},
		versAll:    []string{"客户版"},
		versOwner:  []string{"对外版"},
	}
	for _, tc := range []struct {
		name    string
		id      int64
		visible bool
	}{
		{"ungranted version", hidden, false},
		{"granted, asked for it", mine, true},
		{"granted, someone else asked", theirs, false},
		{"visible to everyone granted it", library, true},
	} {
		r, err := st.GetNew(tc.id, sc)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (r != nil) != tc.visible {
			t.Errorf("%s: visible=%v, want %v", tc.name, r != nil, tc.visible)
		}
	}
	// The same predicate through a list query, where it is appended to a larger WHERE and the
	// argument order has more chances to go wrong.
	reps, err := st.SearchNew(Filters{}, sc)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, r := range reps {
		got[r.ID] = true
	}
	if !got[mine] || !got[library] || got[hidden] || got[theirs] || len(reps) != 2 {
		t.Errorf("scoped list = %v (%d rows), want exactly the granted+owned pair", got, len(reps))
	}
	// And the switcher, which groups by identity minus the version.
	if forms := st.VersionsOfReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策"}, sc); len(forms) != 2 {
		t.Errorf("switcher offered %d forms, want the two this viewer may read", len(forms))
	}
}

// TestPostgresUpgradeRebuildsIdentityIndex is the migration itself on Postgres, from the shape a
// v0.3.10 database has. The DROP/CREATE is the one piece of hand-written migration in the project,
// and a driver difference here corrupts report identity rather than erroring.
func TestPostgresUpgradeRebuildsIdentityIndex(t *testing.T) {
	st := pgStore(t, "reports")
	// Put the table back into the four-column shape and re-run init over it.
	if _, err := st.exec(`DROP INDEX IF EXISTS idx_reports_ident`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`CREATE UNIQUE INDEX idx_reports_ident ON reports(symbol, rdate, rtype, title)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`UPDATE reports SET version=''`); err != nil {
		t.Fatal(err)
	}
	rows := [][]string{
		{"600519", "2026-07-01", "投资决策", "茅台投资决策"},
		{"", "2026-07-01", "主题研究", "白酒行业展望"},
		{"", "2026-07-01", "主题研究", "新能源行业展望"}, // told apart by title alone
	}
	for _, r := range rows {
		if _, err := st.exec(`INSERT INTO reports(symbol,rdate,rtype,title,version) VALUES(?,?,?,?,'')`,
			r[0], r[1], r[2], r[3]); err != nil {
			t.Fatal(err)
		}
	}
	if st.identIndexCoversVersion() {
		t.Fatal("the four-column index must not be reported as covering version")
	}
	if err := st.init(); err != nil {
		t.Fatalf("re-running init over a pre-version shape: %v", err)
	}
	if !st.identIndexCoversVersion() {
		t.Error("the identity index was not rebuilt to cover version")
	}
	var n, offDefault int
	st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	st.queryRow("SELECT COUNT(*) FROM reports WHERE version <> ?", st.DefaultVersion()).Scan(&offDefault)
	if n != len(rows) {
		t.Errorf("row count = %d after the rebuild, want %d — identity changes must never merge reports", n, len(rows))
	}
	if offDefault != 0 {
		t.Errorf("%d rows are off the default version after the backfill", offDefault)
	}
	// A second init is a no-op rather than another full-table rebuild.
	if err := st.init(); err != nil {
		t.Fatalf("second init: %v", err)
	}
}
