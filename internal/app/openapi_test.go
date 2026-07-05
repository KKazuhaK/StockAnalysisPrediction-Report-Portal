package app

import (
	"encoding/json"
	"testing"
)

// The embedded OpenAPI spec must be valid 3.1 JSON and cover every v1 endpoint
// (plus the public ones) so the served spec / rendered docs never drift.
func TestOpenAPISpecValid(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", spec["openapi"])
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing")
	}
	for _, p := range []string{
		"/api/v1/reports", "/api/v1/reports/{uid}", "/api/v1/reports/manifest",
		"/api/v1/runs", "/api/v1/symbols", "/api/v1/tracking", "/api/v1/tracking/{id}",
		"/api/v1/now", "/healthz", "/api/version",
	} {
		if paths[p] == nil {
			t.Errorf("openapi.json missing path %s", p)
		}
	}
}

// The documented IngestRequest schema must match v1Ingest's actual runtime validation: date and
// subtype are always required, but symbol is not — a thematic (no single home stock) report is
// identified by title instead. A caller reading the docs (or a schema-driven tool import) should
// never be told symbol is mandatory when the server will happily accept title-only.
func TestOpenAPIIngestRequestAllowsTitleOnlySymbol(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if r == "symbol" {
			t.Fatalf("IngestRequest.required unconditionally lists symbol — no longer matches v1Ingest (symbol is optional when title is given): %v", required)
		}
	}
	for _, want := range []string{"date", "subtype"} {
		found := false
		for _, r := range required {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("IngestRequest.required missing %q: %v", want, required)
		}
	}
	// "symbol or title" must be expressed somewhere (anyOf is the OpenAPI 3.1 idiom for this).
	anyOf, ok := schema["anyOf"].([]any)
	if !ok || len(anyOf) == 0 {
		t.Fatalf("IngestRequest has no anyOf constraint documenting the symbol-or-title requirement")
	}
	seen := map[string]bool{}
	for _, clause := range anyOf {
		c := clause.(map[string]any)
		for _, r := range c["required"].([]any) {
			seen[r.(string)] = true
		}
	}
	if !seen["symbol"] || !seen["title"] {
		t.Errorf("IngestRequest.anyOf should require symbol in one branch and title in another, got %v", anyOf)
	}
}
