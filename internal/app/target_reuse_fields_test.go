package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDifyTargetEditKeepsReuseFields guards a silent-data-loss trap: the Dify target editor
// re-marshals the whole config, so any field it does not know about would be wiped on an ordinary
// name/inputs edit — quietly disabling same-day reuse (ADR 0022 R1). Editing must round-trip
// output_subtype / symbol_input, and be able to set them.
func TestDifyTargetEditKeepsReuseFields(t *testing.T) {
	s := batchServer(t)
	st := s.st
	if err := st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateTarget(difyPluginSlug, "Valuation",
		`{"base_url":"http://x/v1","api_key":"k","mode":"workflow","output_subtype":"val","symbol_input":"code"}`)
	if err != nil {
		t.Fatal(err)
	}

	edit := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", fmt.Sprintf("/api/admin/batch/dify/targets/%d", id), strings.NewReader(body))
		req.SetPathValue("id", fmt.Sprint(id))
		rec := httptest.NewRecorder()
		s.apiBatchDifyTargetUpdate(rec, req, "admin")
		return rec
	}
	stored := func() (string, string) {
		tg, _ := st.GetTarget(id)
		return targetOutputSubtype(tg.Config), targetSymbolInput(tg.Config)
	}

	// A plain rename that says nothing about the reuse fields must PRESERVE them.
	if rec := edit(`{"name":"Valuation v2","base_url":"http://x/v1"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename → %d (%s)", rec.Code, rec.Body.String())
	}
	if sub, sym := stored(); sub != "val" || sym != "code" {
		t.Errorf("after a rename the reuse fields were lost: subtype=%q symbol_input=%q", sub, sym)
	}

	// And an explicit edit must be able to change them.
	if rec := edit(`{"name":"Valuation v2","base_url":"http://x/v1","output_subtype":"估值分析","symbol_input":"stock"}`); rec.Code != http.StatusOK {
		t.Fatalf("edit → %d (%s)", rec.Code, rec.Body.String())
	}
	if sub, sym := stored(); sub != "估值分析" || sym != "stock" {
		t.Errorf("explicit edit = subtype %q / symbol_input %q, want 估值分析 / stock", sub, sym)
	}

	// The editor must also read them back, so the form can render their current values.
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/admin/batch/dify/targets/%d", id), nil)
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	s.apiBatchDifyTargetGet(rec, req, "admin")
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["output_subtype"] != "估值分析" || got["symbol_input"] != "stock" {
		t.Errorf("GET returned %v / %v, want the stored reuse fields", got["output_subtype"], got["symbol_input"])
	}
}
