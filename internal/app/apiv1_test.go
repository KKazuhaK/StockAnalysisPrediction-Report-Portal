package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	return &Server{st: st, names: LoadNames(t.TempDir(), st)}
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
	if m["ok"] != true || m["uid"] != "300750|2026-07-02|汇总" || m["created"] != true {
		t.Errorf("ingest body = %v, want ok:true uid:300750|2026-07-02|汇总 created:true", m)
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
