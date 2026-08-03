package app

import (
	"testing"
	"time"
)

// A run quota over a period, not only a day.
//
// The cap was runs-per-day and nothing else, which does not fit how the limits are actually sold:
// a client on a monthly retainer, or a trial with a fixed total. The counting query already took a
// "since", so the whole change is which instant that is — and one of the four periods has no
// "since" at all.
//
// Boundaries are computed in the PANEL timezone, like the daily one already was: a quota that
// resets at the server's midnight rather than the business's is wrong for exactly the people it
// applies to.

func quotaServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	s.st.SetSetting("timezone", "Asia/Shanghai")
	return s
}

func TestQuotaPeriodStart(t *testing.T) {
	s := quotaServer(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// A Wednesday, mid-afternoon.
	now := time.Date(2026, 8, 5, 15, 30, 0, 0, loc)

	for _, tc := range []struct {
		period string
		want   time.Time
	}{
		{QuotaDay, time.Date(2026, 8, 5, 0, 0, 0, 0, loc)},
		// Weeks start on Monday: the ISO convention, and the one a business week uses.
		{QuotaWeek, time.Date(2026, 8, 3, 0, 0, 0, 0, loc)},
		{QuotaMonth, time.Date(2026, 8, 1, 0, 0, 0, 0, loc)},
	} {
		got := s.quotaPeriodStart(tc.period, now)
		if !got.Equal(tc.want) {
			t.Errorf("%s start = %s, want %s", tc.period, got.In(loc), tc.want)
		}
	}

	// "total" has no window: the cap is over the life of the account. Represented as the zero time
	// rather than as a very old date, so the caller can tell "no floor" from "a floor long ago".
	if got := s.quotaPeriodStart(QuotaTotal, now); !got.IsZero() {
		t.Errorf("total start = %s, want the zero time", got)
	}
	// An unrecognized value is a day — the pre-existing behaviour, and the narrowest window, so a
	// hand-edited row cannot accidentally widen someone's allowance.
	if got := s.quotaPeriodStart("fortnight", now); !got.Equal(time.Date(2026, 8, 5, 0, 0, 0, 0, loc)) {
		t.Errorf("unknown period did not fall back to the day: %s", got.In(loc))
	}
}

// Sunday is the trap: Go's Weekday() makes it 0, so a naive subtraction puts the week start six
// days in the FUTURE.
func TestQuotaWeekStartsOnMondayEvenOnSunday(t *testing.T) {
	s := quotaServer(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	sunday := time.Date(2026, 8, 9, 10, 0, 0, 0, loc)
	want := time.Date(2026, 8, 3, 0, 0, 0, 0, loc)
	if got := s.quotaPeriodStart(QuotaWeek, sunday); !got.Equal(want) {
		t.Errorf("Sunday's week start = %s, want %s", got.In(loc), want)
	}
}

// The counting end: RunsToday already floors at the account's own creation, and "total" must keep
// that floor rather than counting a previous holder of the username.
func TestTotalQuotaStillExcludesAPreviousHolder(t *testing.T) {
	s := quotaServer(t)
	st := s.st
	const name = "client@corp.example"
	st.UpsertUser(User{Username: name, PasswordHash: "h", Role: "user"})
	st.CreateBatchJob(7, 1, 0, name, []map[string]string{{"symbol": "600519"}}, "")
	stamp := func(ago time.Duration) string { return time.Now().Add(-ago).Format("2006-01-02 15:04:05") }
	st.exec("UPDATE users SET created_at=? WHERE username=?", stamp(48*time.Hour), name)
	st.exec("UPDATE batch_jobs SET created_at=? WHERE created_by=?", stamp(24*time.Hour), name)

	// Over the account's whole life, that run counts.
	if got := st.RunsToday(name, time.Time{}); got != 1 {
		t.Errorf("lifetime usage = %d, want 1", got)
	}
	// After the account is replaced, the new holder starts from zero even on a lifetime cap.
	st.DeleteUser(name)
	st.UpsertUser(User{Username: name, PasswordHash: "h2", Role: "user"})
	if got := st.RunsToday(name, time.Time{}); got != 0 {
		t.Errorf("the new holder inherits %d runs on a lifetime cap, want 0", got)
	}
}

// The period is inherited WITH the number it belongs to, not separately. Resolving them apart would
// let a cap of "20" from one OU meet a period of "month" from another — a limit neither configured.
func TestQuotaPeriodTravelsWithTheNumber(t *testing.T) {
	s := quotaServer(t)
	st := s.st
	root := st.EnsureDefaultGroup()
	st.SetGroupDailyQuota(root, quotaPtr(20), QuotaDay)

	child, _ := st.CreateUserGroup("clients", "", 0)
	st.SetGroupParent(child, root)
	st.UpsertUser(User{Username: "c", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("c", child)

	// The child sets nothing: it inherits both halves from the root.
	if eff := st.EffectiveGroupSettings("c"); eff.DailyRunQuota != 20 || eff.QuotaPeriod != QuotaDay {
		t.Errorf("inherited %d per %q, want 20 per day", eff.DailyRunQuota, eff.QuotaPeriod)
	}
	// The child overrides: BOTH halves come from the child, never one from each.
	st.SetGroupDailyQuota(child, quotaPtr(5), QuotaMonth)
	if eff := st.EffectiveGroupSettings("c"); eff.DailyRunQuota != 5 || eff.QuotaPeriod != QuotaMonth {
		t.Errorf("overrode to %d per %q, want 5 per month", eff.DailyRunQuota, eff.QuotaPeriod)
	}
}

// The row an UPGRADE produces: a cap written by the old binary, so the number is set and the period
// column is NULL. Resolution has to read that as "this OU says day", not as "this OU says nothing
// about the period" — otherwise the number comes from here and the window comes from an ancestor,
// which is the mixed cap the whole design exists to prevent, and it tightens a 5-per-day child to
// 5-per-month the moment an admin puts a monthly cap on the parent.
func TestLegacyQuotaRowDoesNotBorrowAnAncestorsWindow(t *testing.T) {
	s := quotaServer(t)
	st := s.st
	root := st.EnsureDefaultGroup()
	st.SetGroupDailyQuota(root, quotaPtr(100), QuotaMonth)

	child, _ := st.CreateUserGroup("clients", "", 0)
	st.SetGroupParent(child, root)
	st.UpsertUser(User{Username: "c", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("c", child)

	// Exactly what ensureColumns leaves behind: the cap the old binary wrote, no period.
	st.exec("UPDATE user_groups SET daily_run_quota=5, run_quota_period=NULL WHERE id=?", child)

	eff := st.EffectiveGroupSettings("c")
	if eff.DailyRunQuota != 5 {
		t.Fatalf("quota = %d, want the child's 5", eff.DailyRunQuota)
	}
	if eff.QuotaPeriod != QuotaDay {
		t.Errorf("period = %q, want %q — the child's own row is what set the number, so it sets the window too",
			eff.QuotaPeriod, QuotaDay)
	}
}

// The reset instant the 429 and the run form show. A lifetime cap never refills, so there is no
// date to print and the API must say so rather than invent one.
func TestQuotaResetsAt(t *testing.T) {
	s := quotaServer(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 8, 5, 15, 0, 0, 0, loc) // a Wednesday

	for _, tc := range []struct{ period, want string }{
		{QuotaDay, "2026-08-06"},
		{QuotaWeek, "2026-08-10"}, // the following Monday
		{QuotaMonth, "2026-09-01"},
	} {
		got := s.quotaResetsAt(tc.period, now)
		parsed, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Fatalf("%s: %q is not an instant: %v", tc.period, got, err)
		}
		if d := parsed.In(loc).Format("2006-01-02"); d != tc.want {
			t.Errorf("%s resets %s, want %s", tc.period, d, tc.want)
		}
	}
	if got := s.quotaResetsAt(QuotaTotal, now); got != "" {
		t.Errorf("a lifetime cap reported a reset instant %q", got)
	}
}
