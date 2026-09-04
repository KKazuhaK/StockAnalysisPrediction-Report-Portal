package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
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
		"/api/v1/reports", "/api/v1/reports/{id}", "/api/v1/reports/manifest",
		"/api/v1/runs", "/api/v1/symbols", "/api/v1/tracking", "/api/v1/tracking/{id}",
		"/api/v1/now", "/healthz",
	} {
		if paths[p] == nil {
			t.Errorf("openapi.json missing path %s", p)
		}
	}
}

// The documented IngestRequest schema must match v1Ingest's actual runtime validation: date is
// always required, while the identity requires either symbol or title and the report type accepts
// either subtype or its rtype alias.
func TestOpenAPIIngestRequestMatchesRuntimeValidation(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, r := range required {
		switch r {
		case "symbol", "title", "subtype", "rtype":
			t.Fatalf("IngestRequest.required unconditionally lists %q — no longer matches v1Ingest aliases/alternatives: %v", r, required)
		}
	}
	foundDate := false
	for _, r := range required {
		if r == "date" {
			foundDate = true
		}
	}
	if !foundDate {
		t.Errorf("IngestRequest.required missing date: %v", required)
	}
	// The accepted combinations must be expressed somewhere (anyOf is the OpenAPI 3.1 idiom here).
	anyOf, ok := schema["anyOf"].([]any)
	if !ok || len(anyOf) == 0 {
		t.Fatalf("IngestRequest has no anyOf constraint documenting identity/type alternatives")
	}
	seen := map[string]bool{}
	for _, clause := range anyOf {
		c := clause.(map[string]any)
		var parts []string
		for _, r := range c["required"].([]any) {
			parts = append(parts, r.(string))
		}
		seen[strings.Join(parts, "+")] = true
	}
	for _, want := range []string{"symbol+subtype", "symbol+rtype", "title+subtype", "title+rtype"} {
		if !seen[want] {
			t.Errorf("IngestRequest.anyOf missing %s branch: %v", want, anyOf)
		}
	}
}

func TestOpenAPILocalizedEndpoint(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/openapi.json?lang=en-US", nil)
	rec := httptest.NewRecorder()
	(&Server{}).apiOpenAPI(rec, req)
	if got := rec.Header().Get("Content-Language"); got != "en-US" {
		t.Fatalf("Content-Language = %q, want en-US", got)
	}
	if strings.Contains(rec.Body.String(), `"x-i18n"`) {
		t.Fatalf("localized response should strip x-i18n extensions")
	}
	var spec map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("localized openapi response is not valid JSON: %v", err)
	}
	info := spec["info"].(map[string]any)
	if got := info["title"]; got != "Research Report Portal · Dify Machine API (v1)" {
		t.Fatalf("localized info.title = %v", got)
	}
	post := spec["paths"].(map[string]any)["/api/v1/reports"].(map[string]any)["post"].(map[string]any)
	if got := post["summary"].(string); !strings.HasPrefix(got, "Ingest one report") {
		t.Fatalf("localized POST /api/v1/reports summary = %q", got)
	}
	params := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if params["$ref"] == nil {
		t.Fatalf("localized request schema lost its $ref: %v", params)
	}
}

// TestOpenAPIDocumentsEveryV1Route keeps the spec and the router from drifting apart. The spec calls
// itself "the single source of truth" for the machine API, which is only true while it lists what
// the router actually serves — and a route that exists but is undocumented is invisible to anyone
// generating a client from it, while one that is documented and gone is a promise the portal breaks.
//
// This has already happened once: v0.4.45 fixed a field the v1 read had always returned and the spec
// had never listed.
func TestOpenAPIDocumentsEveryV1Route(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete":
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	registered := regexp.MustCompile(`v1\("([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(registered) < 8 {
		// A scan that silently matches nothing would pass every assertion below for ever.
		t.Fatalf("found only %d v1 routes; the scan is broken, not the spec", len(registered))
	}

	for _, m := range registered {
		if !documented[m[1]] {
			t.Errorf("%s is served but not in openapi.json — a machine consumer cannot find it", m[1])
		}
		delete(documented, m[1])
	}
	// Whatever is left must be a deliberate non-v1 entry, not a route that has been removed.
	for route := range documented {
		if strings.Contains(route, "/api/v1") {
			t.Errorf("%s is documented but no longer served — the spec promises something gone", route)
		}
	}
}

// TestHealthzSpecMatchesTheHandler pins the one documented endpoint outside /api/v1. It gained a
// database check and a 503 in v0.4.48, and a spec that still described the old two-field answer
// would tell an orchestrator to treat an unreachable database as healthy.
func TestHealthzSpecMatchesTheHandler(t *testing.T) {
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]json.RawMessage `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatal(err)
	}
	got := spec.Paths["/healthz"].Get.Responses
	for _, code := range []string{"200", "503"} {
		if _, ok := got[code]; !ok {
			t.Errorf("/healthz does not document a %s response, and the handler can return one", code)
		}
	}

	// And the handler really does answer with both, so the assertion above is about something.
	st := newTestStore(t)
	s := &Server{st: st}
	if code, _ := healthGET(t, s); code != http.StatusOK {
		t.Fatalf("healthy → %d", code)
	}
	st.Close()
	s.health = healthCache{}
	if code, _ := healthGET(t, s); code != http.StatusServiceUnavailable {
		t.Fatalf("database down → %d", code)
	}
}
