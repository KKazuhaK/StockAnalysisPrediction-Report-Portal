package app

import (
	"encoding/json"
	"log"
	"strconv"
	"time"
)

// Recurring-task scheduler (scheduled tasks; docs/adr/0018-recurring-tasks.md): the third always-on
// ticker (after scheduleLoop, ADR 0007, and cleanupLoop, ADR 0017). A recurring task is a
// time-triggered producer — on its daily/weekly/monthly cadence this loop creates an ordinary queued
// batch job from the task's saved rows template and hands it to the one run queue, which owns
// execution unchanged. It adds no concurrency gate: a fired task competes for a slot like any other
// run (ADR 0011).

// recurringLoop checks once a minute whether any enabled task's cadence is due. It runs for the
// process lifetime. Under continuous operation the 60s cadence bounds fire lateness; after an outage
// spanning the scheduled time, the same-day period still fires once (as late as the outage lasted),
// and a whole missed period is skipped, never backfilled.
func (s *Server) recurringLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		s.recurringTick()
	}
}

// recurringTick fires every enabled task whose cadence is due. It stamps the period BEFORE firing
// (MarkRecurringFired) so a crash or a slow fire can't let the ticker re-fire the same period.
func (s *Server) recurringTick() {
	now := time.Now()
	loc := s.panelLocation()
	for _, task := range s.st.EnabledRecurringTasks() {
		due, stamp := cadenceDue(task.Freq, task.AtTime, task.Weekday, task.Monthday, task.LastFired, now, loc)
		if !due {
			continue
		}
		// Fail closed: last_fired is the SOLE idempotency guard, so only fire once the stamp is
		// durably recorded. If the write fails (a transient DB error), skip this period rather than
		// risk an unguarded duplicate paid run — the next tick retries, consistent with no-backfill.
		if err := s.st.MarkRecurringFired(task.ID, stamp); err != nil {
			log.Printf("recurring task %d (%q): stamping last_fired failed, not firing: %v", task.ID, task.Name, err)
			continue
		}
		s.fireRecurringTask(task)
	}
}

// fireRecurringTask creates one batch job from the task's template and records the fire. It is used
// by both the cadence tick and the manual "run now" action, so it does no cadence/period bookkeeping
// itself. Returns (0, nil) when there is genuinely nothing to run (empty template or a missing
// target — both logged-and-skipped, never a hard failure that would strand the loop) and
// (0, err) when the job could not be created (a real internal error the caller should surface).
func (s *Server) fireRecurringTask(task RecurringTask) (int64, error) {
	var rows []map[string]string
	if task.Rows != "" {
		json.Unmarshal([]byte(task.Rows), &rows)
	}
	if len(rows) == 0 {
		log.Printf("recurring task %d (%q): empty template, skipped", task.ID, task.Name)
		return 0, nil
	}
	if _, ok := s.st.GetTarget(task.TargetID); !ok {
		log.Printf("recurring task %d (%q): target %d missing, skipped", task.ID, task.Name, task.TargetID)
		return 0, nil
	}
	// Never urgent (ADR 0018 §4): 'idle' runs in the bottom lane; otherwise resolve the creator's
	// group-default base priority at fire time so the task competes as its owner normally would.
	priority := "idle"
	if task.Priority != "idle" {
		priority = strconv.Itoa(s.resolveBasePriority(task.CreatedBy))
	}
	conc := s.clampConcurrency(task.Concurrency)
	maxRetries := task.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	jobID, err := s.st.CreateBatchJob(task.TargetID, conc, maxRetries, task.CreatedBy, rows, priority)
	if err != nil {
		log.Printf("recurring task %d (%q): create job failed: %v", task.ID, task.Name, err)
		return 0, err
	}
	s.st.InsertRecurringRun(task.ID, jobID)
	s.scheduleTick() // admit now if the budget allows, else it waits in the queue
	log.Printf("recurring task %d (%q): fired job %d (%d row(s), priority %s)", task.ID, task.Name, jobID, len(rows), priority)
	return jobID, nil
}
