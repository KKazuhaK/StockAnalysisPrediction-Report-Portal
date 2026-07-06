package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runGate caps concurrent holders GLOBALLY at the current limit, no matter how many
// callers acquire at once — the whole point of the global run cap. The limit is read
// live, so lowering it is honored immediately (no restart).
func TestRunGateCapsConcurrency(t *testing.T) {
	var limit int32 = 2
	gate := newRunGate(func() int { return int(atomic.LoadInt32(&limit)) })
	var cur, max int32

	hold := func() {
		if !gate.Acquire(context.Background()) {
			return
		}
		defer gate.Release()
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
	}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hold() }()
	}
	wg.Wait()
	if max > 2 {
		t.Fatalf("max concurrent holders = %d, want <= 2 (the global cap)", max)
	}

	// Lowering the limit to 1 is honored immediately (no restart): the next burst never
	// exceeds 1 at a time.
	atomic.StoreInt32(&limit, 1)
	atomic.StoreInt32(&max, 0)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hold() }()
	}
	wg.Wait()
	if max > 1 {
		t.Fatalf("after lowering the limit to 1, max concurrent = %d, want <= 1", max)
	}
}

// A cancelled context aborts a blocked acquire and returns false (so the engine leaves
// the row queued) — the single free slot must stay held by whoever has it.
func TestRunGateAcquireCancel(t *testing.T) {
	gate := newRunGate(func() int { return 1 })
	if !gate.Acquire(context.Background()) {
		t.Fatal("expected to acquire the one free slot")
	}
	defer gate.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gate.Acquire(ctx) {
		t.Fatal("acquire on a full gate with a cancelled context should return false")
	}
}
