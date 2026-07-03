package app

import "testing"

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
