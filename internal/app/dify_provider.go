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
const (
	difyReconcilePoll    = 3 * time.Second
	difyReconcileTimeout = difyRunTimeout
)

// The Dify-native batch path (docs/adr/0006-dify-native.md). A Dify target is a
// batch_targets row with plugin_slug == difyPluginSlug and a config JSON of
// {base_url, api_key, inputs}. buildProvider adapts a dify.Client to batch.Provider
// so the engine/queue (ADR 0001/0004) run Dify workflows unchanged.

const difyPluginSlug = "dify"

// difyTargetConfig is what a Dify target stores in batch_targets.config.
type difyTargetConfig struct {
	BaseURL string       `json:"base_url"`
	APIKey  string       `json:"api_key"`
	Inputs  []dify.Input `json:"inputs"`
}

// difyProvider adapts a dify.Client to the batch engine's Provider interface.
// user is the caller identity Dify records for each run (the configurable end-user,
// resolved per job from the dify_end_user template). reconcilePoll/reconcileTimeout
// override the reconcile cadence (0 → the package defaults); tests set them short.
type difyProvider struct {
	c                *dify.Client
	user             string
	reconcilePoll    time.Duration
	reconcileTimeout time.Duration
}

func (p difyProvider) Run(ctx context.Context, inputs map[string]string) (batch.RunResult, error) {
	in := make(map[string]any, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}
	r, runID, err := p.c.RunWorkflowStream(ctx, in, p.user, nil)
	if err == nil {
		return difyResultToBatch(r), nil
	}
	// Nothing started (no run id) or the job was cancelled → let the engine classify
	// and, where appropriate, retry: no workflow is running, so a retry is safe.
	// (Residual: a drop in the tiny window after Dify accepts the POST but before it
	// emits the run id leaves runID empty though a run may have started — unavoidable
	// without a request idempotency key, which Dify's workflow API doesn't offer.)
	if runID == "" || ctx.Err() != nil {
		return batch.RunResult{}, classifyDifyErr(err)
	}
	// A run has STARTED. Never re-run it — reconcile the true outcome by polling. Any
	// reconcile failure is returned as PERMANENT so the engine can't retry (a retry
	// would re-run the started workflow — the ~1M-token duplicate this exists to avoid).
	r, err = p.reconcile(ctx, runID)
	if err != nil {
		return batch.RunResult{}, permanentRunErr{fmt.Errorf("reconcile dify run %s: %w", runID, err)}
	}
	return difyResultToBatch(r), nil
}

// reconcile polls a started run to its terminal state so a dropped stream never
// triggers a re-run. Transient poll failures (5xx / 429 / network) are retried within
// the reconcile deadline; only a permanent error (e.g. an unknown run id) gives up.
func (p difyProvider) reconcile(ctx context.Context, runID string) (dify.RunResult, error) {
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
		r, err := p.c.GetWorkflowRun(ctx, runID)
		if err == nil && r.Status != "" && r.Status != "running" {
			return r, nil // terminal: succeeded / failed / stopped
		}
		if err != nil && isPermanentDifyErr(err) {
			return dify.RunResult{}, err // e.g. run id not found — polling won't help
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

type permanentRunErr struct{ error }

func (permanentRunErr) Transient() bool { return false }

// buildDifyProvider constructs the provider for a Dify target from its config JSON.
// user is the end-user identity Dify records for each run (resolved from the
// dify_end_user template); an empty user falls back to "report-portal" at run time.
func buildDifyProvider(configJSON, user string) (batch.Provider, error) {
	var cfg difyTargetConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("dify target config: %w", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return nil, fmt.Errorf("dify target: base_url and api_key are required")
	}
	return difyProvider{c: dify.New(cfg.BaseURL, cfg.APIKey, &http.Client{Timeout: difyRunTimeout}), user: user}, nil
}

// difyTargetInputs returns a Dify target's discovered inputs (for the run form), or
// nil if the config can't be read.
func difyTargetInputs(configJSON string) []dify.Input {
	var cfg difyTargetConfig
	json.Unmarshal([]byte(configJSON), &cfg)
	return cfg.Inputs
}
