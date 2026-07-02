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
		"/healthz", "/api/version",
	} {
		if paths[p] == nil {
			t.Errorf("openapi.json missing path %s", p)
		}
	}
}
