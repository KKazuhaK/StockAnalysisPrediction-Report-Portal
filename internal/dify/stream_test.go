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

// A stream-level `error` event (bad model config / quota / rate limit) — often emitted
// before any workflow_started, so there is no run id — is surfaced as a terminal failure
// carrying Dify's real message, NOT the generic "stream ended before workflow_finished"
// that used to swallow it.
func TestRunWorkflowStreamErrorEvent(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"error","task_id":"t1","message":"insufficient credits","code":"provider_quota","status":400}`,
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, _, err := c.RunWorkflowStream(context.Background(), map[string]any{"symbol": "1"}, "u", nil)
	if err != nil {
		t.Fatalf("an error event should be a terminal result, not a transport error: %v", err)
	}
	if res.Status != "failed" || res.Error != "insufficient credits" {
		t.Fatalf("res = %+v, want failed / 'insufficient credits'", res)
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

// A chatflow run (workflow events + a message): captures the workflow run id (so a
// drop can be reconciled), sends the query, and reports succeeded.
func TestRunChatStreamChatflow(t *testing.T) {
	var gotQuery string
	var gotInputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotQuery, _ = body["query"].(string)
		gotInputs, _ = body["inputs"].(map[string]any)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event":"workflow_started","task_id":"t","workflow_run_id":"run-1","data":{}}`+"\n\n")
		io.WriteString(w, `data: {"event":"message","task_id":"t","answer":"hello","message_id":"m1"}`+"\n\n")
		io.WriteString(w, `data: {"event":"workflow_finished","task_id":"t","workflow_run_id":"run-1","data":{"status":"succeeded"}}`+"\n\n")
		io.WriteString(w, `data: {"event":"message_end","task_id":"t","message_id":"m1","conversation_id":"c1"}`+"\n\n")
	}))
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, runID, err := c.RunChatStream(context.Background(), map[string]any{"query": "hi", "symbol": "600160"}, "u", nil)
	if err != nil {
		t.Fatalf("RunChatStream: %v", err)
	}
	if gotQuery != "hi" {
		t.Errorf("query = %q, want hi", gotQuery)
	}
	if gotInputs["symbol"] != "600160" || gotInputs["query"] != nil {
		t.Errorf("inputs = %v; want symbol carried and query stripped out", gotInputs)
	}
	if runID != "run-1" || res.Status != "succeeded" || res.TaskID != "t" {
		t.Fatalf("res=%+v runID=%q", res, runID)
	}
}

// A pure chat app (no workflow events): message_end is the terminal; no run id.
func TestRunChatStreamPureChat(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"message","task_id":"t","answer":"hi","message_id":"m1"}`,
		`{"event":"message_end","task_id":"t","message_id":"m1","conversation_id":"c1"}`,
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, runID, err := c.RunChatStream(context.Background(), map[string]any{"query": "hi"}, "u", nil)
	if err != nil {
		t.Fatalf("RunChatStream: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
	if runID != "" {
		t.Errorf("runID = %q, want empty (no workflow to reconcile)", runID)
	}
}

// An `error` event is a terminal failure result, not a transport error to retry.
func TestRunChatStreamError(t *testing.T) {
	srv := sseStub(t, []string{`{"event":"error","task_id":"t","message":"model overloaded","status":500}`})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, _, err := c.RunChatStream(context.Background(), map[string]any{"query": "hi"}, "u", nil)
	if err != nil {
		t.Fatalf("an error event should be a terminal result, not a Go error: %v", err)
	}
	if res.Status != "failed" || res.Error != "model overloaded" {
		t.Errorf("res = %+v, want failed / 'model overloaded'", res)
	}
}

func TestStopChat(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"result":"success"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	if err := c.StopChat(context.Background(), "task-9", "u"); err != nil {
		t.Fatalf("StopChat: %v", err)
	}
	if gotPath != "/chat-messages/task-9/stop" {
		t.Errorf("stop path = %q", gotPath)
	}
}

// A trailing error event after a successful workflow_finished must not flip the
// already-terminal result to failed.
func TestRunChatStreamTrailingErrorKeepsSuccess(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"workflow_finished","task_id":"t","workflow_run_id":"run-1","data":{"status":"succeeded"}}`,
		`{"event":"error","task_id":"t","message":"post-answer hiccup"}`,
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, _, err := c.RunChatStream(context.Background(), map[string]any{"query": "hi"}, "u", nil)
	if err != nil {
		t.Fatalf("RunChatStream: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded (trailing error must not clobber it)", res.Status)
	}
}
