package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// ---------- helpers ----------

// ut builds a UTC instant for the tables below (the resolver tests run with loc = UTC).
func ut(y, mo, d, h, mi int) time.Time { return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.UTC) }

// jobRow reads a job's status + window fields directly (in-package test access).
func jobRow(t *testing.T, st *Store, id int64) (status, runAt, runPreset string) {
	t.Helper()
	if err := st.queryRow("SELECT status, COALESCE(run_at,''), COALESCE(run_preset,'') FROM batch_jobs WHERE id=?", id).
		Scan(&status, &runAt, &runPreset); err != nil {
		t.Fatalf("read job %d: %v", id, err)
	}
	return
}

// postCode posts body to a handler (as admin) and returns the HTTP status code.
func postCode(t *testing.T, h func(http.ResponseWriter, *http.Request, string), body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), "admin")
	return rec.Code
}

// ---------- pure resolver ----------

// nextWindow resolves the current-or-next occurrence [start,end] of a preset window, interpreting
// anchors in the panel timezone. It handles windows that wrap the period boundary (stop before
// start) and clamps invalid month days (day 31 in a short month, 2/29 in a non-leap year).
// weekday: 0=Sun..6=Sat (Go's convention).
func TestNextWindow(t *testing.T) {
	cases := []struct {
		name         string
		freq         string
		start, stop  presetAnchor
		now          time.Time
		wantS, wantE time.Time
		wantOK       bool
	}{
		// daily 00:30–08:30
		{"daily/next", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 9, 0, 30), ut(2026, 7, 9, 8, 30), true},
		{"daily/inside", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 5, 0), ut(2026, 7, 8, 0, 30), ut(2026, 7, 8, 8, 30), true},
		{"daily/before-today", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 0, 0), ut(2026, 7, 8, 0, 30), ut(2026, 7, 8, 8, 30), true},
		// daily wrap 22:00–06:00 (crosses midnight)
		{"daily/wrap-inside", "daily", presetAnchor{Time: "22:00"}, presetAnchor{Time: "06:00"},
			ut(2026, 7, 8, 3, 0), ut(2026, 7, 7, 22, 0), ut(2026, 7, 8, 6, 0), true},
		{"daily/wrap-next", "daily", presetAnchor{Time: "22:00"}, presetAnchor{Time: "06:00"},
			ut(2026, 7, 8, 12, 0), ut(2026, 7, 8, 22, 0), ut(2026, 7, 9, 6, 0), true},
		// weekly Mon 09:00 – Mon 18:00 (same weekday, wraps within the day)
		{"weekly/same-weekday", "weekly", presetAnchor{Weekday: 1, Time: "09:00"}, presetAnchor{Weekday: 1, Time: "18:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 13, 9, 0), ut(2026, 7, 13, 18, 0), true},
		// weekly Mon 09:00 – Wed 18:00 (spans days), submitted Mon before open
		{"weekly/span", "weekly", presetAnchor{Weekday: 1, Time: "09:00"}, presetAnchor{Weekday: 3, Time: "18:00"},
			ut(2026, 7, 6, 8, 0), ut(2026, 7, 6, 9, 0), ut(2026, 7, 8, 18, 0), true},
		// weekly Fri 20:00 – Mon 06:00 (wraps the week)
		{"weekly/wrap", "weekly", presetAnchor{Weekday: 5, Time: "20:00"}, presetAnchor{Weekday: 1, Time: "06:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 10, 20, 0), ut(2026, 7, 13, 6, 0), true},
		// monthly day1 09:00 – day5 18:00 (this month already passed → next month)
		{"monthly/next", "monthly", presetAnchor{Day: 1, Time: "09:00"}, presetAnchor{Day: 5, Time: "18:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 8, 1, 9, 0), ut(2026, 8, 5, 18, 0), true},
		// monthly day31 → clamps to Feb 28 (2026 is not a leap year)
		{"monthly/clamp-short", "monthly", presetAnchor{Day: 31, Time: "09:00"}, presetAnchor{Day: 31, Time: "10:00"},
			ut(2026, 2, 15, 0, 0), ut(2026, 2, 28, 9, 0), ut(2026, 2, 28, 10, 0), true},
		// yearly Feb 29 in a leap year stays Feb 29
		{"yearly/leap-ok", "yearly", presetAnchor{Month: 2, Day: 29, Time: "09:00"}, presetAnchor{Month: 2, Day: 29, Time: "10:00"},
			ut(2024, 1, 1, 0, 0), ut(2024, 2, 29, 9, 0), ut(2024, 2, 29, 10, 0), true},
		// yearly Feb 29, next occurrence 2027 (non-leap) clamps to Feb 28
		{"yearly/nonleap-clamp", "yearly", presetAnchor{Month: 2, Day: 29, Time: "09:00"}, presetAnchor{Month: 2, Day: 29, Time: "10:00"},
			ut(2026, 6, 1, 0, 0), ut(2027, 2, 28, 9, 0), ut(2027, 2, 28, 10, 0), true},
		// yearly Dec 20 – Jan 10 (wraps the year boundary)
		{"yearly/wrap", "yearly", presetAnchor{Month: 12, Day: 20, Time: "00:00"}, presetAnchor{Month: 1, Day: 10, Time: "00:00"},
			ut(2026, 7, 8, 0, 0), ut(2026, 12, 20, 0, 0), ut(2027, 1, 10, 0, 0), true},
		// malformed specs
		{"bad/freq", "hourly", presetAnchor{Time: "00:00"}, presetAnchor{Time: "01:00"},
			ut(2026, 7, 8, 0, 0), time.Time{}, time.Time{}, false},
		{"bad/time", "daily", presetAnchor{Time: "25:00"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 0, 0), time.Time{}, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := nextWindow(c.freq, c.start, c.stop, c.now, time.UTC)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if !s.Equal(c.wantS) || !e.Equal(c.wantE) {
				t.Fatalf("window = [%s, %s], want [%s, %s]",
					s.Format(time.RFC3339), e.Format(time.RFC3339),
					c.wantS.Format(time.RFC3339), c.wantE.Format(time.RFC3339))
			}
			if !e.After(c.now) {
				t.Fatalf("end %s must be after now %s", e.Format(time.RFC3339), c.now.Format(time.RFC3339))
			}
		})
	}
}

// resolvePresetWindow turns a preset window into a run_at (window start) + a JSON snapshot
// carrying the rule + on_overrun + the occurrence end (until). Both instants are formatted in
// the local wall-clock basis the scheduler already uses; the values are computed via the panel
// timezone. We parse them back (basis-independent) and compare absolute instants.
func TestResolvePresetWindow(t *testing.T) {
	runAt, snap, ok := resolvePresetWindow("daily",
		presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"}, "next", ut(2026, 7, 8, 10, 0), time.UTC)
	if !ok {
		t.Fatal("resolvePresetWindow ok=false")
	}
	gotStart, err := time.ParseInLocation("2006-01-02 15:04:05", runAt, time.Local)
	if err != nil || !gotStart.Equal(ut(2026, 7, 9, 0, 30)) {
		t.Fatalf("run_at = %q (%v), want the 2026-07-09 00:30 instant", runAt, err)
	}
	var s runPresetSnapshot
	if err := json.Unmarshal([]byte(snap), &s); err != nil {
		t.Fatalf("snapshot not valid JSON: %v (%s)", err, snap)
	}
	if s.Freq != "daily" || s.OnOverrun != "next" || s.Start.Time != "00:30" || s.Stop.Time != "08:30" {
		t.Fatalf("snapshot rule = %+v", s)
	}
	gotEnd, err := time.ParseInLocation("2006-01-02 15:04:05", s.Until, time.Local)
	if err != nil || !gotEnd.Equal(ut(2026, 7, 9, 8, 30)) {
		t.Fatalf("until = %q (%v), want the 2026-07-09 08:30 instant", s.Until, err)
	}
	if _, _, ok := resolvePresetWindow("daily", presetAnchor{Time: "bad"}, presetAnchor{Time: "08:30"}, "next", ut(2026, 7, 8, 10, 0), time.UTC); ok {
		t.Fatal("a malformed window must resolve ok=false")
	}
}

// ---------- persistence ----------

// run_presets is an admin-managed, ordered collection (like type_config/links): CRUD + reorder
// must round-trip, and ordering is by ord then id.
func TestRunPresetsCRUD(t *testing.T) {
	st := newTestStore(t)

	id, err := st.CreateRunPreset(RunPreset{Label: "低峰期", Freq: "daily",
		StartSpec: `{"time":"00:30"}`, StopSpec: `{"time":"08:30"}`, OnOverrun: "next", Enabled: true})
	if err != nil {
		t.Fatalf("CreateRunPreset: %v", err)
	}
	id2, err := st.CreateRunPreset(RunPreset{Label: "周末", Freq: "weekly",
		StartSpec: `{"weekday":6,"time":"00:00"}`, StopSpec: `{"weekday":0,"time":"23:59"}`, OnOverrun: "cancel", Enabled: true})
	if err != nil {
		t.Fatalf("CreateRunPreset 2: %v", err)
	}

	list := st.ListRunPresets()
	if len(list) != 2 || list[0].ID != id || list[1].ID != id2 {
		t.Fatalf("ListRunPresets order = %+v, want [%d %d]", list, id, id2)
	}

	p, ok := st.GetRunPreset(id)
	if !ok || p.Label != "低峰期" || p.Freq != "daily" || p.OnOverrun != "next" || !p.Enabled || p.StartSpec != `{"time":"00:30"}` {
		t.Fatalf("GetRunPreset = %+v ok=%v", p, ok)
	}

	p.Label, p.OnOverrun, p.Enabled = "低峰期A", "continue", false
	if err := st.UpdateRunPreset(p); err != nil {
		t.Fatalf("UpdateRunPreset: %v", err)
	}
	if p2, _ := st.GetRunPreset(id); p2.Label != "低峰期A" || p2.OnOverrun != "continue" || p2.Enabled {
		t.Fatalf("after update = %+v", p2)
	}

	if err := st.ReorderRunPresets([]int64{id2, id}); err != nil {
		t.Fatalf("ReorderRunPresets: %v", err)
	}
	if list := st.ListRunPresets(); list[0].ID != id2 || list[1].ID != id {
		t.Fatalf("after reorder = [%d %d], want [%d %d]", list[0].ID, list[1].ID, id2, id)
	}

	if err := st.DeleteRunPreset(id); err != nil {
		t.Fatalf("DeleteRunPreset: %v", err)
	}
	if _, ok := st.GetRunPreset(id); ok {
		t.Fatal("deleted preset still present")
	}
	if len(st.ListRunPresets()) != 1 {
		t.Fatalf("after delete want 1 preset")
	}
}

// A job's window (run_at + run_preset snapshot) round-trips; QueuedPresetJobs surfaces only
// queued jobs that carry a window; expiring or clearing removes them from that set.
func TestJobWindowRoundTrip(t *testing.T) {
	st := newTestStore(t)
	snap := `{"freq":"daily","start":{"time":"00:30"},"stop":{"time":"08:30"},"on_overrun":"next","until":"2099-01-01 08:30:00"}`

	jobID, err := st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"k": "v"}}, "idle")
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}
	if got := st.QueuedPresetJobs(); len(got) != 0 {
		t.Fatalf("a job with no window must not be a preset job, got %d", len(got))
	}

	if err := st.SetJobWindow(jobID, "2099-01-01 00:30:00", snap); err != nil {
		t.Fatalf("SetJobWindow: %v", err)
	}
	got := st.QueuedPresetJobs()
	if len(got) != 1 || got[0].ID != jobID || got[0].RunPreset != snap || got[0].RunAt != "2099-01-01 00:30:00" {
		t.Fatalf("QueuedPresetJobs = %+v", got)
	}

	if err := st.ExpireJob(jobID); err != nil {
		t.Fatalf("ExpireJob: %v", err)
	}
	if got := st.QueuedPresetJobs(); len(got) != 0 {
		t.Fatalf("an expired job must drop out of the preset set, got %d", len(got))
	}

	jobID2, _ := st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"k": "v"}}, "normal")
	st.SetJobWindow(jobID2, "2099-01-01 00:30:00", snap)
	if err := st.ClearJobWindow(jobID2); err != nil {
		t.Fatalf("ClearJobWindow: %v", err)
	}
	if got := st.QueuedPresetJobs(); len(got) != 0 {
		t.Fatalf("a cleared job must drop out of the preset set, got %d", len(got))
	}
}

// ---------- overrun sweep ----------

// The overrun sweep, on a window that already closed with the run still queued, applies each
// preset's on_overrun policy: continue → drop the window (run ASAP), cancel → expired, next →
// roll to a future occurrence and keep waiting. Deterministic via a fixed panel tz (UTC) + now.
func TestSweepPresetOverrun(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("timezone", "UTC")
	srv := &Server{st: st}
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC) // noon → the next daily 00:30 window is tomorrow

	mk := func(overrun string) int64 {
		id, err := st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"k": "v"}}, "50")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(runPresetSnapshot{
			Freq: "daily", Start: presetAnchor{Time: "00:30"}, Stop: presetAnchor{Time: "08:30"},
			OnOverrun: overrun, Until: "2000-01-01 00:00:00", // long closed
		})
		st.SetJobWindow(id, "2000-01-01 00:00:00", string(b))
		return id
	}
	cont, next, canc := mk("continue"), mk("next"), mk("cancel")

	srv.sweepPresetWindowsLocked(now)

	if status, ra, rp := jobRow(t, st, cont); status != "queued" || ra != "" || rp != "" {
		t.Fatalf("continue: got status=%q run_at=%q run_preset=%q, want queued with cleared window", status, ra, rp)
	}
	if status, _, _ := jobRow(t, st, canc); status != "expired" {
		t.Fatalf("cancel: status=%q, want expired", status)
	}

	status, ra, rp := jobRow(t, st, next)
	if status != "queued" {
		t.Fatalf("next: status=%q, want queued", status)
	}
	rolled, err := time.ParseInLocation("2006-01-02 15:04:05", ra, time.Local)
	if err != nil || !rolled.After(now) {
		t.Fatalf("next: run_at=%q should roll to a future instant (%v)", ra, err)
	}
	var s runPresetSnapshot
	if json.Unmarshal([]byte(rp), &s) != nil || s.OnOverrun != "next" || s.Freq != "daily" {
		t.Fatalf("next: snapshot lost its rule: %s", rp)
	}
	if until, _ := time.ParseInLocation("2006-01-02 15:04:05", s.Until, time.Local); !until.After(now) {
		t.Fatalf("next: until=%q should be in the future", s.Until)
	}

	if q := st.QueuedPresetJobs(); len(q) != 1 || q[0].ID != next {
		t.Fatalf("QueuedPresetJobs after sweep = %+v, want only the rolled job %d", q, next)
	}
}

// ---------- HTTP surface ----------

// End-to-end through the handlers: create a daily low-peak preset, submit a run into it, and
// confirm the create wiring snapshots the resolved window (run_at + run_preset) onto the job.
// Whether the job runs now or waits depends on the wall clock (a run submitted inside the window
// starts immediately) — that time-sensitive behavior is covered deterministically by the pure
// resolver/sweep tests; here we only assert the seam. A stub provider keeps any immediate run
// clean. Also covers the preset CRUD surface + validation.
func TestRunPresetSchedulesJob(t *testing.T) {
	srv := batchServer(t)
	srv.st.SetSetting("timezone", "UTC")
	srv.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return provFn(func(context.Context, map[string]string) (batch.RunResult, error) {
			return batch.RunResult{Status: batch.Ok}, nil
		}), nil
	}
	post(t, srv.apiBatchPluginImport, batchTestSpec)
	added := post(t, srv.apiBatchTargetAdd, `{"plugin_slug":"p","name":"t","config":{"base_url":"http://example.invalid"}}`)
	targetID := int64(added["id"].(float64))

	pr := post(t, srv.apiRunPresetCreate,
		`{"label":"低峰期","freq":"daily","start":{"time":"00:30"},"stop":{"time":"08:30"},"on_overrun":"next","enabled":true}`)
	presetID := int64(pr["id"].(float64))

	if ps, _ := post(t, srv.apiRunPresets, "{}")["presets"].([]any); len(ps) != 1 {
		t.Fatalf("apiRunPresets returned %d presets, want 1", len(ps))
	}

	created := post(t, srv.apiBatchJobCreate, fmt.Sprintf(
		`{"target_id":%d,"preset_id":%d,"rows":[{"code":"a"}]}`, targetID, presetID))
	jobID := int64(created["job_id"].(float64))

	_, runAt, runPreset := jobRow(t, srv.st, jobID)
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", runAt, time.Local); runAt == "" || err != nil {
		t.Fatalf("preset job run_at should be a resolved window start, got %q (%v)", runAt, err)
	}
	var snap runPresetSnapshot
	if json.Unmarshal([]byte(runPreset), &snap) != nil || snap.Freq != "daily" || snap.OnOverrun != "next" || snap.Start.Time != "00:30" || snap.Until == "" {
		t.Fatalf("run_preset snapshot lost its rule: %s", runPreset)
	}

	if code := postCode(t, srv.apiRunPresetCreate, `{"label":"x","freq":"daily","start":{"time":"99:99"},"stop":{"time":"08:30"}}`); code != 400 {
		t.Fatalf("bad preset window should 400, got %d", code)
	}
}
