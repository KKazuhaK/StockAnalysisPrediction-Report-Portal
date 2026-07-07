package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// A dropped pure-agent chat stream carries no workflow_run_id, so GetWorkflowRun can't
// reconcile it. The provider must instead reconcile via the conversation's message
// history — recovering the real outcome WITHOUT re-running the (possibly expensive,
// still-executing) chat run.
func TestDifyChatProviderReconcilesViaConversation(t *testing.T) {
	var runs, gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat-messages":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Agent app: a conversation_id (but no workflow_run_id), then a drop.
			io.WriteString(w, `data: {"event":"agent_message","task_id":"t","conversation_id":"conv-9","message_id":"m1","answer":"draft"}`+"\n\n")
		case "/messages":
			atomic.AddInt32(&gets, 1)
			io.WriteString(w, `{"data":[{"id":"m1","answer":"the finished analysis","status":"normal","created_at":10}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u", chat: true, reconcilePoll: time.Millisecond, reconcileTimeout: 5 * time.Second}
	res, err := p.Run(context.Background(), map[string]string{"query": "研究"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != batch.Ok {
		t.Errorf("status = %v, want Ok (reconciled via conversation history)", res.Status)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("chat started %d times, want 1 (no re-run)", n)
	}
	if atomic.LoadInt32(&gets) < 1 {
		t.Error("should have reconciled via /messages")
	}
}

// A stream that demonstrably STARTED a run (a task id was captured) but dropped with no
// id to reconcile (no workflow_run_id, no conversation_id) must fail PERMANENTLY. A
// transient error would let the engine re-run a live Dify run — the duplicate-token-burn
// hazard the reconcile design exists to prevent.
func TestDifyProviderStartedButUnreconcilableIsPermanent(t *testing.T) {
	var runs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat-messages":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			// A task id (run started), but no conversation_id and no workflow_run_id, then a drop.
			io.WriteString(w, `data: {"event":"agent_message","task_id":"task-x","answer":"hi"}`+"\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u", chat: true, reconcilePoll: time.Millisecond, reconcileTimeout: 200 * time.Millisecond}
	_, err := p.Run(context.Background(), map[string]string{"query": "x"})
	if err == nil {
		t.Fatal("expected an error when a started run drops with no id to reconcile")
	}
	if batch.IsTransient(err) {
		t.Error("a started-but-unreconcilable run must be PERMANENT (else the engine re-runs it → duplicate burn)")
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("run started %d times, want 1 (no re-run)", n)
	}
}

// A stream that never started anything (no task id, no ids at all — e.g. the upstream
// closed before any event) stays TRANSIENT: nothing ran, so retrying is safe and correct.
func TestDifyProviderNothingStartedStaysTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 + event-stream header, then an immediate clean close with zero events.
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u"}
	_, err := p.Run(context.Background(), map[string]string{"symbol": "1"})
	if err == nil {
		t.Fatal("expected an error when the stream yields nothing")
	}
	if !batch.IsTransient(err) {
		t.Error("a run that never started must stay TRANSIENT (safe to retry)")
	}
}
