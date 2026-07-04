package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Thematic reports (macro/industry/strategy/M&A/multi-company comparison) have no single
// home stock, so ingestReport must accept an empty symbol as long as a title identifies the
// report — and the identity key must fall back to the title so different topics on the same
// day don't collide into one uid.

func newIngestTestServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	st.CreateToken("tok-ingest", "dify-ingest", "ingest", "")
	return &Server{st: st, names: LoadNames(t.TempDir(), st)}
}

func postIngest(t *testing.T, s *Server, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/reports", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok-ingest")
	rec := httptest.NewRecorder()
	s.ingestReport(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), &m)
	}
	return rec, m
}

func TestIngestReportTitleOnlyIdentity(t *testing.T) {
	s := newIngestTestServer(t)

	t.Run("symbol empty but title present is accepted", func(t *testing.T) {
		rec, _ := postIngest(t, s, map[string]any{
			"date": "2026-07-04", "kind": "深度研究", "subtype": "行业分析",
			"title": "新能源汽车行业2026年展望", "body_md": "body",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("symbol and title both empty is rejected", func(t *testing.T) {
		rec, _ := postIngest(t, s, map[string]any{
			"date": "2026-07-04", "kind": "深度研究", "subtype": "行业分析", "body_md": "body",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400", rec.Code)
		}
	})

	t.Run("date empty is rejected regardless of title", func(t *testing.T) {
		rec, _ := postIngest(t, s, map[string]any{
			"title": "x", "subtype": "行业分析", "body_md": "body",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400", rec.Code)
		}
	})

	t.Run("different titles same day don't collide on uid", func(t *testing.T) {
		_, j1 := postIngest(t, s, map[string]any{
			"date": "2026-07-05", "kind": "深度研究", "subtype": "行业分析", "title": "半导体行业分析", "body_md": "a",
		})
		_, j2 := postIngest(t, s, map[string]any{
			"date": "2026-07-05", "kind": "深度研究", "subtype": "行业分析", "title": "医药行业分析", "body_md": "b",
		})
		if j1["uid"] == nil || j2["uid"] == nil || j1["uid"] == j2["uid"] {
			t.Fatalf("expected distinct uids, got %v vs %v", j1["uid"], j2["uid"])
		}
	})

	t.Run("same title same day overwrites (created=false on the repeat)", func(t *testing.T) {
		_, j1 := postIngest(t, s, map[string]any{
			"date": "2026-07-06", "kind": "深度研究", "subtype": "策略分析（非个股）", "title": "红利策略复盘", "body_md": "v1",
		})
		_, j2 := postIngest(t, s, map[string]any{
			"date": "2026-07-06", "kind": "深度研究", "subtype": "策略分析（非个股）", "title": "红利策略复盘", "body_md": "v2",
		})
		if j1["uid"] != j2["uid"] {
			t.Fatalf("expected same uid on repeat, got %v vs %v", j1["uid"], j2["uid"])
		}
		if j1["created"] != true || j2["created"] != false {
			t.Fatalf("expected created=true then false, got %v then %v", j1["created"], j2["created"])
		}
	})
}
