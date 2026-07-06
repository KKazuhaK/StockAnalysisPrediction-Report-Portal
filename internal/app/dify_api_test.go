package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// seedDifyTarget creates a Dify target with a known secret and returns its id.
func seedDifyTarget(t *testing.T, s *Server, name string) int64 {
	t.Helper()
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify Workflow", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{
		BaseURL: "https://dify.example/v1", APIKey: "app-secret",
		Inputs: []dify.Input{{Variable: "symbol", Required: true}},
	})
	id, err := s.st.CreateTarget(difyPluginSlug, name, string(cfg))
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return id
}

func getTarget(t *testing.T, h func(http.ResponseWriter, *http.Request, string), id int64) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	h(rec, req, "admin")
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func putTarget(t *testing.T, h func(http.ResponseWriter, *http.Request, string), id int64, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	req.SetPathValue("id", fmt.Sprint(id))
	h(rec, req, "admin")
	return rec.Code
}

// Editing a Dify target: GET returns its editable config (never the api_key), and
// PUT updates name + inputs while keeping the stored key when the client sends none.
func TestDifyTargetEditRoundTrip(t *testing.T) {
	s := batchServer(t)
	id := seedDifyTarget(t, s, "Old name")

	// GET surfaces name, base_url, inputs, has_key — but not the secret.
	code, got := getTarget(t, s.apiBatchDifyTargetGet, id)
	if code != http.StatusOK {
		t.Fatalf("GET → %d: %v", code, got)
	}
	if got["name"] != "Old name" || got["base_url"] != "https://dify.example/v1" || got["has_key"] != true {
		t.Fatalf("GET body = %v", got)
	}
	if _, leaked := got["api_key"]; leaked {
		t.Fatalf("GET must not surface api_key: %v", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "app-secret") {
		t.Fatalf("GET leaked the api_key: %s", b)
	}

	// PUT with a blank api_key updates name + inputs and preserves the stored key.
	body := `{"name":"New name","base_url":"https://dify.example/v1","api_key":"",` +
		`"inputs":[{"variable":"symbol","required":true},{"variable":"rumor"}]}`
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id, body); code != http.StatusOK {
		t.Fatalf("PUT → %d", code)
	}
	tgt, _ := s.st.GetTarget(id)
	if tgt.Name != "New name" {
		t.Errorf("name = %q, want New name", tgt.Name)
	}
	var after difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &after)
	if after.APIKey != "app-secret" {
		t.Errorf("blank api_key should preserve stored key, got %q", after.APIKey)
	}
	if len(after.Inputs) != 2 || after.Inputs[1].Variable != "rumor" {
		t.Errorf("inputs = %+v", after.Inputs)
	}

	// A fresh api_key rotates the stored one.
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"New name","base_url":"https://dify.example/v1","api_key":"app-rotated","inputs":[{"variable":"symbol"}]}`); code != http.StatusOK {
		t.Fatalf("PUT2 → %d", code)
	}
	tgt, _ = s.st.GetTarget(id)
	json.Unmarshal([]byte(tgt.Config), &after)
	if after.APIKey != "app-rotated" {
		t.Errorf("api_key = %q, want app-rotated", after.APIKey)
	}
}

// The Dify edit endpoints only serve Dify targets, and never a missing one.
func TestDifyTargetEditRejectsNonDifyAndMissing(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertPlugin("custom", "Custom", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	custom, _ := s.st.CreateTarget("custom", "C", "{}")

	if code, _ := getTarget(t, s.apiBatchDifyTargetGet, custom); code != http.StatusNotFound {
		t.Errorf("GET non-dify → %d, want 404", code)
	}
	if code, _ := getTarget(t, s.apiBatchDifyTargetGet, 99999); code != http.StatusNotFound {
		t.Errorf("GET missing → %d, want 404", code)
	}
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, custom,
		`{"name":"x","base_url":"y","api_key":"z","inputs":[]}`); code != http.StatusNotFound {
		t.Errorf("PUT non-dify → %d, want 404", code)
	}
}

// PUT rejects an empty name or base_url, and refuses to save a target with no key
// when none is stored yet.
func TestDifyTargetUpdateValidation(t *testing.T) {
	s := batchServer(t)
	id := seedDifyTarget(t, s, "T")

	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"  ","base_url":"https://dify.example/v1","api_key":"k","inputs":[{"variable":"symbol"}]}`); code != http.StatusBadRequest {
		t.Errorf("blank name → %d, want 400", code)
	}
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"T","base_url":"","api_key":"k","inputs":[{"variable":"symbol"}]}`); code != http.StatusBadRequest {
		t.Errorf("blank base_url → %d, want 400", code)
	}
}
