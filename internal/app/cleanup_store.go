package app

import (
	"database/sql"
	"strings"
	"time"
)

// reportInstantLayouts are the timestamp formats seen in reports.sent_at, tried in order. sent_at is
// heterogeneous in production: the v1 path stamps Go's RFC3339 (`...Z`), but the bulk of rows are a
// timezone-less microsecond form (`2006-01-02T15:04:05.575420`, e.g. from a Python isoformat ingest
// path). A layout with no zone is parsed as UTC (the system's on-the-wire convention for instants).
// The space-separated variants are defensive. A date-only or empty value matches none and is kept
// (fail-closed) — it carries no precise instant to age against.
var reportInstantLayouts = []string{
	time.RFC3339Nano,                // zoned, optional fraction (covers RFC3339 too)
	"2006-01-02T15:04:05.999999999", // no zone + fraction  → UTC
	"2006-01-02T15:04:05",           // no zone, no fraction → UTC
	"2006-01-02 15:04:05.999999999", // space + fraction     → UTC (defensive)
	"2006-01-02 15:04:05",           // space, no fraction   → UTC (defensive)
}

// parseReportInstant parses a reports.sent_at value into a UTC instant, trying each known layout.
// ok=false for a value that is empty, date-only, or otherwise not a full instant (kept, fail-closed).
func parseReportInstant(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range reportInstantLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Storage-cleanup store layer (docs/adr/0017-storage-cleanup.md). Retention deletes are set-based
// and re-assert their terminal predicate so they stay race-safe against the scheduler/reconcile
// paths; reports are handled fail-closed (only rows whose sent_at parses to a real instant
// are ever eligible). Config for the feature lives in the meta k/v table; only the audit history
// (cleanup_runs) is a table.

// Retention floors are Go consts (not settings) so a hand-edited meta value can never drop a
// target below its safety floor — they are clamped on both save (cleanup_api) and read
// (cleanupConfigLoad). Reports are core content, hence the year floor.
const (
	minBatchRetentionDays   = 7
	minReportsRetentionDays = 365
)

// cleanupRunsKeep bounds the audit ring buffer: only the most recent N cleanup_runs rows are kept.
const cleanupRunsKeep = 200

// ---------- Target A: batch history ----------

// batchTerminalPred selects terminal batch jobs (finished/cancelled/expired) with a real
// finished_at strictly before the cutoff. finished_at is written by nowStr() (local
// "2006-01-02 15:04:05"), so a lexical "< cutoff" is a correct chronological compare, and the
// non-empty guard keeps a just-finalized or requeued job (finished_at cleared by RequeueItems)
// out of range.
const batchTerminalPred = `status IN ('finished','cancelled','expired') AND finished_at <> '' AND finished_at < ?`

// CountFinishedJobsBefore counts terminal batch jobs older than cutoff (dry-run / usage).
func (s *Store) CountFinishedJobsBefore(cutoff string) (int64, error) {
	var n int64
	err := s.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE "+batchTerminalPred, cutoff).Scan(&n)
	return n, err
}

// DeleteFinishedJobsBefore deletes terminal batch jobs older than cutoff and their batch_items in
// one transaction, as a single set-based DELETE that re-asserts the terminal predicate (NOT a
// select-then-delete loop). A job requeued between a count and this delete simply no longer matches
// the predicate, so it is never removed mid-flight — the race-safe primitive (ADR 0017). Deleting
// at job granularity means an unreconciled 'untracked' item is only ever removed with its
// already-terminal parent job. Returns how many jobs were deleted.
func (s *Store) DeleteFinishedJobsBefore(cutoff string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	// Children first: items whose parent job matches the terminal+age predicate.
	if _, err := tx.Exec(s.bind("DELETE FROM batch_items WHERE job_id IN (SELECT id FROM batch_jobs WHERE "+batchTerminalPred+")"), cutoff); err != nil {
		tx.Rollback()
		return 0, err
	}
	res, err := tx.Exec(s.bind("DELETE FROM batch_jobs WHERE "+batchTerminalPred), cutoff)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// ---------- Target B: expired API tokens ----------

// tokensExpiredPred selects tokens with a non-empty expires_at strictly before the cutoff.
// expires_at is stored in the same local "2006-01-02 15:04:05" basis TokenValid compares against,
// so a lexical compare is correct; a NULL/empty expires_at (never-expiring token) is excluded.
const tokensExpiredPred = `expires_at IS NOT NULL AND expires_at <> '' AND expires_at < ?`

// CountExpiredTokensBefore counts tokens expired before cutoff (dry-run / usage).
func (s *Store) CountExpiredTokensBefore(cutoff string) (int64, error) {
	var n int64
	err := s.queryRow("SELECT COUNT(*) FROM api_tokens WHERE "+tokensExpiredPred, cutoff).Scan(&n)
	return n, err
}

// DeleteExpiredTokensBefore deletes tokens whose expiry is older than cutoff. Returns rows deleted.
func (s *Store) DeleteExpiredTokensBefore(cutoff string) (int64, error) {
	res, err := s.exec("DELETE FROM api_tokens WHERE "+tokensExpiredPred, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---------- Target C: reports (core content, fail-closed) ----------

// reportUIDsIngestedBefore returns the uids of reports whose sent_at parses (via parseReportInstant)
// to a UTC instant strictly before cutoff. sent_at is the last-write instant of a report (it is
// overwritten on every re-ingest), and there is no server-stamped created_at, so it is the age
// signal. It is heterogeneous — zoned RFC3339 on the v1 path, timezone-less microsecond form on
// another — hence the multi-layout parse; a value with no precise instant (date-only/empty) is
// SKIPPED (fail-closed: never delete a report we cannot prove is old). This biases to under-cleaning,
// the safe failure mode for core content.
type reportKey struct{ uid, sent string }

// reportsDeleteChunk bounds how many reports one transaction deletes, so a large purge releases the
// single SQLite writer between chunks instead of freezing all reads/writes until commit.
const reportsDeleteChunk = 500

func (s *Store) reportsIngestedBefore(cutoff time.Time) ([]reportKey, error) {
	rows, err := s.query("SELECT uid, sent_at FROM reports")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []reportKey
	for rows.Next() {
		var uid, sent sql.NullString
		if err := rows.Scan(&uid, &sent); err != nil {
			return nil, err
		}
		if !uid.Valid || uid.String == "" || !sent.Valid {
			continue
		}
		t, ok := parseReportInstant(sent.String)
		if !ok {
			continue // date-only / empty / malformed → keep
		}
		if t.Before(cutoff) {
			keys = append(keys, reportKey{uid: uid.String, sent: sent.String})
		}
	}
	return keys, rows.Err()
}

// CountReportsIngestedBefore counts reports eligible for retention deletion at cutoff (dry-run / usage).
func (s *Store) CountReportsIngestedBefore(cutoff time.Time) (int64, error) {
	keys, err := s.reportsIngestedBefore(cutoff)
	return int64(len(keys)), err
}

// DeleteReportsIngestedBefore deletes reports whose sent_at parses to an instant older than cutoff,
// cascading their tracking_items. The scan captures each row's exact sent_at and the delete
// RE-ASSERTS it (WHERE uid=? AND sent_at=?): a report re-ingested between scan and delete has a fresh
// sent_at, so it no longer matches and is preserved — keeping the fail-closed guarantee across the
// scan/delete window. Runs in bounded chunks (each its own tx) so a big purge doesn't hold the single
// SQLite connection for the whole run. Returns how many reports were deleted (partial count on error).
func (s *Store) DeleteReportsIngestedBefore(cutoff time.Time) (int64, error) {
	keys, err := s.reportsIngestedBefore(cutoff)
	if err != nil || len(keys) == 0 {
		return 0, err
	}
	var total int64
	for i := 0; i < len(keys); i += reportsDeleteChunk {
		end := i + reportsDeleteChunk
		if end > len(keys) {
			end = len(keys)
		}
		n, err := s.deleteReportChunk(keys[i:end])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// deleteReportChunk deletes one chunk of reports (+ their tracking_items) in a single transaction,
// re-asserting sent_at so a concurrently re-ingested report is skipped. tracking_items are removed
// only for reports whose row actually matched (so a preserved report keeps its items).
func (s *Store) deleteReportChunk(keys []reportKey) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, k := range keys {
		res, err := tx.Exec(s.bind("DELETE FROM reports WHERE uid=? AND sent_at=?"), k.uid, k.sent)
		if err != nil {
			tx.Rollback()
			return n, err
		}
		c, _ := res.RowsAffected()
		if c == 0 {
			continue // re-ingested (sent_at changed) or already gone — keep its tracking_items
		}
		if _, err := tx.Exec(s.bind("DELETE FROM tracking_items WHERE report_uid=?"), k.uid); err != nil {
			tx.Rollback()
			return n, err
		}
		n += c
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// ---------- Storage usage (analysis screen) ----------

// usageCount returns the row count of an internal (non-user-supplied) table name.
func (s *Store) usageCount(table string) int64 {
	var n int64
	s.queryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n
}

// usageBytes returns COALESCE(SUM(expr),0) over a table — an approximate storage footprint from
// its large TEXT columns. table and expr are internal constants (never user input).
func (s *Store) usageBytes(table, expr string) int64 {
	var n int64
	s.queryRow("SELECT COALESCE(SUM(" + expr + "),0) FROM " + table).Scan(&n)
	return n
}

// usageSpan returns the oldest/newest non-empty value of a timestamp column, for the usage display.
func (s *Store) usageSpan(table, col string) (oldest, newest string) {
	var o, nw sql.NullString
	s.queryRow("SELECT MIN(" + col + "), MAX(" + col + ") FROM " + table + " WHERE " + col + " IS NOT NULL AND " + col + " <> ''").Scan(&o, &nw)
	return o.String, nw.String
}

// DBSizeBytes returns the approximate total database size in bytes — sqlite page_count*page_size,
// postgres pg_database_size(current_database()). Best-effort: returns 0 on any error.
func (s *Store) DBSizeBytes() int64 {
	if s.driver == "postgres" {
		var n int64
		if s.queryRow("SELECT pg_database_size(current_database())").Scan(&n) == nil {
			return n
		}
		return 0
	}
	var pageCount, pageSize int64
	s.queryRow("PRAGMA page_count").Scan(&pageCount)
	s.queryRow("PRAGMA page_size").Scan(&pageSize)
	return pageCount * pageSize
}

// ---------- Audit history (cleanup_runs) ----------

// CleanupRun is one recorded cleanup pass, surfaced in the admin console's run history.
type CleanupRun struct {
	ID             int64  `json:"id"`
	RanAt          string `json:"ran_at"`
	Trigger        string `json:"trigger"` // "schedule" | "manual"
	DryRun         bool   `json:"dry_run"`
	OK             bool   `json:"ok"`
	Error          string `json:"error"`
	BatchDeleted   int64  `json:"batch_deleted"`
	TokensDeleted  int64  `json:"tokens_deleted"`
	ReportsDeleted int64  `json:"reports_deleted"`
	DurationMs     int64  `json:"duration_ms"`
}

// InsertCleanupRun appends an audit row and trims the ring buffer to the most recent
// cleanupRunsKeep rows.
func (s *Store) InsertCleanupRun(c CleanupRun) (int64, error) {
	id, err := s.insertID(`INSERT INTO cleanup_runs(ran_at,trigger,dry_run,ok,error,batch_deleted,tokens_deleted,reports_deleted,duration_ms)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		c.RanAt, c.Trigger, boolInt(c.DryRun), boolInt(c.OK), c.Error,
		c.BatchDeleted, c.TokensDeleted, c.ReportsDeleted, c.DurationMs)
	if err != nil {
		return 0, err
	}
	s.trimCleanupRuns()
	return id, nil
}

// trimCleanupRuns keeps only the most recent cleanupRunsKeep audit rows. Idempotency of the
// scheduler is NOT derived from this table (it lives in the meta key cleanup_last_run_period), so
// trimming can never cause a re-fire.
func (s *Store) trimCleanupRuns() {
	s.exec("DELETE FROM cleanup_runs WHERE id NOT IN (SELECT id FROM cleanup_runs ORDER BY id DESC LIMIT ?)", cleanupRunsKeep)
}

// ListCleanupRuns returns recent audit rows, newest first (capped at cleanupRunsKeep).
func (s *Store) ListCleanupRuns(limit int) ([]CleanupRun, error) {
	if limit <= 0 || limit > cleanupRunsKeep {
		limit = cleanupRunsKeep
	}
	rows, err := s.query(`SELECT id,ran_at,trigger,dry_run,ok,error,batch_deleted,tokens_deleted,reports_deleted,duration_ms
		FROM cleanup_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CleanupRun
	for rows.Next() {
		var c CleanupRun
		var dry, ok int64
		if err := rows.Scan(&c.ID, &c.RanAt, &c.Trigger, &dry, &ok, &c.Error, &c.BatchDeleted, &c.TokensDeleted, &c.ReportsDeleted, &c.DurationMs); err != nil {
			return nil, err
		}
		c.DryRun = dry != 0
		c.OK = ok != 0
		out = append(out, c)
	}
	return out, rows.Err()
}
