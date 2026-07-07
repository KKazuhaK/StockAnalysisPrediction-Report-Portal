package dify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GetChatOutcome reconciles a dropped chat/agent run (which has no workflow run id) by
// reading the conversation's latest message: a non-empty answer is success, an `error`
// status is failure, and nothing yet is "running" (keep polling).
func TestGetChatOutcomeSucceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("conversation_id"); got != "conv-1" {
			t.Errorf("conversation_id = %q, want conv-1", got)
		}
		// Newest turn (created_at 200) is the just-finished one; it carries the answer.
		io.WriteString(w, `{"data":[{"id":"m1","answer":"","status":"normal","created_at":100},{"id":"m2","answer":"final report","status":"normal","created_at":200}]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "k", srv.Client())

	r, err := c.GetChatOutcome(context.Background(), "conv-1", "u")
	if err != nil {
		t.Fatalf("GetChatOutcome: %v", err)
	}
	if r.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", r.Status)
	}
	if r.Outputs["answer"] != "final report" {
		t.Errorf("answer = %v, want 'final report'", r.Outputs["answer"])
	}
}

func TestGetChatOutcomeFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[{"id":"m1","answer":"","status":"error","error":"model quota exceeded","created_at":100}]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "k", srv.Client())

	r, err := c.GetChatOutcome(context.Background(), "conv-1", "u")
	if err != nil {
		t.Fatalf("GetChatOutcome: %v", err)
	}
	if r.Status != "failed" || r.Error != "model quota exceeded" {
		t.Errorf("r = %+v, want failed / 'model quota exceeded'", r)
	}
}

// No message persisted yet → "running", so the reconcile loop keeps polling instead of
// declaring a premature failure.
func TestGetChatOutcomeRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "k", srv.Client())

	r, err := c.GetChatOutcome(context.Background(), "conv-1", "u")
	if err != nil {
		t.Fatalf("GetChatOutcome: %v", err)
	}
	if r.Status != "running" {
		t.Errorf("status = %q, want running", r.Status)
	}
}

// A pure agent/chat stream that drops before message_end still surfaces the
// conversation id (and task id) it saw, so the caller can reconcile via the message
// history instead of re-running — the pure-chat analogue of returning the run id.
func TestRunChatStreamDropCapturesConvID(t *testing.T) {
	srv := sseStub(t, []string{
		`{"event":"agent_message","task_id":"t","conversation_id":"conv-7","message_id":"m1","answer":"partial"}`,
		// no message_end / workflow_finished — the connection drops mid-answer
	})
	defer srv.Close()
	c := New(srv.URL, "app-key", srv.Client())

	res, runID, err := c.RunChatStream(context.Background(), map[string]any{"query": "hi"}, "u", false, nil)
	if err == nil {
		t.Fatal("expected an error when the chat stream ends before completion")
	}
	if runID != "" {
		t.Errorf("runID = %q, want empty (pure agent has no workflow run id)", runID)
	}
	if res.ConversationID != "conv-7" {
		t.Errorf("res.ConversationID = %q, want conv-7 (needed to reconcile)", res.ConversationID)
	}
	if res.TaskID != "t" {
		t.Errorf("res.TaskID = %q, want t", res.TaskID)
	}
}
