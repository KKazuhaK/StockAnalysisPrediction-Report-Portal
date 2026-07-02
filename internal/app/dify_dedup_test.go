package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The Dify "check before generate" node dedups on the exact triple symbol+date+subtype
// by reading GET /api/v1/reports and inspecting `total`. These tests lock that contract
// (and the scope separation: reads require a query-scoped token, never an ingest one).

// seedDedupServer returns a v1 server pre-loaded with three reports for 600160:
// two on 2026-07-02 (汇总, 财务分析) and one on 2026-07-01 (汇总). It also mints a
// query-scoped and an ingest-scoped token so scope enforcement can be exercised.
func seedDedupServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	st.CreateToken("tok-query", "dify-query", "query", "")
	st.CreateToken("tok-ingest", "dify-ingest", "ingest", "")
	seed := []struct{ date, subtype string }{
		{"2026-07-02", "汇总"},
		{"2026-07-02", "财务分析"},
		{"2026-07-01", "汇总"},
	}
	for _, r := range seed {
		if _, err := st.UpsertReport(Rep{
			UID: deriveUID("600160", r.date, r.subtype), Symbol: "600160", Date: r.date,
			Kind: "投资决策", RType: r.subtype, Title: "t", Source: "dify", Time: r.date, MD: "body",
		}); err != nil {
			t.Fatalf("seed %s/%s: %v", r.date, r.subtype, err)
		}
	}
	return &Server{st: st, names: LoadNames(t.TempDir(), st)}
}

// listReports drives GET /api/v1/reports with the given query params + Bearer token.
func listReports(t *testing.T, s *Server, params url.Values, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/reports?"+params.Encode(), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.v1QueryReports(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
	}
	return rec, m
}

// The precise dedup probe: symbol+date+subtype → total tells the Dify node whether a
// report for that exact identity already exists (skip) or not (generate).
func TestV1DedupProbeBySymbolDateSubtype(t *testing.T) {
	s := seedDedupServer(t)

	cases := []struct {
		name              string
		symbol, date, sub string
		wantTotal         float64
	}{
		{"exact hit → skip", "600160", "2026-07-02", "汇总", 1},
		{"same day other subtype exists", "600160", "2026-07-02", "财务分析", 1},
		{"subtype absent on that day → generate", "600160", "2026-07-02", "不存在", 0},
		{"subtype exists but other day → generate", "600160", "2026-07-03", "汇总", 0},
		{"day without subtype filter → both rows", "600160", "2026-07-02", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := url.Values{"symbol": {c.symbol}, "date": {c.date}}
			if c.sub != "" {
				p.Set("subtype", c.sub)
			}
			rec, m := listReports(t, s, p, "tok-query")
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if m["ok"] != true {
				t.Fatalf("ok=%v, want true", m["ok"])
			}
			if got := m["total"].(float64); got != c.wantTotal {
				t.Errorf("total=%v, want %v", got, c.wantTotal)
			}
		})
	}
}

// rtype is an accepted alias for subtype on the read path, so the Dify node may send
// either; both must select the same identity.
func TestV1DedupProbeRTypeAlias(t *testing.T) {
	s := seedDedupServer(t)
	rec, m := listReports(t, s, url.Values{"symbol": {"600160"}, "date": {"2026-07-02"}, "rtype": {"汇总"}}, "tok-query")
	if rec.Code != http.StatusOK || m["total"].(float64) != 1 {
		t.Fatalf("rtype alias: status=%d total=%v, want 200/1", rec.Code, m["total"])
	}
}

// Reads must accept a query-scoped token and reject an ingest-scoped one — the query
// node has to use a read-only token, never the ingest token.
func TestV1DedupProbeScopeEnforced(t *testing.T) {
	s := seedDedupServer(t)
	base := url.Values{"symbol": {"600160"}, "date": {"2026-07-02"}, "subtype": {"汇总"}}

	if rec, _ := listReports(t, s, base, "tok-query"); rec.Code != http.StatusOK {
		t.Errorf("query token: status=%d, want 200", rec.Code)
	}
	if rec, m := listReports(t, s, base, "tok-ingest"); rec.Code != http.StatusUnauthorized || m["ok"] != false {
		t.Errorf("ingest token on read: status=%d ok=%v, want 401/false", rec.Code, m["ok"])
	}
	if rec, _ := listReports(t, s, base, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status=%d, want 401", rec.Code)
	}
}

// Manifest is the coarse probe: it answers "any report for this symbol on this date?"
// and "which subtypes exist overall?" but NOT the date×subtype cross — the node must
// fall back to the list probe for exact-identity dedup.
func TestV1ManifestProbe(t *testing.T) {
	s := seedDedupServer(t)
	req := httptest.NewRequest("GET", "/api/v1/reports/manifest?symbol=600160", nil)
	req.Header.Set("Authorization", "Bearer tok-query")
	rec := httptest.NewRecorder()
	s.v1Manifest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", rec.Body.String())
	}
	if m["total"].(float64) != 3 {
		t.Errorf("manifest total=%v, want 3", m["total"])
	}
	subs := toStringSet(m["subtypes"])
	if !subs["汇总"] || !subs["财务分析"] {
		t.Errorf("subtypes=%v, want 汇总+财务分析", m["subtypes"])
	}
	// date 2026-07-02 must report count=2 (both subtypes that day)
	var got0702 float64 = -1
	for _, d := range m["dates"].([]any) {
		di := d.(map[string]any)
		if di["date"] == "2026-07-02" {
			got0702 = di["count"].(float64)
		}
	}
	if got0702 != 2 {
		t.Errorf("dates[2026-07-02].count=%v, want 2", got0702)
	}
}

func toStringSet(v any) map[string]bool {
	out := map[string]bool{}
	if arr, ok := v.([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}
