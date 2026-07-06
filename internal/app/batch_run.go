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
// reconcile poll window (difyReconcileTimeout). Real workflows here run 30-40 minutes,
// so this must comfortably exceed that — a shorter cap cuts the stream and lets the
// reconcile expire while Dify is still running, marking a run failed that then succeeds.
const difyRunTimeout = 60 * time.Minute

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

// parsePriority interprets a stored priority string as (base 0..100, urgent). "urgent"
// is the 加急 escalation; a bare number is the base priority; the legacy tier names map
// on read (normal→50, other→20) so pre-0008 rows keep working.
func parsePriority(p string) (base int, urgent bool) {
	switch strings.TrimSpace(p) {
	case "urgent":
		return 0, true
	case "normal", "":
		return 50, false
	case "other":
		return 20, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil {
		return 50, false
	}
	return clampBase(n), false
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

// batchRowConcurrency is how many rows of a single batch run at once (admin-set; default
// 1 = one row at a time). Capped by the global concurrency budget so a single batch can
// never spawn more simultaneous runs than the "max at once" limit.
func (s *Server) batchRowConcurrency() int {
	n, err := strconv.Atoi(s.st.GetSetting("batch_row_concurrency", "1"))
	if err != nil || n < 1 {
		n = 1
	}
	if b := s.batchBudget(); n > b {
		n = b
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
	base, _ := parsePriority(s.st.GetSetting("run_default_priority", "50"))
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
			if b, urgent := parsePriority(p); !urgent {
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
	base, urgent := parsePriority(j.Priority)
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

// scheduleTick admits as many queued jobs as the budget + reserved-slot rule allow,
// highest priority first, and launches each. It is serialized by schedMu so two
// callers can't over-admit; MarkJobRunning makes each admission atomic on top of
// that. Called after a job is enqueued, after one finishes, and on startup.
func (s *Server) scheduleTick() {
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	items := s.queuedItems()
	if len(items) == 0 {
		return
	}
	running := s.st.RunningJobCount()
	plan := queue.Plan{Budget: s.batchBudget(), Reserved: s.batchReserved()}
	for _, it := range queue.Admit(items, running, plan) {
		if s.st.MarkJobRunning(it.ID) {
			s.launchJob(it.ID)
		}
	}
}

// buildProvider constructs the Provider for a job from its target's plugin
// manifest and config. Returns an error if the plugin/target is missing or the
// manifest no longer compiles (e.g. a plugin was deleted after the job started).
func (s *Server) buildProvider(job BatchJob) (batch.Provider, error) {
	tgt, ok := s.st.GetTarget(job.TargetID)
	if !ok {
		return nil, fmt.Errorf("target %d not found", job.TargetID)
	}
	// Dify-native target (the default): talk to Dify directly via the typed client
	// (docs/adr/0006-dify-native.md). The generic manifest below is the advanced path.
	if tgt.PluginSlug == difyPluginSlug {
		return buildDifyProvider(tgt.Config, s.difyEndUser(job.CreatedBy))
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
	return m.NewProvider(cfg, &http.Client{Timeout: difyRunTimeout}), nil
}

// cancelRunningJob aborts a job's in-flight run if it is executing in this process,
// so a cancel request takes effect immediately instead of waiting for the current
// blocking row to return. A no-op if the job isn't running here.
func (s *Server) cancelRunningJob(jobID int64) {
	if cf, ok := s.jobCancels.Load(jobID); ok {
		cf.(context.CancelFunc)()
	}
}

// launchJob starts (or resumes) a job in the background. It is idempotent: a job
// already running in this process is not launched twice.
func (s *Server) launchJob(jobID int64) {
	if _, loaded := s.batchRunning.LoadOrStore(jobID, struct{}{}); loaded {
		return
	}
	job, ok := s.st.GetBatchJob(jobID)
	if !ok {
		s.batchRunning.Delete(jobID)
		return
	}
	prov, err := s.buildProvider(job)
	if err != nil {
		log.Printf("batch job %d: cannot start: %v", jobID, err)
		s.st.FinishJob(jobID, false) // nothing runnable; close it out
		s.batchRunning.Delete(jobID)
		return
	}
	eng := &batch.Engine{Store: s.st, Log: log.Printf}
	// A batch runs up to batch_row_concurrency rows at once (default 1), capped by the
	// global budget so one batch can't exceed the "max at once" limit. See ADR 0004.
	spec := batch.JobSpec{JobID: jobID, Concurrency: s.batchRowConcurrency(), MaxRetries: job.MaxRetries}
	// A per-job cancellable context so a cancel request aborts the in-flight Dify call
	// immediately (the dify client uses this ctx for its HTTP requests) instead of
	// waiting for the current row's blocking run to return.
	ctx, cancel := context.WithCancel(context.Background())
	s.jobCancels.Store(jobID, cancel)
	go func() {
		defer s.batchRunning.Delete(jobID)
		defer s.jobCancels.Delete(jobID)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("batch job %d panicked: %v", jobID, r)
			}
		}()
		if err := eng.RunJob(ctx, spec, prov); err != nil {
			log.Printf("batch job %d ended with error: %v", jobID, err)
		}
		if j, ok := s.st.GetBatchJob(jobID); ok {
			s.fireEvent(EventBatchFinished, map[string]any{
				"job_id": j.ID, "status": j.Status, "total": j.Total,
				"succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed,
			})
			s.notifyJobDone(j)
		}
		s.scheduleTick() // a slot just freed — admit the next queued job by priority
	}()
}

// resumeBatchJobs is called at startup: it requeues items left 'running' by a
// crash and relaunches every job that was mid-flight.
func (s *Server) resumeBatchJobs() {
	if err := s.st.ResetInFlightItems(); err != nil {
		log.Printf("batch resume: reset in-flight items: %v", err)
		return
	}
	for _, id := range s.st.ResumableJobIDs() {
		log.Printf("batch resume: relaunching job %d", id)
		s.launchJob(id)
	}
	s.scheduleTick() // admit any jobs that were left 'queued' when the server stopped
}
