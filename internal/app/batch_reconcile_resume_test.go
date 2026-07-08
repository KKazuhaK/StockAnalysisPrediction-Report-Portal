package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// reconcileProv implements both Provider.Run and Reconciler.Reconcile with counters, so a test can
// assert a resumed / manually-reconciled run is SETTLED by reconcile and NEVER re-Run (the
// restart-durable half of the reconcile-not-retry money invariant, ADR 0006).
type reconcileProv struct {
	runs, recs *int32
	status     batch.Outcome
}

func (p reconcileProv) Run(_ context.Context, _ map[string]string) (batch.RunResult, error) {
	atomic.AddInt32(p.runs, 1)
	return batch.RunResult{Status: batch.Ok, RunID: "fresh-run"}, nil
}

func (p reconcileProv) Reconcile(_ context.Context, runID, _ string) (batch.RunResult, error) {
	atomic.AddInt32(p.recs, 1)
	return batch.RunResult{Status: p.status, RunID: runID, Detail: "reconciled"}, nil
}

// A crash-orphaned run splits by whether its Dify handle was persisted: the one with a run id is
// left for reconcile; the one without is requeued for a fresh run. Persisted handles survive and
// are exposed for the details view.
func TestSaveDifyRefAndResetScoping(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 2, 0, "admin", []map[string]string{{"c": "a"}, {"c": "b"}}, "normal")
	st.MarkJobRunning(job)
	items := st.BatchJobItems(job)
	st.StartItem(items[0].ID)
	st.StartItem(items[1].ID)
	// item0 captured a run id mid-stream; item1 crashed before any id was emitted.
	if err := st.SaveItemDifyRef(items[0].ID, "run-1", "", "task-1"); err != nil {
		t.Fatalf("SaveItemDifyRef: %v", err)
	}

	refs := st.ResumableInFlightItems()
	if len(refs) != 1 || refs[0].ItemID != items[0].ID || refs[0].RunID != "run-1" {
		t.Fatalf("ResumableInFlightItems = %+v, want one ref for item0 with run-1", refs)
	}

	if err := st.ResetInFlightItems(); err != nil {
		t.Fatalf("ResetInFlightItems: %v", err)
	}
	q, _ := st.QueuedItems(job)
	if len(q) != 1 || q[0].ID != items[1].ID {
		t.Fatalf("queued after reset = %+v, want only the id-less item1", q)
	}

	var it0 BatchItem
	for _, it := range st.BatchJobItems(job) {
		if it.ID == items[0].ID {
			it0 = it
		}
	}
	if it0.Status != "running" || it0.RunID != "run-1" || it0.TaskID != "task-1" || it0.ConvID != "" {
		t.Fatalf("item0 = %+v, want running with run-1 / task-1 (kept for reconcile)", it0)
	}
}

// The core money invariant across a restart: an item that started on Dify (its run id was persisted
// before the crash) is RECONCILED to its true outcome on resume, and the workflow is never re-run.
func TestResumeReconcilesStartedRunNoRerun(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"c": "a"}}, "normal")
	st.MarkJobRunning(job)
	items := st.BatchJobItems(job)
	st.StartItem(items[0].ID)
	st.SaveItemDifyRef(items[0].ID, "run-1", "", "task-1")

	var runs, recs int32
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return reconcileProv{runs: &runs, recs: &recs, status: batch.Ok}, nil
	}

	srv.resumeBatchJobs()

	waitFor(t, "job finished via reconcile", func() bool {
		j, ok := st.GetBatchJob(job)
		return ok && j.Status == "finished"
	})
	if got := atomic.LoadInt32(&recs); got != 1 {
		t.Errorf("Reconcile called %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Errorf("Run called %d times, want 0 — a started run must never be re-run", got)
	}
	if its := st.BatchJobItems(job); its[0].Status != "succeeded" {
		t.Errorf("item status = %q, want succeeded (the reconciled outcome)", its[0].Status)
	}
}

// The admin's manual Reconcile settles a row that looks failed in the portal but actually finished
// on Dify — by reconciling its persisted handle, without re-running.
func TestManualReconcileEndpoint(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"c": "a"}}, "normal")
	st.MarkJobRunning(job)
	items := st.BatchJobItems(job)
	st.StartItem(items[0].ID)
	st.SaveItemDifyRef(items[0].ID, "run-9", "", "task-9")
	st.FinishItem(items[0].ID, batch.Failed, 1, "run-9", "stream ended before workflow_finished")

	var runs, recs int32
	srv := &Server{st: st}
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return reconcileProv{runs: &runs, recs: &recs, status: batch.Ok}, nil
	}

	rec := httptest.NewRecorder()
	idStr := strconv.FormatInt(items[0].ID, 10)
	req := httptest.NewRequest("POST", "/api/admin/batch/items/"+idStr+"/reconcile", nil)
	req.SetPathValue("id", idStr)
	srv.apiBatchItemReconcile(rec, req, "admin")

	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile → %d: %s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&recs) != 1 || atomic.LoadInt32(&runs) != 0 {
		t.Errorf("recs=%d runs=%d, want recs=1 runs=0 (reconcile, not re-run)", recs, runs)
	}
	if its := st.BatchJobItems(job); its[0].Status != "succeeded" {
		t.Errorf("after manual reconcile status = %q, want succeeded", its[0].Status)
	}
}

// An item with no persisted handle can't be manually reconciled — the endpoint refuses rather than
// silently re-running (which would risk a duplicate charged run).
func TestManualReconcileRefusesNoHandle(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	job, _ := st.CreateBatchJob(tgt, 1, 0, "admin", []map[string]string{{"c": "a"}}, "normal")
	items := st.BatchJobItems(job)
	st.FinishItem(items[0].ID, batch.Failed, 1, "", "never started")

	srv := &Server{st: st}
	rec := httptest.NewRecorder()
	idStr := strconv.FormatInt(items[0].ID, 10)
	req := httptest.NewRequest("POST", "/api/admin/batch/items/"+idStr+"/reconcile", nil)
	req.SetPathValue("id", idStr)
	srv.apiBatchItemReconcile(rec, req, "admin")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reconcile with no handle → %d, want 400", rec.Code)
	}
}
