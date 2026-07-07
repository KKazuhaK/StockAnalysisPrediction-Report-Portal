package dify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrStreamEnded means the SSE stream OPENED (Dify returned 2xx and accepted the request)
// but closed before any terminal event. Dify creates the run the moment it accepts the
// POST, so a run has almost certainly STARTED even when we captured no run/task id (e.g. it
// stalled before emitting workflow_started under DB pressure). Callers must NOT re-run on
// this — it would duplicate a live run. It is distinct from a pre-stream failure (connection
// refused / non-2xx), where nothing started and a retry is safe.
var ErrStreamEnded = errors.New("dify stream ended before completion")

// Streaming run + reconcile + stop (docs/adr/0006-dify-native.md). Streaming mode is
// what lets the portal (a) capture the run id the instant a workflow starts — so a
// dropped connection is reconciled by polling instead of re-run — and (b) stop a run
// server-side via its task id. The SSE stream carries the same {status,outputs,error}
// shape as the blocking response, wrapped in per-event envelopes.

// StreamEvent is one progress event forwarded to the caller during a streaming run.
type StreamEvent struct {
	Event  string // workflow_started | node_started | node_finished | workflow_finished | ...
	TaskID string
	RunID  string
	Title  string // node title (a human-readable progress label)
	Index  int    // node sequence index within the run
	Status string // node/workflow status on a *_finished event
}

// streamEnvelope is the JSON shape of each `data:` line in the event stream (both
// the workflow and the chat streams; chat adds message_id/answer and a top-level
// error message).
type streamEnvelope struct {
	Event          string `json:"event"`
	TaskID         string `json:"task_id"`
	WorkflowRunID  string `json:"workflow_run_id"`
	ConversationID string `json:"conversation_id"` // chat/agent apps: assigned on the first event
	MessageID      string `json:"message_id"`
	Answer         string `json:"answer"`  // chat/agent apps: a chunk of the reply
	Message        string `json:"message"` // top-level text of an `error` event
	Data           struct {
		Title   string         `json:"title"`
		Index   int            `json:"index"`
		Status  string         `json:"status"`
		Error   string         `json:"error"`
		Outputs map[string]any `json:"outputs"`
	} `json:"data"`
}

// RunWorkflowStream runs the workflow in streaming mode. It captures the run id (and
// task id) the moment Dify emits `workflow_started`, forwards progress to onEvent
// (nil to ignore), and returns the final RunResult from `workflow_finished`.
//
// runID is returned even on a mid-stream error, so a caller whose connection drops
// after the run started can reconcile the true outcome via GetWorkflowRun instead of
// re-running the workflow (the duplicate-run hazard of blocking mode). onEvent is
// called from this goroutine, synchronously, in stream order.
func (c *Client) RunWorkflowStream(ctx context.Context, inputs map[string]any, user string, stopAtRunID bool, onEvent func(StreamEvent)) (RunResult, string, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	if user == "" {
		user = "report-portal"
	}
	body, _ := json.Marshal(map[string]any{"inputs": inputs, "response_mode": "streaming", "user": user})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/workflows/run", bytes.NewReader(body))
	if err != nil {
		return RunResult{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return RunResult{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return RunResult{}, "", &APIError{Status: resp.StatusCode, Message: apiErrMsg(raw)}
	}

	var res RunResult
	var runID, taskID string
	done := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20) // a workflow_finished frame can be large
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 5 || line[:5] != "data:" {
			continue // skip SSE comments / event: lines / blank separators
		}
		payload := bytes.TrimSpace([]byte(line[5:]))
		if len(payload) == 0 {
			continue
		}
		var ev streamEnvelope
		if json.Unmarshal(payload, &ev) != nil {
			continue // ignore malformed / keepalive frames
		}
		if ev.WorkflowRunID != "" {
			runID = ev.WorkflowRunID
		}
		if ev.TaskID != "" {
			taskID = ev.TaskID
		}
		if onEvent != nil {
			onEvent(StreamEvent{Event: ev.Event, TaskID: taskID, RunID: runID, Title: ev.Data.Title, Index: ev.Data.Index, Status: ev.Data.Status})
		}
		// Poll mode: once the run id is captured, hand off to status polling instead of
		// holding the stream open for the whole run (proxy-friendly; no long-lived SSE).
		if stopAtRunID && runID != "" {
			return res, runID, nil
		}
		switch ev.Event {
		case "workflow_finished":
			res = RunResult{
				WorkflowRunID: runID, TaskID: taskID, Status: ev.Data.Status,
				Error: ev.Data.Error, Outputs: ev.Data.Outputs, Raw: append([]byte(nil), payload...),
			}
			done = true
		case "error":
			// A stream-level error (e.g. bad model config / quota / rate limit) — often
			// emitted before workflow_started, so there is no run to reconcile. Surface
			// Dify's real message as a terminal failure instead of the generic
			// "stream ended" fallback (which hid the actual cause).
			if !done {
				res = RunResult{
					WorkflowRunID: runID, TaskID: taskID, Status: "failed",
					Error: firstNonEmpty(ev.Data.Error, ev.Message), Raw: append([]byte(nil), payload...),
				}
				done = true
			}
		}
	}
	// If workflow_finished already arrived, the run completed — a trailing read error
	// (e.g. the server resetting the just-closed connection) doesn't change the outcome.
	if done {
		return res, runID, nil
	}
	if err := sc.Err(); err != nil {
		return res, runID, err // stream dropped mid-run; runID lets the caller reconcile
	}
	return res, runID, fmt.Errorf("dify stream ended before workflow_finished: %w", ErrStreamEnded)
}

// RunChatStream runs a chat/agent app (/chat-messages) in streaming mode. `query` is
// taken from inputs["query"], the rest are passed as inputs. Like RunWorkflowStream it
// captures a run id early — chatflow/advanced-chat apps emit a workflow_run_id, so a
// dropped connection is reconciled the same way (via GetWorkflowRun) instead of
// re-running. The run completes on workflow_finished or message_end; an `error` event
// is a terminal failure (not a transport error).
func (c *Client) RunChatStream(ctx context.Context, inputs map[string]any, user string, stopAtRunID bool, onEvent func(StreamEvent)) (RunResult, string, error) {
	if user == "" {
		user = "report-portal"
	}
	query, _ := inputs["query"].(string)
	rest := make(map[string]any, len(inputs))
	for k, v := range inputs {
		if k != "query" {
			rest[k] = v
		}
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "inputs": rest, "response_mode": "streaming",
		"user": user, "conversation_id": "",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat-messages", bytes.NewReader(body))
	if err != nil {
		return RunResult{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return RunResult{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return RunResult{}, "", &APIError{Status: resp.StatusCode, Message: apiErrMsg(raw)}
	}

	var res RunResult
	var runID, taskID, convID string
	done := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	// partial carries the ids seen so far, so a stream that drops before a terminal event
	// still hands the caller a conversation id (pure chat/agent) or run id (chatflow) to
	// reconcile with instead of nothing — the pure-chat analogue of returning the run id.
	partial := func() RunResult { return RunResult{WorkflowRunID: runID, ConversationID: convID, TaskID: taskID} }
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
		if ev.WorkflowRunID != "" {
			runID = ev.WorkflowRunID
		}
		if ev.TaskID != "" {
			taskID = ev.TaskID
		}
		if ev.ConversationID != "" {
			convID = ev.ConversationID
		}
		if onEvent != nil {
			onEvent(StreamEvent{Event: ev.Event, TaskID: taskID, RunID: runID, Title: ev.Data.Title, Index: ev.Data.Index, Status: ev.Data.Status})
		}
		if stopAtRunID && runID != "" {
			return partial(), runID, nil // poll mode: hand off to status polling
		}
		switch ev.Event {
		case "workflow_finished":
			res = RunResult{WorkflowRunID: runID, ConversationID: convID, TaskID: taskID, Status: ev.Data.Status, Error: ev.Data.Error, Raw: append([]byte(nil), payload...)}
			done = true
		case "message_end":
			if !done { // chatflow sends workflow_finished first; message_end is the end for pure chat
				res = RunResult{WorkflowRunID: runID, ConversationID: convID, TaskID: taskID, Status: "succeeded", Raw: append([]byte(nil), payload...)}
				done = true
			}
		case "error":
			if !done { // don't let a trailing error clobber an already-terminal result
				res = RunResult{WorkflowRunID: runID, ConversationID: convID, TaskID: taskID, Status: "failed", Error: firstNonEmpty(ev.Data.Error, ev.Message), Raw: append([]byte(nil), payload...)}
				done = true
			}
		}
	}
	if done {
		return res, runID, nil
	}
	if err := sc.Err(); err != nil {
		return partial(), runID, err
	}
	return partial(), runID, fmt.Errorf("dify chat stream ended before completion: %w", ErrStreamEnded)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// StopChat stops a streaming chat run server-side by its task id (best-effort).
func (c *Client) StopChat(ctx context.Context, taskID, user string) error {
	if user == "" {
		user = "report-portal"
	}
	_, err := c.do(ctx, http.MethodPost, "/chat-messages/"+taskID+"/stop", map[string]any{"user": user})
	return err
}

// GetWorkflowRun fetches a run's current state by id, used to reconcile a dropped
// stream without re-running the workflow. Status is one of running/succeeded/failed/
// stopped.
func (c *Client) GetWorkflowRun(ctx context.Context, runID string) (RunResult, error) {
	raw, err := c.do(ctx, http.MethodGet, "/workflows/run/"+runID, nil)
	if err != nil {
		return RunResult{}, err
	}
	var doc struct {
		ID      string         `json:"id"`
		Status  string         `json:"status"`
		Error   string         `json:"error"`
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return RunResult{}, fmt.Errorf("dify /workflows/run/{id}: bad JSON: %w", err)
	}
	return RunResult{WorkflowRunID: doc.ID, Status: doc.Status, Error: doc.Error, Outputs: doc.Outputs, Raw: raw}, nil
}

// StopWorkflow asks Dify to stop a streaming run server-side by its task id (true
// cancel — the workflow stops on Dify, not just on our end). Best-effort.
func (c *Client) StopWorkflow(ctx context.Context, taskID, user string) error {
	if user == "" {
		user = "report-portal"
	}
	_, err := c.do(ctx, http.MethodPost, "/workflows/tasks/"+taskID+"/stop", map[string]any{"user": user})
	return err
}
