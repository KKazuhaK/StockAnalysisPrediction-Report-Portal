package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Dify's ingestReport tool speaks v1 (POST /api/v1/reports), not the legacy /api/reports —
// confirmed by inspecting the tool's OpenAPI schema and its provider_id on DeepResearch's
// ingest nodes. Thematic (symbol-less) reports must therefore be accepted here too, with the
// title standing in for the identity when there's no symbol — mirroring the legacy fix.

func postV1Ingest(t *testing.T, s *Server, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Ingest(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), &m)
	}
	return rec, m
}

func TestV1IngestTitleOnlyIdentity(t *testing.T) {
	s := newV1Server(t)

	t.Run("symbol empty but title present is accepted", func(t *testing.T) {
		rec, m := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-04", "kind": "深度研究", "subtype": "专题研究",
			"title": "CPO行业深度研究报告", "body_md": "body",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if m["ok"] != true {
			t.Fatalf("ok=%v, want true", m["ok"])
		}
	})

	t.Run("symbol and title both empty is rejected", func(t *testing.T) {
		rec, m := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-04", "kind": "深度研究", "subtype": "专题研究", "body_md": "body",
		})
		if rec.Code != http.StatusBadRequest || m["ok"] != false {
			t.Fatalf("status=%d body=%v, want 400 ok:false", rec.Code, m)
		}
	})

	// A title excuses a missing symbol (that is the thematic case above) — it must not also
	// excuse a missing date, which the identity and the whole feed are ordered by.
	t.Run("date empty is rejected even when a title stands in for the symbol", func(t *testing.T) {
		rec, m := postV1Ingest(t, s, map[string]any{
			"title": "x", "subtype": "专题研究", "body_md": "body",
		})
		if rec.Code != http.StatusBadRequest || m["ok"] != false {
			t.Fatalf("status=%d body=%v, want 400 ok:false", rec.Code, m)
		}
	})

	t.Run("different titles same day don't collide", func(t *testing.T) {
		_, j1 := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-05", "kind": "深度研究", "subtype": "专题研究", "title": "半导体行业深度研究", "body_md": "a",
		})
		_, j2 := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-05", "kind": "深度研究", "subtype": "专题研究", "title": "医药行业深度研究", "body_md": "b",
		})
		id1, _ := j1["id"].(float64)
		id2, _ := j2["id"].(float64)
		if id1 <= 0 || id2 <= 0 || id1 == id2 {
			t.Fatalf("expected distinct ids, got %v vs %v", j1["id"], j2["id"])
		}
	})

	t.Run("same title same day overwrites", func(t *testing.T) {
		_, j1 := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-06", "kind": "深度研究", "subtype": "专题研究", "title": "红利策略复盘", "body_md": "v1",
		})
		_, j2 := postV1Ingest(t, s, map[string]any{
			"date": "2026-07-06", "kind": "深度研究", "subtype": "专题研究", "title": "红利策略复盘", "body_md": "v2",
		})
		if j1["id"] != j2["id"] {
			t.Fatalf("expected same id on repeat, got %v vs %v", j1["id"], j2["id"])
		}
		if j1["created"] != true || j2["created"] != false {
			t.Fatalf("expected created=true then false, got %v then %v", j1["created"], j2["created"])
		}
	})
}
