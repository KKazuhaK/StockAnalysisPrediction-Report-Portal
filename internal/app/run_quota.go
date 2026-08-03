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
	// Also floored at the account's own creation. batch_jobs is kept across a delete for audit, so
	// without this the previous holder of a reused username would spend the new holder's daily
	// quota. COALESCE over the whole subquery, not inside it: an absent users row (a machine-created
	// job, or a name nobody holds) must mean NO floor rather than a NULL that excludes everything.
	//
	// created_at is second-granular, so a delete and recreate inside the same second still shares
	// that second's usage. Bounded and self-clearing at the next panel midnight; not worth a
	// sub-second timestamp format that every other column would then disagree with.
	s.queryRow(`SELECT COALESCE(SUM(total),0) FROM batch_jobs
		WHERE created_by=? AND created_at >= ?
		  AND created_at >= COALESCE((SELECT created_at FROM users WHERE username=?), '')`,
		username, since.Format("2006-01-02 15:04:05"), username).Scan(&n)
	return int(n.Int64)
}

// panelMidnight is the start of today's civil day in the panel timezone, returned in the local
// wall-clock form that batch_jobs.created_at is stored in. Going through the panel zone (not the
// server zone) is what makes the reset land on the business day boundary; converting back to local
// keeps it comparable with the stored timestamps, and using a real instant keeps it DST-safe.
// The periods a run quota can be measured over. Stored on the OU; "" means day, which is what
// every row written before this existed meant.
const (
	QuotaDay   = "day"
	QuotaWeek  = "week"
	QuotaMonth = "month"
	// QuotaTotal is the life of the ACCOUNT, not of the OU — a trial with a fixed number of runs.
	// It has no window, so it is the one period with no start instant.
	QuotaTotal = "total"
)

func validQuotaPeriod(p string) bool {
	switch p {
	case QuotaDay, QuotaWeek, QuotaMonth, QuotaTotal:
		return true
	}
	return false
}

// quotaPeriodStart is the instant the current allowance began. The zero time means "no window" —
// a lifetime cap — which the caller must distinguish from a very old date.
//
// Computed in the PANEL timezone, like the daily boundary already was: a quota that resets at the
// server's midnight rather than at the business's is wrong for exactly the people it governs.
//
// An unrecognized value falls back to the day. That is both the pre-existing behaviour and the
// NARROWEST window, so a hand-edited row can never accidentally widen somebody's allowance.
func (s *Server) quotaPeriodStart(period string, now time.Time) time.Time {
	loc := s.panelLocation()
	p := now.In(loc)
	switch period {
	case QuotaTotal:
		return time.Time{}
	case QuotaWeek:
		// Monday. Go numbers Sunday 0, so subtracting Weekday() directly would put the start six
		// days in the FUTURE on a Sunday.
		back := (int(p.Weekday()) + 6) % 7
		d := p.AddDate(0, 0, -back)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).In(time.Local)
	case QuotaMonth:
		return time.Date(p.Year(), p.Month(), 1, 0, 0, 0, 0, loc).In(time.Local)
	default:
		return time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, loc).In(time.Local)
	}
}

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
	eff := s.st.EffectiveGroupSettings(user)
	limit = eff.DailyRunQuota
	if limit <= 0 {
		return 0, 0, true // 0 = unlimited
	}
	used = s.st.RunsToday(user, s.quotaPeriodStart(eff.QuotaPeriod, time.Now()))
	return limit, used, used+rows <= limit
}

// quotaResetsAt is when the current allowance next refills, as a UTC instant, for the 429 payload
// and the run form's hint (instants travel as UTC on the wire; the client localizes).
//
// A lifetime cap never refills, so it returns "" — and the UI must say "no more runs" rather than
// print a date, because there is no date to print.
func (s *Server) quotaResetsAt(period string, now time.Time) string {
	if period == QuotaTotal {
		return ""
	}
	start := s.quotaPeriodStart(period, now).In(s.panelLocation())
	var next time.Time
	switch period {
	case QuotaWeek:
		next = start.AddDate(0, 0, 7)
	case QuotaMonth:
		next = start.AddDate(0, 1, 0)
	default:
		next = start.AddDate(0, 0, 1)
	}
	return next.UTC().Format(time.RFC3339)
}
