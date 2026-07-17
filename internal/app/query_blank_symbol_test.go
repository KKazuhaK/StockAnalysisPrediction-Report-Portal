package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A caller that sends `symbol=` — the parameter present but blank — has lost the code it
// meant to filter by. Dropping the condition and answering with whatever else matches is the
// worst possible reading: on 2026-07-16 a workflow whose symbol resolved to "" asked for the
// previous 股权分析 of its stock, was handed a DIFFERENT company's report, and wrote an
// "incremental update" of it. Blank is not "any" — it is a caller bug, and it must be loud.
//
// An ABSENT symbol still means "any": that is how thematic reports (which have no code) are
// legitimately queried, so the two cases must stay distinguishable.
func TestQueryReportsRejectsBlankSymbol(t *testing.T) {
	s := newV1Server(t)
	s.st.CreateToken("tok-query", "dify-query", "query", "")
	if _, _, err := s.st.UpsertReport(Rep{Symbol: "688116", Title: "688116 股权分析", RType: "股权分析", Date: "2026-07-16", MD: "other company"}); err != nil {
		t.Fatal(err)
	}

	do := func(url string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer tok-query")
		rec := httptest.NewRecorder()
		s.v1QueryReports(rec, req)
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}

	t.Run("blank symbol is rejected, not widened to every stock", func(t *testing.T) {
		rec, m := do("/api/v1/reports?symbol=&subtype=%E8%82%A1%E6%9D%83%E5%88%86%E6%9E%90&limit=1")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 — a blank symbol handed back %v", rec.Code, m["items"])
		}
		if m["ok"] != false {
			t.Errorf("body = %v, want the ok:false error envelope", m)
		}
	})

	t.Run("absent symbol still means any — thematic queries keep working", func(t *testing.T) {
		rec, m := do("/api/v1/reports?subtype=%E8%82%A1%E6%9D%83%E5%88%86%E6%9E%90&limit=1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: omitting symbol must stay a valid 'any stock' query", rec.Code)
		}
		if m["total"] == nil || m["total"].(float64) != 1 {
			t.Errorf("total = %v, want 1", m["total"])
		}
	})
}
