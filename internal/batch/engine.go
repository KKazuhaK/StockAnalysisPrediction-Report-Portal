package batch

import (
	"context"
	"sync"
	"time"
)

// Item is one row of a batch job that the engine drives through a Provider.
type Item struct {
	ID       int64
	RowIndex int
	Inputs   map[string]string
}

// JobStore is the persistence port the engine depends on. internal/app implements
// it against the real Store; per-item state lives in the DB so progress survives a
// page refresh or a server restart. Implementations must be safe for concurrent
// use — the worker pool calls StartItem/FinishItem from many goroutines.
type JobStore interface {
	// QueuedItems returns the still-to-process items for a job, in order.
	QueuedItems(jobID int64) ([]Item, error)
	// StartItem transitions an item to running.
	StartItem(itemID int64) error
	// FinishItem records an item's terminal outcome.
	FinishItem(itemID int64, status Outcome, attempts int, runID, detail string) error
	// Cancelled reports whether the job has been asked to stop.
	Cancelled(jobID int64) (bool, error)
	// FinishJob marks the whole job done (cancelled=true if it stopped early).
	FinishJob(jobID int64, cancelled bool) error
}

// JobSpec parameterises a single run of the engine. Concurrency is fixed for the
// life of the job (see ADR 0001); the app layer clamps it to the admin cap before
// handing it here.
type JobSpec struct {
	JobID       int64
	Concurrency int
	MaxRetries  int
}

// Gate caps how many items run concurrently across ALL jobs (the global run cap).
// Acquire blocks until a slot is free or ctx is cancelled; a false return means
// cancelled, so the caller must NOT run the row — it stays queued and a later RunJob
// re-dispatches it. Release frees the slot. A nil Engine.Gate is a pass-through.
//
// The engine acquires a slot BEFORE marking a row running, so a row shows 'running'
// only once it truly holds a slot: the displayed running count then equals the real
// number of concurrent runs, never more.
type Gate interface {
	Acquire(ctx context.Context) bool
	Release()
}

// Engine runs a batch job: a worker pool triggers a Provider over the job's queued
// items, retries transient failures, honours cancellation, and persists per-item
// state. It is backend-agnostic — it only knows the Provider interface.
type Engine struct {
	Store   JobStore
	Gate    Gate                            // global concurrent-run cap; nil → no cap
	Backoff func(attempt int) time.Duration // nil → defaultBackoff
	Log     func(string, ...any)            // nil → no-op
}

// RunJob drives one job to completion (or cancellation). It blocks until every
// dispatched item finishes. Items not dispatched (because of cancellation) stay
// queued, so a later RunJob resumes them.
func (e *Engine) RunJob(ctx context.Context, spec JobSpec, prov Provider) error {
	items, err := e.Store.QueuedItems(spec.JobID)
	if err != nil {
		return err
	}
	conc := spec.Concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	cancelled := false
	// stop reports whether the job has been cancelled (via context or the store), so
	// the dispatch loop can bail out promptly.
	stop := func() bool {
		if ctx.Err() != nil {
			return true
		}
		c, _ := e.Store.Cancelled(spec.JobID)
		return c
	}
	for _, it := range items {
		if stop() {
			cancelled = true
			break
		}
		sem <- struct{}{}
		// A cancel can arrive while we were blocked waiting for a free worker; re-check
		// before dispatching so no new row starts after cancellation.
		if stop() {
			<-sem
			cancelled = true
			break
		}
		wg.Add(1)
		go func(it Item) {
			defer wg.Done()
			defer func() { <-sem }()
			e.processItem(ctx, spec, prov, it)
		}(it)
	}
	wg.Wait()
	if ctx.Err() != nil {
		cancelled = true
	}
	return e.Store.FinishJob(spec.JobID, cancelled)
}

// processItem triggers one row, retrying transient errors up to MaxRetries. A
// backend that ran but reported failure (RunResult with no error) is terminal and
// never retried; only transport-level transient errors are.
func (e *Engine) processItem(ctx context.Context, spec JobSpec, prov Provider, it Item) {
	// Take a global run slot before marking the row running, so a row only ever shows
	// 'running' once it actually holds a slot. Held for the whole retry loop below —
	// released on return. A cancelled acquire leaves the row queued for a later resume.
	if e.Gate != nil {
		if !e.Gate.Acquire(ctx) {
			return
		}
		defer e.Gate.Release()
	}
	_ = e.Store.StartItem(it.ID)
	attempts := 0
	for {
		attempts++
		res, err := prov.Run(ctx, it.Inputs)
		if err == nil {
			_ = e.Store.FinishItem(it.ID, res.Status, attempts, res.RunID, res.Detail)
			return
		}
		if IsTransient(err) && attempts <= spec.MaxRetries && ctx.Err() == nil {
			e.logf("job %d item %d: transient error (attempt %d), retrying: %v", spec.JobID, it.ID, attempts, err)
			e.sleep(ctx, attempts)
			continue
		}
		_ = e.Store.FinishItem(it.ID, Failed, attempts, "", err.Error())
		return
	}
}

func (e *Engine) sleep(ctx context.Context, attempt int) {
	d := defaultBackoff(attempt)
	if e.Backoff != nil {
		d = e.Backoff(attempt)
	}
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

func (e *Engine) logf(format string, a ...any) {
	if e.Log != nil {
		e.Log(format, a...)
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
