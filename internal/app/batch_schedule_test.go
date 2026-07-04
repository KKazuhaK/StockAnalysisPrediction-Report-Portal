package app

import (
	"math"
	"testing"
	"time"
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

// jobFactors normalizes base/age/fair; with the default 24h age window a 12h wait is
// age 0.5, base 50 is 0.5, and an unseen user is fair 1.
func TestJobFactorsNormalization(t *testing.T) {
	srv := &Server{st: newTestStore(t)}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local)
	j := BatchJob{ID: 1, Priority: "50", CreatedBy: "u",
		CreatedAt: now.Add(-12 * time.Hour).Format("2006-01-02 15:04:05")}
	f := srv.jobFactors(j, now, map[string]float64{})
	if f.Base != 0.5 {
		t.Errorf("base = %v, want 0.5", f.Base)
	}
	if math.Abs(f.Age-0.5) > 1e-9 {
		t.Errorf("age = %v, want 0.5", f.Age)
	}
	if f.Fair != 1 {
		t.Errorf("fair = %v, want 1 (no recent usage)", f.Fair)
	}
	if f.Urgent {
		t.Error("urgent should be false for a numeric base")
	}
}

// A due 定时 job ages from its run_at (its arrival time), not from created_at, so it
// takes a fair position instead of jumping ahead of later immediate submissions.
func TestJobFactorsAgesFromRunAt(t *testing.T) {
	srv := &Server{st: newTestStore(t)}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local)
	// Created just now, but scheduled 48h in the past → aging from run_at saturates the
	// age factor to 1 (aging from the near-now created_at would give ~0).
	j := BatchJob{ID: 1, Priority: "50", CreatedBy: "u",
		CreatedAt: now.Format("2006-01-02 15:04:05"),
		RunAt:     now.Add(-48 * time.Hour).Format("2006-01-02 15:04:05")}
	if f := srv.jobFactors(j, now, nil); f.Age != 1 {
		t.Fatalf("age = %v, want 1 (aged from run_at, not created_at)", f.Age)
	}
}

// A heavier recent user has a lower fair factor: userUsage decays each of their runs
// and jobFactors turns the tally into 2^(-usage), so more recent runs → smaller fair.
func TestFairShareFavoursLighterUser(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	// "heavy" submits three runs; "light" none.
	for i := 0; i < 3; i++ {
		if _, err := st.CreateBatchJob(tgt, 1, 0, "heavy", []map[string]string{{"x": "1"}}, "50"); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	srv := &Server{st: st}
	now := time.Now()
	usage := srv.userUsage(now)
	j := BatchJob{Priority: "50", CreatedAt: now.Format("2006-01-02 15:04:05")}

	heavy := srv.jobFactors(func() BatchJob { j.CreatedBy = "heavy"; return j }(), now, usage).Fair
	light := srv.jobFactors(func() BatchJob { j.CreatedBy = "light"; return j }(), now, usage).Fair
	if !(light > heavy) {
		t.Fatalf("light user fair (%v) should exceed heavy user fair (%v)", light, heavy)
	}
	if light != 1 {
		t.Errorf("an unseen user should be fair 1, got %v", light)
	}
}

// queuedItems hides a job whose run_at is still in the future, so the scheduler
// (and the "N ahead" count) never sees a not-yet-due 定时 job.
func TestQueuedItemsHidesFutureScheduledJobs(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	due, err := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"x": "1"}}, "50")
	if err != nil {
		t.Fatalf("create due: %v", err)
	}
	future, err := st.CreateBatchJob(tgt, 1, 0, "u", []map[string]string{{"x": "2"}}, "50")
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
	jobID, err := st.CreateBatchJob(tgt, 1, 0, "kazuha", []map[string]string{{"symbol": "600519"}}, "50")
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
