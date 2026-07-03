package app

import "testing"

// The queue-layer store contract: jobs are created 'queued' with a priority,
// MarkJobRunning is an atomic single-winner transition, and cancelling a still-
// queued job cancels it outright.
func TestQueueStoreContract(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	rows := []map[string]string{{"code": "a"}}

	j1, _ := st.CreateBatchJob(tgt, 1, 0, "op", rows, "normal")
	j2, _ := st.CreateBatchJob(tgt, 1, 0, "op", rows, "urgent")

	// Both start queued; priority round-trips through the join.
	if q := st.QueuedJobs(); len(q) != 2 {
		t.Fatalf("QueuedJobs = %d, want 2", len(q))
	}
	if st.RunningJobCount() != 0 {
		t.Fatalf("RunningJobCount = %d, want 0", st.RunningJobCount())
	}
	if jb, _ := st.GetBatchJob(j2); jb.Priority != "urgent" || jb.Status != "queued" {
		t.Fatalf("j2 = {priority:%q status:%q}, want urgent/queued", jb.Priority, jb.Status)
	}

	// MarkJobRunning is atomic: exactly one caller wins.
	if !st.MarkJobRunning(j1) {
		t.Fatal("first MarkJobRunning should win")
	}
	if st.MarkJobRunning(j1) {
		t.Fatal("second MarkJobRunning on the same job must fail")
	}
	if st.RunningJobCount() != 1 {
		t.Fatalf("RunningJobCount after admit = %d, want 1", st.RunningJobCount())
	}
	if q := st.QueuedJobs(); len(q) != 1 || q[0].ID != j2 {
		t.Fatalf("QueuedJobs after admit = %v, want just [%d]", q, j2)
	}

	// Re-prioritise updates the stored level.
	if err := st.SetJobPriority(j2, "other"); err != nil {
		t.Fatalf("SetJobPriority: %v", err)
	}
	if jb, _ := st.GetBatchJob(j2); jb.Priority != "other" {
		t.Fatalf("j2 priority after reprioritise = %q, want other", jb.Priority)
	}

	// Cancelling a still-queued job cancels it outright (never runs).
	if err := st.CancelBatchJob(j2); err != nil {
		t.Fatalf("CancelBatchJob: %v", err)
	}
	if jb, _ := st.GetBatchJob(j2); jb.Status != "cancelled" {
		t.Fatalf("cancelled queued job status = %q, want cancelled", jb.Status)
	}
	if len(st.QueuedJobs()) != 0 {
		t.Fatal("no jobs should remain queued after admit + cancel")
	}
}
