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
	TaskID         string // Dify task id, for a server-side stop of an in-flight turn
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

// ChatStream sends a message in STREAMING mode so the conversation_id and task_id are
// captured the moment Dify emits them (early events) and handed to onMeta — letting the
// caller persist the conversation↔Dify linkage AND record a stop handle BEFORE a
// possibly-long turn finishes, so both survive a page reload mid-generation. It still
// returns one aggregated ChatReply (the portal accumulates the answer chunks and answers
// the browser once), so callers stay request/response. onMeta may be nil; it fires once
// per newly-seen id (conversation id and task id can arrive on different events, so up to
// twice), each time carrying every id captured so far.
// onChunk (optional) is called with each answer delta as it arrives, so a caller can forward the
// reply to the browser token-by-token (SSE). It's independent of the accumulation: the aggregated
// ChatReply is still returned either way, so a nil onChunk keeps the blocking behaviour.
func (c *Client) ChatStream(ctx context.Context, query string, inputs map[string]any, user, conversationID string, onMeta func(convID, msgID, taskID string), onChunk func(delta string)) (ChatReply, error) {
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
	metaSent, taskSent := false, false
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
		}
		if ev.TaskID != "" {
			out.TaskID = ev.TaskID
		}
		// Fire onMeta the first time each id appears (conversation id and task id can arrive on
		// different events), carrying every id captured so far — so the caller can persist the
		// linkage AND record the stop handle mid-turn. Fires at most twice.
		if onMeta != nil && ((ev.ConversationID != "" && !metaSent) || (ev.TaskID != "" && !taskSent)) {
			metaSent = metaSent || ev.ConversationID != ""
			taskSent = taskSent || ev.TaskID != ""
			onMeta(out.ConversationID, out.MessageID, out.TaskID)
		}
		switch ev.Event {
		case "message", "agent_message":
			answer.WriteString(ev.Answer)
			if onChunk != nil && ev.Answer != "" {
				onChunk(ev.Answer)
			}
		case "message_end", "workflow_finished":
			out.Answer = answer.String()
			return out, nil
		case "error":
			return ChatReply{}, fmt.Errorf("%s", firstNonEmpty(ev.Data.Error, ev.Message))
		}
	}
	if err := sc.Err(); err != nil {
		out.Answer = answer.String() // a cancelled/dropped turn still returns its partial answer + ids
		return out, err
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

// GetChatOutcome reconciles a dropped chat/agent run from its conversation history.
// A pure agent/basic-chat app never emits a workflow_run_id, so GetWorkflowRun can't
// reconcile it (docs/adr/0006-dify-native.md); instead we read the conversation's newest
// message: an `error` status is a terminal failure, a non-empty answer is success, and
// nothing-yet is "running" so the caller keeps polling. The returned RunResult mirrors
// GetWorkflowRun's shape so the same reconcile loop drives both.
func (c *Client) GetChatOutcome(ctx context.Context, conversationID, user string) (RunResult, error) {
	if user == "" {
		user = "report-portal"
	}
	q := url.Values{}
	q.Set("conversation_id", conversationID)
	q.Set("user", user)
	q.Set("limit", "20")
	raw, err := c.do(ctx, http.MethodGet, "/messages?"+q.Encode(), nil)
	if err != nil {
		return RunResult{}, err
	}
	var doc struct {
		Data []struct {
			ID        string `json:"id"`
			Answer    string `json:"answer"`
			Status    string `json:"status"`
			Error     string `json:"error"`
			CreatedAt int64  `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return RunResult{}, fmt.Errorf("dify /messages: bad JSON: %w", err)
	}
	if len(doc.Data) == 0 {
		return RunResult{ConversationID: conversationID, Status: "running", Raw: raw}, nil // not persisted yet
	}
	latest := doc.Data[0] // pick the newest turn regardless of the API's sort order
	for _, m := range doc.Data[1:] {
		if m.CreatedAt > latest.CreatedAt {
			latest = m
		}
	}
	out := RunResult{ConversationID: conversationID, Raw: raw}
	switch {
	case latest.Status == "error":
		out.Status, out.Error = "failed", firstNonEmpty(latest.Error, "dify chat message failed")
	case latest.Answer != "":
		out.Status, out.Outputs = "succeeded", map[string]any{"answer": latest.Answer}
	default:
		out.Status = "running" // no answer yet and not marked failed → still generating
	}
	return out, nil
}
