package batch

import (
	"context"
	"time"
)

// This file runs ONE item against a Provider with a retry policy. Concurrency,
// persistence, cancellation, and crash recovery are the caller's job: the app-layer
// run queue schedules individual items and owns the single global concurrency cap
// (docs/adr/0011-run-level-scheduling.md, superseding the per-job worker pool of
// docs/adr/0001-batch-run-engine.md).

// Item is one row of a batch job to run through a Provider.
type Item struct {
	ID       int64
	RowIndex int
	Inputs   map[string]string
}

// RunItem triggers one item and returns its terminal outcome plus the attempt count.
// It retries ONLY transient transport errors, up to maxRetries; a backend that ran but
// reported failure (RunResult with no error) is terminal and never retried — the
// money-invariant "a started run is reconciled, never re-run" lives inside the Dify
// provider (docs/adr/0006-dify-native.md), not here. RunItem is stateless: the caller
// persists the result. A nil backoff uses the default capped exponential; a nil logf
// is a no-op.
func RunItem(ctx context.Context, prov Provider, inputs map[string]string, maxRetries int, backoff func(attempt int) time.Duration, logf func(string, ...any)) (RunResult, int) {
	if backoff == nil {
		backoff = defaultBackoff
	}
	attempts := 0
	for {
		attempts++
		res, err := prov.Run(ctx, inputs)
		if err == nil {
			return res, attempts
		}
		if IsTransient(err) && attempts <= maxRetries && ctx.Err() == nil {
			if logf != nil {
				logf("batch item: transient error (attempt %d), retrying: %v", attempts, err)
			}
			sleepCtx(ctx, backoff(attempts))
			continue
		}
		return RunResult{Status: Failed, Detail: err.Error()}, attempts
	}
}

// sleepCtx waits d, or returns early if ctx is cancelled. A non-positive d is a no-op.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// defaultBackoff is a simple capped exponential backoff: 1s, 2s, 4s, … max 30s.
func defaultBackoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
	}
	return d
}
