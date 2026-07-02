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
	ID                                  int64
	Slug, Name, Version, Spec, Source   string
	Enabled                             bool
	ImportedAt                          string
}

type BatchTarget struct {
	ID                             int64
	PluginSlug, Name, Config, Created string
}

type BatchJob struct {
	ID, TargetID                            int64
	Status                                  string
	Concurrency, MaxRetries                 int
	Total, Succeeded, Partial, Failed       int
	CreatedBy, CreatedAt, StartedAt, FinishedAt string
}

type BatchItem struct {
	ID, JobID              int64
	RowIndex               int
	Inputs                 string
	Status                 string
	Attempts               int
	RunID, Error           string
	StartedAt, FinishedAt  string
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

// CreateBatchJob inserts a running job and its queued items in one shot.
func (s *Store) CreateBatchJob(targetID int64, concurrency, maxRetries int, createdBy string, rows []map[string]string) (int64, error) {
	now := nowStr()
	jobID, err := s.insertID(`INSERT INTO batch_jobs(target_id,status,concurrency,max_retries,total,created_by,created_at,started_at)
		VALUES(?,?,?,?,?,?,?,?)`, targetID, "running", concurrency, maxRetries, len(rows), createdBy, now, now)
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

// CancelBatchJob asks a running job to stop; workers observe it via Cancelled().
func (s *Store) CancelBatchJob(jobID int64) error {
	_, err := s.exec("UPDATE batch_jobs SET status='cancelling' WHERE id=? AND status='running'", jobID)
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
	s.exec("UPDATE batch_jobs SET status='running', finished_at='' WHERE id=?", jobID)
	return int(n), nil
}

func (s *Store) GetBatchJob(id int64) (BatchJob, bool) {
	var j BatchJob
	var createdBy, createdAt, startedAt, finishedAt, status sql.NullString
	err := s.queryRow(`SELECT id,target_id,status,concurrency,max_retries,total,succeeded,partial,failed,created_by,created_at,started_at,finished_at
		FROM batch_jobs WHERE id=?`, id).Scan(&j.ID, &j.TargetID, &status, &j.Concurrency, &j.MaxRetries,
		&j.Total, &j.Succeeded, &j.Partial, &j.Failed, &createdBy, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		return BatchJob{}, false
	}
	j.Status, j.CreatedBy, j.CreatedAt, j.StartedAt, j.FinishedAt = status.String, createdBy.String, createdAt.String, startedAt.String, finishedAt.String
	return j, true
}

func (s *Store) ListBatchJobs() []BatchJob {
	rows, err := s.query(`SELECT id,target_id,status,concurrency,max_retries,total,succeeded,partial,failed,created_by,created_at,started_at,finished_at
		FROM batch_jobs ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BatchJob
	for rows.Next() {
		var j BatchJob
		var createdBy, createdAt, startedAt, finishedAt, status sql.NullString
		rows.Scan(&j.ID, &j.TargetID, &status, &j.Concurrency, &j.MaxRetries,
			&j.Total, &j.Succeeded, &j.Partial, &j.Failed, &createdBy, &createdAt, &startedAt, &finishedAt)
		j.Status, j.CreatedBy, j.CreatedAt, j.StartedAt, j.FinishedAt = status.String, createdBy.String, createdAt.String, startedAt.String, finishedAt.String
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
