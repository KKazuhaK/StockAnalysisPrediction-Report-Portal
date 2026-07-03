package app

import (
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// runAtDue: empty and past/malformed run_at are due; a future run_at is not.
func TestRunAtDue(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local)
	cases := []struct {
		runAt string
		due   bool
	}{
		{"", true},                     // unscheduled → run ASAP
		{"2026-07-04 11:59:59", true},  // past
		{"2026-07-04 12:00:00", true},  // exactly now
		{"2026-07-04 12:00:01", false}, // future
		{"not-a-timestamp", true},      // malformed → don't strand the job
	}
	for _, c := range cases {
		if got := runAtDue(c.runAt, now); got != c.due {
			t.Errorf("runAtDue(%q) = %v, want %v", c.runAt, got, c.due)
		}
	}
}

// normalizeRunAt accepts the canonical local format or RFC3339 and returns the
// canonical form; garbage is rejected.
func TestNormalizeRunAt(t *testing.T) {
	if v, ok := normalizeRunAt("2026-07-04 09:00:00"); !ok || v != "2026-07-04 09:00:00" {
		t.Errorf("canonical: got %q ok=%v", v, ok)
	}
	if v, ok := normalizeRunAt("garbage"); ok || v != "" {
		t.Errorf("garbage should be rejected, got %q ok=%v", v, ok)
	}
	// RFC3339 is accepted and reduced to the canonical local basis.
	if v, ok := normalizeRunAt("2026-07-04T09:00:00Z"); !ok || v == "" {
		t.Errorf("RFC3339 should be accepted, got %q ok=%v", v, ok)
	}
}

// A due 定时 job ages from its run_at (its arrival time), not from created_at, so
// it takes a fair position instead of jumping ahead of later immediate submissions.
func TestScheduledJobAgesFromRunAt(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	jid, err := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"x": "1"}}, "normal")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const past = "2000-01-01 00:00:00" // due (far past), but far from created_at (~now)
	st.ScheduleJob(jid, past)

	items := (&Server{st: st}).queuedItems()
	if len(items) != 1 {
		t.Fatalf("want 1 due item, got %d", len(items))
	}
	// SchedKey must be derived from run_at, not the (near-now) created_at.
	want := queue.DefaultRegistry().SchedKey("normal", parseEnqueueUnix(past))
	if items[0].SchedKey != want {
		t.Fatalf("SchedKey = %d, want %d (aged from run_at, not created_at)", items[0].SchedKey, want)
	}
}

// queuedItems hides a job whose run_at is still in the future, so the scheduler
// (and the "N ahead" count) never sees a not-yet-due 定时 job.
func TestQueuedItemsHidesFutureScheduledJobs(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	due, err := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"x": "1"}}, "normal")
	if err != nil {
		t.Fatalf("create due: %v", err)
	}
	future, err := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"x": "2"}}, "normal")
	if err != nil {
		t.Fatalf("create future: %v", err)
	}
	st.ScheduleJob(due, time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05"))   // past → due
	st.ScheduleJob(future, time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")) // future → hidden

	items := (&Server{st: st}).queuedItems()
	if len(items) != 1 || items[0].ID != due {
		t.Fatalf("queuedItems = %+v, want only the due job %d", items, due)
	}
}

// A one-shot schedule (定时运行) is stored as a job_schedule row on an ordinary
// queued job: run_at round-trips through GetBatchJob and QueuedJobs, reschedules,
// clears cleanly, and DeleteBatchJob removes the job plus its side rows.
// See docs/adr/0007-run-analysis-and-scheduling.md.
func TestJobScheduleRoundTripAndDelete(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	jobID, err := st.CreateBatchJob(tgt, 1, 0, "kazuha", []map[string]string{{"symbol": "600519"}}, "normal")
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}

	// An unscheduled job reads back with empty run_at.
	if j, ok := st.GetBatchJob(jobID); !ok || j.RunAt != "" {
		t.Fatalf("fresh job RunAt = %q (ok=%v), want empty", j.RunAt, ok)
	}

	// Schedule → run_at surfaces in both GetBatchJob and QueuedJobs.
	const runAt = "2999-01-02 09:00:00"
	if err := st.ScheduleJob(jobID, runAt); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if j, _ := st.GetBatchJob(jobID); j.RunAt != runAt {
		t.Fatalf("GetBatchJob RunAt = %q, want %q", j.RunAt, runAt)
	}
	q := st.QueuedJobs()
	if len(q) != 1 || q[0].ID != jobID || q[0].RunAt != runAt {
		t.Fatalf("QueuedJobs = %+v, want one job %d with run_at %q", q, jobID, runAt)
	}

	// Reschedule overwrites; clearing (empty run_at) removes the row.
	if err := st.ScheduleJob(jobID, "2999-06-06 06:06:06"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if j, _ := st.GetBatchJob(jobID); j.RunAt != "2999-06-06 06:06:06" {
		t.Fatalf("after reschedule RunAt = %q", j.RunAt)
	}
	if err := st.ScheduleJob(jobID, ""); err != nil {
		t.Fatalf("clear schedule: %v", err)
	}
	if j, _ := st.GetBatchJob(jobID); j.RunAt != "" {
		t.Fatalf("after clear RunAt = %q, want empty", j.RunAt)
	}

	// Delete removes the job entirely (and leaves the queue empty).
	if err := st.DeleteBatchJob(jobID); err != nil {
		t.Fatalf("DeleteBatchJob: %v", err)
	}
	if _, ok := st.GetBatchJob(jobID); ok {
		t.Fatal("job still present after DeleteBatchJob")
	}
	if len(st.QueuedJobs()) != 0 {
		t.Fatalf("QueuedJobs not empty after delete: %+v", st.QueuedJobs())
	}
}
