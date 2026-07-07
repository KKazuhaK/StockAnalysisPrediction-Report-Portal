package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// HTTP handlers for the interactive chat/assistant surface (docs/adr/0012-interactive-chat.md).
// Cookie-session, gated by PermRunBatch (a chat turn runs a Dify app — the same money gate
// as batch). Conversations are personal: every handler is scoped to the caller's own rows.
// The portal is a passthrough — it sends one message + the conversation id and Dify owns
// the history/context; this layer only indexes conversations so a user can list/reopen them.

// chatTimeout bounds one blocking chat turn. Interactive replies (even agent apps with
// tool calls) return in well under this; it is far shorter than a batch run's hour.
const chatTimeout = 5 * time.Minute

// difyChatClient builds a Dify client for a chat/agent target from its stored config.
func difyChatClient(configJSON string) (*dify.Client, error) {
	var cfg difyTargetConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("dify target config: %w", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return nil, fmt.Errorf("dify target: base_url and api_key are required")
	}
	return dify.New(cfg.BaseURL, cfg.APIKey, &http.Client{Timeout: chatTimeout}), nil
}

// chatTitle derives a conversation title from its first message (trimmed to a short line).
func chatTitle(q string) string {
	q = strings.TrimSpace(q)
	if r := []rune(q); len(r) > 24 {
		return string(r[:24]) + "…"
	}
	return q
}

func convJSON(c ChatConversation) map[string]any {
	return map[string]any{
		"id": c.ID, "target_id": c.TargetID, "title": c.Title,
		"created_at": c.CreatedAt, "updated_at": c.UpdatedAt, "started": c.ConvID != "",
	}
}

// ownConversation loads a conversation and confirms the caller owns it (chats are private,
// no admin override). Writes a 404 and returns ok=false otherwise.
func (s *Server) ownConversation(w http.ResponseWriter, id int64, user string) (ChatConversation, bool) {
	conv, ok := s.st.GetConversation(id)
	if !ok || conv.CreatedBy != user {
		jsonError(w, http.StatusNotFound, "conversation not found")
		return ChatConversation{}, false
	}
	return conv, true
}

// apiChatTargets lists the chat/agent Dify targets a user can converse with. Workflow
// targets are excluded — they aren't conversational.
func (s *Server) apiChatTargets(w http.ResponseWriter, r *http.Request, user string) {
	out := make([]map[string]any, 0)
	for _, tg := range s.st.ListTargets() {
		if tg.PluginSlug != difyPluginSlug {
			continue
		}
		if mode := difyTargetMode(tg.Config); mode != "" && mode != "workflow" {
			out = append(out, map[string]any{"id": tg.ID, "name": tg.Name, "mode": mode})
		}
	}
	writeJSON(w, map[string]any{"targets": out})
}

// apiChatConversations lists the caller's conversations, newest first, optionally scoped
// to one target via ?target_id=.
func (s *Server) apiChatConversations(w http.ResponseWriter, r *http.Request, user string) {
	var targetID int64
	if v := strings.TrimSpace(r.URL.Query().Get("target_id")); v != "" {
		fmt.Sscan(v, &targetID)
	}
	out := make([]map[string]any, 0)
	for _, c := range s.st.ListConversations(user, targetID) {
		out = append(out, convJSON(c))
	}
	writeJSON(w, map[string]any{"conversations": out})
}

// apiChatConversationCreate starts a new (empty) conversation for a chat/agent target.
func (s *Server) apiChatConversationCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		TargetID int64 `json:"target_id"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	tgt, ok := s.st.GetTarget(in.TargetID)
	if !ok || tgt.PluginSlug != difyPluginSlug {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	if mode := difyTargetMode(tgt.Config); mode == "" || mode == "workflow" {
		jsonError(w, http.StatusBadRequest, "target is not a chat/agent app")
		return
	}
	id, err := s.st.CreateConversation(in.TargetID, user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	conv, _ := s.st.GetConversation(id)
	writeJSON(w, convJSON(conv))
}

// apiChatConversationDelete removes the caller's conversation index row (Dify keeps the
// messages; this just drops it from the portal's list).
func (s *Server) apiChatConversationDelete(w http.ResponseWriter, r *http.Request, user string) {
	conv, ok := s.ownConversation(w, pathID(r, "id"), user)
	if !ok {
		return
	}
	if err := s.st.DeleteConversation(conv.ID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiChatConversationRename sets a conversation's title.
func (s *Server) apiChatConversationRename(w http.ResponseWriter, r *http.Request, user string) {
	conv, ok := s.ownConversation(w, pathID(r, "id"), user)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.st.RenameConversation(conv.ID, strings.TrimSpace(in.Title)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiChatSend sends one message to the conversation's target and returns Dify's reply.
// It threads the stored conversation_id (Dify assembles the context), then records the
// assigned conversation_id + a title (first turn) + updated_at.
func (s *Server) apiChatSend(w http.ResponseWriter, r *http.Request, user string) {
	conv, ok := s.ownConversation(w, pathID(r, "id"), user)
	if !ok {
		return
	}
	var in struct {
		Query string `json:"query"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		jsonError(w, http.StatusBadRequest, "message is empty")
		return
	}
	tgt, ok := s.st.GetTarget(conv.TargetID)
	if !ok {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	client, err := difyChatClient(tgt.Config)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Detach from the browser connection: a chat turn should finish — and be stored by
	// Dify + have its conversation_id saved here — even if the user navigates away
	// mid-generation, so the result is there when they return. (net/http cancels
	// r.Context() on client disconnect but keeps the handler goroutine running; using a
	// fresh context means the Dify call isn't aborted with the browser.) The turn is not
	// persisted across a server restart, only across the client leaving.
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()
	reply, err := client.Chat(ctx, query, nil, s.difyEndUser(conv.CreatedBy), conv.ConvID)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "dify: "+err.Error())
		return
	}
	if err := s.st.AfterTurn(conv.ID, reply.ConversationID, chatTitle(query)); err != nil {
		// The turn succeeded at Dify; a failed index update is non-fatal for this reply.
		writeJSON(w, map[string]any{"answer": reply.Answer, "conversation_id": reply.ConversationID})
		return
	}
	writeJSON(w, map[string]any{"answer": reply.Answer, "conversation_id": reply.ConversationID})
}

// apiChatHistory returns a conversation's prior turns from Dify, for display on reopen. A
// conversation that hasn't sent anything yet (no conv_id) has no history.
func (s *Server) apiChatHistory(w http.ResponseWriter, r *http.Request, user string) {
	conv, ok := s.ownConversation(w, pathID(r, "id"), user)
	if !ok {
		return
	}
	if conv.ConvID == "" {
		writeJSON(w, map[string]any{"turns": []any{}})
		return
	}
	tgt, ok := s.st.GetTarget(conv.TargetID)
	if !ok {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	client, err := difyChatClient(tgt.Config)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	turns, err := client.Messages(r.Context(), conv.ConvID, s.difyEndUser(conv.CreatedBy), 100)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "dify: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"turns": turns})
}
