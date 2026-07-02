package batch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// difySpec is a Dify-shaped declarative manifest used across the interpreter tests.
const difySpec = `{
  "schema_version": "1",
  "id": "dify-workflow",
  "name": "Dify Workflow",
  "version": "1.0.0",
  "inputs": [{"key": "code", "required": true}, {"key": "rumor"}],
  "config": [{"key": "base_url"}, {"key": "api_key", "secret": true}],
  "request": {
    "method": "POST",
    "url": "{{config.base_url}}/v1/workflows/run",
    "headers": {"Authorization": "Bearer {{config.api_key}}", "Content-Type": "application/json"},
    "body": {"inputs": "{{inputs}}", "response_mode": "blocking", "user": "report-portal-batch"}
  },
  "response": {
    "run_id": "workflow_run_id",
    "status": "data.status",
    "map": {"succeeded": "ok", "failed": "failed", "stopped": "failed"},
    "partial_when": {"path": "data.outputs.result_status", "equals": "partial"},
    "detail": "data.error"
  }
}`

func newProvider(t *testing.T, baseURL string, client *http.Client) Provider {
	t.Helper()
	m, err := Compile([]byte(difySpec))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return m.NewProvider(map[string]string{"base_url": baseURL, "api_key": "sk-test"}, client)
}

// The interpreter must render {{config.*}} into URL/headers and expand the special
// {{inputs}} token into the inputs object, then map a succeeded response to Ok.
func TestManifestProviderOkRendersRequest(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"workflow_run_id":"run-123","data":{"status":"succeeded","error":null}}`)
	}))
	defer srv.Close()

	res, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(),
		map[string]string{"code": "600519", "rumor": "merger talk"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != Ok {
		t.Errorf("Status = %v, want Ok", res.Status)
	}
	if res.RunID != "run-123" {
		t.Errorf("RunID = %q, want run-123", res.RunID)
	}
	if gotPath != "/v1/workflows/run" {
		t.Errorf("path = %q, want /v1/workflows/run", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	inputs, ok := gotBody["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("body.inputs is not an object: %#v", gotBody["inputs"])
	}
	if inputs["code"] != "600519" || inputs["rumor"] != "merger talk" {
		t.Errorf("body.inputs = %#v", inputs)
	}
	if gotBody["response_mode"] != "blocking" {
		t.Errorf("body.response_mode = %#v, want blocking", gotBody["response_mode"])
	}
}

// A backend that ran but reported failure is a normal RunResult{Failed}, not an error.
func TestManifestProviderFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"failed","error":"bad code"}}`)
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(), map[string]string{"code": "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != Failed {
		t.Errorf("Status = %v, want Failed", res.Status)
	}
	if res.Detail != "bad code" {
		t.Errorf("Detail = %q, want bad code", res.Detail)
	}
}

// partial_when overrides a succeeded status when the workflow emits a partial marker.
func TestManifestProviderPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"succeeded","outputs":{"result_status":"partial"}}}`)
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(), map[string]string{"code": "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != Partial {
		t.Errorf("Status = %v, want Partial", res.Status)
	}
}

// 5xx / 429 are transport-transient: the engine should retry them.
func TestManifestProviderTransientOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(), map[string]string{"code": "x"})
	if err == nil {
		t.Fatal("expected an error for 502")
	}
	if !IsTransient(err) {
		t.Errorf("502 should classify as transient, got %v", err)
	}
}

// 4xx is a permanent request problem: no retry.
func TestManifestProviderPermanentOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(), map[string]string{"code": "x"})
	if err == nil {
		t.Fatal("expected an error for 400")
	}
	if IsTransient(err) {
		t.Errorf("400 should classify as permanent, got %v", err)
	}
}

// A missing required input is rejected before any HTTP call is made.
func TestManifestProviderMissingRequiredInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("backend must not be called when a required input is missing")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, srv.Client()).Run(context.Background(), map[string]string{"rumor": "no code"})
	if err == nil {
		t.Fatal("expected an error for missing required input 'code'")
	}
	if IsTransient(err) {
		t.Errorf("missing input should be permanent, got %v", err)
	}
}
