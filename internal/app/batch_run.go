package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// This file is the batch orchestration layer: it ties the store, the engine, and
// the manifest interpreter together, running each job as a background goroutine so
// it survives page refreshes and (via resumeBatchJobs) server restarts. See
// docs/adr/0001-batch-run-engine.md.

// difyRunTimeout bounds a single Dify run: it caps the streaming connection AND the
// reconcile poll window (difyReconcileTimeout). Real workflows here run long — a Deep
// Research run was observed at ~61 minutes — so this must comfortably exceed that. A
// too-short cap cuts the stream and (worse) lets the reconcile poll expire while Dify is
// still running, marking a run failed that then succeeds a minute later (a false negative
// we hit at the old 60m cap). 3h gives generous headroom; in poll mode each GET is fast,
// so a large cap only bounds a genuinely stuck run, not a healthy long one.
const difyRunTimeout = 180 * time.Minute

// batchMaxConcurrency is the admin-set ceiling a per-job concurrency is clamped to.
func (s *Server) batchMaxConcurrency() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_max_concurrency", "10"))
	if err != nil || n < 1 {
		return 10
	}
	return n
}

// clampConcurrency keeps an operator's requested concurrency within [1, adminMax].
func (s *Server) clampConcurrency(requested int) int {
	if requested < 1 {
		requested = 1
	}
	if max := s.batchMaxConcurrency(); requested > max {
		requested = max
	}
	return requested
}

// ---------- multifactor run queue (docs/adr/0008-multifactor-priority.md) ----------

// baseMax is the top of the base-priority scale: base priorities are numbers in
// [0, baseMax], normalized to [0,1] as the base factor.
const baseMax = 100

// clampBase keeps a base priority within [0, baseMax].
func clampBase(n int) int {
	if n < 0 {
		return 0
	}
	if n > baseMax {
		return baseMax
	}
	return n
}

// parsePriority interprets a stored priority string as (base 0..100, urgent, idle). "urgent"
// is the 加急 escalation and "idle" is the run-when-queue-idle bottom lane (mutually exclusive,
// ADR 0014); a bare number is the base priority; the legacy tier names map on read (normal→50,
// other→20) so pre-0008 rows keep working. urgent/idle carry base 0 (their Score anchor
// dominates, so base is irrelevant to their ordering).
func parsePriority(p string) (base int, urgent, idle bool) {
	switch strings.TrimSpace(p) {
	case "urgent":
		return 0, true, false
	case "idle":
		return 0, false, true
	case "normal", "":
		return 50, false, false
	case "other":
		return 20, false, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil {
		return 50, false, false
	}
	return clampBase(n), false, false
}

// normalizePriorityInput canonicalizes a client-supplied priority to its stored form:
// "urgent" (加急) or a clamped base number as a string. ok=false rejects garbage.
func normalizePriorityInput(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "urgent" {
		return "urgent", true
	}
	if n, err := strconv.Atoi(p); err == nil {
		return strconv.Itoa(clampBase(n)), true
	}
	return "", false
}

// settingFloat reads a non-negative float admin setting, falling back to def.
func (s *Server) settingFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(s.st.GetSetting(key, ""), 64); err == nil && v >= 0 {
		return v
	}
	return def
}

// prioWeights are the multifactor priority weights (Slurm's PriorityWeight*), admin-set.
// Default 1000 each so base/age/fair contribute comparably before tuning.
func (s *Server) prioWeights() queue.Weights {
	return queue.Weights{
		Base: s.settingFloat("batch_prio_w_base", 1000),
		Age:  s.settingFloat("batch_prio_w_age", 1000),
		Fair: s.settingFloat("batch_prio_w_fair", 1000),
	}
}

// prioAgeHours is the wait time at which the age factor saturates to 1 (anti-starvation).
func (s *Server) prioAgeHours() float64 {
	if h := s.settingFloat("batch_prio_age_hours", 24); h > 0 {
		return h
	}
	return 24
}

// prioFairHalflifeHours is the fair-share usage half-life: a user's recent runs decay
// by half every this-many hours.
func (s *Server) prioFairHalflifeHours() float64 {
	if h := s.settingFloat("batch_prio_fair_halflife_hours", 168); h > 0 {
		return h
	}
	return 168
}

// batchBudget is how many jobs may run at once across the whole queue (admin-set).
// Default 1 — jobs run one at a time, ordered by priority, which is the original
// "queue and run them one by one" ask.
func (s *Server) batchBudget() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_max_concurrent_jobs", "1"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// batchReserved is how many slots to hold for the top (加急) tier. Clamped to
// [0, budget-1] by the scheduler, so it only bites once the budget is raised.
func (s *Server) batchReserved() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_reserved_slots", "1"))
	if err != nil || n < 0 {
		return 1
	}
	return n
}

// ticketPeriodDays is how often 加急 tickets refill (admin-set; default weekly).
func (s *Server) ticketPeriodDays() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_ticket_period_days", "7"))
	if err != nil || n < 1 {
		return 7
	}
	return n
}

// difyPollSeconds is how often to poll a started run's status (poll mode). 0 = off: hold
// the streaming connection open until the run finishes (the default). Poll mode captures
// the run id then closes the stream, which avoids long-lived SSE through a proxy.
func (s *Server) difyPollSeconds() int {
	n, err := strconv.Atoi(s.st.GetSetting("dify_poll_seconds", "0"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// difyRunTimeoutDur is the admin-set cap on one Dify run: it bounds the portal's HTTP
// client AND the reconcile poll window, so a run that legitimately takes long isn't cut
// short / falsely marked failed. Stored in minutes; default = difyRunTimeout (180m),
// clamped to >= 1.
func (s *Server) difyRunTimeoutDur() time.Duration {
	n, err := strconv.Atoi(s.st.GetSetting("dify_run_timeout_minutes", strconv.Itoa(int(difyRunTimeout/time.Minute))))
	if err != nil || n < 1 {
		return difyRunTimeout
	}
	return time.Duration(n) * time.Minute
}

// urgentEnabled reports whether the 加急 escalation is offered at all (admin toggle;
// default off, so batch/runs have no urgent lane unless an admin turns it on).
func (s *Server) urgentEnabled() bool {
	return s.st.GetSetting("batch_urgent_enabled", "0") == "1"
}

// difyEndUser resolves the end-user identity Dify records for a run from the
// dify_end_user template, substituting [username] with the submitting portal user.
// The default (or a blank template) is the fixed "report-portal".
func (s *Server) difyEndUser(username string) string {
	tmpl := strings.TrimSpace(s.st.GetSetting("dify_end_user", "report-portal"))
	if tmpl == "" {
		return "report-portal"
	}
	return strings.ReplaceAll(tmpl, "[username]", username)
}

// urgentAllowed gates a submitted 加急 run: if the 加急 lane is disabled it always
// downgrades to the base priority; otherwise unlimited groups use it freely and
// everyone else must spend a 加急 ticket. Returns the effective stored priority and
// whether it was downgraded.
func (s *Server) urgentAllowed(user, priority string, base int) (string, bool) {
	if priority != "urgent" {
		return priority, false
	}
	if !s.urgentEnabled() {
		return strconv.Itoa(base), true // urgent lane turned off → run at base priority
	}
	gs := s.st.EffectiveGroupSettings(user)
	if !gs.AllowUrgent {
		return strconv.Itoa(base), true // the user's group disallows urgent
	}
	if gs.UrgentUnlimited {
		return priority, false
	}
	if ok, _ := s.st.SpendTicket(user, gs.Weight, s.ticketPeriodDays(), time.Now()); ok {
		return priority, false
	}
	return strconv.Itoa(base), true // out of tickets → runs at its base priority
}

// runWindowOpenAt reports whether the user's effective group allows a run at the given
// panel-time hour, plus the window string for messaging. An empty/degenerate window is
// always open. The caller passes the effective run hour (a scheduled run's run_at hour,
// else now) so the window governs execution time, not submission time.
func (s *Server) runWindowOpenAt(user string, hour int) (bool, string) {
	win := s.st.EffectiveGroupSettings(user).RunWindow
	start, end, ok := parseRunWindow(win)
	if !ok {
		return true, ""
	}
	if start < end {
		return hour >= start && hour < end, win
	}
	return hour >= start || hour < end, win // window wraps midnight (e.g. 22-6)
}

// parseRunWindow parses "H1-H2" hours (0..23). ok=false for "" or a degenerate window.
func parseRunWindow(win string) (int, int, bool) {
	win = strings.TrimSpace(win)
	if win == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(win, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	b, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || a < 0 || a > 23 || b < 0 || b > 23 || a == b {
		return 0, 0, false
	}
	return a, b, true
}

// runDefaultPriority is the system-wide fallback base priority (0..100) for a run
// whose submitter is in no group with a default (admin setting; ADR 0008).
func (s *Server) runDefaultPriority() int {
	base, _, _ := parsePriority(s.st.GetSetting("run_default_priority", "50"))
	return base
}

// groupPriorityValid normalizes a group default priority to its stored base-number
// string, or "" to clear/reject it. 加急 is never a group default — it stays
// ticket-gated via explicit escalation only (ADR 0008).
func (s *Server) groupPriorityValid(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Legacy tier names normalize to their base number; 加急 and garbage are rejected.
	switch p {
	case "urgent":
		return ""
	case "normal":
		return "50"
	case "other":
		return "20"
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return ""
	}
	return strconv.Itoa(clampBase(n))
}

// resolveBasePriority is a run's base priority (0..100) when the caller didn't force an
// explicit value: the submitter's primary-group priority override, else the system
// default (group model B). The Default group carries no priority override — its members
// (and every unassigned user) use the system default (run_default_priority), so priority
// resolves symmetrically with weight/urgent. A never-a-default urgent override is ignored.
func (s *Server) resolveBasePriority(user string) int {
	if gid := s.st.PrimaryGroupOf(user); gid != 0 && gid != s.st.DefaultGroupID() {
		if p := s.st.GroupPriority(gid); p != "" {
			if b, urgent, _ := parsePriority(p); !urgent {
				return b
			}
		}
	}
	return s.runDefaultPriority()
}

// parseEnqueueUnix parses a stored "2006-01-02 15:04:05" timestamp (local time) to
// a unix second. A malformed/empty value sorts first (unix 0), which is harmless.
func parseEnqueueUnix(ts string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// runAtDue reports whether a stored one-shot schedule (定时运行) has arrived. An
// empty run_at means "run ASAP"; a malformed value is treated as due so a bad
// timestamp can never strand a job forever. run_at shares created_at's local
// basis (see docs/adr/0007-run-analysis-and-scheduling.md).
func runAtDue(runAt string, now time.Time) bool {
	if runAt == "" {
		return true
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", runAt, time.Local)
	if err != nil {
		return true
	}
	return !t.After(now)
}

// userUsage returns each user's decayed recent run count for the fair-share factor:
// Σ over the user's recent jobs of 0.5^(job_age_hours / halflife). A heavy recent user
// accumulates usage, so their fair factor 2^(-usage) shrinks toward 0 (ADR 0008). The
// scan is bounded to ~8 half-lives, past which a job's contribution is negligible.
func (s *Server) userUsage(now time.Time) map[string]float64 {
	halflife := s.prioFairHalflifeHours()
	since := now.Add(-time.Duration(halflife * 8 * float64(time.Hour))).Format("2006-01-02 15:04:05")
	usage := map[string]float64{}
	for _, a := range s.st.RecentJobActivity(since) {
		ageHours := now.Sub(time.Unix(parseEnqueueUnix(a.CreatedAt), 0)).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		usage[a.User] += math.Exp2(-ageHours / halflife)
	}
	return usage
}

// jobFactors computes a queued job's normalized priority factors at `now`. usage is the
// precomputed decayed-usage map (see userUsage); an absent user has usage 0 → fair 1.
func (s *Server) jobFactors(j BatchJob, now time.Time, usage map[string]float64) queue.Factors {
	base, urgent, idle := parsePriority(j.Priority)
	// A 定时 job "arrives" at its run_at, so it ages from then — not from when it was
	// created — otherwise a long-scheduled job would unfairly jump ahead of runs
	// submitted after it (ADR 0007). Immediate jobs age from created_at.
	enqueue := j.CreatedAt
	if j.RunAt != "" {
		enqueue = j.RunAt
	}
	wait := now.Sub(time.Unix(parseEnqueueUnix(enqueue), 0)).Seconds()
	age := wait / (s.prioAgeHours() * 3600)
	if age < 0 {
		age = 0
	} else if age > 1 {
		age = 1
	}
	return queue.Factors{
		Base:   float64(base) / baseMax,
		Age:    age,
		Fair:   math.Exp2(-usage[j.CreatedBy]),
		Urgent: urgent,
		Idle:   idle,
	}
}

// queuedItems maps the currently-queued jobs to scored scheduler items, used both by
// the scheduler and by the "N ahead" computation. A job scheduled for the future is
// hidden until its run_at passes, so it is never admitted early nor counted as waiting.
func (s *Server) queuedItems() []queue.Item {
	now := time.Now()
	w := s.prioWeights()
	usage := s.userUsage(now)
	jobs := s.st.QueuedJobs()
	items := make([]queue.Item, 0, len(jobs))
	for _, j := range jobs {
		if !runAtDue(j.RunAt, now) {
			continue
		}
		f := s.jobFactors(j, now, usage)
		items = append(items, queue.Item{ID: j.ID, Score: w.Score(f), Urgent: f.Urgent})
	}
	return items
}

// scheduleLoop periodically re-runs admission so one-shot scheduled jobs start
// when their run_at passes, even while the system is otherwise idle. The queue is
// event-driven; this ticker is the only always-on timer (ADR 0007). It runs for
// the process lifetime.
func (s *Server) scheduleLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.scheduleTick()
	}
}

// scheduleTick admits runnable runs (individual items) up to the budget, highest
// priority first, and starts each. It is serialized by schedMu so concurrent ticks
// can't over-admit; MarkItemRunning makes each admission atomic on top of that. Called
// after a job is enqueued, after a run finishes, when a scheduled job comes due, and on
// startup. The budget is the ONLY concurrency gate now — one queue, one slot per run
// (docs/adr/0011-run-level-scheduling.md).
func (s *Server) scheduleTick() {
	s.schedMu.Lock()
	s.sweepPresetWindowsLocked(time.Now())
	s.admitLocked()
	// Backstop: finalize any active job that has no runs left to do but that no run's
	// afterItem will ever close out — an all-done straggler, or a job whose rows all
	// vanished (a retry that requeued nothing). Empty at creation is rejected upstream.
	// Runs are admitted first, so a job with more rows to run is never finalized early.
	var done []BatchJob
	for _, j := range s.st.SchedulableJobs() {
		if queued, running, _, _, _, _ := s.st.LiveJobCounts(j.ID); queued == 0 && running == 0 {
			if fin := s.finalizeLocked(j.ID); fin != nil {
				done = append(done, *fin)
			}
		}
	}
	// A job cancelled while it had no in-flight run isn't in SchedulableJobs and no run's
	// afterItem will close it out; finalizeLocked finalizes it once its runs have drained.
	for _, id := range s.st.CancellingJobIDs() {
		if fin := s.finalizeLocked(id); fin != nil {
			done = append(done, *fin)
		}
	}
	s.schedMu.Unlock()
	for _, j := range done {
		s.fireEvent(EventBatchFinished, map[string]any{
			"job_id": j.ID, "status": j.Status, "total": j.Total,
			"succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed,
		})
		s.notifyJobDone(j)
	}
}

// sweepPresetWindowsLocked handles preset-windowed runs (ADR 0014) whose window closed before
// they ever started. Only still-queued jobs are swept — a run that began inside its window is
// left to finish (non-preemptive). Per the snapshot's on_overrun: "continue" drops the window
// and runs ASAP, "next" rolls to the next occurrence (keep waiting), "cancel" marks 'expired'.
// The 30s scheduleLoop bounds how long after a window closes this fires. Caller holds schedMu.
func (s *Server) sweepPresetWindowsLocked(now time.Time) {
	loc := s.panelLocation()
	for _, j := range s.st.QueuedPresetJobs() {
		var snap runPresetSnapshot
		if json.Unmarshal([]byte(j.RunPreset), &snap) != nil {
			continue // unparseable snapshot: never strand a job on our own bug
		}
		if snap.Until == "" || !runAtDue(snap.Until, now) {
			continue // current sub-window still open — normal admission handles it
		}
		// Window closed with the run not started. Find the next eligible sub-window across the
		// union. If another falls in the SAME period (e.g. later today), auto-advance to it
		// regardless of policy — the run just tries the day's next window. Only when the period is
		// exhausted does on_overrun bite: "next" rolls to the next period, "continue" runs ASAP,
		// "cancel" expires. A single-interval preset always exhausts on close (ADR 0014).
		ns, ne, ok := nextWindow(snap.Freq, snap.Intervals, now, loc)
		if ok && (samePeriod(snap.Freq, now, ns, loc) || snap.OnOverrun == "next") {
			b, _ := json.Marshal(runPresetSnapshot{Freq: snap.Freq, Intervals: snap.Intervals, OnOverrun: snap.OnOverrun, Until: fmtLocal(ne)})
			s.st.SetJobWindow(j.ID, fmtLocal(ns), string(b))
		} else if snap.OnOverrun == "cancel" {
			s.st.ExpireJob(j.ID)
		} else { // "continue" (or an unresolvable rule) → run ASAP
			s.st.ClearJobWindow(j.ID)
		}
	}
}

// candMeta ties a scheduler item id back to its job and row for dispatch.
type candMeta struct {
	job  BatchJob
	item batch.Item
}

// admitLocked is the body of scheduleTick; the caller holds schedMu. It fills the free
// slots (budget − running runs) with the highest-priority runnable runs and starts them.
func (s *Server) admitLocked() {
	budget := s.batchBudget()
	running := s.st.RunningItemCount()
	if budget-running <= 0 {
		return
	}
	cands, meta := s.itemCandidates()
	if len(cands) == 0 {
		return
	}
	plan := queue.Plan{Budget: budget, Reserved: s.batchReserved()}
	for _, it := range queue.Admit(cands, running, plan) {
		m := meta[it.ID]
		if s.st.MarkItemRunning(it.ID) {
			s.st.MarkJobRunning(m.job.ID) // queued → running (no-op if already running)
			s.startItem(m.job, m.item)
		}
	}
}

// itemCandidates builds the run-level candidate set: for every schedulable job (queued
// or running, due, not cancelling), up to `concurrency − its running runs` of its queued
// rows — the producer's in-flight window — each scored by ITS job's priority factors, so
// all of a job's runs share the job's score and a higher-priority job's runs rank first.
// Returns the scheduler items plus an id → (job, row) lookup for dispatch.
func (s *Server) itemCandidates() ([]queue.Item, map[int64]candMeta) {
	now := time.Now()
	w := s.prioWeights()
	usage := s.userUsage(now)
	var cands []queue.Item
	meta := map[int64]candMeta{}
	for _, j := range s.st.SchedulableJobs() {
		if !runAtDue(j.RunAt, now) {
			continue // a not-yet-due 定时 job contributes no runs
		}
		_, running, _, _, _, _ := s.st.LiveJobCounts(j.ID)
		window := j.Concurrency
		if window < 1 {
			window = 1
		}
		if window -= running; window <= 0 {
			continue // this job already has its share of runs in flight
		}
		items, err := s.st.QueuedItems(j.ID)
		if err != nil || len(items) == 0 {
			continue
		}
		f := s.jobFactors(j, now, usage)
		score := w.Score(f)
		for i, it := range items {
			if i >= window {
				break
			}
			cands = append(cands, queue.Item{ID: it.ID, Score: score, Urgent: f.Urgent})
			meta[it.ID] = candMeta{job: j, item: it}
		}
	}
	return cands, meta
}

// providerFor builds the Provider for a job through the test seam when one is set (buildProv),
// else the real buildProvider. All run + reconcile paths go through here so a test can inject a
// fake provider once. onRef (may be nil) persists the streaming run/conversation/task ids;
// onStarted (may be nil) persists "the run reached Dify" on stream-open. The test seam takes only
// onRef — a fake provider doesn't open a real stream, so onStarted is irrelevant to it.
func (s *Server) providerFor(job BatchJob, onRef func(runID, convID, taskID string), onStarted func()) (batch.Provider, error) {
	if s.buildProv != nil {
		return s.buildProv(job, onRef)
	}
	return s.buildProvider(job, onRef, onStarted)
}

// buildProvider constructs the Provider for a job from its target's plugin manifest and config.
// onRef (may be nil) is handed to the Dify provider to persist the run/conversation/task ids as
// they stream in; onStarted (may be nil) marks the run started the instant the stream opens — the
// restart-durable hooks. Returns an error if the plugin/target is missing or the manifest no
// longer compiles (e.g. a plugin was deleted after the job started).
func (s *Server) buildProvider(job BatchJob, onRef func(runID, convID, taskID string), onStarted func()) (batch.Provider, error) {
	tgt, ok := s.st.GetTarget(job.TargetID)
	if !ok {
		return nil, fmt.Errorf("target %d not found", job.TargetID)
	}
	// Dify-native target (the default): talk to Dify directly via the typed client
	// (docs/adr/0006-dify-native.md). The generic manifest below is the advanced path.
	if tgt.PluginSlug == difyPluginSlug {
		return buildDifyProvider(tgt.Config, s.difyEndUser(job.CreatedBy), s.difyPollSeconds() > 0, time.Duration(s.difyPollSeconds())*time.Second, s.difyRunTimeoutDur(), onRef, onStarted)
	}
	plug, ok := s.st.GetPlugin(tgt.PluginSlug)
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", tgt.PluginSlug)
	}
	m, err := batch.Compile([]byte(plug.Spec))
	if err != nil {
		return nil, fmt.Errorf("plugin %q manifest: %w", tgt.PluginSlug, err)
	}
	cfg := map[string]string{}
	json.Unmarshal([]byte(tgt.Config), &cfg)
	return m.NewProvider(cfg, &http.Client{Timeout: s.difyRunTimeoutDur()}), nil
}

// jobRun is the shared cancellable scope for all in-flight runs of one job: cancelling
// it aborts every in-flight Dify call of the job at once (the client uses this ctx for
// its HTTP requests), so a cancel takes effect immediately instead of waiting for a
// blocking run to return.
type jobRun struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// jobRunFor returns the job's shared run scope, creating it on first use.
func (s *Server) jobRunFor(jobID int64) *jobRun {
	if v, ok := s.jobRuns.Load(jobID); ok {
		return v.(*jobRun)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jr := &jobRun{ctx: ctx, cancel: cancel}
	if actual, loaded := s.jobRuns.LoadOrStore(jobID, jr); loaded {
		cancel() // lost the race; use the winner's scope
		return actual.(*jobRun)
	}
	return jr
}

// cancelRunningJob aborts a job's in-flight runs immediately (a no-op if none run here).
func (s *Server) cancelRunningJob(jobID int64) {
	if v, ok := s.jobRuns.Load(jobID); ok {
		v.(*jobRun).cancel()
	}
}

// cancelRunningItem aborts ONE in-flight run (a single row) immediately, leaving the
// job's other runs untouched. A no-op if that row isn't running here.
func (s *Server) cancelRunningItem(itemID int64) {
	if v, ok := s.itemCancels.Load(itemID); ok {
		v.(context.CancelFunc)()
	}
}

// startItem runs one admitted item in the background: build the provider, trigger the
// run (retrying only transient transport errors), persist the outcome, then finalize the
// job if it's done and re-admit into the freed slot. The scheduler already marked the
// item running, so it holds one of the budget's slots for its whole lifetime.
func (s *Server) startItem(job BatchJob, item batch.Item) {
	jr := s.jobRunFor(job.ID)
	// A per-row cancel scope, a child of the job's, so cancelling the whole job OR just
	// this row aborts this run — and marks it 'cancelled', not 'failed'.
	itemCtx, itemCancel := context.WithCancel(jr.ctx)
	s.itemCancels.Store(item.ID, itemCancel)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("batch job %d item %d panicked: %v", job.ID, item.ID, r)
			}
			s.itemCancels.Delete(item.ID)
			itemCancel()
			s.afterItem(job.ID)
		}()
		// Persist the Dify handles the instant they stream in, so a crash/restart mid-run
		// reconciles this row by id instead of re-running it (the restart-durable money guard).
		onRef := func(runID, convID, taskID string) {
			if err := s.st.SaveItemDifyRef(item.ID, runID, convID, taskID); err != nil {
				log.Printf("batch job %d item %d: persist dify ref: %v", job.ID, item.ID, err)
			}
		}
		// Mark the run started the instant the stream opens (before any id): a crash in that window
		// then resumes as untracked (never re-run) instead of being re-fired as if it never started.
		onStarted := func() {
			if err := s.st.MarkItemDifyStarted(item.ID); err != nil {
				log.Printf("batch job %d item %d: mark dify started: %v", job.ID, item.ID, err)
			}
		}
		prov, err := s.providerFor(job, onRef, onStarted)
		if err != nil {
			log.Printf("batch job %d item %d: cannot start: %v", job.ID, item.ID, err)
			s.st.FinishItem(item.ID, batch.Failed, 1, "", err.Error())
			return
		}
		res, attempts := batch.RunItem(itemCtx, prov, item.Inputs, job.MaxRetries, nil, log.Printf)
		// A cancelled run (this row or its whole job) is recorded as 'cancelled', not
		// 'failed' — the operator stopped it on purpose. A run that actually finished
		// keeps its real outcome even if a cancel raced in just after.
		if itemCtx.Err() != nil {
			s.st.FinishItemCancelled(item.ID)
			return
		}
		s.st.FinishItem(item.ID, res.Status, attempts, res.RunID, res.Detail)
	}()
}

// afterItem runs when a run finishes: finalize the job if nothing is left to do, then
// re-admit so the freed slot picks up the next-highest-priority run.
func (s *Server) afterItem(jobID int64) {
	s.finalizeJob(jobID)
	s.scheduleTick()
}

// finalizeJob closes a job out — aggregate counts + terminal status, the finished event,
// and the done-notification — exactly once, if it has no runs left to do. Serialized by
// schedMu so two finishing runs can't double-finalize.
func (s *Server) finalizeJob(jobID int64) {
	s.schedMu.Lock()
	done := s.finalizeLocked(jobID)
	s.schedMu.Unlock()
	if done != nil {
		s.fireEvent(EventBatchFinished, map[string]any{
			"job_id": done.ID, "status": done.Status, "total": done.Total,
			"succeeded": done.Succeeded, "partial": done.Partial, "failed": done.Failed,
		})
		s.notifyJobDone(*done)
	}
}

// finalizeLocked transitions a job to its terminal state when it has 0 running runs and
// either 0 queued runs (all done → finished) or it is cancelling (remaining runs
// abandoned → cancelled). The caller holds schedMu; because each run persists its
// outcome BEFORE this reads the counts, the last run to finish always sees running == 0
// and finalizes. Returns the finalized job (for event/notify) or nil if not yet done.
func (s *Server) finalizeLocked(jobID int64) *BatchJob {
	job, ok := s.st.GetBatchJob(jobID)
	if !ok || job.Status == "finished" || job.Status == "cancelled" {
		return nil // already terminal
	}
	queued, running, _, _, _, _ := s.st.LiveJobCounts(jobID)
	cancelling := job.Status == "cancelling"
	if running > 0 || (queued > 0 && !cancelling) {
		return nil // runs still in flight or waiting
	}
	if err := s.st.FinishJob(jobID, cancelling); err != nil {
		log.Printf("batch job %d: finalize: %v", jobID, err)
		return nil
	}
	if v, ok := s.jobRuns.LoadAndDelete(jobID); ok {
		v.(*jobRun).cancel()
	}
	j, ok := s.st.GetBatchJob(jobID)
	if !ok {
		return nil
	}
	return &j
}

// resumeBatchJobs is called at startup to recover runs left in-flight by a crash. It splits the
// orphaned 'running' rows three ways by what each captured before the crash, so a started (charged)
// run is never duplicated:
//   - run/conversation id       → RECONCILE to the true outcome, never re-run (reconcileResumedItem);
//   - started but no such id     → a task id, or the stream opened (dify_started_at) with no id → the
//                                  run started but left nothing to poll → mark UNTRACKED, never re-run
//                                  (mirrors the in-process guard in dify_provider.go);
//   - no evidence it started     → no id and no stream-open stamp → never reached Dify → requeue and
//                                  re-trigger.
//
// Then re-admit from the persisted item state and finalize any job with nothing left to run.
func (s *Server) resumeBatchJobs() {
	// Snapshot the reconcilable runs BEFORE requeuing; ResetInFlightItems only requeues the fully
	// id-less ones, so these stay 'running' (holding their slot) until reconcile settles them.
	resumable := s.st.ResumableInFlightItems()
	for _, ref := range resumable {
		s.reconcileResumedItem(ref)
	}
	if len(resumable) > 0 {
		log.Printf("batch resume: reconciling %d in-flight run(s) by id (no re-run)", len(resumable))
	}
	// Started-but-unreconcilable: the run reached Dify (a task id, or the stream opened) but left no
	// run/conversation id to poll. Its outcome is UNKNOWN, not a failure — mark it untracked rather
	// than re-run, since a duplicate charged run is the exact thing reconcile-not-retry prevents
	// (same outcome the in-process path produces).
	if stuck := s.st.StartedUnreconcilableItems(); len(stuck) > 0 {
		for _, ref := range stuck {
			detail := "resume: run reached dify but left no reconcilable id; not re-run to avoid a duplicate charged run"
			if ref.TaskID != "" {
				detail = fmt.Sprintf("resume: run started on dify (task %s) but left no reconcilable id; not re-run to avoid a duplicate charged run", ref.TaskID)
			}
			s.st.FinishItem(ref.ItemID, batch.Untracked, 1, "", detail)
		}
		log.Printf("batch resume: marked %d started-but-unreconcilable run(s) untracked (no re-run)", len(stuck))
	}
	if err := s.st.ResetInFlightItems(); err != nil {
		log.Printf("batch resume: reset in-flight items: %v", err)
		return
	}
	s.scheduleTick()
	for _, id := range s.st.ResumableJobIDs() {
		s.finalizeJob(id)
	}
}

// reconcileResumedItem settles ONE crash-orphaned run by reconciling its persisted Dify handle in
// the background — it never re-runs the workflow. On success (or a permanent reconcile failure) it
// records the outcome and re-admits the freed slot; a provider that can't reconcile leaves the item
// failed. Runs under the job's cancellable scope so a cancel still aborts the reconcile.
func (s *Server) reconcileResumedItem(ref ResumeRef) {
	job, ok := s.st.GetBatchJob(ref.JobID)
	if !ok {
		return
	}
	jr := s.jobRunFor(ref.JobID)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("batch resume: job %d item %d reconcile panicked: %v", ref.JobID, ref.ItemID, r)
			}
			s.afterItem(ref.JobID)
		}()
		prov, err := s.providerFor(job, nil, nil)
		if err != nil {
			log.Printf("batch resume: job %d item %d: cannot build provider: %v", ref.JobID, ref.ItemID, err)
			s.st.FinishItem(ref.ItemID, batch.Failed, 1, ref.RunID, "resume: "+err.Error())
			return
		}
		rec, ok := prov.(batch.Reconciler)
		if !ok {
			log.Printf("batch resume: job %d item %d: provider cannot reconcile, marking failed", ref.JobID, ref.ItemID)
			s.st.FinishItem(ref.ItemID, batch.Failed, 1, ref.RunID, "resume: provider cannot reconcile a started run")
			return
		}
		res, err := rec.Reconcile(jr.ctx, ref.RunID, ref.ConvID)
		if err != nil {
			// Reconcile failed (deadline / unknown id). Do NOT re-run — record the failure so a
			// started run is never fired twice; the admin can retry the reconcile manually.
			s.st.FinishItem(ref.ItemID, batch.Failed, 1, ref.RunID, "resume reconcile: "+err.Error())
			return
		}
		s.st.FinishItem(ref.ItemID, res.Status, 1, res.RunID, res.Detail)
	}()
}
