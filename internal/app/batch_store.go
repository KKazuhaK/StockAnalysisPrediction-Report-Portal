package app

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// This file is the persistence layer for the batch-run feature: plugin/target/job
// CRUD plus the batch.JobStore implementation the engine drives. See
// docs/adr/0001-batch-run-engine.md.

// Store satisfies the engine's persistence port.
var _ batch.JobStore = (*Store)(nil)

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
	RunAt                                       string // one-shot scheduled start (定时运行, ADR 0007); "" = run ASAP
}

type BatchItem struct {
	ID, JobID             int64
	RowIndex              int
	Inputs                string
	Status                string
	Attempts              int
	RunID, Error          string
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

// ---------- batch.JobStore implementation ----------

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
	_, err := s.exec("UPDATE batch_items SET status=?, attempts=?, run_id=?, error=?, finished_at=? WHERE id=?",
		itemStatus(st), attempts, runID, detail, nowStr(), id)
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

// ResetInFlightItems requeues items left in 'running' by a crash so a resumed job
// re-triggers them (safe because report ingest is idempotent on uid).
func (s *Store) ResetInFlightItems() error {
	_, err := s.exec("UPDATE batch_items SET status='queued' WHERE status='running'")
	return err
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

// ---------- jobs ----------

// CreateBatchJob inserts a queued job (status 'queued', no started_at yet) with the
// given priority and its items. The scheduler admits it to 'running' later, subject
// to the global budget (see docs/adr/0004-run-queue.md).
func (s *Store) CreateBatchJob(targetID int64, concurrency, maxRetries int, createdBy string, rows []map[string]string, priority string) (int64, error) {
	if priority == "" {
		priority = "normal"
	}
	now := nowStr()
	jobID, err := s.insertID(`INSERT INTO batch_jobs(target_id,status,concurrency,max_retries,total,created_by,created_at,started_at)
		VALUES(?,?,?,?,?,?,?,?)`, targetID, "queued", concurrency, maxRetries, len(rows), createdBy, now, "")
	if err != nil {
		return 0, err
	}
	if _, err := s.exec(`INSERT INTO job_queue(job_id,priority) VALUES(?,?)`, jobID, priority); err != nil {
		return jobID, err
	}
	for i, row := range rows {
		b, _ := json.Marshal(row)
		if _, err := s.exec(`INSERT INTO batch_items(job_id,row_index,inputs,status) VALUES(?,?,?,'queued')`, jobID, i, string(b)); err != nil {
			return jobID, err
		}
	}
	return jobID, nil
}

// QueuedJobs lists jobs waiting to be admitted (status 'queued'), with their
// priority and enqueue time (created_at), for the scheduler and the queue view.
func (s *Store) QueuedJobs() []BatchJob {
	rows, err := s.query(`SELECT b.id, COALESCE(q.priority,'normal'), b.created_at, COALESCE(sc.run_at,'')
		FROM batch_jobs b
		LEFT JOIN job_queue q ON q.job_id=b.id
		LEFT JOIN job_schedule sc ON sc.job_id=b.id
		WHERE b.status='queued' ORDER BY b.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchJob
	for rows.Next() {
		var j BatchJob
		var priority, createdAt, runAt sql.NullString
		rows.Scan(&j.ID, &priority, &createdAt, &runAt)
		j.Status, j.Priority, j.CreatedAt, j.RunAt = "queued", priority.String, createdAt.String, runAt.String
		out = append(out, j)
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

// SetJobPriority changes a job's queue priority (the 插队 / re-prioritise action).
func (s *Store) SetJobPriority(jobID int64, priority string) error {
	_, err := s.exec(`INSERT INTO job_queue(job_id,priority) VALUES(?,?)
		ON CONFLICT(job_id) DO UPDATE SET priority=excluded.priority`, jobID, priority)
	return err
}

// ScheduleJob sets a one-shot future start time (定时运行, ADR 0007). run_at uses the
// same "2006-01-02 15:04:05" local basis as created_at; queuedItems() hides the job
// until run_at passes. An empty run_at clears the schedule (立即运行 / run now).
func (s *Store) ScheduleJob(jobID int64, runAt string) error {
	if runAt == "" {
		return s.ClearSchedule(jobID)
	}
	_, err := s.exec(`INSERT INTO job_schedule(job_id,run_at) VALUES(?,?)
		ON CONFLICT(job_id) DO UPDATE SET run_at=excluded.run_at`, jobID, runAt)
	return err
}

// ClearSchedule drops a job's scheduled start, making it eligible on the next tick.
func (s *Store) ClearSchedule(jobID int64) error {
	_, err := s.exec("DELETE FROM job_schedule WHERE job_id=?", jobID)
	return err
}

// DeleteBatchJob removes a job and all its rows (items + queue + schedule). Intended
// for terminal jobs (finished/cancelled); callers gate on status so a running job is
// cancelled first.
func (s *Store) DeleteBatchJob(jobID int64) error {
	s.exec("DELETE FROM batch_items WHERE job_id=?", jobID)
	s.exec("DELETE FROM job_queue WHERE job_id=?", jobID)
	s.exec("DELETE FROM job_schedule WHERE job_id=?", jobID)
	_, err := s.exec("DELETE FROM batch_jobs WHERE id=?", jobID)
	return err
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
	res, err := s.exec("UPDATE batch_items SET status='queued', error='', run_id='', started_at='', finished_at='' WHERE job_id=? AND status IN ("+ph+")", args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Re-enter the queue (status 'queued'); the scheduler re-admits it by priority.
	s.exec("UPDATE batch_jobs SET status='queued', finished_at='' WHERE id=?", jobID)
	return int(n), nil
}

// batchJobCols is the shared SELECT list for a job row joined with its queue
// priority (COALESCE so a job without a job_queue row reads as 'normal').
const batchJobCols = `b.id,b.target_id,b.status,COALESCE(q.priority,'normal'),b.concurrency,b.max_retries,
	b.total,b.succeeded,b.partial,b.failed,b.created_by,b.created_at,b.started_at,b.finished_at,COALESCE(sc.run_at,'')`

// batchJobFrom is the shared FROM clause: a job joined with its queue priority and
// its (optional) one-shot schedule.
const batchJobFrom = `FROM batch_jobs b
	LEFT JOIN job_queue q ON q.job_id=b.id
	LEFT JOIN job_schedule sc ON sc.job_id=b.id`

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
func (s *Store) LiveJobCounts(jobID int64) (queued, running, succeeded, partial, failed int) {
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
		}
	}
	return
}

func (s *Store) BatchJobItems(jobID int64) []BatchItem {
	rows, err := s.query(`SELECT id,job_id,row_index,inputs,status,attempts,run_id,error,started_at,finished_at
		FROM batch_items WHERE job_id=? ORDER BY row_index`, jobID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchItem
	for rows.Next() {
		var it BatchItem
		var inputs, status, runID, errMsg, startedAt, finishedAt sql.NullString
		rows.Scan(&it.ID, &it.JobID, &it.RowIndex, &inputs, &status, &it.Attempts, &runID, &errMsg, &startedAt, &finishedAt)
		it.Inputs, it.Status, it.RunID, it.Error, it.StartedAt, it.FinishedAt =
			inputs.String, status.String, runID.String, errMsg.String, startedAt.String, finishedAt.String
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
	rows, err := s.query(`SELECT id,plugin_slug,name,config,created_at FROM batch_targets ORDER BY id DESC`)
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
