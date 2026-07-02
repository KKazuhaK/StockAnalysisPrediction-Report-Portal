package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

const batchTestSpec = `{
  "id":"p","name":"P","version":"1.0.0",
  "inputs":[{"key":"code","required":true}],
  "request":{"method":"POST","url":"{{config.base_url}}/run","headers":{"Content-Type":"application/json"},"body":{"inputs":"{{inputs}}"}},
  "response":{"run_id":"workflow_run_id","status":"data.status","map":{"succeeded":"ok","failed":"failed"},"detail":"data.error"}
}`

func batchServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
}

func post(t *testing.T, h func(http.ResponseWriter, *http.Request, string), body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("handler → %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func difyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		inputs, _ := body["inputs"].(map[string]any)
		if code, _ := inputs["code"].(string); code == "fail" {
			io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"failed","error":"bad code"}}`)
			return
		}
		io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"succeeded"}}`)
	}))
}

func waitForJobDone(t *testing.T, st *Store, jobID int64) BatchJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := st.GetBatchJob(jobID); ok && (j.Status == "finished" || j.Status == "cancelled") {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %d did not finish in time", jobID)
	return BatchJob{}
}

// Full path: import a plugin, configure a target at a fake Dify, run a job, and
// confirm per-row outcomes land as final aggregate counts.
func TestBatchEndToEndViaHandlers(t *testing.T) {
	dify := difyServer(t)
	defer dify.Close()
	srv := batchServer(t)

	post(t, srv.apiBatchPluginImport, batchTestSpec)

	added := post(t, srv.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, dify.URL))
	targetID := int64(added["id"].(float64))

	created := post(t, srv.apiBatchJobCreate, fmt.Sprintf(
		`{"target_id":%d,"concurrency":2,"max_retries":1,"rows":[{"code":"a"},{"code":"fail"},{"code":"c"}]}`, targetID))
	jobID := int64(created["job_id"].(float64))

	j := waitForJobDone(t, srv.st, jobID)
	if j.Status != "finished" {
		t.Errorf("status = %q, want finished", j.Status)
	}
	if j.Total != 3 || j.Succeeded != 2 || j.Failed != 1 {
		t.Errorf("counts = total:%d ok:%d fail:%d, want 3/2/1", j.Total, j.Succeeded, j.Failed)
	}
}

// The operator's concurrency is clamped to the admin ceiling.
func TestBatchConcurrencyClampedToAdminMax(t *testing.T) {
	dify := difyServer(t)
	defer dify.Close()
	srv := batchServer(t)
	srv.st.SetSetting("batch_max_concurrency", "2")
	post(t, srv.apiBatchPluginImport, batchTestSpec)
	added := post(t, srv.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, dify.URL))
	targetID := int64(added["id"].(float64))

	created := post(t, srv.apiBatchJobCreate, fmt.Sprintf(
		`{"target_id":%d,"concurrency":10,"rows":[{"code":"a"}]}`, targetID))
	if got := int(created["concurrency"].(float64)); got != 2 {
		t.Errorf("clamped concurrency = %d, want 2", got)
	}
	jobID := int64(created["job_id"].(float64))
	if j, _ := srv.st.GetBatchJob(jobID); j.Concurrency != 2 {
		t.Errorf("stored concurrency = %d, want 2", j.Concurrency)
	}
	waitForJobDone(t, srv.st, jobID)
}

// A malformed manifest is rejected at import.
func TestBatchPluginImportRejectsInvalid(t *testing.T) {
	srv := batchServer(t)
	rec := httptest.NewRecorder()
	srv.apiBatchPluginImport(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"id":"x"}`)), "admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid manifest → %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// Installing from the market fetches the index + manifest from the configured repo.
func TestBatchMarketInstall(t *testing.T) {
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			io.WriteString(w, `{"plugins":[{"slug":"p","name":"P","version":"1.0.0","path":"p.json"}]}`)
		case "/p.json":
			io.WriteString(w, batchTestSpec)
		default:
			w.WriteHeader(404)
		}
	}))
	defer market.Close()

	srv := batchServer(t)
	srv.st.SetSetting("batch_market_index_url", market.URL+"/index.json")
	post(t, srv.apiBatchMarketInstall, `{"slug":"p"}`)

	p, ok := srv.st.GetPlugin("p")
	if !ok {
		t.Fatal("plugin not installed from market")
	}
	if p.Source != "market" || p.Version != "1.0.0" {
		t.Errorf("installed plugin = {source:%q version:%q}, want market/1.0.0", p.Source, p.Version)
	}
}
