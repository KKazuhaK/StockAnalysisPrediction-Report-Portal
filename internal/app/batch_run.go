package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// This file is the batch orchestration layer: it ties the store, the engine, and
// the manifest interpreter together, running each job as a background goroutine so
// it survives page refreshes and (via resumeBatchJobs) server restarts. See
// docs/adr/0001-batch-run-engine.md.

// difyRunTimeout bounds a single blocking workflow run. Dify runs can take minutes.
const difyRunTimeout = 10 * time.Minute

const defaultMarketIndexURL = "https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/main/plugins/index.json"

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

// ---------- priority run queue (docs/adr/0004-run-queue.md) ----------

// priorityRegistry is the queue's priority taxonomy. For now it is the built-in
// default (加急 / 普通 / 其他); admin-configurable custom levels are a later step.
func (s *Server) priorityRegistry() queue.Registry { return queue.DefaultRegistry() }

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

// urgentAllowed decides the effective priority for a submitted job: admins may use
// 加急 freely; everyone else must spend a 加急 ticket, otherwise the job is
// downgraded to 普通. Returns the effective priority and whether it was downgraded.
func (s *Server) urgentAllowed(user, priority string) (string, bool) {
	if priority != "urgent" || s.isAdmin(user) {
		return priority, false
	}
	alloc := s.st.UserTicketAllocation(user)
	if ok, _ := s.st.SpendTicket(user, alloc, s.ticketPeriodDays(), time.Now()); ok {
		return priority, false
	}
	return "normal", true // out of 加急 tickets → runs as 普通
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

// queuedItems maps the currently-queued jobs to scheduler items (id + level +
// aging key), used both by the scheduler and by the "N ahead" computation.
func (s *Server) queuedItems() []queue.Item {
	reg := s.priorityRegistry()
	jobs := s.st.QueuedJobs()
	items := make([]queue.Item, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, queue.Item{ID: j.ID, Level: j.Priority, SchedKey: reg.SchedKey(j.Priority, parseEnqueueUnix(j.CreatedAt))})
	}
	return items
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
	reg := s.priorityRegistry()
	running := s.st.RunningJobCount()
	plan := queue.Plan{Budget: s.batchBudget(), Reserved: s.batchReserved()}
	for _, it := range reg.Admit(items, running, plan) {
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
		return buildDifyProvider(tgt.Config)
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
	spec := batch.JobSpec{JobID: jobID, Concurrency: job.Concurrency, MaxRetries: job.MaxRetries}
	go func() {
		defer s.batchRunning.Delete(jobID)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("batch job %d panicked: %v", jobID, r)
			}
		}()
		if err := eng.RunJob(context.Background(), spec, prov); err != nil {
			log.Printf("batch job %d ended with error: %v", jobID, err)
		}
		if j, ok := s.st.GetBatchJob(jobID); ok {
			s.fireEvent(EventBatchFinished, map[string]any{
				"job_id": j.ID, "status": j.Status, "total": j.Total,
				"succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed,
			})
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
