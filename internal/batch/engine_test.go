package batch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- test doubles ----

type fakeItem struct {
	id       int64
	rowIndex int
	inputs   map[string]string
	status   string // queued | running | done
	outcome  Outcome
	attempts int
	runID    string
	detail   string
}

type fakeStore struct {
	mu           sync.Mutex
	items        []*fakeItem
	cancelled    bool
	jobFinished  bool
	jobCancelled bool
}

func (s *fakeStore) QueuedItems(int64) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if it.status == "queued" {
			out = append(out, Item{ID: it.id, RowIndex: it.rowIndex, Inputs: it.inputs})
		}
	}
	return out, nil
}

func (s *fakeStore) find(id int64) *fakeItem {
	for _, it := range s.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

func (s *fakeStore) StartItem(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.find(id).status = "running"
	return nil
}

func (s *fakeStore) FinishItem(id int64, st Outcome, attempts int, runID, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(id)
	it.status, it.outcome, it.attempts, it.runID, it.detail = "done", st, attempts, runID, detail
	return nil
}

func (s *fakeStore) Cancelled(int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled, nil
}

func (s *fakeStore) FinishJob(_ int64, cancelled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobFinished, s.jobCancelled = true, cancelled
	return nil
}

func (s *fakeStore) statusOf(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(id).status
}

// providerFunc adapts a function to a Provider.
type providerFunc func(ctx context.Context, inputs map[string]string) (RunResult, error)

func (f providerFunc) Run(ctx context.Context, inputs map[string]string) (RunResult, error) {
	return f(ctx, inputs)
}

func seedStore(n int) *fakeStore {
	s := &fakeStore{}
	for i := 0; i < n; i++ {
		s.items = append(s.items, &fakeItem{id: int64(i + 1), rowIndex: i, inputs: map[string]string{"code": "x"}, status: "queued"})
	}
	return s
}

func noBackoff(int) time.Duration { return 0 }

// countingGate is a test Gate that enforces `limit` concurrent holders and records the
// peak, so a test can assert the engine never runs more rows at once than the gate allows.
type countingGate struct {
	slots    chan struct{}
	mu       sync.Mutex
	cur, max int
}

func newCountingGate(limit int) *countingGate { return &countingGate{slots: make(chan struct{}, limit)} }

func (g *countingGate) Acquire(ctx context.Context) bool {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	g.mu.Lock()
	g.cur++
	if g.cur > g.max {
		g.max = g.cur
	}
	g.mu.Unlock()
	return true
}

func (g *countingGate) Release() {
	g.mu.Lock()
	g.cur--
	g.mu.Unlock()
	<-g.slots
}

func (g *countingGate) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

// rejectGate always refuses a slot (as if the job were cancelled before any slot freed).
type rejectGate struct{}

func (rejectGate) Acquire(context.Context) bool { return false }
func (rejectGate) Release()                      {}

// ---- tests ----

// The global gate caps concurrent rows below the job's own worker-pool size: a job with
// Concurrency 4 but a gate limit of 2 runs at most 2 rows at once.
func TestEngineGateCapsBelowConcurrency(t *testing.T) {
	st := seedStore(6)
	gate := newCountingGate(2)
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		time.Sleep(10 * time.Millisecond)
		return RunResult{Status: Ok}, nil
	})
	eng := &Engine{Store: st, Backoff: noBackoff, Gate: gate}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 4}, prov)

	if p := gate.peak(); p > 2 {
		t.Fatalf("peak concurrent rows = %d, want <= 2 (the gate limit)", p)
	}
	for _, it := range st.items {
		if it.status != "done" || it.outcome != Ok {
			t.Fatalf("item %d: status=%s outcome=%v, want done/Ok", it.id, it.status, it.outcome)
		}
	}
}

// A row must not be marked running until it holds a gate slot: with a gate that refuses
// every slot, no item is ever started and the provider is never called (they stay queued,
// resumable later). This pins the acquire-before-StartItem ordering — the core fix.
func TestEngineGateGuardsStartItem(t *testing.T) {
	st := seedStore(3)
	var runs int32
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		atomic.AddInt32(&runs, 1)
		return RunResult{Status: Ok}, nil
	})
	eng := &Engine{Store: st, Backoff: noBackoff, Gate: rejectGate{}}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 2}, prov)

	if n := atomic.LoadInt32(&runs); n != 0 {
		t.Fatalf("provider ran %d times, want 0 (no slot was ever granted)", n)
	}
	for _, it := range st.items {
		if it.status != "queued" {
			t.Fatalf("item %d status=%q, want queued (never started without a slot)", it.id, it.status)
		}
	}
}

// The worker pool must run at most Concurrency items simultaneously.
func TestEngineRespectsConcurrency(t *testing.T) {
	const conc = 3
	st := seedStore(6)
	started := make(chan struct{}, 100)
	release := make(chan struct{})
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		started <- struct{}{}
		<-release
		return RunResult{Status: Ok}, nil
	})
	eng := &Engine{Store: st, Backoff: noBackoff}
	done := make(chan struct{})
	go func() { eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: conc}, prov); close(done) }()

	for i := 0; i < conc; i++ {
		<-started
	}
	select {
	case <-started:
		t.Fatal("more than Concurrency items ran at once")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	if !st.jobFinished || st.jobCancelled {
		t.Errorf("job should finish normally: finished=%v cancelled=%v", st.jobFinished, st.jobCancelled)
	}
	for _, it := range st.items {
		if it.status != "done" || it.outcome != Ok {
			t.Errorf("item %d: status=%s outcome=%v, want done/Ok", it.id, it.status, it.outcome)
		}
	}
}

// A transient error is retried up to MaxRetries; a later success is the outcome.
func TestEngineRetriesTransient(t *testing.T) {
	st := seedStore(1)
	var calls int32
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		if atomic.AddInt32(&calls, 1) < 3 {
			return RunResult{}, transientErr("boom")
		}
		return RunResult{Status: Ok, RunID: "r"}, nil
	})
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 1, MaxRetries: 3}, prov)

	it := st.items[0]
	if it.outcome != Ok {
		t.Errorf("outcome = %v, want Ok", it.outcome)
	}
	if it.attempts != 3 {
		t.Errorf("attempts = %d, want 3", it.attempts)
	}
}

// A permanent error is not retried; the item fails after one attempt.
func TestEnginePermanentNoRetry(t *testing.T) {
	st := seedStore(1)
	var calls int32
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		atomic.AddInt32(&calls, 1)
		return RunResult{}, permanentErr("nope")
	})
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 1, MaxRetries: 3}, prov)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider called %d times, want 1", got)
	}
	it := st.items[0]
	if it.outcome != Failed || it.attempts != 1 || it.detail != "nope" {
		t.Errorf("item = {outcome:%v attempts:%d detail:%q}, want Failed/1/nope", it.outcome, it.attempts, it.detail)
	}
}

// Exhausting retries on a transient error lands the item in Failed.
func TestEngineTransientExhaustedFails(t *testing.T) {
	st := seedStore(1)
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		return RunResult{}, transientErr("always down")
	})
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 1, MaxRetries: 2}, prov)

	it := st.items[0]
	if it.outcome != Failed {
		t.Errorf("outcome = %v, want Failed", it.outcome)
	}
	if it.attempts != 3 { // 1 initial + 2 retries
		t.Errorf("attempts = %d, want 3", it.attempts)
	}
}

// A cancelled job dispatches nothing new and is marked cancelled; unstarted items
// stay queued (so a later re-run resumes them).
func TestEngineCancelledDispatchesNothing(t *testing.T) {
	st := seedStore(3)
	st.cancelled = true
	var calls int32
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		atomic.AddInt32(&calls, 1)
		return RunResult{Status: Ok}, nil
	})
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 2}, prov)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("provider called %d times on a cancelled job, want 0", got)
	}
	if !st.jobCancelled {
		t.Error("job should be marked cancelled")
	}
	for _, it := range st.items {
		if it.status != "queued" {
			t.Errorf("item %d status=%s, want queued", it.id, it.status)
		}
	}
}

// Only queued items are processed; already-done items are left alone (resume-safe).
func TestEngineProcessesOnlyQueued(t *testing.T) {
	st := seedStore(3)
	st.items[0].status = "done"
	st.items[0].outcome = Ok
	var codes sync.Map
	prov := providerFunc(func(ctx context.Context, inputs map[string]string) (RunResult, error) {
		codes.Store(inputs["code"], true)
		return RunResult{Status: Ok}, nil
	})
	// give the two queued items distinct inputs to observe which ran
	st.items[1].inputs = map[string]string{"code": "b"}
	st.items[2].inputs = map[string]string{"code": "c"}
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 2}, prov)

	if _, ran := codes.Load("x"); ran {
		t.Error("the already-done item must not be reprocessed")
	}
	if _, ok := codes.Load("b"); !ok {
		t.Error("queued item b was not processed")
	}
	if _, ok := codes.Load("c"); !ok {
		t.Error("queued item c was not processed")
	}
}

// A cancel that arrives while the worker pool is full (the next row waiting on a
// free worker) must not dispatch that next row.
func TestEngineCancelMidRunStopsDispatch(t *testing.T) {
	st := seedStore(3)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	prov := providerFunc(func(ctx context.Context, _ map[string]string) (RunResult, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			st.mu.Lock()
			st.cancelled = true // cancel arrives while the first row holds the only worker
			st.mu.Unlock()
			close(started)
			<-release // hold the slot so the loop blocks acquiring it for the next row
		}
		return RunResult{Status: Ok}, nil
	})
	go func() {
		<-started
		close(release)
	}()
	eng := &Engine{Store: st, Backoff: noBackoff}
	eng.RunJob(context.Background(), JobSpec{JobID: 1, Concurrency: 1}, prov)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider called %d times, want 1 (no new dispatch after mid-run cancel)", got)
	}
	if !st.jobCancelled {
		t.Error("job should be marked cancelled")
	}
}
