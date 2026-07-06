package dify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sseStub streams the given JSON payloads as `data:` SSE frames, flushing each so a
// reader sees them incrementally, then closes the connection.
func sseStub(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			io.WriteString(w, "data: "+c+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

// A full streaming run: the run id is captured, progress is forwarded, and the
// workflow_finished status is returned.
func TestRunWorkflowStreamHappy(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"workflow_started","task_id":"t1","workflow_run_id":"run-1","data":{}}`,
		`{"event":"node_started","task_id":"t1","workflow_run_id":"run-1","data":{"title":"LLM","index":1}}`,
		`{"event":"node_finished","task_id":"t1","workflow_run_id":"run-1","data":{"title":"LLM","index":1,"status":"succeeded"}}`,
		`{"event":"workflow_finished","task_id":"t1","workflow_run_id":"run-1","data":{"status":"succeeded","outputs":{"uid":"x"}}}`,
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	var titles []string
	res, runID, err := c.RunWorkflowStream(context.Background(), map[string]any{"symbol": "1"}, "u", func(e StreamEvent) {
		if e.Event == "node_started" {
			titles = append(titles, e.Title)
		}
	})
	if err != nil {
		t.Fatalf("RunWorkflowStream: %v", err)
	}
	if runID != "run-1" || res.Status != "succeeded" || res.TaskID != "t1" {
		t.Fatalf("res=%+v runID=%q", res, runID)
	}
	if len(titles) != 1 || titles[0] != "LLM" {
		t.Errorf("progress titles = %v, want [LLM]", titles)
	}
}

// A stream that ends before workflow_finished (a dropped connection) still returns
// the run id, so the caller can reconcile the outcome instead of re-running.
func TestRunWorkflowStreamDropReturnsRunID(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"workflow_started","task_id":"t1","workflow_run_id":"run-9","data":{}}`,
		`{"event":"node_started","task_id":"t1","workflow_run_id":"run-9","data":{"title":"A","index":1}}`,
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	_, runID, err := c.RunWorkflowStream(context.Background(), nil, "u", nil)
	if err == nil {
		t.Fatal("expected an error when the stream ends before workflow_finished")
	}
	if runID != "run-9" {
		t.Errorf("runID = %q, want run-9 (needed to reconcile)", runID)
	}
}

// A non-2xx on the initial POST is a permanent APIError with no run id (nothing
// started), so the caller may safely retry.
func TestRunWorkflowStream4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"bad_request","message":"nope"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	_, runID, err := c.RunWorkflowStream(context.Background(), nil, "u", nil)
	if runID != "" {
		t.Errorf("runID = %q, want empty (nothing started)", runID)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("want APIError 400, got %v", err)
	}
}

func TestGetWorkflowRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflows/run/run-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"id":"run-1","status":"succeeded","error":null,"outputs":{"uid":"x"}}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	r, err := c.GetWorkflowRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if r.Status != "succeeded" || r.WorkflowRunID != "run-1" {
		t.Errorf("run = %+v", r)
	}
}

func TestStopWorkflow(t *testing.T) {
	var gotPath, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		gotUser, _ = b["user"].(string)
		io.WriteString(w, `{"result":"success"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	if err := c.StopWorkflow(context.Background(), "task-7", "kazuha"); err != nil {
		t.Fatalf("StopWorkflow: %v", err)
	}
	if gotPath != "/workflows/tasks/task-7/stop" {
		t.Errorf("stop path = %q", gotPath)
	}
	if gotUser != "kazuha" {
		t.Errorf("stop user = %q, want kazuha", gotUser)
	}
}
