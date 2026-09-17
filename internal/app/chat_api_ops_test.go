package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatFixture returns a server with one chat-mode Dify target and one workflow-mode target, plus
// a second account to prove the per-user scoping.
func chatFixture(t *testing.T) (*Server, int64, int64) {
	t.Helper()
	s := userAdminServer(t)
	if err := s.st.UpsertUser(User{Username: "bob", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	chatCfg, _ := json.Marshal(difyTargetConfig{BaseURL: "https://dify.example/v1", APIKey: "k", Mode: "chat"})
	chatID, err := s.st.CreateTarget(difyPluginSlug, "Assistant", string(chatCfg))
	if err != nil {
		t.Fatal(err)
	}
	wfCfg, _ := json.Marshal(difyTargetConfig{BaseURL: "https://dify.example/v1", APIKey: "k"})
	wfID, err := s.st.CreateTarget(difyPluginSlug, "Workflow", string(wfCfg))
	if err != nil {
		t.Fatal(err)
	}
	return s, chatID, wfID
}

// A conversation title is the first message, trimmed and clipped to 24 runes.
func TestChatTitleClamp(t *testing.T) {
	if got := chatTitle("  hello world  "); got != "hello world" {
		t.Fatalf("chatTitle = %q", got)
	}
	long := strings.Repeat("研", 30)
	got := []rune(chatTitle(long))
	if len(got) != 25 || got[len(got)-1] != '…' {
		t.Fatalf("clamped title = %q (%d runes), want 24 + ellipsis", string(got), len(got))
	}
}

// The chat surface lists only conversational targets; conversations are personal, so every
// mutation is owner-scoped, and renames are trimmed.
func TestChatTargetsAndConversationCRUD(t *testing.T) {
	s, chatID, wfID := chatFixture(t)

	code, out := call(t, s.apiChatTargets, ``, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiChatTargets → %d", code)
	}
	targets := out["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("chat targets = %v, want only the chat-mode one", targets)
	}
	if got := targets[0].(map[string]any); int64(got["id"].(float64)) != chatID || got["mode"] != "chat" {
		t.Fatalf("chat target = %v", targets[0])
	}

	// Creating against an unknown or non-conversational target is refused.
	if code, _ = call(t, s.apiChatConversationCreate, `{"target_id":424242}`, "admin"); code != http.StatusNotFound {
		t.Fatalf("unknown target → %d, want 404", code)
	}
	if code, _ = call(t, s.apiChatConversationCreate, fmt.Sprintf(`{"target_id":%d}`, wfID), "admin"); code != http.StatusBadRequest {
		t.Fatalf("workflow target → %d, want 400", code)
	}

	code, out = call(t, s.apiChatConversationCreate, fmt.Sprintf(`{"target_id":%d}`, chatID), "admin")
	if code != http.StatusOK {
		t.Fatalf("create → %d", code)
	}
	id := int64(out["id"].(float64))
	if out["started"] != false || out["title"] != "" {
		t.Fatalf("fresh conversation = %v", out)
	}
	path := map[string]string{"id": fmt.Sprint(id)}

	// A second user neither sees it nor can touch it.
	if _, bout := call(t, s.apiChatConversations, ``, "bob"); len(bout["conversations"].([]any)) != 0 {
		t.Fatalf("bob sees someone else's conversation: %v", bout["conversations"])
	}
	if code, _ = callPath(t, s.apiChatConversationRename, http.MethodPost, `{"title":"mine"}`, path, "bob"); code != http.StatusNotFound {
		t.Fatalf("foreign rename → %d, want 404", code)
	}
	if code, _ = callPath(t, s.apiChatConversationStar, http.MethodPost, `{"starred":true}`, path, "bob"); code != http.StatusNotFound {
		t.Fatalf("foreign star → %d, want 404", code)
	}
	if code, _ = callPath(t, s.apiChatConversationDelete, http.MethodPost, ``, path, "bob"); code != http.StatusNotFound {
		t.Fatalf("foreign delete → %d, want 404", code)
	}

	// Rename trims; star persists; the list reflects both.
	if code, _ = callPath(t, s.apiChatConversationRename, http.MethodPost, `{"title":"  My chat  "}`, path, "admin"); code != http.StatusOK {
		t.Fatalf("rename → %d", code)
	}
	if code, _ = callPath(t, s.apiChatConversationStar, http.MethodPost, `{"starred":true}`, path, "admin"); code != http.StatusOK {
		t.Fatalf("star → %d", code)
	}
	conv, ok := s.st.GetConversation(id)
	if !ok || conv.Title != "My chat" || !conv.Starred || conv.CreatedBy != "admin" {
		t.Fatalf("stored conversation = %+v", conv)
	}
	_, out = call(t, s.apiChatConversations, ``, "admin")
	got := out["conversations"].([]any)[0].(map[string]any)
	if got["title"] != "My chat" || got["starred"] != true {
		t.Fatalf("listed conversation = %v", got)
	}

	// The ?target_id= filter hides conversations of other targets.
	rec := httptest.NewRecorder()
	s.apiChatConversations(rec, httptest.NewRequest("GET", "/x?target_id=424242", nil), "admin")
	var filtered struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Conversations) != 0 {
		t.Fatalf("target filter leaked %d conversations", len(filtered.Conversations))
	}

	// Delete removes it from the owner's list.
	if code, _ = callPath(t, s.apiChatConversationDelete, http.MethodPost, ``, path, "admin"); code != http.StatusOK {
		t.Fatalf("delete → %d", code)
	}
	if _, ok := s.st.GetConversation(id); ok {
		t.Fatal("conversation survived delete")
	}
}

// difyChatClient rejects an incomplete target config before any request is made.
func TestDifyChatClientValidation(t *testing.T) {
	if _, err := difyChatClient(`{}`, 0); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := difyChatClient(`{"base_url":"https://x","api_key":""}`, 0); err == nil {
		t.Fatal("config without api_key accepted")
	}
	if _, err := difyChatClient(`{"base_url":"https://x","api_key":"k"}`, 0); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
