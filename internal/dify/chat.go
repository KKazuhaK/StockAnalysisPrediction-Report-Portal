package dify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// ChatIntro fetches a chat/agent app's opening statement and suggested opening questions
// from /parameters — the greeting Dify shows at the start of a new conversation. Both are
// optional (empty when the app configures none).
func (c *Client) ChatIntro(ctx context.Context) (opening string, suggested []string, err error) {
	raw, err := c.do(ctx, http.MethodGet, "/parameters", nil)
	if err != nil {
		return "", nil, err
	}
	var doc struct {
		OpeningStatement   string   `json:"opening_statement"`
		SuggestedQuestions []string `json:"suggested_questions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("dify /parameters: bad JSON: %w", err)
	}
	return doc.OpeningStatement, doc.SuggestedQuestions, nil
}

// ChatStream sends a message in STREAMING mode so the conversation_id is captured the
// moment Dify assigns it (the first event) and handed to onMeta — letting the caller
// persist the conversation↔Dify linkage BEFORE a possibly-long turn finishes, so it
// survives a page reload mid-generation. It still returns one aggregated ChatReply (the
// portal accumulates the answer chunks and answers the browser once), so callers stay
// request/response. onMeta may be nil; it fires at most once.
func (c *Client) ChatStream(ctx context.Context, query string, inputs map[string]any, user, conversationID string, onMeta func(convID, msgID string)) (ChatReply, error) {
	if user == "" {
		user = "report-portal"
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "inputs": inputs, "response_mode": "streaming",
		"user": user, "conversation_id": conversationID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat-messages", bytes.NewReader(body))
	if err != nil {
		return ChatReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ChatReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return ChatReply{}, &APIError{Status: resp.StatusCode, Message: apiErrMsg(raw)}
	}

	var out ChatReply
	var answer strings.Builder
	metaSent := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 5 || line[:5] != "data:" {
			continue
		}
		payload := bytes.TrimSpace([]byte(line[5:]))
		if len(payload) == 0 {
			continue
		}
		var ev streamEnvelope
		if json.Unmarshal(payload, &ev) != nil {
			continue
		}
		if ev.ConversationID != "" {
			out.ConversationID = ev.ConversationID
			out.MessageID = ev.MessageID
			if !metaSent && onMeta != nil {
				metaSent = true
				onMeta(ev.ConversationID, ev.MessageID) // persist the linkage now, early
			}
		}
		switch ev.Event {
		case "message", "agent_message":
			answer.WriteString(ev.Answer)
		case "message_end", "workflow_finished":
			out.Answer = answer.String()
			return out, nil
		case "error":
			return ChatReply{}, fmt.Errorf("%s", firstNonEmpty(ev.Data.Error, ev.Message))
		}
	}
	if err := sc.Err(); err != nil {
		return ChatReply{}, err
	}
	out.Answer = answer.String() // stream ended without an explicit end event
	return out, nil
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
