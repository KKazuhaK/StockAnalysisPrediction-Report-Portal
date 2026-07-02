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
	for _, tbl := range []string{"reports", "stocks"} {
		if _, err := st.exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	ins := "INSERT INTO reports(uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)"
	if _, err := st.exec(ins, "u1", "T1", "600160", "Apple Inc", "交易分析", "2026-07-01", "重组决策", "run1", "dify", nowStr(), "body one", ""); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := st.exec(ins, "u2", "T2", "600160", "Apple Inc", "舆情分析", "2026-07-01", "重组决策", "run1", "dify", nowStr(), "body two", ""); err != nil {
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
}
