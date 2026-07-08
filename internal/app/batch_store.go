package app

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// This file is the persistence layer for the batch-run feature: plugin/target/job/item
// CRUD that the app-layer run-level scheduler drives. See
// docs/adr/0011-run-level-scheduling.md (item-level scheduling; the batch package is now
// just the stateless per-run trigger).

// ---------- types ----------

type Plugin struct {
	ID                                int64
	Slug, Name, Version, Spec, Source string
	Enabled                           bool
	ImportedAt                        string
}

type BatchTarget struct {
	ID                                int64
	PluginSlug, Name, Config, Created string
}

type BatchJob struct {
	ID, TargetID                                int64
	Status                                      string
	Priority                                    string // queue priority level (see docs/adr/0004-run-queue.md)
	Concurrency, MaxRetries                     int
	Total, Succeeded, Partial, Failed           int
	CreatedBy, CreatedAt, StartedAt, FinishedAt string
	RunAt                                       string // one-shot scheduled start (ADR 0007); "" = run ASAP
}

type BatchItem struct {
	ID, JobID             int64
	RowIndex              int
	Inputs                string
	Status                string
	Attempts              int
	RunID, ConvID, TaskID string // Dify handles, for tracing + manual reconcile + the details view
	Error                 string
	StartedAt, FinishedAt string
}

// itemStatus maps a normalised Outcome to the terminal item status vocabulary.
func itemStatus(o batch.Outcome) string {
	switch o {
	case batch.Ok:
		return "succeeded"
	case batch.Partial:
		return "partial"
	default:
		return "failed"
	}
}

// ---------- job/item persistence (driven by the run-level scheduler, ADR 0011) ----------

func (s *Store) QueuedItems(jobID int64) ([]batch.Item, error) {
	rows, err := s.query(`SELECT id,row_index,inputs FROM batch_items WHERE job_id=? AND status='queued' ORDER BY row_index`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []batch.Item
	for rows.Next() {
		var it batch.Item
		var inputs sql.NullString
		if err := rows.Scan(&it.ID, &it.RowIndex, &inputs); err != nil {
			return nil, err
		}
		it.Inputs = map[string]string{}
		if inputs.Valid && inputs.String != "" {
			json.Unmarshal([]byte(inputs.String), &it.Inputs)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) StartItem(id int64) error {
	_, err := s.exec("UPDATE batch_items SET status='running', started_at=? WHERE id=?", nowStr(), id)
	return err
}

func (s *Store) FinishItem(id int64, st batch.Outcome, attempts int, runID, detail string) error {
	// run_id is written only when non-empty so a finish that carries no id (e.g. a pure-chat run
	// whose result has no workflow run id) never wipes the id already persisted mid-stream.
	_, err := s.exec("UPDATE batch_items SET status=?, attempts=?, run_id=CASE WHEN ?<>'' THEN ? ELSE run_id END, error=?, finished_at=? WHERE id=?",
		itemStatus(st), attempts, runID, runID, detail, nowStr(), id)
	return err
}

// SaveItemDifyRef persists the Dify handles (run / conversation / task id) for an in-flight run the
// moment they stream in, so a crash or restart can reconcile the run by id instead of re-running it
// (the restart-durable half of reconcile-not-retry, ADR 0006). Each id is written only when
// non-empty, so a later event that lacks one never clobbers an already-captured value.
func (s *Store) SaveItemDifyRef(itemID int64, runID, convID, taskID string) error {
	_, err := s.exec(`UPDATE batch_items SET
		run_id=CASE WHEN ?<>'' THEN ? ELSE run_id END,
		conversation_id=CASE WHEN ?<>'' THEN ? ELSE conversation_id END,
		task_id=CASE WHEN ?<>'' THEN ? ELSE task_id END
		WHERE id=?`, runID, runID, convID, convID, taskID, taskID, itemID)
	return err
}

func (s *Store) Cancelled(jobID int64) (bool, error) {
	var status string
	if err := s.queryRow("SELECT status FROM batch_jobs WHERE id=?", jobID).Scan(&status); err != nil {
		return false, err
	}
	return status == "cancelling" || status == "cancelled", nil
}

func (s *Store) FinishJob(jobID int64, cancelled bool) error {
	var total, ok, partial, failed int
	s.queryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='partial'   THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed'    THEN 1 ELSE 0 END),0)
		FROM batch_items WHERE job_id=?`, jobID).Scan(&total, &ok, &partial, &failed)
	status := "finished"
	if cancelled {
		status = "cancelled"
	}
	_, err := s.exec(`UPDATE batch_jobs SET status=?, total=?, succeeded=?, partial=?, failed=?, finished_at=? WHERE id=?`,
		status, total, ok, partial, failed, nowStr(), jobID)
	return err
}

// ---------- crash recovery ----------

// ResetInFlightItems requeues crash-orphaned 'running' items that carry NO reconcilable Dify handle
// (run/conversation id) so a resumed job re-triggers them from scratch — safe because they never
// reached Dify (or crashed in the tiny window before its first event) and report ingest is idempotent
// on uid. Items that DO carry an id are left 'running' for resumeBatchJobs to RECONCILE, not re-run,
// so a run that already started (and cost tokens) is settled by its true outcome, never duplicated.
func (s *Store) ResetInFlightItems() error {
	_, err := s.exec(`UPDATE batch_items SET status='queued'
		WHERE status='running' AND COALESCE(run_id,'')='' AND COALESCE(conversation_id,'')=''`)
	return err
}

// ResumeRef is a crash-orphaned in-flight run that CAN be reconciled: it carries a Dify handle
// (run id and/or conversation id) that was persisted before the crash.
type ResumeRef struct {
	ItemID, JobID int64
	RunID, ConvID string
}

// ResumableInFlightItems returns the 'running' items that carry a reconcilable Dify handle — the
// runs resumeBatchJobs settles by reconciling their true outcome instead of re-running them.
func (s *Store) ResumableInFlightItems() []ResumeRef {
	rows, err := s.query(`SELECT id, job_id, COALESCE(run_id,''), COALESCE(conversation_id,'')
		FROM batch_items WHERE status='running'
		AND (COALESCE(run_id,'')<>'' OR COALESCE(conversation_id,'')<>'')`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ResumeRef
	for rows.Next() {
		var r ResumeRef
		if rows.Scan(&r.ItemID, &r.JobID, &r.RunID, &r.ConvID) == nil {
			out = append(out, r)
		}
	}
	return out
}

// ItemReconcileRef returns one item's persisted Dify handle and current status — the input to a
// manual reconcile (settle the row by its true outcome without re-running). ok=false if absent.
func (s *Store) ItemReconcileRef(itemID int64) (ref ResumeRef, status string, ok bool) {
	var runID, convID, st sql.NullString
	if err := s.queryRow(`SELECT job_id, COALESCE(run_id,''), COALESCE(conversation_id,''), status
		FROM batch_items WHERE id=?`, itemID).Scan(&ref.JobID, &runID, &convID, &st); err != nil {
		return ResumeRef{}, "", false
	}
	ref.ItemID = itemID
	ref.RunID, ref.ConvID, status = runID.String, convID.String, st.String
	return ref, status, true
}

// ResumableJobIDs lists jobs that were mid-flight when the server stopped.
func (s *Store) ResumableJobIDs() []int64 {
	rows, err := s.query("SELECT id FROM batch_jobs WHERE status IN ('running','cancelling') ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// CancellingJobIDs lists jobs mid-cancel (status 'cancelling'), so the scheduler can
// finalize any whose in-flight runs have all drained. SchedulableJobs deliberately
// excludes them (a cancelling job admits no new runs), so the backstop sweeps them here
// — otherwise a job cancelled while it had no in-flight run would strand (ADR 0011).
func (s *Store) CancellingJobIDs() []int64 {
	rows, err := s.query("SELECT id FROM batch_jobs WHERE status='cancelling' ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// ---------- jobs ----------

// CreateBatchJob inserts a queued job (status 'queued', no started_at yet) with the
// given priority and its items. The scheduler admits it to 'running' later, subject
// to the global budget (see docs/adr/0004-run-queue.md).
func (s *Store) CreateBatchJob(targetID int64, concurrency, maxRetries int, createdBy string, rows []map[string]string, priority string) (int64, error) {
	if priority == "" {
		priority = "normal"
	}
	now := nowStr()
	jobID, err := s.insertID(`INSERT INTO batch_jobs(target_id,status,concurrency,max_retries,total,created_by,created_at,started_at,priority)
		VALUES(?,?,?,?,?,?,?,?,?)`, targetID, "queued", concurrency, maxRetries, len(rows), createdBy, now, "", priority)
	if err != nil {
		return 0, err
	}
	for i, row := range rows {
		b, _ := json.Marshal(row)
		if _, err := s.exec(`INSERT INTO batch_items(job_id,row_index,inputs,status) VALUES(?,?,?,'queued')`, jobID, i, string(b)); err != nil {
			return jobID, err
		}
	}
	return jobID, nil
}

// QueuedJobs lists jobs waiting to be admitted (status 'queued'), with their priority,
// submitter, and enqueue time (created_at), for the scheduler and the queue view. The
// submitter feeds the fair-share factor (docs/adr/0008-multifactor-priority.md).
func (s *Store) QueuedJobs() []BatchJob {
	rows, err := s.query(`SELECT b.id, b.priority, COALESCE(b.created_by,''), b.created_at, b.run_at
		FROM batch_jobs b
		WHERE b.status='queued' ORDER BY b.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchJob
	for rows.Next() {
		var j BatchJob
		var priority, createdBy, createdAt, runAt sql.NullString
		rows.Scan(&j.ID, &priority, &createdBy, &createdAt, &runAt)
		j.Status, j.Priority, j.CreatedBy, j.CreatedAt, j.RunAt = "queued", priority.String, createdBy.String, createdAt.String, runAt.String
		out = append(out, j)
	}
	return out
}

// JobActivity is one run's (submitter, created_at) — the raw input to the fair-share
// factor. created_at is the stored local "2006-01-02 15:04:05" string; the caller
// parses and time-decays it (docs/adr/0008-multifactor-priority.md).
type JobActivity struct {
	User, CreatedAt string
}

// RecentJobActivity returns every batch job created at or after `since` (a stored
// "2006-01-02 15:04:05" timestamp), for the fair-share usage tally. The string bound
// sorts correctly because the timestamp format is zero-padded and lexicographic.
func (s *Store) RecentJobActivity(since string) []JobActivity {
	rows, err := s.query(`SELECT COALESCE(created_by,''), COALESCE(created_at,'')
		FROM batch_jobs WHERE created_at>=? ORDER BY created_at`, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []JobActivity
	for rows.Next() {
		var a JobActivity
		if rows.Scan(&a.User, &a.CreatedAt) == nil {
			out = append(out, a)
		}
	}
	return out
}

// RunningJobCount returns how many jobs are currently admitted (status 'running'
// or 'cancelling') — the in-flight count the scheduler weighs against the budget.
func (s *Store) RunningJobCount() int {
	var n int
	s.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE status IN ('running','cancelling')").Scan(&n)
	return n
}

// RunningItemCount returns how many individual rows are executing right now (item
// status 'running') across all jobs — the true count of concurrent runs the global
// run gate caps. This is what "N running at once" means to the user, whereas
// RunningJobCount counts whole jobs (a batch can hold several running rows).
func (s *Store) RunningItemCount() int {
	var n int
	s.queryRow("SELECT COUNT(*) FROM batch_items WHERE status='running'").Scan(&n)
	return n
}

// MarkJobRunning atomically flips a job from 'queued' to 'running' (stamping
// started_at). It returns true only for the caller that won the transition, so two
// concurrent scheduler ticks can never launch the same job twice.
func (s *Store) MarkJobRunning(jobID int64) bool {
	res, err := s.exec("UPDATE batch_jobs SET status='running', started_at=? WHERE id=? AND status='queued'", nowStr(), jobID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// MarkItemRunning atomically flips ONE item (run) from 'queued' to 'running' (stamping
// started_at). It returns true only for the caller that won the transition — the per-run
// admission gate (ADR 0011): the winner takes one of the budget's concurrency slots, and
// two scheduler ticks can never dispatch the same run twice.
func (s *Store) MarkItemRunning(itemID int64) bool {
	res, err := s.exec("UPDATE batch_items SET status='running', started_at=? WHERE id=? AND status='queued'", nowStr(), itemID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// SchedulableJobs lists jobs the run-level scheduler may draw runs from: those still
// queued or already running (a running job keeps contributing rows as earlier ones
// finish). Each carries its target, status, queue priority, producer window
// (concurrency), retry budget, submitter, enqueue time, and one-shot run_at —
// everything itemCandidates and the provider need. Cancelling/cancelled/finished jobs
// are excluded. See docs/adr/0011-run-level-scheduling.md.
func (s *Store) SchedulableJobs() []BatchJob {
	rows, err := s.query(`SELECT b.id, b.target_id, b.status, b.priority,
		b.concurrency, b.max_retries, COALESCE(b.created_by,''), b.created_at, b.run_at
		FROM batch_jobs b
		WHERE b.status IN ('queued','running') ORDER BY b.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchJob
	for rows.Next() {
		var j BatchJob
		var priority, createdBy, createdAt, runAt sql.NullString
		if err := rows.Scan(&j.ID, &j.TargetID, &j.Status, &priority, &j.Concurrency, &j.MaxRetries, &createdBy, &createdAt, &runAt); err != nil {
			continue
		}
		j.Priority, j.CreatedBy, j.CreatedAt, j.RunAt = priority.String, createdBy.String, createdAt.String, runAt.String
		out = append(out, j)
	}
	return out
}

// SetJobPriority changes a job's queue priority (the re-prioritise / jump-the-queue action).
// A no-op if the job no longer exists.
func (s *Store) SetJobPriority(jobID int64, priority string) error {
	_, err := s.exec(`UPDATE batch_jobs SET priority=? WHERE id=?`, priority, jobID)
	return err
}

// ScheduleJob sets a one-shot future start time (scheduled run, ADR 0007). run_at uses the
// same "2006-01-02 15:04:05" local basis as created_at; queuedItems() hides the job until
// run_at passes. An empty run_at clears the schedule (run now).
func (s *Store) ScheduleJob(jobID int64, runAt string) error {
	if runAt == "" {
		return s.ClearSchedule(jobID)
	}
	_, err := s.exec(`UPDATE batch_jobs SET run_at=? WHERE id=?`, runAt, jobID)
	return err
}

// ClearSchedule drops a job's scheduled start (run_at back to the ” run-ASAP sentinel),
// making it eligible on the next tick.
func (s *Store) ClearSchedule(jobID int64) error {
	_, err := s.exec("UPDATE batch_jobs SET run_at='' WHERE id=?", jobID)
	return err
}

// DeleteBatchJob removes a job and its item rows. Intended for terminal jobs
// (finished/cancelled); callers gate on status so a running job is cancelled first. The
// job's priority/run_at are columns on batch_jobs now, so they vanish with the row.
func (s *Store) DeleteBatchJob(jobID int64) error {
	s.exec("DELETE FROM batch_items WHERE job_id=?", jobID)
	_, err := s.exec("DELETE FROM batch_jobs WHERE id=?", jobID)
	return err
}

// DeleteFinishedJobs removes every terminal (finished / cancelled) job and its rows,
// returning how many jobs were cleared. Active jobs (queued/running/cancelling) are
// left untouched.
func (s *Store) DeleteFinishedJobs() int {
	rows, err := s.query("SELECT id FROM batch_jobs WHERE status IN ('finished','cancelled')")
	if err != nil {
		return 0
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.DeleteBatchJob(id)
	}
	return len(ids)
}

// CancelBatchJob cancels a job. A still-queued job is cancelled outright (nothing
// is dispatching it); a running job is asked to stop and its workers observe the
// 'cancelling' status via Cancelled().
func (s *Store) CancelBatchJob(jobID int64) error {
	res, err := s.exec("UPDATE batch_jobs SET status='cancelled', finished_at=? WHERE id=? AND status='queued'", nowStr(), jobID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
	}
	_, err = s.exec("UPDATE batch_jobs SET status='cancelling' WHERE id=? AND status='running'", jobID)
	return err
}

// RequeueItems moves finished items of the given statuses back to queued and marks
// the job running again, so the engine can resume just those rows. Defaults to
// 'failed' when no statuses are given.
func (s *Store) RequeueItems(jobID int64, statuses ...string) (int, error) {
	if len(statuses) == 0 {
		statuses = []string{"failed"}
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(statuses)), ",")
	args := []any{jobID}
	for _, st := range statuses {
		args = append(args, st)
	}
	res, err := s.exec("UPDATE batch_items SET status='queued', error='', run_id='', conversation_id='', task_id='', started_at='', finished_at='' WHERE job_id=? AND status IN ("+ph+")", args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Re-enter the queue (status 'queued'); the scheduler re-admits it by priority.
	s.exec("UPDATE batch_jobs SET status='queued', finished_at='' WHERE id=?", jobID)
	return int(n), nil
}

// batchJobCols is the shared SELECT list for a job row. priority and run_at are columns
// on batch_jobs now (folded from job_queue / job_schedule, ADR 0013).
const batchJobCols = `b.id,b.target_id,b.status,b.priority,b.concurrency,b.max_retries,
	b.total,b.succeeded,b.partial,b.failed,b.created_by,b.created_at,b.started_at,b.finished_at,b.run_at`

// batchJobFrom is the shared FROM clause.
const batchJobFrom = `FROM batch_jobs b`

func scanBatchJob(scan func(...any) error) (BatchJob, error) {
	var j BatchJob
	var priority, createdBy, createdAt, startedAt, finishedAt, status, runAt sql.NullString
	if err := scan(&j.ID, &j.TargetID, &status, &priority, &j.Concurrency, &j.MaxRetries,
		&j.Total, &j.Succeeded, &j.Partial, &j.Failed, &createdBy, &createdAt, &startedAt, &finishedAt, &runAt); err != nil {
		return BatchJob{}, err
	}
	j.Status, j.Priority, j.CreatedBy, j.CreatedAt, j.StartedAt, j.FinishedAt, j.RunAt =
		status.String, priority.String, createdBy.String, createdAt.String, startedAt.String, finishedAt.String, runAt.String
	return j, nil
}

func (s *Store) GetBatchJob(id int64) (BatchJob, bool) {
	j, err := scanBatchJob(s.queryRow(`SELECT `+batchJobCols+` `+batchJobFrom+` WHERE b.id=?`, id).Scan)
	if err != nil {
		return BatchJob{}, false
	}
	return j, true
}

func (s *Store) ListBatchJobs() []BatchJob {
	rows, err := s.query(`SELECT ` + batchJobCols + ` ` + batchJobFrom + ` ORDER BY b.id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchJob
	for rows.Next() {
		j, err := scanBatchJob(rows.Scan)
		if err != nil {
			continue
		}
		out = append(out, j)
	}
	return out
}

// LiveJobCounts returns the live per-status tallies computed from items (so a
// running job's progress is always consistent, not a stale cached counter).
// cancelled counts rows the operator cancelled individually (ADR 0011) — terminal
// but neither success nor failure.
func (s *Store) LiveJobCounts(jobID int64) (queued, running, succeeded, partial, failed, cancelled int) {
	rows, err := s.query("SELECT status, COUNT(*) FROM batch_items WHERE job_id=? GROUP BY status", jobID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		rows.Scan(&st, &n)
		switch st {
		case "queued":
			queued = n
		case "running":
			running = n
		case "succeeded":
			succeeded = n
		case "partial":
			partial = n
		case "failed":
			failed = n
		case "cancelled":
			cancelled = n
		}
	}
	return
}

// CancelQueuedItem marks ONE queued item (a run that hasn't started) 'cancelled' so the
// scheduler never admits it. Atomic on status='queued', so it can't cancel a row that
// started running between the read and the write; returns true only for the winner.
func (s *Store) CancelQueuedItem(itemID int64) bool {
	res, err := s.exec("UPDATE batch_items SET status='cancelled', finished_at=? WHERE id=? AND status='queued'", nowStr(), itemID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// FinishItemCancelled marks a running item 'cancelled' (its in-flight run was aborted by
// a per-item or job cancel). Unconditional: the run's own goroutine calls it after the
// provider returns, so it owns the transition.
func (s *Store) FinishItemCancelled(itemID int64) error {
	_, err := s.exec("UPDATE batch_items SET status='cancelled', finished_at=? WHERE id=?", nowStr(), itemID)
	return err
}

// ItemJobAndStatus returns the parent job id and current status of an item, for the
// per-item cancel endpoint to authorize (job ownership) and route (queued vs running).
func (s *Store) ItemJobAndStatus(itemID int64) (jobID int64, status string, ok bool) {
	err := s.queryRow("SELECT job_id, status FROM batch_items WHERE id=?", itemID).Scan(&jobID, &status)
	if err != nil {
		return 0, "", false
	}
	return jobID, status, true
}

// AllJobsFirstInputs returns each job's first row's inputs (raw JSON string), so a
// job list can show what a run is about (e.g. its 标的) without a per-job query.
func (s *Store) AllJobsFirstInputs() map[int64]string {
	out := map[int64]string{}
	rows, err := s.query("SELECT job_id, inputs FROM batch_items WHERE row_index=0")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var inputs sql.NullString
		if rows.Scan(&id, &inputs) == nil {
			out[id] = inputs.String
		}
	}
	return out
}

func (s *Store) BatchJobItems(jobID int64) []BatchItem {
	rows, err := s.query(`SELECT id,job_id,row_index,inputs,status,attempts,run_id,conversation_id,task_id,error,started_at,finished_at
		FROM batch_items WHERE job_id=? ORDER BY row_index`, jobID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchItem
	for rows.Next() {
		var it BatchItem
		var inputs, status, runID, convID, taskID, errMsg, startedAt, finishedAt sql.NullString
		rows.Scan(&it.ID, &it.JobID, &it.RowIndex, &inputs, &status, &it.Attempts, &runID, &convID, &taskID, &errMsg, &startedAt, &finishedAt)
		it.Inputs, it.Status, it.RunID, it.ConvID, it.TaskID, it.Error, it.StartedAt, it.FinishedAt =
			inputs.String, status.String, runID.String, convID.String, taskID.String, errMsg.String, startedAt.String, finishedAt.String
		out = append(out, it)
	}
	return out
}

// ---------- targets ----------

func (s *Store) CreateTarget(pluginSlug, name, config string) (int64, error) {
	return s.insertID(`INSERT INTO batch_targets(plugin_slug,name,config,created_at) VALUES(?,?,?,?)`,
		pluginSlug, name, config, nowStr())
}

func (s *Store) ListTargets() []BatchTarget {
	// Admin drag-order first (batch_targets.ord), then any not-yet-ordered target newest-first
	// — the pre-ordering default. 2147483647 = "unordered, sort last".
	rows, err := s.query(`SELECT b.id, b.plugin_slug, b.name, b.config, b.created_at
		FROM batch_targets b
		ORDER BY COALESCE(b.ord, 2147483647) ASC, b.id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchTarget
	for rows.Next() {
		var t BatchTarget
		var slug, name, config, created sql.NullString
		rows.Scan(&t.ID, &slug, &name, &config, &created)
		t.PluginSlug, t.Name, t.Config, t.Created = slug.String, name.String, config.String, created.String
		out = append(out, t)
	}
	return out
}

// SetTargetOrder records a target's admin-set display position (drag-to-sort). A plain
// UPDATE on the target row (ord is a batch_targets column now, ADR 0013); re-ordering just
// overwrites it.
func (s *Store) SetTargetOrder(id int64, ord int) error {
	_, err := s.exec(`UPDATE batch_targets SET ord=? WHERE id=?`, ord, id)
	return err
}

func (s *Store) GetTarget(id int64) (BatchTarget, bool) {
	var t BatchTarget
	var slug, name, config, created sql.NullString
	err := s.queryRow(`SELECT id,plugin_slug,name,config,created_at FROM batch_targets WHERE id=?`, id).
		Scan(&t.ID, &slug, &name, &config, &created)
	if err != nil {
		return BatchTarget{}, false
	}
	t.PluginSlug, t.Name, t.Config, t.Created = slug.String, name.String, config.String, created.String
	return t, true
}

func (s *Store) UpdateTarget(id int64, name, config string) error {
	_, err := s.exec(`UPDATE batch_targets SET name=?, config=? WHERE id=?`, name, config, id)
	return err
}

func (s *Store) DeleteTarget(id int64) error {
	_, err := s.exec("DELETE FROM batch_targets WHERE id=?", id)
	return err
}

// ---------- plugins ----------

func (s *Store) UpsertPlugin(slug, name, version, spec, source string) error {
	_, err := s.exec(`INSERT INTO plugins(slug,name,version,spec,enabled,source,imported_at)
		VALUES(?,?,?,?,1,?,?)
		ON CONFLICT(slug) DO UPDATE SET name=excluded.name,version=excluded.version,spec=excluded.spec,source=excluded.source,imported_at=excluded.imported_at`,
		slug, name, version, spec, source, nowStr())
	return err
}

func (s *Store) ListPlugins() []Plugin {
	rows, err := s.query(`SELECT id,slug,name,version,spec,enabled,source,imported_at FROM plugins ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Plugin
	for rows.Next() {
		var p Plugin
		var slug, name, version, spec, source, imported sql.NullString
		var enabled sql.NullInt64
		rows.Scan(&p.ID, &slug, &name, &version, &spec, &enabled, &source, &imported)
		p.Slug, p.Name, p.Version, p.Spec, p.Source, p.ImportedAt = slug.String, name.String, version.String, spec.String, source.String, imported.String
		p.Enabled = enabled.Int64 != 0
		out = append(out, p)
	}
	return out
}

func (s *Store) GetPlugin(slug string) (Plugin, bool) {
	var p Plugin
	var sl, name, version, spec, source, imported sql.NullString
	var enabled sql.NullInt64
	err := s.queryRow(`SELECT id,slug,name,version,spec,enabled,source,imported_at FROM plugins WHERE slug=?`, slug).
		Scan(&p.ID, &sl, &name, &version, &spec, &enabled, &source, &imported)
	if err != nil {
		return Plugin{}, false
	}
	p.Slug, p.Name, p.Version, p.Spec, p.Source, p.ImportedAt = sl.String, name.String, version.String, spec.String, source.String, imported.String
	p.Enabled = enabled.Int64 != 0
	return p, true
}

func (s *Store) DeletePlugin(slug string) error {
	_, err := s.exec("DELETE FROM plugins WHERE slug=?", slug)
	return err
}
