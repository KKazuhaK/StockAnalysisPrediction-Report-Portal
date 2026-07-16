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
	runs := st.ListRuns("600160", "")
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
	reps, err := st.SearchNew(Filters{Q: "apple"})
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
