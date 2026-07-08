package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// Reconnect-not-retry tuning: how often to poll a started-but-disconnected run, and
// how long to keep reconciling before giving up (bounded by the same run budget).
// difyStopTimeout bounds the best-effort server-side stop issued on cancel.
const (
	difyReconcilePoll    = 3 * time.Second
	difyReconcileTimeout = difyRunTimeout
	difyStopTimeout      = 15 * time.Second
)

// The Dify-native batch path (docs/adr/0006-dify-native.md). A Dify target is a
// batch_targets row with plugin_slug == difyPluginSlug and a config JSON of
// {base_url, api_key, inputs}. buildProvider adapts a dify.Client to batch.Provider
// so the engine/queue (ADR 0001/0004) run Dify workflows unchanged.

const difyPluginSlug = "dify"

// difyTargetConfig is what a Dify target stores in batch_targets.config. Mode picks
// the run surface: "workflow" (default, /workflows/run) or "chat" (a chat/agent app,
// /chat-messages) — the row's "query" input becomes the chat message.
type difyTargetConfig struct {
	BaseURL string       `json:"base_url"`
	APIKey  string       `json:"api_key"`
	Mode    string       `json:"mode,omitempty"`
	Inputs  []dify.Input `json:"inputs"`
}

// difyModeChat reports whether a probed app mode is a chat/agent app (anything that
// isn't the workflow app). Dify workflow apps report mode "workflow".
func difyModeChat(mode string) bool {
	return mode != "" && mode != "workflow"
}

// difyProvider adapts a dify.Client to the batch engine's Provider interface.
// user is the caller identity Dify records for each run (the configurable end-user,
// resolved per job from the dify_end_user template). reconcilePoll/reconcileTimeout
// override the reconcile cadence (0 → the package defaults); tests set them short.
type difyProvider struct {
	c                *dify.Client
	user             string
	chat             bool // chat/agent app (/chat-messages) vs workflow (/workflows/run)
	poll             bool // poll mode: capture the run id then poll for the outcome (don't hold the stream open)
	reconcilePoll    time.Duration
	reconcileTimeout time.Duration
	// onRef is called the instant a run/conversation/task id streams in, so the caller can
	// persist it immediately — a crash/restart mid-run then reconciles by that id instead of
	// re-running (the restart-durable half of reconcile-not-retry). nil disables it. It may be
	// called repeatedly as ids accumulate; each call carries every id known so far.
	onRef func(runID, convID, taskID string)
	// onStarted is called once the instant the stream opens (2xx), BEFORE any id — the caller
	// persists "this run reached Dify" so a crash before the first id still marks it started
	// (→ untracked, never re-run) rather than un-started (→ re-run). nil disables it.
	onStarted func()
}

// runStream dispatches to the chat or workflow stream by the target's mode. Both
// return the same shape, so the reconnect / reconcile / stop logic is identical. In poll
// mode the stream returns as soon as the run id is captured.
func (p difyProvider) runStream(ctx context.Context, in map[string]any, onEvent func(dify.StreamEvent)) (dify.RunResult, string, error) {
	if p.chat {
		return p.c.RunChatStream(ctx, in, p.user, p.poll, onEvent)
	}
	return p.c.RunWorkflowStream(ctx, in, p.user, p.poll, onEvent)
}

func (p difyProvider) Run(ctx context.Context, inputs map[string]string) (batch.RunResult, error) {
	in := make(map[string]any, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}
	// Capture the ids as they stream: the task id lets a cancel stop the run server-side, and
	// every id is handed to onRef the moment it appears so the caller can persist it before the
	// run finishes — that is what makes a crash/restart mid-run reconcilable instead of re-run.
	var taskID, evRunID, evConvID string
	r, runID, err := p.runStream(ctx, in, func(e dify.StreamEvent) {
		if e.Event == dify.EventStreamOpen { // stream is open (2xx) — the run reached Dify
			if p.onStarted != nil {
				p.onStarted()
			}
			return
		}
		changed := false
		if e.TaskID != "" && e.TaskID != taskID {
			taskID, changed = e.TaskID, true
		}
		if e.RunID != "" && e.RunID != evRunID {
			evRunID, changed = e.RunID, true
		}
		if e.ConvID != "" && e.ConvID != evConvID {
			evConvID, changed = e.ConvID, true
		}
		if changed && p.onRef != nil {
			p.onRef(evRunID, evConvID, taskID)
		}
	})
	convID := r.ConversationID // chat/agent apps: the handle to reconcile a dropped run
	// Poll mode: the stream returned as soon as the run id was captured. Poll the run to
	// its terminal state instead of holding the connection open. Reconcile never re-runs.
	if p.poll && err == nil && runID != "" && !difyTerminal(r.Status) {
		r, err = p.reconcile(ctx, runID)
		if err != nil {
			// Couldn't poll the started run to a terminal state — the outcome is unknown, not a
			// failure. Untracked (no error → no re-run) keeps run_id so it can be reconciled later.
			return batch.RunResult{Status: batch.Untracked, RunID: runID, Detail: fmt.Sprintf("poll dify run %s: %v", runID, err)}, nil
		}
		return difyResultToBatch(r), nil
	}
	if err == nil {
		return difyResultToBatch(r), nil
	}
	// Job cancelled: aborting the stream only stops OUR wait — Dify keeps executing the
	// workflow until told to stop. Best-effort stop it, then let the engine mark the row.
	if ctx.Err() != nil {
		if taskID != "" {
			p.stop(taskID)
		}
		return batch.RunResult{}, classifyDifyErr(err)
	}
	// The stream dropped mid-run. NEVER re-run a run that started — reconcile its true outcome by
	// polling whatever handle we captured. If reconcile itself can't reach a terminal state the
	// outcome is UNKNOWN, not failed: return Untracked (no error → the engine can't retry, so a
	// retry never re-runs the started run — the ~1M-token duplicate this exists to avoid) and keep
	// the handle so it can be reconciled later.
	if runID != "" { // workflow / chatflow: reconcile by workflow run id
		r, err = p.reconcile(ctx, runID)
		if err != nil {
			return batch.RunResult{Status: batch.Untracked, RunID: runID, Detail: fmt.Sprintf("reconcile dify run %s: %v", runID, err)}, nil
		}
		return difyResultToBatch(r), nil
	}
	if p.chat && convID != "" { // pure agent/chat: no run id — reconcile via message history
		r, err = p.reconcileChat(ctx, convID)
		if err != nil {
			return batch.RunResult{Status: batch.Untracked, Detail: fmt.Sprintf("reconcile dify chat %s: %v", convID, err)}, nil
		}
		return difyResultToBatch(r), nil
	}
	// A task id means a run demonstrably STARTED but left us no id to reconcile it with (e.g. the
	// stream was torn down before the run/conversation id was emitted). Its outcome is UNKNOWN and
	// re-running would duplicate it, so mark Untracked — never re-run.
	if taskID != "" {
		return batch.RunResult{Status: batch.Untracked, Detail: fmt.Sprintf("dify run started (task %s) but the stream ended before an id to reconcile; not re-run to avoid a duplicate charged run: %v", taskID, err)}, nil
	}
	// No ids at all — but if the STREAM OPENED (Dify returned 2xx and accepted the request), a run
	// was almost certainly created and is running blind (e.g. it stalled before emitting any event
	// under DB pressure). Re-running duplicates it, so mark Untracked — never re-run. This is also
	// the runaway amplifier: without it, an overloaded Dify makes every run look "unstarted", the
	// engine re-fires them, and that piles on more load + burns tokens on duplicate runs.
	if errors.Is(err, dify.ErrStreamEnded) {
		return batch.RunResult{Status: batch.Untracked, Detail: fmt.Sprintf("dify accepted the run but the stream ended before any id; not re-run to avoid a duplicate charged run: %v", err)}, nil
	}
	// A genuine pre-stream failure (connection refused / non-2xx) → nothing started → safe to retry.
	return batch.RunResult{}, classifyDifyErr(err)
}

// Reconcile recovers a run's terminal outcome from a persisted handle WITHOUT re-running it —
// by workflow run id if present, else by conversation id for a pure agent/chat app. It is the
// restart/resume and manual-reconcile entry point (batch.Reconciler); an empty pair means there
// is no handle to reconcile from.
func (p difyProvider) Reconcile(ctx context.Context, runID, convID string) (batch.RunResult, error) {
	var r dify.RunResult
	var err error
	switch {
	case runID != "":
		r, err = p.reconcile(ctx, runID)
	case convID != "":
		r, err = p.reconcileChat(ctx, convID)
	default:
		return batch.RunResult{}, fmt.Errorf("no run or conversation id to reconcile")
	}
	if err != nil {
		return batch.RunResult{}, err
	}
	return difyResultToBatch(r), nil
}

// reconcile polls a started WORKFLOW/chatflow run to its terminal state by its run id.
func (p difyProvider) reconcile(ctx context.Context, runID string) (dify.RunResult, error) {
	return p.reconcileLoop(ctx, func(ctx context.Context) (dify.RunResult, error) {
		return p.c.GetWorkflowRun(ctx, runID)
	})
}

// reconcileChat polls a started pure agent/chat run to its terminal state by its
// conversation id — it has no workflow run id, so GetWorkflowRun can't reconcile it;
// the outcome is read from the conversation's message history instead.
func (p difyProvider) reconcileChat(ctx context.Context, convID string) (dify.RunResult, error) {
	return p.reconcileLoop(ctx, func(ctx context.Context) (dify.RunResult, error) {
		return p.c.GetChatOutcome(ctx, convID, p.user)
	})
}

// reconcileLoop polls fetch until it reports a terminal state so a dropped stream never
// triggers a re-run. Transient poll failures (5xx / 429 / network) and a still-"running"
// status are retried within the reconcile deadline; only a permanent error (e.g. an
// unknown id) gives up early.
func (p difyProvider) reconcileLoop(ctx context.Context, fetch func(context.Context) (dify.RunResult, error)) (dify.RunResult, error) {
	poll, deadline := p.reconcilePoll, p.reconcileTimeout
	if poll <= 0 {
		poll = difyReconcilePoll
	}
	if deadline <= 0 {
		deadline = difyReconcileTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		r, err := fetch(ctx)
		if err == nil && r.Status != "" && r.Status != "running" {
			return r, nil // terminal: succeeded / failed / stopped
		}
		if err != nil && isPermanentDifyErr(err) {
			return dify.RunResult{}, err // e.g. id not found — polling won't help
		}
		// A transient error or a still-running status: wait and poll again until the
		// deadline (whereupon ctx.Done fires and we give up — permanently, per Run).
		select {
		case <-ctx.Done():
			return dify.RunResult{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// stop asks Dify to stop a run server-side (true cancel). It runs on a fresh, short
// context because the job context that triggered the cancel is already done. Best
// effort — a failed stop only means the run finishes on Dify as it would have.
func (p difyProvider) stop(taskID string) {
	ctx, cancel := context.WithTimeout(context.Background(), difyStopTimeout)
	defer cancel()
	if p.chat {
		_ = p.c.StopChat(ctx, taskID, p.user)
		return
	}
	_ = p.c.StopWorkflow(ctx, taskID, p.user)
}

// difyTerminal reports whether a Dify status is final (succeeded / failed / stopped) —
// i.e. not empty and not "running".
func difyTerminal(status string) bool {
	return status != "" && status != "running"
}

// difyResultToBatch maps a Dify run outcome to the engine's per-row result. A Dify
// workflow status is succeeded / failed / stopped; only succeeded is a success.
func difyResultToBatch(r dify.RunResult) batch.RunResult {
	out := batch.Failed
	if r.Status == "succeeded" {
		out = batch.Ok
	}
	return batch.RunResult{RunID: r.WorkflowRunID, Status: out, Detail: r.Error, Raw: r.Raw}
}

// isPermanentDifyErr reports whether an error is a non-retryable Dify 4xx (a 429
// rate-limit is still transient).
func isPermanentDifyErr(err error) bool {
	var ae *dify.APIError
	return errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests
}

// classifyDifyErr marks a run error retryable unless it's a permanent 4xx.
func classifyDifyErr(err error) error {
	if isPermanentDifyErr(err) {
		return permanentRunErr{err}
	}
	return transientRunErr{err}
}

// transientRunErr / permanentRunErr carry the retry classification the batch engine
// reads via batch.IsTransient (which looks for an interface{ Transient() bool }).
type transientRunErr struct{ error }

func (transientRunErr) Transient() bool { return true }
func (e transientRunErr) Unwrap() error { return e.error }

type permanentRunErr struct{ error }

func (permanentRunErr) Transient() bool { return false }
func (e permanentRunErr) Unwrap() error { return e.error }

// buildDifyProvider constructs the provider for a Dify target from its config JSON.
// user is the end-user identity Dify records for each run (resolved from the
// dify_end_user template); an empty user falls back to "report-portal" at run time.
// onRef (may be nil) persists the run/conversation/task ids as they stream in; onStarted (may be
// nil) persists "the run reached Dify" the instant the stream opens, before any id.
func buildDifyProvider(configJSON, user string, poll bool, pollInterval, runTimeout time.Duration, onRef func(runID, convID, taskID string), onStarted func()) (batch.Provider, error) {
	var cfg difyTargetConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("dify target config: %w", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return nil, fmt.Errorf("dify target: base_url and api_key are required")
	}
	if runTimeout <= 0 {
		runTimeout = difyRunTimeout // package default (admin can override via dify_run_timeout_minutes)
	}
	p := difyProvider{
		c:                dify.New(cfg.BaseURL, cfg.APIKey, &http.Client{Timeout: runTimeout}),
		user:             user,
		chat:             difyModeChat(cfg.Mode),
		poll:             poll,
		reconcileTimeout: runTimeout, // the reconcile poll window matches the run cap
		onRef:            onRef,
		onStarted:        onStarted,
	}
	if poll && pollInterval > 0 {
		p.reconcilePoll = pollInterval
	}
	return p, nil
}

// difyTargetInputs returns a Dify target's discovered inputs (for the run form), or
// nil if the config can't be read.
func difyTargetInputs(configJSON string) []dify.Input {
	var cfg difyTargetConfig
	json.Unmarshal([]byte(configJSON), &cfg)
	return cfg.Inputs
}

// difyTargetMode returns a Dify target's app mode ("" / "workflow" / "chat").
func difyTargetMode(configJSON string) string {
	var cfg difyTargetConfig
	json.Unmarshal([]byte(configJSON), &cfg)
	return cfg.Mode
}
