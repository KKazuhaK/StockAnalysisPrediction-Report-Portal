package app

import (
	"database/sql"
	"time"
)

// Daily run quota for restricted (external) OUs — ADR 0022 R2. There is no counter table and no
// cron: the day's usage is a windowed SUM over batch_jobs, mirroring the lazy-period ticket logic
// (tickets.go). The window resets implicitly at the panel-tz civil midnight.

// RunsToday is how many RUNS a user has submitted since the given civil midnight. It sums
// batch_jobs.total (rows), not jobs, so a single multi-row submit cannot dodge the cap. since must
// be the panel-tz civil midnight expressed in the local wall-clock form batch_jobs.created_at is
// stored in (nowStr), so the string comparison matches the stored format.
func (s *Store) RunsToday(username string, since time.Time) int {
	var n sql.NullInt64
	s.queryRow(`SELECT COALESCE(SUM(total),0) FROM batch_jobs WHERE created_by=? AND created_at >= ?`,
		username, since.Format("2006-01-02 15:04:05")).Scan(&n)
	return int(n.Int64)
}

// panelMidnight is the start of today's civil day in the panel timezone, returned in the local
// wall-clock form that batch_jobs.created_at is stored in. Going through the panel zone (not the
// server zone) is what makes the reset land on the business day boundary; converting back to local
// keeps it comparable with the stored timestamps, and using a real instant keeps it DST-safe.
func (s *Server) panelMidnight(now time.Time) time.Time {
	loc := s.panelLocation()
	p := now.In(loc)
	return time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, loc).In(time.Local)
}

// runQuotaCheck reports whether a submit of `rows` runs is allowed under the caller's daily quota.
// It returns the effective limit and the rows already used today for the error/UX payload. Only a
// restricted OU is capped: internal users and admins resolve to a nil viewer scope and always pass,
// as does an effective quota of 0 (unlimited). Counting happens at SUBMIT, so a later failure still
// costs the quota — deliberate, and abuse-resistant.
func (s *Server) runQuotaCheck(user string, rows int) (limit, used int, ok bool) {
	if s.viewerScope(user) == nil { // internal / admin / anonymous → never rate-limited
		return 0, 0, true
	}
	limit = s.st.EffectiveGroupSettings(user).DailyRunQuota
	if limit <= 0 {
		return 0, 0, true // 0 = unlimited
	}
	used = s.st.RunsToday(user, s.panelMidnight(time.Now()))
	return limit, used, used+rows <= limit
}

// quotaResetsAt is the next panel-tz civil midnight as a UTC instant, for the 429 payload and the
// run form's "resets at" hint (instants travel as UTC on the wire; the client localizes).
func (s *Server) quotaResetsAt(now time.Time) string {
	loc := s.panelLocation()
	p := now.In(loc)
	return time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC().Format(time.RFC3339)
}
