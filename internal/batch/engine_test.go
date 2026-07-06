package batch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// providerFunc adapts a function to a Provider.
type providerFunc func(ctx context.Context, inputs map[string]string) (RunResult, error)

func (f providerFunc) Run(ctx context.Context, inputs map[string]string) (RunResult, error) {
	return f(ctx, inputs)
}

func noBackoff(int) time.Duration { return 0 }

// A run that succeeds first try comes back Ok in one attempt.
func TestRunItemSuccess(t *testing.T) {
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		return RunResult{Status: Ok, RunID: "r1"}, nil
	})
	res, attempts := RunItem(context.Background(), prov, nil, 3, noBackoff, nil)
	if res.Status != Ok || res.RunID != "r1" || attempts != 1 {
		t.Fatalf("got {status:%v runID:%q attempts:%d}, want Ok/r1/1", res.Status, res.RunID, attempts)
	}
}

// A transient error is retried up to maxRetries; a later success is the outcome.
func TestRunItemRetriesTransient(t *testing.T) {
	var calls int32
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		if atomic.AddInt32(&calls, 1) < 3 {
			return RunResult{}, transientErr("boom")
		}
		return RunResult{Status: Ok, RunID: "r"}, nil
	})
	res, attempts := RunItem(context.Background(), prov, nil, 3, noBackoff, nil)
	if res.Status != Ok {
		t.Errorf("status = %v, want Ok", res.Status)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// A permanent error is not retried; it fails after one attempt, carrying the detail.
func TestRunItemPermanentNoRetry(t *testing.T) {
	var calls int32
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		atomic.AddInt32(&calls, 1)
		return RunResult{}, permanentErr("nope")
	})
	res, attempts := RunItem(context.Background(), prov, nil, 3, noBackoff, nil)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider called %d times, want 1", got)
	}
	if res.Status != Failed || attempts != 1 || res.Detail != "nope" {
		t.Errorf("got {status:%v attempts:%d detail:%q}, want Failed/1/nope", res.Status, attempts, res.Detail)
	}
}

// Exhausting retries on a transient error lands the item in Failed.
func TestRunItemTransientExhaustedFails(t *testing.T) {
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		return RunResult{}, transientErr("always down")
	})
	res, attempts := RunItem(context.Background(), prov, nil, 2, noBackoff, nil)
	if res.Status != Failed {
		t.Errorf("status = %v, want Failed", res.Status)
	}
	if attempts != 3 { // 1 initial + 2 retries
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// A ran-but-failed backend result (no error) is terminal — never retried.
func TestRunItemBackendFailureIsTerminal(t *testing.T) {
	var calls int32
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		atomic.AddInt32(&calls, 1)
		return RunResult{Status: Failed, Detail: "workflow failed"}, nil
	})
	res, attempts := RunItem(context.Background(), prov, nil, 3, noBackoff, nil)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider called %d times, want 1 (backend failure is terminal)", got)
	}
	if res.Status != Failed || attempts != 1 {
		t.Errorf("got {status:%v attempts:%d}, want Failed/1", res.Status, attempts)
	}
}

// A cancelled context stops retries between attempts.
func TestRunItemCancelStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	prov := providerFunc(func(context.Context, map[string]string) (RunResult, error) {
		atomic.AddInt32(&calls, 1)
		cancel() // cancel after the first attempt
		return RunResult{}, transientErr("down")
	})
	res, _ := RunItem(ctx, prov, nil, 5, noBackoff, nil)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider called %d times, want 1 (cancel stops retry)", got)
	}
	if res.Status != Failed {
		t.Errorf("status = %v, want Failed", res.Status)
	}
}
