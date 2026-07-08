package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// provFn adapts a ctx-aware func to a batch.Provider for scheduler tests.
type provFn func(ctx context.Context, in map[string]string) (batch.RunResult, error)

func (f provFn) Run(ctx context.Context, in map[string]string) (batch.RunResult, error) {
	return f(ctx, in)
}

// waitFor polls cond until true or the deadline, failing the test on timeout.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The run cap is now a true cap on concurrent RUNS across all jobs: with the budget at 2,
// a 2-row batch (concurrency 2) plus two single runs must never run more than 2 at once —
// the exact regression that made the old per-job pools show 4 running against a cap of 3.
func TestSchedulerCapsConcurrentRunsAcrossJobs(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "2")
	st.SetSetting("batch_reserved_slots", "0")
	tgt := seedTarget(t, st)
	st.CreateBatchJob(tgt, 2, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}, {"c": "d"}}, "50") // batch, window 2
	st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "e"}}, "50")
	st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "f"}}, "50")

	var cur, max int32
	release := make(chan struct{})
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return provFn(func(ctx context.Context, _ map[string]string) (batch.RunResult, error) {
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			<-release
			atomic.AddInt32(&cur, -1)
			return batch.RunResult{Status: batch.Ok}, nil
		}), nil
	}

	srv.scheduleTick()
	// Two runs must fill the budget and block; the rest wait for a slot.
	waitFor(t, "2 runs to be concurrently in flight", func() bool { return atomic.LoadInt32(&max) >= 2 })
	if got := st.RunningItemCount(); got > 2 {
		t.Fatalf("RunningItemCount = %d while blocked, want <= 2", got)
	}
	close(release) // let every wave drain

	waitFor(t, "all jobs to finish", func() bool {
		for _, j := range st.ListBatchJobs() {
			if j.Status != "finished" {
				return false
			}
		}
		return true
	})
	if max > 2 {
		t.Fatalf("peak concurrent runs = %d, want <= 2 (the budget) — the cap leaked", max)
	}
	if max < 2 {
		t.Fatalf("peak concurrent runs = %d, want 2 (runs should parallelise up to the budget)", max)
	}
}

// The single gate is priority-ordered: with only one slot free, the higher-priority job's
// run is admitted before a lower-priority job's — something the old plain semaphore gate
// could not guarantee.
func TestSchedulerAdmitsByRunPriority(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "1")
	st.SetSetting("batch_reserved_slots", "0")
	tgt := seedTarget(t, st)
	low, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "low"}}, "10")
	high, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "high"}}, "90")

	release := make(chan struct{})
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return provFn(func(context.Context, map[string]string) (batch.RunResult, error) {
			<-release
			return batch.RunResult{Status: batch.Ok}, nil
		}), nil
	}

	srv.scheduleTick()
	// The one free slot must go to the high-priority job (its run is executing); the
	// low-priority job stays fully queued.
	waitFor(t, "the high-priority run to start", func() bool {
		_, running, _, _, _, _ := st.LiveJobCounts(high)
		return running == 1
	})
	if _, running, _, _, _, _ := st.LiveJobCounts(low); running != 0 {
		t.Fatalf("low-priority job has %d running runs, want 0 (out-prioritised)", running)
	}
	close(release)
}

// A job finalizes on its own once its last run completes: the scheduler + startItem write
// the aggregate counts and terminal status — no explicit FinishJob call needed.
func TestSchedulerFinalizesJob(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "4")
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 3, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}, {"c": "c"}}, "50")

	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return provFn(func(_ context.Context, in map[string]string) (batch.RunResult, error) {
			if in["c"] == "b" {
				return batch.RunResult{Status: batch.Failed, Detail: "bad"}, nil
			}
			return batch.RunResult{Status: batch.Ok}, nil
		}), nil
	}

	srv.scheduleTick()
	waitFor(t, "the job to finish", func() bool {
		j, _ := st.GetBatchJob(job)
		return j.Status == "finished"
	})
	j, _ := st.GetBatchJob(job)
	if j.Total != 3 || j.Succeeded != 2 || j.Failed != 1 {
		t.Fatalf("counts = total:%d ok:%d fail:%d, want 3/2/1", j.Total, j.Succeeded, j.Failed)
	}
}

// Regression: a job cancelled while it has NO in-flight run (its rows parked behind a
// saturated budget) must still finalize — not strand in 'cancelling' forever. Both the
// cancel handler's finalizeJob and the scheduleTick backstop must close it out.
func TestStrandedCancellingJobFinalizes(t *testing.T) {
	setup := func(t *testing.T) (*Server, int64) {
		st := newTestStore(t)
		tgt := seedTarget(t, st)
		job, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}}, "50")
		st.MarkJobRunning(job) // admitted (running)
		items := st.BatchJobItems(job)
		st.FinishItem(items[0].ID, batch.Ok, 1, "r", "") // row a done; row b parked queued, none in-flight
		if err := st.CancelBatchJob(job); err != nil {   // running → cancelling
			t.Fatalf("CancelBatchJob: %v", err)
		}
		return &Server{st: st}, job
	}

	// The cancel handler's immediate finalize.
	t.Run("finalizeJob", func(t *testing.T) {
		srv, job := setup(t)
		srv.finalizeJob(job)
		if j, _ := srv.st.GetBatchJob(job); j.Status != "cancelled" {
			t.Fatalf("status = %q, want cancelled (finalizeJob must close a stranded cancel)", j.Status)
		}
	})

	// The always-on backstop (30s tick / any later run finishing).
	t.Run("scheduleTick backstop", func(t *testing.T) {
		srv, job := setup(t)
		srv.scheduleTick()
		if j, _ := srv.st.GetBatchJob(job); j.Status != "cancelled" {
			t.Fatalf("status = %q, want cancelled (backstop must sweep cancelling jobs)", j.Status)
		}
	})
}

// blockingProv blocks each run until `release` closes or its ctx is cancelled — so a
// test can hold runs in flight and observe per-row cancellation.
func blockingProv(release <-chan struct{}) batch.Provider {
	return provFn(func(ctx context.Context, _ map[string]string) (batch.RunResult, error) {
		select {
		case <-release:
			return batch.RunResult{Status: batch.Ok}, nil
		case <-ctx.Done():
			return batch.RunResult{}, ctx.Err()
		}
	})
}

// Cancelling ONE running row aborts only that run and marks it 'cancelled' (not
// 'failed'); the batch's other runs keep going and it finalizes normally.
func TestCancelRunningRowAbortsOnlyThatRow(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "2")
	st.SetSetting("batch_reserved_slots", "0")
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 2, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}}, "50")

	release := make(chan struct{})
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return blockingProv(release), nil
	}

	srv.scheduleTick()
	waitFor(t, "both rows running", func() bool {
		_, running, _, _, _, _ := st.LiveJobCounts(job)
		return running == 2
	})
	row0 := st.BatchJobItems(job)[0].ID
	srv.cancelRunningItem(row0) // abort just row 0

	waitFor(t, "row 0 cancelled", func() bool { return st.BatchJobItems(job)[0].Status == "cancelled" })
	if s := st.BatchJobItems(job)[1].Status; s != "running" {
		t.Fatalf("row 1 status = %q, want running (only row 0 was cancelled)", s)
	}
	close(release) // let row 1 finish
	waitFor(t, "job finished", func() bool { j, _ := st.GetBatchJob(job); return j.Status == "finished" })
	_, _, succeeded, _, failed, cancelled := st.LiveJobCounts(job)
	if succeeded != 1 || cancelled != 1 || failed != 0 {
		t.Fatalf("counts: ok=%d cancelled=%d failed=%d, want 1/1/0", succeeded, cancelled, failed)
	}
}

// Cancelling a still-queued row skips it — it never runs; the job finalizes with that row
// 'cancelled' and the rest run normally.
func TestCancelQueuedRowSkips(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "1")
	st.SetSetting("batch_reserved_slots", "0")
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}}, "50")

	release := make(chan struct{})
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return blockingProv(release), nil
	}

	srv.scheduleTick() // budget 1 → row 0 runs, row 1 queued
	waitFor(t, "row 0 running", func() bool {
		_, running, _, _, _, _ := st.LiveJobCounts(job)
		return running == 1
	})
	row1 := st.BatchJobItems(job)[1].ID
	if !st.CancelQueuedItem(row1) { // mirrors the endpoint: cancel a queued row
		t.Fatal("CancelQueuedItem(row1) should succeed")
	}
	srv.finalizeJob(job) // no-op while row 0 still runs
	close(release)
	waitFor(t, "job finished", func() bool { j, _ := st.GetBatchJob(job); return j.Status == "finished" })

	its := st.BatchJobItems(job)
	if its[0].Status != "succeeded" || its[1].Status != "cancelled" {
		t.Fatalf("rows = %q/%q, want succeeded/cancelled", its[0].Status, its[1].Status)
	}
}

// A cancelling job contributes no runs to the scheduler — its remaining queued rows are
// never admitted.
func TestItemCandidatesExcludeCancellingJob(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}}, "50")
	if err := st.CancelBatchJob(job); err != nil {
		t.Fatalf("CancelBatchJob: %v", err)
	}
	srv := &Server{st: st}
	cands, _ := srv.itemCandidates()
	if len(cands) != 0 {
		t.Fatalf("itemCandidates = %d, want 0 (a cancelling job offers no runs)", len(cands))
	}
}
