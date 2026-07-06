package app

import (
	"context"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// fakeProv adapts a function to batch.Provider for store-integration tests.
type fakeProv struct {
	fn func(map[string]string) (batch.RunResult, error)
}

func (p fakeProv) Run(_ context.Context, in map[string]string) (batch.RunResult, error) {
	return p.fn(in)
}

// driveJobSync runs a job's queued items to completion through the store, the way the
// run-level scheduler + startItem do (docs/adr/0011), but synchronously and in order for
// deterministic store-integration tests. A cancelling/cancelled job dispatches nothing.
func driveJobSync(st *Store, jobID int64, prov batch.Provider, maxRetries int) {
	if c, _ := st.Cancelled(jobID); c {
		st.FinishJob(jobID, true)
		return
	}
	st.MarkJobRunning(jobID)
	items, _ := st.QueuedItems(jobID)
	for _, it := range items {
		st.MarkItemRunning(it.ID)
		res, attempts := batch.RunItem(context.Background(), prov, it.Inputs, maxRetries, func(int) time.Duration { return 0 }, nil)
		st.FinishItem(it.ID, res.Status, attempts, res.RunID, res.Detail)
	}
	cancelled, _ := st.Cancelled(jobID)
	st.FinishJob(jobID, cancelled)
}

func seedTarget(t *testing.T, st *Store) int64 {
	t.Helper()
	if err := st.UpsertPlugin("dify", "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	id, err := st.CreateTarget("dify", "My Workflow", `{"base_url":"x","api_key":"k"}`)
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return id
}

// End-to-end: create a job, drive it with the real engine over the real Store,
// and assert per-item state plus the final aggregate counts.
func TestBatchJobLifecycleWithEngine(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	rows := []map[string]string{{"code": "a"}, {"code": "b"}, {"code": "c"}}
	job, err := st.CreateBatchJob(tgt, 2, 1, "admin", rows, "normal")
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}

	prov := fakeProv{fn: func(in map[string]string) (batch.RunResult, error) {
		if in["code"] == "b" {
			return batch.RunResult{Status: batch.Failed, Detail: "bad code"}, nil
		}
		return batch.RunResult{Status: batch.Ok, RunID: "run-" + in["code"]}, nil
	}}
	driveJobSync(st, job, prov, 1)

	j, ok := st.GetBatchJob(job)
	if !ok {
		t.Fatal("job vanished")
	}
	if j.Status != "finished" {
		t.Errorf("status = %q, want finished", j.Status)
	}
	if j.Total != 3 || j.Succeeded != 2 || j.Failed != 1 || j.Partial != 0 {
		t.Errorf("counts = total:%d ok:%d fail:%d partial:%d, want 3/2/1/0", j.Total, j.Succeeded, j.Failed, j.Partial)
	}
	for _, it := range st.BatchJobItems(job) {
		switch {
		case it.Inputs == `{"code":"b"}`:
			if it.Status != "failed" || it.Error != "bad code" {
				t.Errorf("item b = {status:%s error:%q}, want failed/bad code", it.Status, it.Error)
			}
		default:
			if it.Status != "succeeded" {
				t.Errorf("item %s status=%s, want succeeded", it.Inputs, it.Status)
			}
		}
	}
}

// Crash recovery: items left running are requeued and the job is resumable.
func TestResetInFlightItemsRequeues(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"code": "a"}, {"code": "b"}}, "normal")
	st.MarkJobRunning(job) // the job had been admitted (running) before the crash
	// simulate a crash mid-run: one item stuck running
	items := st.BatchJobItems(job)
	st.StartItem(items[0].ID)

	if err := st.ResetInFlightItems(); err != nil {
		t.Fatalf("ResetInFlightItems: %v", err)
	}
	q, _ := st.QueuedItems(job)
	if len(q) != 2 {
		t.Errorf("queued after reset = %d, want 2 (both items resumable)", len(q))
	}
	ids := st.ResumableJobIDs()
	if len(ids) != 1 || ids[0] != job {
		t.Errorf("ResumableJobIDs = %v, want [%d]", ids, job)
	}
}

// A cancelled job stops dispatching and unstarted rows stay queued.
func TestCancelBatchJobStopsDispatch(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"code": "a"}, {"code": "b"}, {"code": "c"}}, "normal")
	if err := st.CancelBatchJob(job); err != nil {
		t.Fatalf("CancelBatchJob: %v", err)
	}

	var calls int
	prov := fakeProv{fn: func(map[string]string) (batch.RunResult, error) {
		calls++
		return batch.RunResult{Status: batch.Ok}, nil
	}}
	driveJobSync(st, job, prov, 0)

	if calls != 0 {
		t.Errorf("provider called %d times on a cancelled job, want 0", calls)
	}
	j, _ := st.GetBatchJob(job)
	if j.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", j.Status)
	}
	q, _ := st.QueuedItems(job)
	if len(q) != 3 {
		t.Errorf("queued = %d, want 3 (nothing dispatched)", len(q))
	}
}

// Per-row cancel: a queued row can be cancelled (skipped) but a running one can't be
// queued-cancelled; LiveJobCounts tallies the new 'cancelled' status.
func TestCancelQueuedItemAndCounts(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"c": "a"}, {"c": "b"}}, "normal")
	items := st.BatchJobItems(job)

	if !st.CancelQueuedItem(items[0].ID) {
		t.Fatal("CancelQueuedItem should cancel a queued row")
	}
	if st.CancelQueuedItem(items[0].ID) {
		t.Fatal("CancelQueuedItem should return false the second time (already cancelled)")
	}
	st.MarkItemRunning(items[1].ID)
	if st.CancelQueuedItem(items[1].ID) {
		t.Fatal("CancelQueuedItem must not cancel a running row")
	}
	_, running, _, _, _, cancelled := st.LiveJobCounts(job)
	if cancelled != 1 || running != 1 {
		t.Fatalf("counts: cancelled=%d running=%d, want 1/1", cancelled, running)
	}
}

// Retrying failed rows requeues only them and reopens the job.
func TestRequeueFailedOnly(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"code": "a"}, {"code": "b"}}, "normal")
	items := st.BatchJobItems(job)
	st.FinishItem(items[0].ID, batch.Ok, 1, "r", "")
	st.FinishItem(items[1].ID, batch.Failed, 1, "", "boom")
	st.FinishJob(job, false)

	n, err := st.RequeueItems(job, "failed")
	if err != nil {
		t.Fatalf("RequeueItems: %v", err)
	}
	if n != 1 {
		t.Errorf("requeued = %d, want 1", n)
	}
	q, _ := st.QueuedItems(job)
	if len(q) != 1 || q[0].Inputs["code"] != "b" {
		t.Errorf("queued = %v, want only the failed row b", q)
	}
	j, _ := st.GetBatchJob(job)
	if j.Status != "queued" {
		t.Errorf("status after requeue = %q, want queued (re-enters the queue)", j.Status)
	}
}
