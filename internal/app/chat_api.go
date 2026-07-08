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
		"id": c.ID, "target_id": c.TargetID, "title": c.Title, "starred": c.Starred,
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

// apiChatTargetIntro returns a chat/agent target's opening statement + suggested questions
// (Dify's start-of-conversation greeting), for the empty-thread state. A Dify error is
// non-fatal — the chat just opens without a greeting.
func (s *Server) apiChatTargetIntro(w http.ResponseWriter, r *http.Request, user string) {
	tgt, ok := s.st.GetTarget(pathID(r, "id"))
	if !ok || tgt.PluginSlug != difyPluginSlug {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	client, err := difyChatClient(tgt.Config)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	opening, suggested, err := client.ChatIntro(r.Context())
	if err != nil || suggested == nil {
		suggested = []string{}
	}
	writeJSON(w, map[string]any{"opening": opening, "suggested": suggested})
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

// apiChatConversationStar pins/unpins a conversation to the top of the caller's list.
func (s *Server) apiChatConversationStar(w http.ResponseWriter, r *http.Request, user string) {
	conv, ok := s.ownConversation(w, pathID(r, "id"), user)
	if !ok {
		return
	}
	var in struct {
		Starred bool `json:"starred"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.st.SetConversationStarred(conv.ID, in.Starred); err != nil {
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
	// Detach from the browser connection so the turn finishes and lands at Dify even if the user
	// navigates away mid-generation (net/http cancels r.Context() on disconnect but keeps the
	// handler goroutine running; a fresh context isn't aborted with the browser). cancel() is ALSO
	// the stop handle stored on the live turn so the owner or an admin can abort the run.
	endUser := s.difyEndUser(conv.CreatedBy)
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()
	// Chat is interactive — it must not queue behind the batch run system (that queue exists to
	// DEFER slow report runs; a chat turn can't wait). An independent ceiling instead sheds load
	// when too many turns are already in flight, so a burst can't overwhelm Dify (0 = unlimited).
	// The turn carries its stop handle (cancel + client + end-user + a task id latched as it
	// streams in) so apiChatStop / apiAdminChatStop can end it.
	turnID, ok := s.chatAcquire(&chatTurn{
		User: user, TargetID: tgt.ID, TargetName: tgt.Name,
		ConvID: conv.ID, ConvTitle: conv.Title, Started: time.Now(),
		Cancel: cancel, Client: client, EndUser: endUser,
	})
	if !ok {
		jsonError(w, http.StatusTooManyRequests, "assistant is busy: too many chats in progress, please retry shortly")
		return
	}
	defer s.chatRelease(turnID)
	// Stream so the conversation_id is captured the INSTANT Dify assigns it and persisted right
	// away (title too), and the task id is recorded onto the live turn as its stop handle. A long
	// turn (e.g. a Deep Research chatflow) can then be reopened after a reload / from another tab.
	reply, err := client.ChatStream(ctx, query, nil, endUser, conv.ConvID, func(convID, _, taskID string) {
		if convID != "" {
			s.st.AfterTurn(conv.ID, convID, chatTitle(query))
		}
		if taskID != "" {
			s.chatSetTaskID(turnID, taskID)
		}
	})
	if err != nil {
		// A user/admin stop cancels ctx → ChatStream returns context.Canceled with the partial
		// answer. Report that as a normal (stopped) reply, not a 502 — only a genuine Dify failure
		// (or the chatTimeout → DeadlineExceeded) is an error.
		if ctx.Err() == context.Canceled {
			s.st.AfterTurn(conv.ID, reply.ConversationID, chatTitle(query))
			writeJSON(w, map[string]any{"answer": reply.Answer, "conversation_id": reply.ConversationID, "stopped": true})
			return
		}
		jsonError(w, http.StatusBadGateway, "dify: "+err.Error())
		return
	}
	s.st.AfterTurn(conv.ID, reply.ConversationID, chatTitle(query)) // bump updated_at; conv_id/title sticky
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
