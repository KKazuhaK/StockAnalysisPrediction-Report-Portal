package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// repByIdent looks up the report a test ingested for a code+date+subtype. The full identity
// also carries the title (see reportIdentExpr), but every caller here stores a single report
// per code+date+subtype, so this coarser lookup stays unambiguous and keeps the call sites on
// what they actually assert.
func repByIdent(t *testing.T, st *Store, symbol, date, rtype string) *Rep {
	t.Helper()
	var id int64
	if err := st.queryRow("SELECT id FROM reports WHERE symbol=? AND rdate=? AND rtype=? ORDER BY id", symbol, date, rtype).Scan(&id); err != nil {
		return nil
	}
	rep, err := st.GetNew(id)
	if err != nil {
		t.Fatalf("GetNew(%d): %v", id, err)
	}
	return rep
}

func TestValidReportDate(t *testing.T) {
	for _, d := range []string{"2026-07-02", "2026-01-01", "2024-12-31"} {
		if !validReportDate(d) {
			t.Errorf("%q should be valid", d)
		}
	}
	for _, d := range []string{"2026-7-2", "2026/07/02", "", "20260702", " 2026-07-02", "2026-13-01", "2026-02-30"} {
		if validReportDate(d) {
			t.Errorf("%q should be invalid", d)
		}
	}
}

func newV1Server(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	st.CreateToken("tok-all", "test", "all", "")
	srv := &Server{st: st, names: LoadNames(t.TempDir(), st)}
	srv.names.fetch = func(string) string { return "" } // no network in tests unless a test opts in
	return srv
}

// v1 ingest: portal-generated uid, created flag, JSON error envelope, date validation.
func TestV1IngestContract(t *testing.T) {
	s := newV1Server(t)

	do := func(body, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
		return m
	}

	// no auth → 401 JSON envelope
	rec := do(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总"}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", rec.Code)
	}
	if m := decode(rec); m["ok"] != false || m["error"] == nil {
		t.Errorf("no-auth body = %v, want ok:false + error{}", m)
	}

	// valid ingest → 200, portal-derived numeric id, created:true
	rec = do(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总","kind":"投资决策","title":"t","body_md":"x"}`, "Bearer tok-all")
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d body=%s", rec.Code, rec.Body.String())
	}
	m := decode(rec)
	// The v1 API speaks the numeric report id as "id" — a JSON number, the store's row id.
	id, ok := m["id"].(float64)
	if m["ok"] != true || m["created"] != true || !ok || id <= 0 {
		t.Errorf("ingest body = %v, want ok:true created:true id:<positive number>", m)
	}
	// re-ingest same identity (title included — it is part of it) → created:false
	if m := decode(do(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总","title":"t"}`, "Bearer tok-all")); m["created"] != false {
		t.Errorf("re-ingest created = %v, want false", m["created"])
	}

	// missing symbol → 400 JSON envelope with a machine code
	rec = do(`{"date":"2026-07-02","subtype":"汇总"}`, "Bearer tok-all")
	if rec.Code != http.StatusBadRequest || decode(rec)["ok"] != false {
		t.Errorf("missing-symbol status=%d body=%s", rec.Code, rec.Body.String())
	}
	// invalid date → 400
	rec = do(`{"symbol":"300750","date":"2026-7-2","subtype":"汇总"}`, "Bearer tok-all")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad-date status=%d, want 400", rec.Code)
	}
}

// A report ingested with only body_md must not persist a derived HTML copy: storing
// both wastes space and risks the two drifting apart. HTML is rendered on demand
// instead (TestV1GetReportDerivesHTMLOnRead).
func TestV1IngestDoesNotPersistDerivedHTML(t *testing.T) {
	s := newV1Server(t)
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600160","date":"2026-07-02","subtype":"综合决策","body_md":"# hi"}`))
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Ingest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}

	rep := repByIdent(t, s.st, "600160", "2026-07-02", "综合决策")
	if rep == nil {
		t.Fatal("report not found")
	}
	if rep.MD != "# hi" {
		t.Errorf("stored MD = %q, want %q", rep.MD, "# hi")
	}
	if rep.HTML != "" {
		t.Errorf("stored HTML = %q, want empty (md-only ingest should not persist a derived copy)", rep.HTML)
	}

	// A caller-supplied HTML (no md) is still stored verbatim — this is the legacy-import shape.
	req2 := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600161","date":"2026-07-02","subtype":"综合决策","body_html":"<p>legacy</p>"}`))
	req2.Header.Set("Authorization", "Bearer tok-all")
	s.v1Ingest(httptest.NewRecorder(), req2)
	rep2 := repByIdent(t, s.st, "600161", "2026-07-02", "综合决策")
	if rep2 == nil || rep2.HTML != "<p>legacy</p>" {
		t.Errorf("caller-supplied HTML not stored verbatim: %+v", rep2)
	}

	// A caller sending BOTH is not a legacy import — md is authoritative, so any supplied
	// html is dropped too (otherwise a caller could keep the duplicate storage alive).
	req3 := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600162","date":"2026-07-02","subtype":"综合决策","body_md":"# hi","body_html":"<p>should be dropped</p>"}`))
	req3.Header.Set("Authorization", "Bearer tok-all")
	s.v1Ingest(httptest.NewRecorder(), req3)
	rep3 := repByIdent(t, s.st, "600162", "2026-07-02", "综合决策")
	if rep3 == nil {
		t.Fatal("report not found")
	}
	if rep3.HTML != "" {
		t.Errorf("stored HTML = %q, want empty — md present should discard the supplied html", rep3.HTML)
	}
	if rep3.MD != "# hi" {
		t.Errorf("stored MD = %q, want %q", rep3.MD, "# hi")
	}
}

// An explicit payload name wins over a live-resolved one (see v1Ingest), but the caller's
// company-info tool can itself return a padded name (eastmoney does) — that must come out
// clean on the frozen row too, not just when report-portal resolves the name itself.
func TestV1IngestCleansExplicitName(t *testing.T) {
	s := newV1Server(t)
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"000528","date":"2026-07-02","subtype":"综合决策","name":"柳    工","body_md":"# hi"}`))
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Ingest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
	rep := repByIdent(t, s.st, "000528", "2026-07-02", "综合决策")
	if rep == nil {
		t.Fatal("report not found")
	}
	if rep.Name != "柳工" {
		t.Errorf("stored Name = %q, want 柳工 (cleaned)", rep.Name)
	}
}

// The v1 API's report identity is the numeric row id: ingest returns it as "id" (a JSON
// number), and GET /api/v1/reports/{id} resolves it. The old composite uid is gone from
// the wire entirely — there is exactly one identifier now.
func TestV1ReportIdentityIsNumericID(t *testing.T) {
	s := newV1Server(t)
	get := func(id string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/reports/"+id, nil)
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1GetReport(rec, req)
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}

	ing := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600519","date":"2026-07-01","subtype":"汇总","body_md":"# hi"}`))
	ing.Header.Set("Authorization", "Bearer tok-all")
	irec := httptest.NewRecorder()
	s.v1Ingest(irec, ing)
	var im map[string]any
	json.Unmarshal(irec.Body.Bytes(), &im)
	id, ok := im["id"].(float64) // JSON numbers decode as float64
	if !ok || id <= 0 {
		t.Fatalf("ingest id = %v (%T), want a positive JSON number", im["id"], im["id"])
	}
	if _, stale := im["uid"]; stale {
		t.Errorf("ingest response still carries a uid key: %v", im)
	}

	// Fetch by the returned id → the response's id echoes it back.
	rec, m := get(strconv.FormatInt(int64(id), 10))
	if rec.Code != http.StatusOK || m["id"] != id || m["symbol"] != "600519" {
		t.Fatalf("get by id = %d %v, want 200 id:%v symbol:600519", rec.Code, m, id)
	}

	// The composite uid is not an identifier any more: it must not resolve.
	if rec, _ := get("600519|2026-07-01|汇总"); rec.Code != http.StatusNotFound {
		t.Errorf("get by legacy composite uid = %d, want 404 (composite ids are retired)", rec.Code)
	}
}

// The tracking endpoint reports its parent by the same numeric report id the report
// API speaks, as report_id.
func TestV1TrackingReportIDIsNumeric(t *testing.T) {
	s := newV1Server(t)
	ing := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600519","date":"2026-07-01","subtype":"汇总","body_md":"# hi","tracking":[{"itype":"assumption","content":"H2 +20%"}]}`))
	ing.Header.Set("Authorization", "Bearer tok-all")
	irec := httptest.NewRecorder()
	s.v1Ingest(irec, ing)
	var im map[string]any
	json.Unmarshal(irec.Body.Bytes(), &im)
	id, _ := im["id"].(float64)

	req := httptest.NewRequest("GET", "/api/v1/tracking?symbol=600519", nil)
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Tracking(rec, req)
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	items, _ := m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("tracking items = %v, want 1", m["items"])
	}
	if got := items[0].(map[string]any)["report_id"]; got != id {
		t.Errorf("tracking report_id = %v, want the numeric id %v", got, id)
	}
}

// v1GetReport still returns a rendered body_html for md-only reports even though it
// isn't persisted — the API contract for external consumers doesn't change.
func TestV1GetReportDerivesHTMLOnRead(t *testing.T) {
	s := newV1Server(t)
	ireq := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600160","date":"2026-07-02","subtype":"综合决策","body_md":"# hi"}`))
	ireq.Header.Set("Authorization", "Bearer tok-all")
	s.v1Ingest(httptest.NewRecorder(), ireq)

	rep := repByIdent(t, s.st, "600160", "2026-07-02", "综合决策")
	if rep == nil {
		t.Fatal("report not found")
	}
	idStr := strconv.FormatInt(rep.ID, 10)
	greq := httptest.NewRequest("GET", "/api/v1/reports/"+idStr, nil)
	greq.SetPathValue("id", idStr)
	greq.Header.Set("Authorization", "Bearer tok-all")
	grec := httptest.NewRecorder()
	s.v1GetReport(grec, greq)
	m := map[string]any{}
	if err := json.Unmarshal(grec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", grec.Body.String())
	}
	if html, _ := m["body_html"].(string); html != "<h1>hi</h1>\n" {
		t.Errorf("body_html = %q, want rendered <h1>hi</h1>", html)
	}
}

// v1 query scope: a query must be scoped by at least one selector, but subtype (or
// kind/source/date) alone is enough — a dedup tool queries by subtype for symbol-less
// reports (e.g. 行业分析), and requiring specifically symbol/q/run_id wrongly 400s those.
func TestV1QueryScope(t *testing.T) {
	s := newV1Server(t)
	ingest := func(body string) {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest → %d: %s", rec.Code, rec.Body.String())
		}
	}
	query := func(qs string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/reports?"+qs, nil)
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1QueryReports(rec, req)
		return rec
	}

	// A symbol-less industry report (identified by title + subtype).
	ingest(`{"date":"2026-07-02","subtype":"行业分析","title":"某行业深度","body_md":"x"}`)

	// No filters at all → still rejected (can't scan the whole store).
	if rec := query(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no-filter query = %d, want 400", rec.Code)
	}

	// subtype alone is now a valid scope → 200, and it finds the report.
	rec := query("subtype=" + url.QueryEscape("行业分析") + "&limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("subtype-only query = %d: %s", rec.Code, rec.Body.String())
	}
	var m struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	json.Unmarshal(rec.Body.Bytes(), &m)
	if !m.OK || m.Count < 1 {
		t.Fatalf("subtype-only query returned ok=%v count=%d, want the ingested report", m.OK, m.Count)
	}
}
