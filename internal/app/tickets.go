package app

import (
	"database/sql"
	"time"
)

// Priority "次票": a per-user quota of 加急 runs allocated by group weight and
// refilled each period. State is lazy — a period rollover is applied on access, so
// there is no cron. See docs/adr/0005-priority-tickets.md.

const ticketTimeFmt = "2006-01-02 15:04:05"

// ticketRefill applies any whole-period rollovers between periodStart and now, and
// returns the resulting remaining count and (advanced) period start. It is pure so
// the rollover policy is unit-testable. A zero periodStart (first use) begins a
// fresh period at now with a full allocation.
func ticketRefill(remaining int, periodStart time.Time, allocation, periodDays int, now time.Time) (int, time.Time) {
	if periodStart.IsZero() || now.Before(periodStart) {
		return allocation, now
	}
	period := time.Duration(periodDays) * 24 * time.Hour
	if period <= 0 {
		return remaining, periodStart // misconfigured period: never refill
	}
	if elapsed := now.Sub(periodStart); elapsed >= period {
		periods := elapsed / period
		periodStart = periodStart.Add(periods * period) // keep the original cadence
		remaining = allocation
	}
	return remaining, periodStart
}

// UserTicketAllocation is the per-period 加急 allowance for a user: the highest
// weight among the groups they belong to (0 if none).
func (s *Store) UserTicketAllocation(username string) int {
	var w sql.NullInt64
	s.queryRow(`SELECT COALESCE(MAX(g.weight),0) FROM user_group_members m
		JOIN user_groups g ON g.id=m.group_id WHERE m.username=?`, username).Scan(&w)
	return int(w.Int64)
}

// UserUrgentUnlimited reports whether any of the user's groups grants unlimited
// 加急 runs. This is independent of role: admins are limited unless placed in such
// a group.
func (s *Store) UserUrgentUnlimited(username string) bool {
	var v sql.NullInt64
	s.queryRow(`SELECT COALESCE(MAX(g.urgent_unlimited),0) FROM user_group_members m
		JOIN user_groups g ON g.id=m.group_id WHERE m.username=?`, username).Scan(&v)
	return v.Int64 != 0
}

func (s *Store) ticketRow(username string) (int, time.Time) {
	var remaining sql.NullInt64
	var ps sql.NullString
	s.queryRow("SELECT remaining, period_start FROM priority_tickets WHERE username=?", username).Scan(&remaining, &ps)
	if ps.String == "" {
		return 0, time.Time{}
	}
	t, _ := time.ParseInLocation(ticketTimeFmt, ps.String, time.Local)
	return int(remaining.Int64), t
}

func (s *Store) saveTicket(username string, remaining int, periodStart time.Time) {
	s.exec(`INSERT INTO priority_tickets(username,remaining,period_start) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET remaining=excluded.remaining, period_start=excluded.period_start`,
		username, remaining, periodStart.Format(ticketTimeFmt))
}

// TicketStatus applies any refill and returns the user's remaining 加急 tickets.
func (s *Store) TicketStatus(username string, allocation, periodDays int, now time.Time) int {
	rem, ps := s.ticketRow(username)
	if ps.IsZero() && allocation <= 0 {
		return 0 // nothing to grant or track yet — don't anchor an empty period
	}
	newRem, newPs := ticketRefill(rem, ps, allocation, periodDays, now)
	if newRem != rem || !newPs.Equal(ps) {
		s.saveTicket(username, newRem, newPs)
	}
	return newRem
}

// SpendTicket refills, then consumes one ticket if any remain. It returns whether a
// ticket was spent and the count left afterward. Note: an allocation change takes
// effect at the next period (a mid-period raise doesn't retroactively top up).
func (s *Store) SpendTicket(username string, allocation, periodDays int, now time.Time) (bool, int) {
	rem, ps := s.ticketRow(username)
	if ps.IsZero() && allocation <= 0 {
		return false, 0 // no allocation and no state — nothing to spend, nothing to persist
	}
	rem, ps = ticketRefill(rem, ps, allocation, periodDays, now)
	if rem <= 0 {
		s.saveTicket(username, rem, ps)
		return false, 0
	}
	rem--
	s.saveTicket(username, rem, ps)
	return true, rem
}
