package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// cleanupDue fires at most once per matching day, gated by frequency, weekday/month-day, the
// scheduled time, and the last-run stamp (restart double-fire guard).
func TestCleanupDue(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 7, 13, 3, 30, 0, 0, loc) // a Monday, 03:30
	daily := cleanupConfig{Freq: "daily", Time: "03:00"}

	cases := []struct {
		name    string
		c       cleanupConfig
		lastRun string
		now     time.Time
		want    bool
	}{
		{"off", cleanupConfig{Freq: "off", Time: "03:00"}, "", base, false},
		{"daily due", daily, "", base, true},
		{"daily before time", daily, "", time.Date(2026, 7, 13, 2, 30, 0, 0, loc), false},
		{"daily already ran today", daily, "2026-07-13", base, false},
		{"daily ran yesterday", daily, "2026-07-12", base, true},
		{"weekly matching weekday", cleanupConfig{Freq: "weekly", Time: "03:00", Weekday: int(base.Weekday())}, "", base, true},
		{"weekly other weekday", cleanupConfig{Freq: "weekly", Time: "03:00", Weekday: (int(base.Weekday()) + 1) % 7}, "", base, false},
		{"monthly matching day", cleanupConfig{Freq: "monthly", Time: "03:00", Monthday: 13}, "", base, true},
		{"monthly other day", cleanupConfig{Freq: "monthly", Time: "03:00", Monthday: 14}, "", base, false},
		{"monthly day-31 clamps to Feb 28", cleanupConfig{Freq: "monthly", Time: "03:00", Monthday: 31}, "", time.Date(2026, 2, 28, 4, 0, 0, 0, loc), true},
		{"bad time", cleanupConfig{Freq: "daily", Time: "nope"}, "", base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stamp := cleanupDue(tc.c, tc.lastRun, tc.now, loc)
			if got != tc.want {
				t.Fatalf("cleanupDue = %v; want %v", got, tc.want)
			}
			if got && stamp != tc.now.In(loc).Format("2006-01-02") {
				t.Errorf("stamp = %q; want today's date", stamp)
			}
		})
	}
}

// A scheduled pass acts only on the enabled targets, records a cleanup_runs row + last-result blob,
// and leaves disabled targets untouched.
func TestRunCleanupRespectsToggles(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	// Only batch is enabled.
	st.SetSetting("cleanup_batch_enabled", "1")
	st.SetSetting("cleanup_batch_days", "90")
	// tokens + reports disabled (defaults).
	seedJob(t, st, "finished", agoLocal(120))
	st.CreateToken("tok", "n", "all", agoLocal(120))
	st.UpsertReport(Rep{UID: "r1", Symbol: "600000", Date: "2020-01-01", RType: "x",
		Time: time.Now().UTC().AddDate(0, 0, -900).Format(time.RFC3339)})

	res := s.runCleanup("manual", false, s.cleanupConfigLoad().scheduledTargets())
	if res.Batch != 1 {
		t.Errorf("batch deleted = %d; want 1", res.Batch)
	}
	if res.Tokens != 0 || res.Reports != 0 {
		t.Errorf("disabled targets touched: tokens=%d reports=%d", res.Tokens, res.Reports)
	}
	if countRows(t, st, "api_tokens") != 1 || countRows(t, st, "reports") != 1 {
		t.Errorf("disabled-target rows were deleted")
	}
	if countRows(t, st, "cleanup_runs") != 1 {
		t.Errorf("expected one cleanup_runs audit row, got %d", countRows(t, st, "cleanup_runs"))
	}
	if st.GetSetting("cleanup_last_result", "") == "" {
		t.Errorf("cleanup_last_result blob not written")
	}
}

// The reports retention is clamped to its floor on read: a hand-set below-floor value can't delete
// recent reports.
func TestRunCleanupReportsFloorClamp(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	st.SetSetting("cleanup_reports_enabled", "1")
	st.SetSetting("cleanup_reports_days", "5") // below the 365 floor
	st.UpsertReport(Rep{UID: "r-6d", Symbol: "600000", Date: "2026-07-01", RType: "x",
		Time: time.Now().UTC().AddDate(0, 0, -6).Format(time.RFC3339)})

	res := s.runCleanup("manual", false, cleanupTargets{Reports: true})
	if res.Reports != 0 {
		t.Errorf("reports deleted = %d; want 0 (floor clamp protects a 6-day-old report)", res.Reports)
	}
	if countRows(t, st, "reports") != 1 {
		t.Errorf("a recent report was deleted despite the floor")
	}
}

// A dry run counts but deletes nothing and records no audit row.
func TestRunCleanupDryRun(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	st.SetSetting("cleanup_batch_enabled", "1")
	seedJob(t, st, "finished", agoLocal(120))

	res := s.runCleanup("preview", true, cleanupTargets{Batch: true})
	if res.Batch != 1 {
		t.Errorf("dry-run batch count = %d; want 1", res.Batch)
	}
	if countRows(t, st, "batch_jobs") != 1 {
		t.Errorf("dry run deleted rows")
	}
	if countRows(t, st, "cleanup_runs") != 0 {
		t.Errorf("dry run recorded an audit row")
	}
}

// The config save rejects a below-floor retention and leaves the stored value unchanged.
func TestApiCleanupConfigSaveRejectsBelowFloor(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "k"}}
	req := httptest.NewRequest("POST", "/api/admin/cleanup/config", strings.NewReader(`{"reports_days":5}`))
	rec := httptest.NewRecorder()
	s.apiCleanupConfigSave(rec, req, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if got := st.GetSetting("cleanup_reports_days", ""); got != "" {
		t.Errorf("reports_days was persisted (%q) despite being below the floor", got)
	}
}
