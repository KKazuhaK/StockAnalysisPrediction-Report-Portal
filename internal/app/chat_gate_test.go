package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
