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

// iv builds a daily-style interval (time-only anchors) for the union tests.
func iv(a, b string) presetInterval {
	return presetInterval{Start: presetAnchor{Time: a}, Stop: presetAnchor{Time: b}}
}

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

func parseLocal(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// ---------- single sub-window resolver ----------

// nextInterval resolves one sub-window's current-or-next occurrence, interpreting anchors in the
// panel timezone. It handles windows that wrap the period boundary (stop before start) and clamps
// invalid month days (day 31 in a short month, 2/29 in a non-leap year). weekday: 0=Sun..6=Sat.
func TestNextInterval(t *testing.T) {
	cases := []struct {
		name         string
		freq         string
		start, stop  presetAnchor
		now          time.Time
		wantS, wantE time.Time
		wantOK       bool
	}{
		{"daily/next", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 9, 0, 30), ut(2026, 7, 9, 8, 30), true},
		{"daily/inside", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 5, 0), ut(2026, 7, 8, 0, 30), ut(2026, 7, 8, 8, 30), true},
		{"daily/before-today", "daily", presetAnchor{Time: "00:30"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 0, 0), ut(2026, 7, 8, 0, 30), ut(2026, 7, 8, 8, 30), true},
		{"daily/wrap-inside", "daily", presetAnchor{Time: "22:00"}, presetAnchor{Time: "06:00"},
			ut(2026, 7, 8, 3, 0), ut(2026, 7, 7, 22, 0), ut(2026, 7, 8, 6, 0), true},
		{"daily/wrap-next", "daily", presetAnchor{Time: "22:00"}, presetAnchor{Time: "06:00"},
			ut(2026, 7, 8, 12, 0), ut(2026, 7, 8, 22, 0), ut(2026, 7, 9, 6, 0), true},
		{"weekly/same-weekday", "weekly", presetAnchor{Weekday: 1, Time: "09:00"}, presetAnchor{Weekday: 1, Time: "18:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 13, 9, 0), ut(2026, 7, 13, 18, 0), true},
		{"weekly/span", "weekly", presetAnchor{Weekday: 1, Time: "09:00"}, presetAnchor{Weekday: 3, Time: "18:00"},
			ut(2026, 7, 6, 8, 0), ut(2026, 7, 6, 9, 0), ut(2026, 7, 8, 18, 0), true},
		{"weekly/wrap", "weekly", presetAnchor{Weekday: 5, Time: "20:00"}, presetAnchor{Weekday: 1, Time: "06:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 7, 10, 20, 0), ut(2026, 7, 13, 6, 0), true},
		{"monthly/next", "monthly", presetAnchor{Day: 1, Time: "09:00"}, presetAnchor{Day: 5, Time: "18:00"},
			ut(2026, 7, 8, 10, 0), ut(2026, 8, 1, 9, 0), ut(2026, 8, 5, 18, 0), true},
		{"monthly/clamp-short", "monthly", presetAnchor{Day: 31, Time: "09:00"}, presetAnchor{Day: 31, Time: "10:00"},
			ut(2026, 2, 15, 0, 0), ut(2026, 2, 28, 9, 0), ut(2026, 2, 28, 10, 0), true},
		{"yearly/leap-ok", "yearly", presetAnchor{Month: 2, Day: 29, Time: "09:00"}, presetAnchor{Month: 2, Day: 29, Time: "10:00"},
			ut(2024, 1, 1, 0, 0), ut(2024, 2, 29, 9, 0), ut(2024, 2, 29, 10, 0), true},
		{"yearly/nonleap-clamp", "yearly", presetAnchor{Month: 2, Day: 29, Time: "09:00"}, presetAnchor{Month: 2, Day: 29, Time: "10:00"},
			ut(2026, 6, 1, 0, 0), ut(2027, 2, 28, 9, 0), ut(2027, 2, 28, 10, 0), true},
		{"yearly/wrap", "yearly", presetAnchor{Month: 12, Day: 20, Time: "00:00"}, presetAnchor{Month: 1, Day: 10, Time: "00:00"},
			ut(2026, 7, 8, 0, 0), ut(2026, 12, 20, 0, 0), ut(2027, 1, 10, 0, 0), true},
		{"bad/freq", "hourly", presetAnchor{Time: "00:00"}, presetAnchor{Time: "01:00"},
			ut(2026, 7, 8, 0, 0), time.Time{}, time.Time{}, false},
		{"bad/time", "daily", presetAnchor{Time: "25:00"}, presetAnchor{Time: "08:30"},
			ut(2026, 7, 8, 0, 0), time.Time{}, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := nextInterval(c.freq, c.start, c.stop, c.now, time.UTC)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if !s.Equal(c.wantS) || !e.Equal(c.wantE) {
				t.Fatalf("window = [%s, %s], want [%s, %s]",
					s.Format(time.RFC3339), e.Format(time.RFC3339), c.wantS.Format(time.RFC3339), c.wantE.Format(time.RFC3339))
			}
		})
	}
}

// nextWindow picks, across a preset's union of sub-windows, the occurrence whose end is the
// earliest still after now — the next moment the run is eligible (a split low-peak 9-12 & 14-18).
func TestNextWindowUnion(t *testing.T) {
	ivs := []presetInterval{iv("09:00", "12:00"), iv("14:00", "18:00")}
	cases := []struct {
		name         string
		now          time.Time
		wantS, wantE time.Time
	}{
		{"before-both", ut(2026, 7, 8, 8, 0), ut(2026, 7, 8, 9, 0), ut(2026, 7, 8, 12, 0)},
		{"inside-first", ut(2026, 7, 8, 10, 0), ut(2026, 7, 8, 9, 0), ut(2026, 7, 8, 12, 0)},
		{"between", ut(2026, 7, 8, 12, 30), ut(2026, 7, 8, 14, 0), ut(2026, 7, 8, 18, 0)},
		{"inside-second", ut(2026, 7, 8, 15, 0), ut(2026, 7, 8, 14, 0), ut(2026, 7, 8, 18, 0)},
		{"after-both", ut(2026, 7, 8, 19, 0), ut(2026, 7, 9, 9, 0), ut(2026, 7, 9, 12, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := nextWindow("daily", ivs, c.now, time.UTC)
			if !ok || !s.Equal(c.wantS) || !e.Equal(c.wantE) {
				t.Fatalf("union = [%s,%s] ok=%v, want [%s,%s]", s.Format(time.RFC3339), e.Format(time.RFC3339), ok, c.wantS.Format(time.RFC3339), c.wantE.Format(time.RFC3339))
			}
		})
	}
	if _, _, ok := nextWindow("daily", nil, ut(2026, 7, 8, 8, 0), time.UTC); ok {
		t.Fatal("empty union must resolve ok=false")
	}
}

// samePeriod distinguishes "another sub-window later this period" (auto-advance) from "period
// exhausted" (apply on_overrun) for each frequency.
func TestSamePeriod(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		freq string
		a, b time.Time
		want bool
	}{
		{"daily", ut(2026, 7, 8, 1, 0), ut(2026, 7, 8, 23, 0), true},
		{"daily", ut(2026, 7, 8, 23, 0), ut(2026, 7, 9, 1, 0), false},
		{"weekly", ut(2026, 7, 6, 0, 0), ut(2026, 7, 12, 0, 0), true},  // Mon..Sun, same ISO week
		{"weekly", ut(2026, 7, 12, 0, 0), ut(2026, 7, 13, 0, 0), false}, // Sun → next Mon
		{"monthly", ut(2026, 7, 1, 0, 0), ut(2026, 7, 31, 0, 0), true},
		{"monthly", ut(2026, 7, 31, 0, 0), ut(2026, 8, 1, 0, 0), false},
		{"yearly", ut(2026, 1, 1, 0, 0), ut(2026, 12, 31, 0, 0), true},
		{"yearly", ut(2026, 12, 31, 0, 0), ut(2027, 1, 1, 0, 0), false},
	}
	for _, c := range cases {
		if got := samePeriod(c.freq, c.a, c.b, loc); got != c.want {
			t.Errorf("samePeriod(%s, %s, %s) = %v, want %v", c.freq, c.a.Format(time.RFC3339), c.b.Format(time.RFC3339), got, c.want)
		}
	}
}

// resolvePresetWindow turns a preset (union of intervals) into a run_at (window start) + a JSON
// snapshot carrying the rule + on_overrun + the occurrence end (until). Both instants round-trip
// through the local wall-clock basis the scheduler uses.
func TestResolvePresetWindow(t *testing.T) {
	runAt, snap, ok := resolvePresetWindow("daily",
		[]presetInterval{iv("00:30", "08:30")}, "next", ut(2026, 7, 8, 10, 0), time.UTC)
	if !ok {
		t.Fatal("resolvePresetWindow ok=false")
	}
	if !parseLocal(t, runAt).Equal(ut(2026, 7, 9, 0, 30)) {
		t.Fatalf("run_at = %q, want the 2026-07-09 00:30 instant", runAt)
	}
	var s runPresetSnapshot
	if err := json.Unmarshal([]byte(snap), &s); err != nil {
		t.Fatalf("snapshot not valid JSON: %v (%s)", err, snap)
	}
	if s.Freq != "daily" || s.OnOverrun != "next" || len(s.Intervals) != 1 || s.Intervals[0].Start.Time != "00:30" {
		t.Fatalf("snapshot rule = %+v", s)
	}
	if !parseLocal(t, s.Until).Equal(ut(2026, 7, 9, 8, 30)) {
		t.Fatalf("until = %q, want the 2026-07-09 08:30 instant", s.Until)
	}
	if _, _, ok := resolvePresetWindow("daily", []presetInterval{iv("bad", "08:30")}, "next", ut(2026, 7, 8, 10, 0), time.UTC); ok {
		t.Fatal("a malformed window must resolve ok=false")
	}
}

// ---------- persistence ----------

// run_presets is an admin-managed, ordered collection (like type_config/links): CRUD + reorder
// must round-trip, and ordering is by ord then id. intervals is stored as JSON.
func TestRunPresetsCRUD(t *testing.T) {
	st := newTestStore(t)

	id, err := st.CreateRunPreset(RunPreset{Label: "低峰期", Freq: "daily",
		Intervals: `[{"start":{"time":"09:00"},"stop":{"time":"12:00"}},{"start":{"time":"14:00"},"stop":{"time":"18:00"}}]`,
		OnOverrun: "next", Enabled: true})
	if err != nil {
		t.Fatalf("CreateRunPreset: %v", err)
	}
	id2, err := st.CreateRunPreset(RunPreset{Label: "周末", Freq: "weekly",
		Intervals: `[{"start":{"weekday":6,"time":"00:00"},"stop":{"weekday":0,"time":"23:59"}}]`, OnOverrun: "cancel", Enabled: true})
	if err != nil {
		t.Fatalf("CreateRunPreset 2: %v", err)
	}

	list := st.ListRunPresets()
	if len(list) != 2 || list[0].ID != id || list[1].ID != id2 {
		t.Fatalf("ListRunPresets order = %+v, want [%d %d]", list, id, id2)
	}

	p, ok := st.GetRunPreset(id)
	if !ok || p.Label != "低峰期" || p.Freq != "daily" || p.OnOverrun != "next" || !p.Enabled {
		t.Fatalf("GetRunPreset = %+v ok=%v", p, ok)
	}
	var got []presetInterval
	if json.Unmarshal([]byte(p.Intervals), &got); len(got) != 2 || got[1].Start.Time != "14:00" {
		t.Fatalf("intervals round-trip lost: %s", p.Intervals)
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
}

// A job's window (run_at + run_preset snapshot) round-trips; QueuedPresetJobs surfaces only
// queued jobs that carry a window; expiring or clearing removes them from that set.
func TestJobWindowRoundTrip(t *testing.T) {
	st := newTestStore(t)
	snap := `{"freq":"daily","intervals":[{"start":{"time":"00:30"},"stop":{"time":"08:30"}}],"on_overrun":"next","until":"2099-01-01 08:30:00"}`

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
}

// ---------- overrun sweep ----------

// makePresetJob seeds a queued job carrying a preset snapshot (window already closed via a far-past
// until unless overridden by the caller's now vs until).
func makePresetJob(t *testing.T, st *Store, intervals []presetInterval, overrun, until string) int64 {
	t.Helper()
	id, err := st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"k": "v"}}, "50")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(runPresetSnapshot{Freq: "daily", Intervals: intervals, OnOverrun: overrun, Until: until})
	st.SetJobWindow(id, until, string(b))
	return id
}

// Single-interval overrun: on close (the sole window always exhausts the period), continue → drop
// the window, cancel → expired, next → roll to the next day. Deterministic via UTC panel + fixed now.
func TestSweepPresetOverrun(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("timezone", "UTC")
	srv := &Server{st: st}
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC) // noon → today's 00:30–08:30 window has closed
	one := []presetInterval{iv("00:30", "08:30")}

	cont := makePresetJob(t, st, one, "continue", "2000-01-01 00:00:00")
	next := makePresetJob(t, st, one, "next", "2000-01-01 00:00:00")
	canc := makePresetJob(t, st, one, "cancel", "2000-01-01 00:00:00")

	srv.sweepPresetWindowsLocked(now)

	if status, ra, rp := jobRow(t, st, cont); status != "queued" || ra != "" || rp != "" {
		t.Fatalf("continue: got status=%q run_at=%q run_preset=%q, want queued + cleared", status, ra, rp)
	}
	if status, _, _ := jobRow(t, st, canc); status != "expired" {
		t.Fatalf("cancel: status=%q, want expired", status)
	}
	status, ra, _ := jobRow(t, st, next)
	if status != "queued" || !parseLocal(t, ra).After(now) {
		t.Fatalf("next: status=%q run_at=%q should roll to the future", status, ra)
	}
}

// Union overrun: with two sub-windows, missing the first auto-advances to the second SAME DAY
// regardless of policy; only once the whole day is exhausted does on_overrun (here cancel) fire.
func TestSweepPresetUnion(t *testing.T) {
	st := newTestStore(t)
	st.SetSetting("timezone", "UTC")
	srv := &Server{st: st}
	two := []presetInterval{iv("09:00", "12:00"), iv("14:00", "18:00")}

	// Just after the 09–12 window closed; the 14–18 window is still ahead today → auto-advance
	// even though the policy is cancel.
	midday := time.Date(2099, 1, 1, 12, 30, 0, 0, time.UTC)
	adv := makePresetJob(t, st, two, "cancel", "2099-01-01 12:00:00")
	srv.sweepPresetWindowsLocked(midday)
	status, ra, rp := jobRow(t, st, adv)
	if status != "queued" {
		t.Fatalf("union auto-advance: status=%q, want queued (not cancelled mid-day)", status)
	}
	if !parseLocal(t, ra).Equal(ut(2099, 1, 1, 14, 0)) {
		t.Fatalf("union auto-advance: run_at=%q, want today 14:00", ra)
	}
	var s runPresetSnapshot
	json.Unmarshal([]byte(rp), &s)
	if !parseLocal(t, s.Until).Equal(ut(2099, 1, 1, 18, 0)) {
		t.Fatalf("union auto-advance: until=%q, want today 18:00", s.Until)
	}

	// After BOTH windows closed today → period exhausted → cancel fires.
	evening := time.Date(2099, 1, 1, 19, 0, 0, 0, time.UTC)
	exp := makePresetJob(t, st, two, "cancel", "2099-01-01 18:00:00")
	srv.sweepPresetWindowsLocked(evening)
	if status, _, _ := jobRow(t, st, exp); status != "expired" {
		t.Fatalf("union exhausted: status=%q, want expired", status)
	}
}

// ---------- HTTP surface ----------

// End-to-end through the handlers: create a split (two-interval) preset, submit a run into it, and
// confirm the create wiring snapshots the resolved window (run_at + run_preset) onto the job. A
// stub provider keeps any immediate run clean. Also covers CRUD validation.
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
		`{"label":"低峰期","freq":"daily","intervals":[{"start":{"time":"09:00"},"stop":{"time":"12:00"}},{"start":{"time":"14:00"},"stop":{"time":"18:00"}}],"on_overrun":"next","enabled":true}`)
	presetID := int64(pr["id"].(float64))

	if ps, _ := post(t, srv.apiRunPresets, "{}")["presets"].([]any); len(ps) != 1 {
		t.Fatalf("apiRunPresets returned %d presets, want 1", len(ps))
	}

	created := post(t, srv.apiBatchJobCreate, fmt.Sprintf(
		`{"target_id":%d,"preset_id":%d,"rows":[{"code":"a"}]}`, targetID, presetID))
	jobID := int64(created["job_id"].(float64))

	_, runAt, runPreset := jobRow(t, srv.st, jobID)
	if runAt == "" {
		t.Fatal("preset job should carry a resolved run_at")
	}
	parseLocal(t, runAt) // must be a valid timestamp
	var snap runPresetSnapshot
	if json.Unmarshal([]byte(runPreset), &snap) != nil || snap.Freq != "daily" || len(snap.Intervals) != 2 || snap.Until == "" {
		t.Fatalf("run_preset snapshot lost its rule: %s", runPreset)
	}

	// A preset with no intervals, or a malformed time, is rejected at create.
	if code := postCode(t, srv.apiRunPresetCreate, `{"label":"x","freq":"daily","intervals":[]}`); code != 400 {
		t.Fatalf("empty-interval preset should 400, got %d", code)
	}
	if code := postCode(t, srv.apiRunPresetCreate, `{"label":"x","freq":"daily","intervals":[{"start":{"time":"99:99"},"stop":{"time":"12:00"}}]}`); code != 400 {
		t.Fatalf("bad-time preset should 400, got %d", code)
	}
}
