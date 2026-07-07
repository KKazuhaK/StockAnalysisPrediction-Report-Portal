package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// v1 identity is portal-derived and deterministic: symbol|date|rtype (no kind).
func TestDeriveUID(t *testing.T) {
	if got := deriveUID("300750", "2026-07-02", "汇总"); got != "300750|2026-07-02|汇总" {
		t.Errorf("deriveUID = %q, want 300750|2026-07-02|汇总", got)
	}
	// kind is NOT part of identity, so it can't fork the uid
	a := deriveUID("300750", "2026-07-02", "汇总")
	b := deriveUID("300750", "2026-07-02", "汇总")
	if a != b {
		t.Errorf("deriveUID not deterministic: %q vs %q", a, b)
	}
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

	// valid ingest → 200, portal-derived uid, created:true
	rec = do(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总","kind":"投资决策","title":"t","body_md":"x"}`, "Bearer tok-all")
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d body=%s", rec.Code, rec.Body.String())
	}
	m := decode(rec)
	uid, _ := m["uid"].(string)
	// The v1 API speaks the numeric report id (rid, "n123") as "uid"; the composite
	// symbol|date|rtype stays the internal dedup key and is never exposed.
	if m["ok"] != true || m["created"] != true || !strings.HasPrefix(uid, "n") || strings.Contains(uid, "|") {
		t.Errorf("ingest body = %v, want ok:true created:true uid:<rid like n1>", m)
	}
	// re-ingest same identity → created:false
	if m := decode(do(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总"}`, "Bearer tok-all")); m["created"] != false {
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

	rep := s.st.GetByUID("600160|2026-07-02|综合决策")
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
	rep2 := s.st.GetByUID("600161|2026-07-02|综合决策")
	if rep2 == nil || rep2.HTML != "<p>legacy</p>" {
		t.Errorf("caller-supplied HTML not stored verbatim: %+v", rep2)
	}

	// A caller sending BOTH is not a legacy import — md is authoritative, so any supplied
	// html is dropped too (otherwise a caller could keep the duplicate storage alive).
	req3 := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600162","date":"2026-07-02","subtype":"综合决策","body_md":"# hi","body_html":"<p>should be dropped</p>"}`))
	req3.Header.Set("Authorization", "Bearer tok-all")
	s.v1Ingest(httptest.NewRecorder(), req3)
	rep3 := s.st.GetByUID("600162|2026-07-02|综合决策")
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

// The v1 API's report identity is the numeric rid ("n123"): ingest returns it as
// "uid", and GET/{id} resolves it — while the internal composite uid still works for
// back-compat. The field/route name stays "uid" so external callers (Dify) don't change.
func TestV1ReportIdentityIsRid(t *testing.T) {
	s := newV1Server(t)
	get := func(id string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/reports/"+id, nil)
		req.SetPathValue("uid", id)
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
	rid, _ := im["uid"].(string)
	if !strings.HasPrefix(rid, "n") || strings.Contains(rid, "|") {
		t.Fatalf("ingest uid = %q, want a numeric rid (nN)", rid)
	}

	// Fetch by the returned rid → the response's uid echoes the rid.
	rec, m := get(rid)
	if rec.Code != http.StatusOK || m["uid"] != rid || m["symbol"] != "600519" {
		t.Fatalf("get by rid = %d %v, want 200 uid:%s symbol:600519", rec.Code, m, rid)
	}

	// Back-compat: the internal composite uid still resolves.
	if rec, _ := get(deriveUID("600519", "2026-07-01", "汇总")); rec.Code != http.StatusOK {
		t.Fatalf("back-compat get by composite uid = %d, want 200", rec.Code)
	}
}

// The tracking endpoint reports its parent by the same numeric report id (rid) the
// report API speaks — not the internal composite uid.
func TestV1TrackingReportUIDIsRid(t *testing.T) {
	s := newV1Server(t)
	ing := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600519","date":"2026-07-01","subtype":"汇总","body_md":"# hi","tracking":[{"itype":"assumption","content":"H2 +20%"}]}`))
	ing.Header.Set("Authorization", "Bearer tok-all")
	irec := httptest.NewRecorder()
	s.v1Ingest(irec, ing)
	var im map[string]any
	json.Unmarshal(irec.Body.Bytes(), &im)
	rid, _ := im["uid"].(string)

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
	if got := items[0].(map[string]any)["report_uid"]; got != rid {
		t.Errorf("tracking report_uid = %v, want the rid %s (not the composite uid)", got, rid)
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

	greq := httptest.NewRequest("GET", "/api/v1/reports/600160|2026-07-02|综合决策", nil)
	greq.SetPathValue("uid", "600160|2026-07-02|综合决策")
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
