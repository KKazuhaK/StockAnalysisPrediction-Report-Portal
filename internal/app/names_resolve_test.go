package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With no name in the payload, Resolve re-fetches the current live name every time (no
// memo/TTL), so a rename is reflected on the very next report; a failed fetch falls back
// to the last known name and never regresses to empty.
func TestNamesResolve(t *testing.T) {
	st := newTestStore(t)
	n := LoadNames(t.TempDir(), st)
	calls := 0
	cur := "旧名"
	n.fetch = func(code string) string { calls++; return cur }

	// fetches the current name and caches it
	if got := n.Resolve("600000"); got != "旧名" || calls != 1 {
		t.Fatalf("first resolve = %q calls=%d, want 旧名/1", got, calls)
	}
	if n.Get("600000") != "旧名" {
		t.Fatalf("cache not updated: %q", n.Get("600000"))
	}

	// every resolve fetches live — no caching shortcut
	if got := n.Resolve("600000"); got != "旧名" || calls != 2 {
		t.Fatalf("second resolve = %q calls=%d, want 旧名/2", got, calls)
	}

	// a rename is picked up on the very next resolve
	cur = "新名"
	if got := n.Resolve("600000"); got != "新名" || calls != 3 {
		t.Fatalf("post-rename = %q calls=%d, want 新名/3", got, calls)
	}

	// fetch fails → falls back to the last known name
	cur = ""
	if got := n.Resolve("600000"); got != "新名" {
		t.Fatalf("fetch-fail fallback = %q, want 新名", got)
	}

	if got := n.Resolve(""); got != "" {
		t.Fatalf("empty code = %q, want empty", got)
	}
}

// FetchOneName retries each source before falling over: a source is tried up to
// nameFetchAttempts times, and only after it exhausts its retries do we move to the next.
func TestFetchNameWithRetry(t *testing.T) {
	restore := nameRetryDelay
	nameRetryDelay = 0
	defer func() { nameRetryDelay = restore }()

	sources := []nameSource{
		{url: "A", parse: func(s string) string { return s }},
		{url: "B", parse: func(s string) string { return s }},
	}

	// A fails once then succeeds on its retry → returned; B never reached
	calls := map[string]int{}
	get := func(url, ref string) string {
		calls[url]++
		if url == "A" && calls["A"] >= 2 {
			return "华资名"
		}
		return ""
	}
	if got := fetchNameWithRetry(sources, get); got != "华资名" {
		t.Fatalf("retry-then-success = %q, want 华资名", got)
	}
	if calls["A"] != 2 || calls["B"] != 0 {
		t.Fatalf("attempts A=%d B=%d, want 2/0 (B not reached)", calls["A"], calls["B"])
	}

	// A exhausts its retries → fall over to the next source
	calls = map[string]int{}
	get = func(url, ref string) string {
		calls[url]++
		if url == "B" {
			return "新浪名"
		}
		return ""
	}
	if got := fetchNameWithRetry(sources, get); got != "新浪名" {
		t.Fatalf("failover = %q, want 新浪名", got)
	}
	if calls["A"] != nameFetchAttempts || calls["B"] != 1 {
		t.Fatalf("attempts A=%d B=%d, want %d/1", calls["A"], calls["B"], nameFetchAttempts)
	}

	// all sources exhausted → empty, each tried nameFetchAttempts times
	calls = map[string]int{}
	get = func(url, ref string) string { calls[url]++; return "" }
	if got := fetchNameWithRetry(sources, get); got != "" {
		t.Fatalf("all-fail = %q, want empty", got)
	}
	if calls["A"] != nameFetchAttempts || calls["B"] != nameFetchAttempts {
		t.Fatalf("attempts A=%d B=%d, want %d each", calls["A"], calls["B"], nameFetchAttempts)
	}
}

// Ingest freezes the as-of name onto each report row; a later rename produces a new
// snapshot for new reports but must never rewrite earlier reports (不能改之前的).
func TestV1IngestFreezesNameKeepsHistory(t *testing.T) {
	s := newV1Server(t)
	name := "旧名"
	s.names.fetch = func(code string) string { return name }

	ingest := func(date string) {
		body := `{"symbol":"600001","date":"` + date + `","subtype":"综合决策","body_md":"x"}`
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest %s: %d %s", date, rec.Code, rec.Body.String())
		}
	}

	ingest("2026-07-03")
	if r := repByIdent(t, s.st, "600001", "2026-07-03", "综合决策"); r == nil || r.Name != "旧名" {
		t.Fatalf("first report name = %v, want 旧名", r)
	}

	// rename → the next report snapshots the new name
	name = "新名"
	ingest("2026-07-04")
	if r := repByIdent(t, s.st, "600001", "2026-07-04", "综合决策"); r == nil || r.Name != "新名" {
		t.Fatalf("second report name = %v, want 新名", r)
	}

	// the earlier report is untouched
	if r := repByIdent(t, s.st, "600001", "2026-07-03", "综合决策"); r.Name != "旧名" {
		t.Fatalf("history changed: earlier report name = %q, want 旧名", r.Name)
	}
}

// FreezeReportNames snapshots the current name onto rows that have none (legacy imports),
// so their displayed name stops depending on the mutable stocks cache. It must never touch
// rows that already carry a frozen name, nor invent a name where the stocks cache has none.
func TestFreezeReportNames(t *testing.T) {
	st := newTestStore(t)
	st.SyncStocks(map[string]string{"600100": "华资名", "600200": "有名"})
	// A: empty name, symbol known → frozen from stocks
	st.UpsertReport(Rep{Symbol: "600100", Date: "2026-07-01", RType: "综合决策", Kind: "重组决策", Title: "a"})
	// B: already carries a frozen name → must be left as-is
	st.UpsertReport(Rep{Symbol: "600200", Name: "报告时名", Date: "2026-07-01", RType: "综合决策", Kind: "重组决策", Title: "b"})
	// C: empty name, symbol not in stocks → stays empty
	st.UpsertReport(Rep{Symbol: "600300", Date: "2026-07-01", RType: "综合决策", Kind: "重组决策", Title: "c"})

	n, err := st.FreezeReportNames()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("frozen rows = %d, want 1", n)
	}
	if r := repByIdent(t, st, "600100", "2026-07-01", "综合决策"); r.Name != "华资名" {
		t.Fatalf("A name = %q, want 华资名 (frozen from stocks)", r.Name)
	}
	if r := repByIdent(t, st, "600200", "2026-07-01", "综合决策"); r.Name != "报告时名" {
		t.Fatalf("B name = %q, want 报告时名 (unchanged)", r.Name)
	}
	if r := repByIdent(t, st, "600300", "2026-07-01", "综合决策"); r.Name != "" {
		t.Fatalf("C name = %q, want empty (no stocks entry)", r.Name)
	}

	// idempotent: a second run freezes nothing new
	if n2, _ := st.FreezeReportNames(); n2 != 0 {
		t.Fatalf("second freeze = %d, want 0", n2)
	}
}

// An explicit name in the payload is authoritative and skips the live fetch entirely.
func TestV1IngestExplicitNameWins(t *testing.T) {
	s := newV1Server(t)
	s.names.fetch = func(string) string {
		t.Fatal("must not fetch when payload carries a name")
		return ""
	}
	body := `{"symbol":"600002","date":"2026-07-03","subtype":"综合决策","name":"指定名","body_md":"x"}`
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Ingest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body.String())
	}
	if r := repByIdent(t, s.st, "600002", "2026-07-03", "综合决策"); r == nil || r.Name != "指定名" {
		t.Fatalf("report name = %v, want 指定名", r)
	}
}
