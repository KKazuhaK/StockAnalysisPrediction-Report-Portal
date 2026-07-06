package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// providerRunFunc adapts a func to batch.Provider for the gate test.
type providerRunFunc func(context.Context, map[string]string) (batch.RunResult, error)

func (f providerRunFunc) Run(ctx context.Context, in map[string]string) (batch.RunResult, error) {
	return f(ctx, in)
}

// gatedProvider caps concurrent runs across all callers at the gate's size, no matter how
// many rows/jobs fire at once — the whole point of the global run cap.
func TestGatedProviderCapsConcurrency(t *testing.T) {
	const cap = 2
	gate := make(chan struct{}, cap)
	var cur, max int32
	inner := providerRunFunc(func(ctx context.Context, _ map[string]string) (batch.RunResult, error) {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		return batch.RunResult{Status: batch.Ok}, nil
	})
	gp := gatedProvider{inner: inner, gate: gate}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); gp.Run(context.Background(), nil) }()
	}
	wg.Wait()
	if max > cap {
		t.Fatalf("max concurrent runs = %d, want <= %d (the global cap)", max, cap)
	}
	// A nil gate is a pass-through (used in tests / when unsized).
	if _, err := (gatedProvider{inner: inner}).Run(context.Background(), nil); err != nil {
		t.Fatalf("nil-gate pass-through: %v", err)
	}
}
