package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// The chat concurrency gate is independent of the batch run queue: chat is interactive and
// must never wait behind a report run. It's a simple ceiling that sheds load when full.

func TestChatMaxConcurrentSetting(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	// Default is 0 = unlimited, so an upgrade never silently caps a running deployment.
	if got := s.chatMaxConcurrent(); got != 0 {
		t.Fatalf("default chatMaxConcurrent = %d, want 0 (unlimited)", got)
	}
	st.SetSetting("chat_max_concurrent", "3")
	if got := s.chatMaxConcurrent(); got != 3 {
		t.Fatalf("chatMaxConcurrent = %d, want 3", got)
	}
	// Garbage / negative → treated as unlimited (0).
	st.SetSetting("chat_max_concurrent", "-5")
	if got := s.chatMaxConcurrent(); got != 0 {
		t.Fatalf("negative chatMaxConcurrent = %d, want 0", got)
	}
}

func TestChatGateCeilingAndRelease(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	st.SetSetting("chat_max_concurrent", "2")

	a, ok := s.chatAcquire(&chatTurn{User: "alice", TargetName: "A"})
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	if _, ok := s.chatAcquire(&chatTurn{User: "bob", TargetName: "B"}); !ok {
		t.Fatal("second acquire (at limit 2) must succeed")
	}
	// Third exceeds the ceiling → rejected (shed, not queued).
	if _, ok := s.chatAcquire(&chatTurn{User: "carol"}); ok {
		t.Fatal("third acquire past the ceiling must be rejected")
	}
	if n := len(s.chatLiveTurns()); n != 2 {
		t.Fatalf("live turns = %d, want 2", n)
	}
	// Releasing one frees a slot.
	s.chatRelease(a)
	if _, ok := s.chatAcquire(&chatTurn{User: "carol"}); !ok {
		t.Fatal("after releasing a slot, acquire must succeed")
	}
	if n := len(s.chatLiveTurns()); n != 2 {
		t.Fatalf("live turns after release+acquire = %d, want 2", n)
	}
}

func TestChatGateUnlimited(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st} // default 0 = unlimited
	for i := 0; i < 50; i++ {
		if _, ok := s.chatAcquire(&chatTurn{User: "x"}); !ok {
			t.Fatalf("unlimited gate rejected acquire #%d", i)
		}
	}
	if n := len(s.chatLiveTurns()); n != 50 {
		t.Fatalf("live turns = %d, want 50", n)
	}
}

func TestAdminChatConfigAndLive(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	// Save the ceiling.
	rec := httptest.NewRecorder()
	s.apiAdminChatConfigSave(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"max_concurrent":4}`)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("config save → %d: %s", rec.Code, rec.Body.String())
	}
	if got := st.GetSetting("chat_max_concurrent", ""); got != "4" {
		t.Fatalf("chat_max_concurrent = %q, want 4", got)
	}

	// Two in-flight turns show up in the live view, along with the ceiling.
	s.chatAcquire(&chatTurn{User: "alice", TargetName: "研报助手", ConvTitle: "hello"})
	s.chatAcquire(&chatTurn{User: "bob", TargetName: "Agent"})
	rec = httptest.NewRecorder()
	s.apiAdminChatLive(rec, httptest.NewRequest("GET", "/x", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("live → %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Turns         []map[string]any `json:"turns"`
		MaxConcurrent int              `json:"max_concurrent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("live not JSON: %v", err)
	}
	if len(out.Turns) != 2 {
		t.Fatalf("live turns = %d, want 2", len(out.Turns))
	}
	if out.MaxConcurrent != 4 {
		t.Fatalf("max_concurrent = %d, want 4", out.MaxConcurrent)
	}
}

// A stop cancels the turn's context AND best-effort tells Dify to stop the run (POST
// /chat-messages/{task}/stop with the turn's OWN end-user), so the run stops billing.
func TestChatStopCancelsAndCallsDify(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	stopCh := make(chan [2]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		u, _ := body["user"].(string)
		stopCh <- [2]string{r.URL.Path, u}
		io.WriteString(w, `{"result":"success"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	turnID, _ := s.chatAcquire(&chatTurn{
		User: "alice", ConvID: 7, Started: time.Now(),
		Cancel: cancel, Client: dify.New(srv.URL, "app-key", srv.Client()), EndUser: "alice@x",
	})
	s.chatSetTaskID(turnID, "task-42") // task id streamed in mid-turn

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("id", strconv.FormatInt(turnID, 10))
	s.apiAdminChatStop(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin stop → %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case <-ctx.Done(): // the turn's stream read is aborted
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not cancel the turn context")
	}
	select {
	case got := <-stopCh:
		if got[0] != "/chat-messages/task-42/stop" || got[1] != "alice@x" {
			t.Fatalf("dify stop = {path:%q user:%q}, want /chat-messages/task-42/stop + alice@x (turn owner)", got[0], got[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dify StopChat was not called")
	}
}

// A stop before any task id streamed still cancels the turn locally, with no Dify handle to
// call StopChat with — and must not panic.
func TestChatStopBeforeTaskIDCancelsOnly(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	ctx, cancel := context.WithCancel(context.Background())
	turnID, _ := s.chatAcquire(&chatTurn{ConvID: 3, Cancel: cancel}) // no Client, no TaskID
	h, ok := s.chatStopByID(turnID)
	if !ok {
		t.Fatal("turn not found")
	}
	s.chatStop(h)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop must cancel the turn even without a task id")
	}
}

// Admin stop of an unknown / already-finished turn id is a 404, not a panic.
func TestAdminChatStopUnknownTurn(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("id", "999")
	s.apiAdminChatStop(rec, req, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stop unknown turn → %d, want 404", rec.Code)
	}
}

// chatStopByConv finds every in-flight turn on a conversation (owner path), and none for one
// with nothing in flight.
func TestChatStopByConv(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	s.chatAcquire(&chatTurn{ConvID: 7, Cancel: func() {}})
	s.chatAcquire(&chatTurn{ConvID: 9, Cancel: func() {}})
	if hs := s.chatStopByConv(7); len(hs) != 1 {
		t.Fatalf("chatStopByConv(7) = %d, want 1", len(hs))
	}
	if hs := s.chatStopByConv(5); len(hs) != 0 {
		t.Fatalf("chatStopByConv(5) = %d, want 0 (no match)", len(hs))
	}
}
