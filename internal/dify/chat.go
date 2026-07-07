package dify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// This file covers the interactive chat/assistant surface (docs/adr/0012-interactive-chat.md):
// send one message to a chat/agent app and read a conversation's history. Unlike batch
// runs (which stream to capture a run id), chat uses Dify's BLOCKING mode — a single
// request/response that returns the whole answer plus the conversation_id, which is both
// simpler and proxy-friendly (no long-held SSE). Context/memory is entirely Dify's: the
// portal sends only the new message + which conversation, and Dify assembles the history.

// ChatReply is one blocking chat turn's result.
type ChatReply struct {
	Answer         string
	ConversationID string // the conversation this turn belongs to (assigned on the first turn)
	MessageID      string
}

// Chat sends one message to a chat/agent app in blocking mode. conversationID is "" to
// start a new conversation (Dify assigns one, returned in the reply) or an existing id to
// continue with full context. user is the Dify end-user the conversation is keyed to.
func (c *Client) Chat(ctx context.Context, query string, inputs map[string]any, user, conversationID string) (ChatReply, error) {
	if user == "" {
		user = "report-portal"
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	raw, err := c.do(ctx, http.MethodPost, "/chat-messages", map[string]any{
		"query": query, "inputs": inputs, "response_mode": "blocking",
		"user": user, "conversation_id": conversationID,
	})
	if err != nil {
		return ChatReply{}, err
	}
	var out struct {
		Answer         string `json:"answer"`
		ConversationID string `json:"conversation_id"`
		MessageID      string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ChatReply{}, fmt.Errorf("dify /chat-messages: bad JSON: %w", err)
	}
	return ChatReply{Answer: out.Answer, ConversationID: out.ConversationID, MessageID: out.MessageID}, nil
}

// ChatTurn is one stored exchange in a conversation's history: the user's query and the
// assistant's answer, as Dify records them (one /messages row carries both).
type ChatTurn struct {
	Query     string `json:"query"`
	Answer    string `json:"answer"`
	CreatedAt int64  `json:"created_at"`
}

// Messages fetches a conversation's history from Dify (for display on reopen). It does
// NOT drive context — Dify feeds the model from conversation_id internally; this is only
// so the UI can show what was said. Returned oldest-first.
func (c *Client) Messages(ctx context.Context, conversationID, user string, limit int) ([]ChatTurn, error) {
	if user == "" {
		user = "report-portal"
	}
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{}
	q.Set("conversation_id", conversationID)
	q.Set("user", user)
	q.Set("limit", strconv.Itoa(limit))
	raw, err := c.do(ctx, http.MethodGet, "/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []ChatTurn `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("dify /messages: bad JSON: %w", err)
	}
	return out.Data, nil
}
