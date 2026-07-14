package app

import "time"

// Shared daily/weekly/monthly cadence engine. Both the storage-cleanup pass (docs/adr/0017) and the
// recurring-task scheduler (docs/adr/0018) fire at most once per matching civil day, at/after a
// wall-clock time in the panel timezone, with a YYYY-MM-DD period-stamp as the restart double-fire
// guard. Keeping the rule in one tested pure function means the two schedulers can never drift.

// cadenceDue reports whether a daily/weekly/monthly cadence should fire at `now` and returns the
// YYYY-MM-DD period-stamp to record if it does. It fires every day (daily), on `weekday` (weekly,
// 0=Sun..6=Sat), or on `monthday` (monthly, clamped to the month's length so day 31 fires on the
// last day of a short month), each at/after `hhmm` in `loc`. A `lastRun` stamp equal to today
// suppresses re-firing. Any other `freq` ("off"/unknown) never fires.
func cadenceDue(freq, hhmm string, weekday, monthday int, lastRun string, now time.Time, loc *time.Location) (bool, string) {
	hh, mm, ok := parseHHMM(hhmm)
	if !ok {
		return false, ""
	}
	n := now.In(loc)
	switch freq {
	case "daily":
		// fires every day
	case "weekly":
		if weekday < 0 || weekday > 6 || int(n.Weekday()) != weekday {
			return false, ""
		}
	case "monthly":
		if monthday < 1 || monthday > 31 {
			return false, ""
		}
		last := time.Date(n.Year(), n.Month()+1, 0, 0, 0, 0, 0, loc).Day()
		want := monthday
		if want > last {
			want = last // day 31 in a short month fires on the last day
		}
		if n.Day() != want {
			return false, ""
		}
	default:
		return false, "" // off / unknown
	}
	sched := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, loc)
	if n.Before(sched) {
		return false, ""
	}
	today := n.Format("2006-01-02")
	if lastRun == today {
		return false, ""
	}
	return true, today
}

// nextCadence returns the next instant (in loc) at which the cadence will fire strictly after `now`,
// and ok=false for an unfireable rule (bad time / out-of-range weekday|monthday / off/unknown freq).
// It is a display helper for the "next run" label — the actual firing is decided by cadenceDue on
// the tick, so this only needs to agree on the *next* occurrence, not re-implement the period guard.
func nextCadence(freq, hhmm string, weekday, monthday int, now time.Time, loc *time.Location) (time.Time, bool) {
	hh, mm, ok := parseHHMM(hhmm)
	if !ok {
		return time.Time{}, false
	}
	n := now.In(loc)
	switch freq {
	case "daily":
		t := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, loc)
		if !t.After(n) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	case "weekly":
		if weekday < 0 || weekday > 6 {
			return time.Time{}, false
		}
		// days until the target weekday (0..6); 0 = today, then require the time to be in the future.
		delta := (weekday - int(n.Weekday()) + 7) % 7
		t := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, loc).AddDate(0, 0, delta)
		if !t.After(n) {
			t = t.AddDate(0, 0, 7)
		}
		return t, true
	case "monthly":
		if monthday < 1 || monthday > 31 {
			return time.Time{}, false
		}
		t := atClamped(n.Year(), n.Month(), monthday, hh, mm, loc)
		if !t.After(n) {
			t = atClamped(n.Year(), n.Month()+1, monthday, hh, mm, loc)
		}
		return t, true
	}
	return time.Time{}, false
}
