package app

import (
	"context"
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

func TestBatchConfigSaveIsPartial(t *testing.T) {
	srv := batchServer(t)
	srv.st.SetSetting("batch_max_concurrent_jobs", "8")
	srv.st.SetSetting("batch_reserved_slots", "3")
	srv.st.SetSetting("batch_ticket_period_days", "7")

	post(t, srv.apiBatchConfigSave, `{"ticket_period_days":14}`)

	if got := srv.ticketPeriodDays(); got != 14 {
		t.Fatalf("ticket period = %d, want 14", got)
	}
	if got := srv.batchBudget(); got != 8 {
		t.Fatalf("queue budget was overwritten: got %d, want 8", got)
	}
	if got := srv.batchReserved(); got != 3 {
		t.Fatalf("reserved slots were overwritten: got %d, want 3", got)
	}
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

// Only admins may hand a run an explicit base priority; a non-admin's priority
// number is ignored and resolved from their group / the system default, so they
// can't jump the queue. (Scheduled far in the future so nothing launches.)
func TestJobCreateBasePriorityAdminOnly(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: "x", Role: "admin"})
	s.st.UpsertUser(User{Username: "op", PasswordHash: "x", Role: "operator"})
	tgt := seedDifyTarget(t, s, "T")

	submit := func(user, priority string) string {
		rec := httptest.NewRecorder()
		body := fmt.Sprintf(`{"target_id":%d,"priority":%q,"run_at":"2099-01-01 00:00:00","rows":[{"symbol":"1"}]}`, tgt, priority)
		s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), user)
		if rec.Code != http.StatusOK {
			t.Fatalf("create(%s) → %d: %s", user, rec.Code, rec.Body.String())
		}
		var out struct {
			Priority string `json:"priority"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Priority
	}

	if p := submit("op", "90"); p != "50" {
		t.Errorf("non-admin explicit base priority = %q, want 50 (ignored → system default)", p)
	}
	if p := submit("admin", "90"); p != "90" {
		t.Errorf("admin explicit base priority = %q, want 90", p)
	}
}

// Cancelling a job aborts its in-flight run's context immediately (so the blocking
// Dify call is dropped) — and an unknown job id is a harmless no-op.
func TestCancelRunningJobAbortsContext(t *testing.T) {
	s := batchServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.jobRuns.Store(int64(7), &jobRun{ctx: ctx, cancel: cancel})

	s.cancelRunningJob(7)
	select {
	case <-ctx.Done():
		// good — the cancel propagated to the run's context
	case <-time.After(time.Second):
		t.Fatal("cancelRunningJob did not cancel the in-flight context")
	}

	s.cancelRunningJob(999) // not running here → no panic, no effect
}
