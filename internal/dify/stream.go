package dify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

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

// streamEnvelope is the JSON shape of each `data:` line in the event stream.
type streamEnvelope struct {
	Event         string `json:"event"`
	TaskID        string `json:"task_id"`
	WorkflowRunID string `json:"workflow_run_id"`
	Data          struct {
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
func (c *Client) RunWorkflowStream(ctx context.Context, inputs map[string]any, user string, onEvent func(StreamEvent)) (RunResult, string, error) {
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
		if ev.Event == "workflow_finished" {
			res = RunResult{
				WorkflowRunID: runID, TaskID: taskID, Status: ev.Data.Status,
				Error: ev.Data.Error, Outputs: ev.Data.Outputs, Raw: append([]byte(nil), payload...),
			}
			done = true
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
	return res, runID, fmt.Errorf("dify stream ended before workflow_finished")
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
