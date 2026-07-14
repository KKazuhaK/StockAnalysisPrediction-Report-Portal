package app

import (
	"encoding/json"
	"strconv"
	"time"
)

// Storage cleanup engine (docs/adr/0017-storage-cleanup.md). A second always-on ticker (the first
// is scheduleLoop, ADR 0007) runs an admin-configured retention pass daily/weekly/monthly at a
// preset time; the same runCleanup path also backs the manual "clean now" and (as a count-only
// preview) the dry-run. All config + the last-run stamp/summary live in the meta table; each real
// pass is recorded in cleanup_runs. Every target ships disabled; reports are additionally
// fail-closed and floored (see cleanup_store.go).

// cleanupConfig is the resolved, floor-clamped cleanup configuration.
type cleanupConfig struct {
	Freq     string // off|daily|weekly|monthly
	Time     string // "HH:MM" in the panel timezone
	Weekday  int    // 0=Sun..6=Sat (weekly)
	Monthday int    // 1..31, clamped to month length (monthly)

	BatchEnabled    bool
	BatchDays       int
	TokensEnabled   bool
	TokensGraceDays int
	ReportsEnabled  bool
	ReportsDays     int
}

// cleanupTargets selects which targets a pass acts on.
type cleanupTargets struct{ Batch, Tokens, Reports bool }

func (t cleanupTargets) any() bool { return t.Batch || t.Tokens || t.Reports }

// scheduledTargets is the set of targets a scheduled pass would act on (those enabled in config).
func (c cleanupConfig) scheduledTargets() cleanupTargets {
	return cleanupTargets{Batch: c.BatchEnabled, Tokens: c.TokensEnabled, Reports: c.ReportsEnabled}
}

// cleanupResult is the outcome of one pass (also the JSON returned by run/preview and the
// last-result blob stored in meta).
type cleanupResult struct {
	At         string `json:"at"`      // UTC RFC3339 instant the pass ran
	Trigger    string `json:"trigger"` // "schedule" | "manual" | "preview"
	DryRun     bool   `json:"dry_run"`
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	Batch      int64  `json:"batch"`
	Tokens     int64  `json:"tokens"`
	Reports    int64  `json:"reports"`
	DurationMs int64  `json:"duration_ms"`
}

// note folds a per-target error into the result, keeping OK=false sticky.
func (r *cleanupResult) note(err error) {
	if err == nil {
		return
	}
	r.OK = false
	if r.Error != "" {
		r.Error += "; "
	}
	r.Error += err.Error()
}

// cleanupConfigLoad reads the cleanup config from meta and clamps retentions to their floors on
// read, so a hand-edited meta value can never bypass a safety floor.
func (s *Server) cleanupConfigLoad() cleanupConfig {
	atoi := func(k string, def int) int {
		if n, err := strconv.Atoi(s.st.GetSetting(k, "")); err == nil {
			return n
		}
		return def
	}
	c := cleanupConfig{
		Freq:            s.st.GetSetting("cleanup_schedule_freq", "off"),
		Time:            s.st.GetSetting("cleanup_schedule_time", "03:00"),
		Weekday:         atoi("cleanup_schedule_weekday", 1),
		Monthday:        atoi("cleanup_schedule_monthday", 1),
		BatchEnabled:    s.st.GetSetting("cleanup_batch_enabled", "0") == "1",
		BatchDays:       atoi("cleanup_batch_days", 90),
		TokensEnabled:   s.st.GetSetting("cleanup_tokens_enabled", "0") == "1",
		TokensGraceDays: atoi("cleanup_tokens_grace_days", 30),
		ReportsEnabled:  s.st.GetSetting("cleanup_reports_enabled", "0") == "1",
		ReportsDays:     atoi("cleanup_reports_days", 730),
	}
	if c.BatchDays < minBatchRetentionDays {
		c.BatchDays = minBatchRetentionDays
	}
	if c.ReportsDays < minReportsRetentionDays {
		c.ReportsDays = minReportsRetentionDays
	}
	if c.TokensGraceDays < 0 {
		c.TokensGraceDays = 0
	}
	return c
}

// cutoffs returns the retention cutoffs for a pass. batch/token compare against finished_at /
// expires_at, which are written by nowStr() in the SYSTEM-local wall clock, so their cutoffs are
// formatted from system-local time (NOT the panel timezone — a panel/container tz mismatch would
// otherwise delete up to the offset short of the configured retention). reports compares against a
// parsed UTC instant, so its cutoff is a UTC time.Time. now must be time.Now().
func (c cleanupConfig) cutoffs(now time.Time) (batchCut, tokenCut string, reportsCut time.Time) {
	batchCut = now.AddDate(0, 0, -c.BatchDays).Format("2006-01-02 15:04:05")
	tokenCut = now.AddDate(0, 0, -c.TokensGraceDays).Format("2006-01-02 15:04:05")
	reportsCut = now.UTC().AddDate(0, 0, -c.ReportsDays)
	return
}

// cleanupDue reports whether a scheduled cleanup should fire now and the YYYY-MM-DD period-stamp to
// record if it does — a thin adapter over the shared daily/weekly/monthly cadence engine
// (cadenceDue, cadence.go), which the recurring-task scheduler (ADR 0018) also uses.
func cleanupDue(c cleanupConfig, lastRun string, now time.Time, loc *time.Location) (bool, string) {
	return cadenceDue(c.Freq, c.Time, c.Weekday, c.Monthday, lastRun, now, loc)
}

// cleanupLoop is the second always-on ticker (amends ADR 0007's "only always-on timer"): once a
// minute it checks whether a scheduled cleanup is due. It runs for the process lifetime.
func (s *Server) cleanupLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		s.cleanupTick()
	}
}

// cleanupTick fires a scheduled cleanup when the cadence is due. It stamps the period BEFORE running
// so a slow pass or a crash mid-run can't cause the 60s ticker to re-fire the same day (a missed
// pass simply waits for the next period — cleanup is stateless).
func (s *Server) cleanupTick() {
	c := s.cleanupConfigLoad()
	due, stamp := cleanupDue(c, s.st.GetSetting("cleanup_last_run_period", ""), time.Now(), s.panelLocation())
	if !due {
		return
	}
	s.st.SetSetting("cleanup_last_run_period", stamp)
	if sel := c.scheduledTargets(); sel.any() {
		s.runCleanup("schedule", false, sel)
	}
}

// runCleanup executes (or, when dryRun, counts) the selected targets at the current floor-clamped
// retention, serialized by cleanupMu so a scheduled pass and a manual "clean now" never overlap.
// The selection is the gate: the scheduler passes the enabled targets, a manual run passes the
// admin's explicit choice. A real (non-dry) pass is recorded in cleanup_runs and its summary stored
// in meta; a preview records nothing.
func (s *Server) runCleanup(trigger string, dryRun bool, sel cleanupTargets) cleanupResult {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()

	start := time.Now()
	c := s.cleanupConfigLoad()
	batchCut, tokenCut, reportsCut := c.cutoffs(start)
	res := cleanupResult{Trigger: trigger, DryRun: dryRun, OK: true}

	if sel.Batch {
		var n int64
		var err error
		if dryRun {
			n, err = s.st.CountFinishedJobsBefore(batchCut)
		} else {
			n, err = s.st.DeleteFinishedJobsBefore(batchCut)
		}
		res.Batch, _ = n, err
		res.note(err)
	}
	if sel.Tokens {
		var n int64
		var err error
		if dryRun {
			n, err = s.st.CountExpiredTokensBefore(tokenCut)
		} else {
			n, err = s.st.DeleteExpiredTokensBefore(tokenCut)
		}
		res.Tokens, _ = n, err
		res.note(err)
	}
	if sel.Reports {
		cutoff := reportsCut
		var n int64
		var err error
		if dryRun {
			n, err = s.st.CountReportsIngestedBefore(cutoff)
		} else {
			n, err = s.st.DeleteReportsIngestedBefore(cutoff)
		}
		res.Reports, _ = n, err
		res.note(err)
	}

	res.At = start.UTC().Format(time.RFC3339)
	res.DurationMs = time.Since(start).Milliseconds()

	if !dryRun {
		s.st.InsertCleanupRun(CleanupRun{
			RanAt: nowStr(), Trigger: trigger, DryRun: false, OK: res.OK, Error: res.Error,
			BatchDeleted: res.Batch, TokensDeleted: res.Tokens, ReportsDeleted: res.Reports,
			DurationMs: res.DurationMs,
		})
		if b, err := json.Marshal(res); err == nil {
			s.st.SetSetting("cleanup_last_result", string(b))
		}
	}
	return res
}
