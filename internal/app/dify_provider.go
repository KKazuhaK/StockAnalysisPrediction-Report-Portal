package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
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
// resolved per job from the dify_end_user template).
type difyProvider struct {
	c    *dify.Client
	user string
}

func (p difyProvider) Run(ctx context.Context, inputs map[string]string) (batch.RunResult, error) {
	in := make(map[string]any, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}
	r, err := p.c.RunWorkflow(ctx, in, p.user)
	if err != nil {
		return batch.RunResult{}, classifyDifyErr(err)
	}
	out := batch.Failed
	switch r.Status {
	case "succeeded":
		out = batch.Ok
	case "partial":
		out = batch.Partial
	}
	return batch.RunResult{RunID: r.WorkflowRunID, Status: out, Detail: r.Error, Raw: r.Raw}, nil
}

// classifyDifyErr marks a Dify run error retryable unless it's a permanent 4xx
// (a 429 rate-limit is still retryable).
func classifyDifyErr(err error) error {
	var ae *dify.APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests {
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
