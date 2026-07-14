package app

// Persistence for recurring tasks (scheduled tasks; docs/adr/0018-recurring-tasks.md): a saved job template
// + a daily/weekly/monthly cadence that a background loop fires into the run queue. recurring_tasks
// holds the definitions; recurring_runs is the fire→job audit chain (trimmed to a per-task ring so
// it can't grow unbounded). Idempotency of the scheduler lives in recurring_tasks.last_fired (the
// period-stamp), never derived from recurring_runs.

// recurringRunsKeepPerTask bounds the audit history retained per task (newest kept).
const recurringRunsKeepPerTask = 50

// RecurringTask is one saved schedule. Rows is a JSON array of input maps in the exact shape
// CreateBatchJob takes (1 row = a single run, N rows = a batch). Priority is empty for normal
// (resolves to the creator's group base at fire time) or idle; never urgent (ADR 0018 section 4). Cadence fields
// mirror the storage-cleanup engine (freq daily|weekly|monthly + AtTime "HH:MM" panel tz + Weekday
// for weekly + Monthday for monthly). LastFired is the YYYY-MM-DD period-stamp guarding re-fire.
type RecurringTask struct {
	ID          int64
	Name        string
	TargetID    int64
	Rows        string // JSON [{k:v}, ...]
	Concurrency int
	Priority    string // '' | 'idle'
	MaxRetries  int
	Freq        string // daily | weekly | monthly
	AtTime      string // "HH:MM" in the panel timezone
	Weekday     int    // 0=Sun..6=Sat (weekly)
	Monthday    int    // 1..31, clamped to month length (monthly)
	Enabled     bool
	CreatedBy   string
	CreatedAt   string
	LastFired   string // YYYY-MM-DD period-stamp
}

// RecurringRun is one audit row: a task fired and created batch job JobID at FiredAt.
type RecurringRun struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	JobID     int64  `json:"job_id"`
	FiredAt   string `json:"fired_at"`
	JobStatus string `json:"job_status,omitempty"` // joined from batch_jobs for the history view
}

const recurringTaskCols = `id, COALESCE(name,''), COALESCE(target_id,0), COALESCE(rows,'[]'),
	COALESCE(concurrency,1), COALESCE(priority,''), COALESCE(max_retries,0),
	COALESCE(freq,''), COALESCE(at_time,''), COALESCE(weekday,1), COALESCE(monthday,1),
	COALESCE(enabled,1), COALESCE(created_by,''), COALESCE(created_at,''), COALESCE(last_fired,'')`

func scanRecurringTask(sc interface{ Scan(...any) error }) (RecurringTask, bool) {
	var t RecurringTask
	var enabled int
	if err := sc.Scan(&t.ID, &t.Name, &t.TargetID, &t.Rows, &t.Concurrency, &t.Priority, &t.MaxRetries,
		&t.Freq, &t.AtTime, &t.Weekday, &t.Monthday, &enabled, &t.CreatedBy, &t.CreatedAt, &t.LastFired); err != nil {
		return RecurringTask{}, false
	}
	t.Enabled = enabled != 0
	return t, true
}

// ListRecurringTasks returns every task, newest first (the console lists them; the handler filters
// to the caller's own for a non-admin).
func (s *Store) ListRecurringTasks() []RecurringTask {
	rows, err := s.query(`SELECT ` + recurringTaskCols + ` FROM recurring_tasks ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RecurringTask
	for rows.Next() {
		if t, ok := scanRecurringTask(rows); ok {
			out = append(out, t)
		}
	}
	return out
}

// EnabledRecurringTasks returns only enabled tasks — the candidate set the cadence loop scans.
func (s *Store) EnabledRecurringTasks() []RecurringTask {
	rows, err := s.query(`SELECT ` + recurringTaskCols + ` FROM recurring_tasks WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RecurringTask
	for rows.Next() {
		if t, ok := scanRecurringTask(rows); ok {
			out = append(out, t)
		}
	}
	return out
}

// GetRecurringTask fetches one task by id (ok=false if absent).
func (s *Store) GetRecurringTask(id int64) (RecurringTask, bool) {
	return scanRecurringTask(s.queryRow(`SELECT `+recurringTaskCols+` FROM recurring_tasks WHERE id=?`, id))
}

// CreateRecurringTask inserts a task, returning its new id. created_at is stamped here; last_fired
// starts empty so the first matching period fires.
func (s *Store) CreateRecurringTask(t RecurringTask) (int64, error) {
	return s.insertID(`INSERT INTO recurring_tasks
		(name,target_id,rows,concurrency,priority,max_retries,freq,at_time,weekday,monthday,enabled,created_by,created_at,last_fired)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'')`,
		t.Name, t.TargetID, t.Rows, t.Concurrency, t.Priority, t.MaxRetries,
		t.Freq, t.AtTime, t.Weekday, t.Monthday, boolInt(t.Enabled), t.CreatedBy, nowStr())
}

// UpdateRecurringTask saves the editable fields (identity, created_by/at and last_fired are not
// touched — a config edit must not rewrite the fire-guard stamp).
func (s *Store) UpdateRecurringTask(t RecurringTask) error {
	_, err := s.exec(`UPDATE recurring_tasks SET
		name=?, target_id=?, rows=?, concurrency=?, priority=?, max_retries=?,
		freq=?, at_time=?, weekday=?, monthday=?, enabled=? WHERE id=?`,
		t.Name, t.TargetID, t.Rows, t.Concurrency, t.Priority, t.MaxRetries,
		t.Freq, t.AtTime, t.Weekday, t.Monthday, boolInt(t.Enabled), t.ID)
	return err
}

// SetRecurringEnabled flips just the enabled flag (the list's quick toggle).
func (s *Store) SetRecurringEnabled(id int64, enabled bool) error {
	_, err := s.exec(`UPDATE recurring_tasks SET enabled=? WHERE id=?`, boolInt(enabled), id)
	return err
}

// MarkRecurringFired stamps the period the loop is about to fire, BEFORE creating the job, so a
// crash or slow fire can't let the 60s ticker re-fire the same period (mirrors cleanupTick).
func (s *Store) MarkRecurringFired(id int64, stamp string) error {
	_, err := s.exec(`UPDATE recurring_tasks SET last_fired=? WHERE id=?`, stamp, id)
	return err
}

// DeleteRecurringTask removes a task and its audit history.
func (s *Store) DeleteRecurringTask(id int64) error {
	s.exec(`DELETE FROM recurring_runs WHERE task_id=?`, id)
	_, err := s.exec(`DELETE FROM recurring_tasks WHERE id=?`, id)
	return err
}

// InsertRecurringRun appends a fire→job audit row and trims the task's history to the newest
// recurringRunsKeepPerTask rows.
func (s *Store) InsertRecurringRun(taskID, jobID int64) (int64, error) {
	id, err := s.insertID(`INSERT INTO recurring_runs(task_id,job_id,fired_at) VALUES(?,?,?)`, taskID, jobID, nowStr())
	if err != nil {
		return 0, err
	}
	s.exec(`DELETE FROM recurring_runs WHERE task_id=? AND id NOT IN
		(SELECT id FROM recurring_runs WHERE task_id=? ORDER BY id DESC LIMIT ?)`,
		taskID, taskID, recurringRunsKeepPerTask)
	return id, nil
}

// ListRecurringRuns returns a task's fire history, newest first, joining each fired job's current
// status for the detail view.
func (s *Store) ListRecurringRuns(taskID int64, limit int) []RecurringRun {
	if limit <= 0 || limit > recurringRunsKeepPerTask {
		limit = recurringRunsKeepPerTask
	}
	rows, err := s.query(`SELECT r.id, r.task_id, r.job_id, COALESCE(r.fired_at,''), COALESCE(j.status,'')
		FROM recurring_runs r LEFT JOIN batch_jobs j ON j.id=r.job_id
		WHERE r.task_id=? ORDER BY r.id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RecurringRun
	for rows.Next() {
		var r RecurringRun
		if err := rows.Scan(&r.ID, &r.TaskID, &r.JobID, &r.FiredAt, &r.JobStatus); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}
