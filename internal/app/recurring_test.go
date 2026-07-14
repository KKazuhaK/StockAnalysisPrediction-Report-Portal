package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// cadenceDue is the shared daily/weekly/monthly engine (cleanup + recurring). It fires at most once
// per matching day, gated by frequency, weekday/month-day, the time, and the last-run stamp.
func TestCadenceDue(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 7, 13, 3, 30, 0, 0, loc) // a Monday, 03:30
	wd := int(base.Weekday())
	cases := []struct {
		name              string
		freq, hhmm        string
		weekday, monthday int
		lastRun           string
		now               time.Time
		want              bool
	}{
		{"off", "off", "03:00", 0, 0, "", base, false},
		{"daily due", "daily", "03:00", 0, 0, "", base, true},
		{"daily before time", "daily", "03:00", 0, 0, "", time.Date(2026, 7, 13, 2, 30, 0, 0, loc), false},
		{"daily already ran today", "daily", "03:00", 0, 0, "2026-07-13", base, false},
		{"daily ran yesterday", "daily", "03:00", 0, 0, "2026-07-12", base, true},
		{"weekly matching", "weekly", "03:00", wd, 0, "", base, true},
		{"weekly other", "weekly", "03:00", (wd + 1) % 7, 0, "", base, false},
		{"monthly matching", "monthly", "03:00", 0, 13, "", base, true},
		{"monthly other", "monthly", "03:00", 0, 14, "", base, false},
		{"monthly day-31 clamps to Feb 28", "monthly", "03:00", 0, 31, "", time.Date(2026, 2, 28, 4, 0, 0, 0, loc), true},
		{"bad time", "daily", "nope", 0, 0, "", base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stamp := cadenceDue(tc.freq, tc.hhmm, tc.weekday, tc.monthday, tc.lastRun, tc.now, loc)
			if got != tc.want {
				t.Fatalf("cadenceDue = %v; want %v", got, tc.want)
			}
			if got && stamp != tc.now.In(loc).Format("2006-01-02") {
				t.Errorf("stamp = %q; want today", stamp)
			}
		})
	}
}

// nextCadence resolves the next fire instant strictly after now, across wrap and month-clamp edges.
func TestNextCadence(t *testing.T) {
	loc := time.UTC
	mon := time.Date(2026, 7, 13, 10, 0, 0, 0, loc) // Monday 10:00
	cases := []struct {
		name              string
		freq, hhmm        string
		weekday, monthday int
		now               time.Time
		want              time.Time
		ok                bool
	}{
		{"daily later today", "daily", "12:00", 0, 0, mon, time.Date(2026, 7, 13, 12, 0, 0, 0, loc), true},
		{"daily rolls to tomorrow", "daily", "08:00", 0, 0, mon, time.Date(2026, 7, 14, 8, 0, 0, 0, loc), true},
		{"weekly same day future", "weekly", "12:00", 1, 0, mon, time.Date(2026, 7, 13, 12, 0, 0, 0, loc), true},
		{"weekly same day past rolls a week", "weekly", "08:00", 1, 0, mon, time.Date(2026, 7, 20, 8, 0, 0, 0, loc), true},
		{"weekly later this week", "weekly", "09:00", 3, 0, mon, time.Date(2026, 7, 15, 9, 0, 0, 0, loc), true},
		{"monthly later this month", "monthly", "09:00", 0, 20, mon, time.Date(2026, 7, 20, 9, 0, 0, 0, loc), true},
		{"monthly rolls to next month", "monthly", "09:00", 0, 5, mon, time.Date(2026, 8, 5, 9, 0, 0, 0, loc), true},
		{"monthly clamps day 31", "monthly", "09:00", 0, 31, time.Date(2026, 2, 1, 0, 0, 0, 0, loc), time.Date(2026, 2, 28, 9, 0, 0, 0, loc), true},
		{"bad rule", "daily", "nope", 0, 0, mon, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nextCadence(tc.freq, tc.hhmm, tc.weekday, tc.monthday, tc.now, loc)
			if ok != tc.ok {
				t.Fatalf("ok = %v; want %v", ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("next = %v; want %v", got, tc.want)
			}
		})
	}
}

func seedRecurring(t *testing.T, st *Store, tgt int64, owner string, task RecurringTask) int64 {
	t.Helper()
	task.TargetID = tgt
	task.CreatedBy = owner
	if task.Rows == "" {
		task.Rows = `[{"code":"600000"}]`
	}
	if task.Concurrency == 0 {
		task.Concurrency = 1
	}
	id, err := st.CreateRecurringTask(task)
	if err != nil {
		t.Fatalf("CreateRecurringTask: %v", err)
	}
	return id
}

// A task survives a create → get → update → enable-toggle → delete round trip, and its runs are
// recorded and removed with it.
func TestRecurringStoreRoundTrip(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	id := seedRecurring(t, st, tgt, "alice", RecurringTask{
		Name: "Daily review", Freq: "daily", AtTime: "09:30", Priority: "idle", MaxRetries: 2, Enabled: true,
	})

	got, ok := st.GetRecurringTask(id)
	if !ok || got.Name != "Daily review" || got.Freq != "daily" || got.AtTime != "09:30" ||
		got.Priority != "idle" || got.MaxRetries != 2 || !got.Enabled || got.CreatedBy != "alice" {
		t.Fatalf("round trip mismatch: %+v (ok=%v)", got, ok)
	}
	if got.CreatedAt == "" {
		t.Errorf("created_at not stamped")
	}

	got.Name = "Weekly review"
	got.Freq = "weekly"
	got.Weekday = 3
	if err := st.UpdateRecurringTask(got); err != nil {
		t.Fatalf("UpdateRecurringTask: %v", err)
	}
	if u, _ := st.GetRecurringTask(id); u.Name != "Weekly review" || u.Freq != "weekly" || u.Weekday != 3 {
		t.Errorf("update not persisted: %+v", u)
	}

	st.SetRecurringEnabled(id, false)
	if u, _ := st.GetRecurringTask(id); u.Enabled {
		t.Errorf("SetRecurringEnabled(false) did not stick")
	}
	if len(st.EnabledRecurringTasks()) != 0 {
		t.Errorf("disabled task still in the enabled set")
	}

	// audit rows link fire→job and are removed with the task.
	st.InsertRecurringRun(id, 777)
	if runs := st.ListRecurringRuns(id, 0); len(runs) != 1 || runs[0].JobID != 777 {
		t.Fatalf("ListRecurringRuns = %+v", runs)
	}
	if err := st.DeleteRecurringTask(id); err != nil {
		t.Fatalf("DeleteRecurringTask: %v", err)
	}
	if _, ok := st.GetRecurringTask(id); ok {
		t.Errorf("task still present after delete")
	}
	if len(st.ListRecurringRuns(id, 0)) != 0 {
		t.Errorf("audit rows not cascaded on delete")
	}
}

// okProvider makes a Server whose runs succeed instantly (no network), so firing a task and letting
// the queue drain it is deterministic in a unit test.
func okProviderServer(st *Store) *Server {
	s := &Server{st: st}
	s.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return provFn(func(context.Context, map[string]string) (batch.RunResult, error) {
			return batch.RunResult{Status: batch.Ok}, nil
		}), nil
	}
	return s
}

// recurringTick fires a due task exactly once per period: it creates a job + an audit row, stamps
// last_fired, and a second tick in the same period does not re-fire.
func TestRecurringTickFiresStampsAndDedups(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := okProviderServer(st)
	// Daily at 00:00 is due any time today with an empty last_fired.
	id := seedRecurring(t, st, tgt, "alice", RecurringTask{Name: "T", Freq: "daily", AtTime: "00:00", Enabled: true})

	s.recurringTick()
	if n := countRows(t, st, "batch_jobs"); n != 1 {
		t.Fatalf("after first tick batch_jobs = %d; want 1", n)
	}
	if n := countRows(t, st, "recurring_runs"); n != 1 {
		t.Fatalf("recurring_runs = %d; want 1", n)
	}
	today := time.Now().In(s.panelLocation()).Format("2006-01-02")
	if got, _ := st.GetRecurringTask(id); got.LastFired != today {
		t.Errorf("last_fired = %q; want %q", got.LastFired, today)
	}

	s.recurringTick() // same period → no re-fire
	if n := countRows(t, st, "batch_jobs"); n != 1 {
		t.Errorf("second tick re-fired: batch_jobs = %d; want 1", n)
	}
}

// A disabled task is never fired by the tick.
func TestRecurringTickSkipsDisabled(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := okProviderServer(st)
	seedRecurring(t, st, tgt, "alice", RecurringTask{Name: "off", Freq: "daily", AtTime: "00:00", Enabled: false})
	s.recurringTick()
	if n := countRows(t, st, "batch_jobs"); n != 0 {
		t.Errorf("disabled task fired: batch_jobs = %d; want 0", n)
	}
}

// The fire path uses the task's stored priority as-is (idle / urgent / an explicit base number) and
// resolves a blank to the creator's group base — never spending an urgent ticket (ADR 0018 sec 4).
func TestFireRecurringTaskPriorityResolution(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := okProviderServer(st)
	fire := func(p string) string {
		task, _ := st.GetRecurringTask(seedRecurring(t, st, tgt, "alice",
			RecurringTask{Name: "p", Freq: "daily", AtTime: "00:00", Priority: p, Enabled: true}))
		job, _ := s.fireRecurringTask(task)
		j, _ := st.GetBatchJob(job)
		return j.Priority
	}
	if got := fire("idle"); got != "idle" {
		t.Errorf("idle -> %q; want idle", got)
	}
	if got := fire(""); got == "urgent" || got == "idle" || got == "" {
		t.Errorf("normal -> %q; want a resolved base number", got)
	}
	if got := fire("urgent"); got != "urgent" {
		t.Errorf("urgent -> %q; want urgent (admin-configured, passed through, ticketless)", got)
	}
	if got := fire("90"); got != "90" {
		t.Errorf("base 90 -> %q; want 90", got)
	}
}

// A task pointing at a deleted target (or with an empty template) is logged-and-skipped, not a hard
// failure — fireRecurringTask returns 0 and creates no job.
func TestFireRecurringTaskSkips(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := okProviderServer(st)

	missing, _ := st.GetRecurringTask(seedRecurring(t, st, tgt, "alice",
		RecurringTask{Name: "m", Freq: "daily", AtTime: "00:00", Enabled: true}))
	st.DeleteTarget(tgt)
	if got, err := s.fireRecurringTask(missing); got != 0 || err != nil {
		t.Errorf("fire with missing target = (%d, %v); want (0, nil)", got, err)
	}

	tgt2 := seedTarget(t, st)
	empty, _ := st.GetRecurringTask(seedRecurring(t, st, tgt2, "alice",
		RecurringTask{Name: "e", Freq: "daily", AtTime: "00:00", Rows: `[]`, Enabled: true}))
	if got, err := s.fireRecurringTask(empty); got != 0 || err != nil {
		t.Errorf("fire with empty template = (%d, %v); want (0, nil)", got, err)
	}
}

func seedAdminUser(t *testing.T, st *Store, name string) {
	t.Helper()
	if _, err := st.exec("INSERT INTO users(username,password_hash,role) VALUES(?,?,?)", name, "h", "admin"); err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
}

// The create endpoint rejects bad input; a non-admin's attempt at urgent / an explicit base number is
// coerced to normal, while an admin may pin urgent (top priority) or a base number.
func TestApiRecurringCreateValidation(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := &Server{st: st}
	seedAdminUser(t, st, "boss")

	postAs := func(user, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/admin/batch/recurring", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.apiRecurringCreate(rec, req, user)
		return rec
	}
	body := func(extra string) string {
		return `{"name":"n","target_id":` + itoa(tgt) + `,"freq":"daily","at_time":"09:00","enabled":true,"rows":[{"code":"x"}]` + extra + `}`
	}
	lastPriority := func() string {
		tasks := st.ListRecurringTasks()
		if len(tasks) == 0 {
			return "<none>"
		}
		return tasks[0].Priority // newest first
	}

	if rec := postAs("alice", `{"name":"","target_id":`+itoa(tgt)+`,"freq":"daily","at_time":"09:00","rows":[{"code":"x"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty name: status %d; want 400", rec.Code)
	}
	if rec := postAs("alice", `{"name":"n","target_id":`+itoa(tgt)+`,"freq":"hourly","at_time":"09:00","rows":[{"code":"x"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad freq: status %d; want 400", rec.Code)
	}
	if rec := postAs("alice", `{"name":"n","target_id":`+itoa(tgt)+`,"freq":"daily","at_time":"09:00","rows":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty rows: status %d; want 400", rec.Code)
	}

	// Non-admin (alice): urgent and an explicit base number are both coerced to normal ('').
	if rec := postAs("alice", body(`,"priority":"urgent"`)); rec.Code != http.StatusOK || lastPriority() != "" {
		t.Errorf("non-admin urgent: status %d, stored %q; want 200 + '' (coerced)", rec.Code, lastPriority())
	}
	if rec := postAs("alice", body(`,"priority":"95"`)); rec.Code != http.StatusOK || lastPriority() != "" {
		t.Errorf("non-admin base 95: status %d, stored %q; want 200 + '' (coerced)", rec.Code, lastPriority())
	}
	// Admin (boss): urgent and a base number are honored as stored.
	if rec := postAs("boss", body(`,"priority":"urgent"`)); rec.Code != http.StatusOK || lastPriority() != "urgent" {
		t.Errorf("admin urgent: status %d, stored %q; want 200 + 'urgent'", rec.Code, lastPriority())
	}
	if rec := postAs("boss", body(`,"priority":"90"`)); rec.Code != http.StatusOK || lastPriority() != "90" {
		t.Errorf("admin base 90: status %d, stored %q; want 200 + '90'", rec.Code, lastPriority())
	}
	// 'idle' is open to anyone.
	if rec := postAs("alice", body(`,"priority":"idle"`)); rec.Code != http.StatusOK || lastPriority() != "idle" {
		t.Errorf("idle: status %d, stored %q; want 200 + 'idle'", rec.Code, lastPriority())
	}
}

// A non-admin may not touch another user's task.
func TestApiRecurringOwnership(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := &Server{st: st}
	id := seedRecurring(t, st, tgt, "alice", RecurringTask{Name: "a", Freq: "daily", AtTime: "09:00", Enabled: true})

	// bob (non-admin, not owner) is forbidden from deleting alice's task.
	req := httptest.NewRequest("DELETE", "/api/admin/batch/recurring/"+itoa(id), nil)
	req.SetPathValue("id", itoa(id))
	rec := httptest.NewRecorder()
	s.apiRecurringDelete(rec, req, "bob")
	if rec.Code != http.StatusForbidden {
		t.Errorf("bob deleting alice's task: status %d; want 403", rec.Code)
	}
	if _, ok := st.GetRecurringTask(id); !ok {
		t.Errorf("task was deleted despite forbidden")
	}

	// bob's own list excludes alice's task.
	lreq := httptest.NewRequest("GET", "/api/admin/batch/recurring", nil)
	lrec := httptest.NewRecorder()
	s.apiRecurringList(lrec, lreq, "bob")
	var out struct {
		Tasks []map[string]any `json:"tasks"`
	}
	json.Unmarshal(lrec.Body.Bytes(), &out)
	if len(out.Tasks) != 0 {
		t.Errorf("bob sees %d tasks; want 0 (alice's is hidden)", len(out.Tasks))
	}
}
