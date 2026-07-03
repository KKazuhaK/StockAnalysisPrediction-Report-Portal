package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// HTTP handlers for the batch-run feature. Plugin/market/target/config management
// is admin-only (PermManage); listing targets and running jobs is PermRunBatch.
// See docs/adr/0001-batch-run-engine.md.

// itemByID returns the queue item with the given id (zero value if absent), so the
// API can compute its "N ahead" position.
func itemByID(items []queue.Item, id int64) queue.Item {
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	return queue.Item{ID: id}
}

// ---------- plugins ----------

func pluginJSON(p Plugin) map[string]any {
	inputs := []batch.InputDecl{}
	config := []batch.ConfigDecl{}
	if m, err := batch.Compile([]byte(p.Spec)); err == nil {
		inputs = m.Inputs()
		config = m.Config()
	}
	return map[string]any{
		"slug": p.Slug, "name": p.Name, "version": p.Version,
		"source": p.Source, "enabled": p.Enabled, "inputs": inputs, "config": config,
	}
}

func (s *Server) apiBatchPlugins(w http.ResponseWriter, r *http.Request, user string) {
	out := make([]map[string]any, 0)
	for _, p := range s.st.ListPlugins() {
		out = append(out, pluginJSON(p))
	}
	writeJSON(w, map[string]any{"plugins": out})
}

// apiBatchPluginImport sideloads a manifest from the request body (the offline /
// private path). The manifest is validated by the interpreter before it is stored.
func (s *Server) apiBatchPluginImport(w http.ResponseWriter, r *http.Request, user string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body failed")
		return
	}
	m, err := batch.Compile(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid manifest: "+err.Error())
		return
	}
	if m.ID == "" {
		jsonError(w, http.StatusBadRequest, "manifest 'id' is required")
		return
	}
	if err := s.st.UpsertPlugin(m.ID, m.Name, m.Version, string(raw), "imported"); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "slug": m.ID})
}

func (s *Server) apiBatchPluginDelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeletePlugin(r.PathValue("slug")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// ---------- market (GitHub) ----------

type marketEntry struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Path        string `json:"path"` // manifest path relative to the index URL
	URL         string `json:"url"`  // absolute manifest URL (overrides Path when set)
}

func fetchBytes(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) marketIndexURL() string {
	return s.st.GetSetting("batch_market_index_url", defaultMarketIndexURL)
}

func (s *Server) fetchMarket(ctx context.Context) ([]marketEntry, error) {
	raw, err := fetchBytes(ctx, s.marketIndexURL())
	if err != nil {
		return nil, err
	}
	var idx struct {
		Plugins []marketEntry `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index is not valid JSON: %w", err)
	}
	return idx.Plugins, nil
}

func (s *Server) apiBatchMarket(w http.ResponseWriter, r *http.Request, user string) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	entries, err := s.fetchMarket(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch market failed: "+err.Error())
		return
	}
	installed := map[string]bool{}
	for _, p := range s.st.ListPlugins() {
		installed[p.Slug] = true
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"slug": e.Slug, "name": e.Name, "version": e.Version,
			"description": e.Description, "installed": installed[e.Slug],
		})
	}
	writeJSON(w, map[string]any{"index_url": s.marketIndexURL(), "plugins": out})
}

// apiBatchMarketInstall fetches one manifest from the configured GitHub market and
// installs it (validated by the interpreter first).
func (s *Server) apiBatchMarketInstall(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Slug string `json:"slug"`
	}
	if err := readJSON(r, &in); err != nil || in.Slug == "" {
		jsonError(w, http.StatusBadRequest, "slug is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	entries, err := s.fetchMarket(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch market failed: "+err.Error())
		return
	}
	var entry *marketEntry
	for i := range entries {
		if entries[i].Slug == in.Slug {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		jsonError(w, http.StatusNotFound, "plugin not found in market")
		return
	}
	manifestURL := entry.URL
	if manifestURL == "" {
		base, err := url.Parse(s.marketIndexURL())
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "bad market index url")
			return
		}
		ref, err := url.Parse(entry.Path)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "bad manifest path")
			return
		}
		manifestURL = base.ResolveReference(ref).String()
	}
	raw, err := fetchBytes(ctx, manifestURL)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch manifest failed: "+err.Error())
		return
	}
	if _, err := batch.Compile(raw); err != nil {
		jsonError(w, http.StatusBadGateway, "manifest is invalid: "+err.Error())
		return
	}
	if err := s.st.UpsertPlugin(entry.Slug, entry.Name, entry.Version, string(raw), "market"); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "slug": entry.Slug})
}

// ---------- config (admin) ----------

func (s *Server) apiBatchConfigGet(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{
		"max_concurrency":    s.batchMaxConcurrency(),
		"max_jobs":           s.batchBudget(),        // queue budget: jobs running at once (ADR 0004)
		"reserved_slots":     s.batchReserved(),      // slots held for the urgent tier
		"ticket_period_days": s.ticketPeriodDays(),   // how often 加急 tickets refill (ADR 0005)
		"default_priority":   s.runDefaultPriority(), // fallback priority for no-group runs (ADR 0007)
		"market_index_url":   s.marketIndexURL(),
	})
}

func (s *Server) apiBatchConfigSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		MaxConcurrency   int    `json:"max_concurrency"`
		MaxJobs          int    `json:"max_jobs"`
		ReservedSlots    int    `json:"reserved_slots"`
		TicketPeriodDays int    `json:"ticket_period_days"`
		DefaultPriority  string `json:"default_priority"`
		MarketIndexURL   string `json:"market_index_url"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Only a registered, non-reserved tier may be the no-group default (加急 stays
	// ticket-gated, so it can't be a silent default).
	if p := s.groupPriorityValid(in.DefaultPriority); p != "" {
		s.st.SetSetting("run_default_priority", p)
	}
	if in.MaxConcurrency >= 1 {
		s.st.SetSetting("batch_max_concurrency", strconv.Itoa(in.MaxConcurrency))
	}
	if in.MaxJobs >= 1 {
		s.st.SetSetting("batch_max_concurrent_jobs", strconv.Itoa(in.MaxJobs))
	}
	if in.ReservedSlots >= 0 {
		s.st.SetSetting("batch_reserved_slots", strconv.Itoa(in.ReservedSlots))
	}
	if in.TicketPeriodDays >= 1 {
		s.st.SetSetting("batch_ticket_period_days", strconv.Itoa(in.TicketPeriodDays))
	}
	if in.MarketIndexURL != "" {
		s.st.SetSetting("batch_market_index_url", in.MarketIndexURL)
	}
	// A raised budget may let queued jobs start right away.
	s.scheduleTick()
	writeJSON(w, okJSON)
}

// ---------- targets ----------

// targetJSON never exposes a target's config (it holds secrets like the api_key);
// it surfaces only what the UI needs, including the plugin's declared inputs so the
// job-create form can render the right fields.
func (s *Server) targetJSON(t BatchTarget) map[string]any {
	m := map[string]any{"id": t.ID, "plugin_slug": t.PluginSlug, "name": t.Name, "created_at": t.Created}
	// Dify-native target: inputs come from the workflow's discovered fields, not a
	// manifest (docs/adr/0006-dify-native.md).
	if t.PluginSlug == difyPluginSlug {
		m["plugin_name"] = "Dify Workflow"
		m["dify"] = true
		m["inputs"] = difyInputsJSON(t.Config)
		return m
	}
	if p, ok := s.st.GetPlugin(t.PluginSlug); ok {
		m["plugin_name"] = p.Name
		if mf, err := batch.Compile([]byte(p.Spec)); err == nil {
			m["inputs"] = mf.Inputs()
		}
	}
	return m
}

func (s *Server) apiBatchTargets(w http.ResponseWriter, r *http.Request, user string) {
	out := make([]map[string]any, 0)
	for _, t := range s.st.ListTargets() {
		out = append(out, s.targetJSON(t))
	}
	writeJSON(w, map[string]any{"targets": out})
}

func (s *Server) apiBatchTargetAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		PluginSlug string            `json:"plugin_slug"`
		Name       string            `json:"name"`
		Config     map[string]string `json:"config"`
	}
	if err := readJSON(r, &in); err != nil || in.PluginSlug == "" {
		jsonError(w, http.StatusBadRequest, "plugin_slug is required")
		return
	}
	if _, ok := s.st.GetPlugin(in.PluginSlug); !ok {
		jsonError(w, http.StatusBadRequest, "unknown plugin")
		return
	}
	cfg, _ := json.Marshal(in.Config)
	id, err := s.st.CreateTarget(in.PluginSlug, in.Name, string(cfg))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiBatchTargetDelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeleteTarget(pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// ---------- jobs ----------

func jobJSON(j BatchJob) map[string]any {
	return map[string]any{
		"id": j.ID, "target_id": j.TargetID, "status": j.Status, "priority": j.Priority,
		"concurrency": j.Concurrency, "max_retries": j.MaxRetries,
		"total": j.Total, "succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed,
		"created_by": j.CreatedBy, "created_at": j.CreatedAt, "started_at": j.StartedAt, "finished_at": j.FinishedAt,
		"run_at": j.RunAt, // one-shot scheduled start ("" = ASAP; ADR 0007)
	}
}

// normalizeRunAt validates a one-shot schedule time and returns it in the canonical
// local "2006-01-02 15:04:05" basis (the same the aging clock uses). It accepts that
// format or RFC3339, so the client can send either. ok=false on an unparseable value.
func normalizeRunAt(v string) (string, bool) {
	const layout = "2006-01-02 15:04:05"
	if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
		return t.Format(layout), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Local().Format(layout), true
	}
	return "", false
}

func (s *Server) apiBatchJobs(w http.ResponseWriter, r *http.Request, user string) {
	waiting := s.queuedItems() // for the live "N ahead" of each queued job
	out := make([]map[string]any, 0)
	for _, j := range s.st.ListBatchJobs() {
		m := jobJSON(j)
		// A running job's stored counts are only written at finish; fill live counts
		// so the console shows real-time progress.
		if j.Status == "running" || j.Status == "cancelling" {
			_, _, succeeded, partial, failed := s.st.LiveJobCounts(j.ID)
			m["succeeded"], m["partial"], m["failed"] = succeeded, partial, failed
		}
		if j.Status == "queued" {
			m["ahead"] = queue.Ahead(itemByID(waiting, j.ID), waiting)
		}
		out = append(out, m)
	}
	writeJSON(w, map[string]any{"jobs": out, "budget": s.batchBudget()})
}

// apiBatchJobCreate validates the target's plugin compiles, clamps concurrency to
// the admin cap, persists the job + queued rows, and launches it in the background.
func (s *Server) apiBatchJobCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		TargetID    int64               `json:"target_id"`
		Concurrency int                 `json:"concurrency"`
		MaxRetries  int                 `json:"max_retries"`
		Priority    string              `json:"priority"`
		RunAt       string              `json:"run_at"` // one-shot 定时运行; "" = run now
		Rows        []map[string]string `json:"rows"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// No explicit choice → resolve from the submitter's group default / system
	// default; an explicit value (incl. 加急) is kept as-is (ADR 0007).
	in.Priority = s.resolvePriority(user, in.Priority)
	if !s.priorityRegistry().Has(in.Priority) {
		jsonError(w, http.StatusBadRequest, "unknown priority")
		return
	}
	if len(in.Rows) == 0 {
		jsonError(w, http.StatusBadRequest, "no rows to run")
		return
	}
	// Validate the optional schedule up front so a bad time never leaves an orphan job.
	runAt := ""
	if in.RunAt != "" {
		rt, ok := normalizeRunAt(in.RunAt)
		if !ok {
			jsonError(w, http.StatusBadRequest, "bad run_at")
			return
		}
		runAt = rt
	}
	tgt, ok := s.st.GetTarget(in.TargetID)
	if !ok {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	// Dify-native targets have no manifest (they run via buildDifyProvider, ADR 0006);
	// only generic plugin targets carry a manifest to validate.
	if tgt.PluginSlug != difyPluginSlug {
		plug, ok := s.st.GetPlugin(tgt.PluginSlug)
		if !ok {
			jsonError(w, http.StatusBadRequest, "target's plugin is missing")
			return
		}
		if _, err := batch.Compile([]byte(plug.Spec)); err != nil {
			jsonError(w, http.StatusBadRequest, "target's plugin manifest is invalid: "+err.Error())
			return
		}
	}
	conc := s.clampConcurrency(in.Concurrency)
	maxRetries := in.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	// 加急 costs a ticket for non-admins; out of tickets → runs as 普通.
	priority, downgraded := s.urgentAllowed(user, in.Priority)
	jobID, err := s.st.CreateBatchJob(in.TargetID, conc, maxRetries, user, in.Rows, priority)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runAt != "" {
		s.st.ScheduleJob(jobID, runAt) // hidden from admission until run_at passes
	}
	s.scheduleTick() // admit now if due + budget allows, else it waits (or waits for its schedule)
	writeJSON(w, map[string]any{"ok": true, "job_id": jobID, "concurrency": conc, "priority": priority, "downgraded": downgraded, "run_at": runAt})
}

// apiBatchTickets reports the caller's 加急 ticket balance for the run form. Admins
// are exempt (unlimited).
func (s *Server) apiBatchTickets(w http.ResponseWriter, r *http.Request, user string) {
	if s.isAdmin(user) {
		writeJSON(w, map[string]any{"unlimited": true})
		return
	}
	alloc := s.st.UserTicketAllocation(user)
	remaining := s.st.TicketStatus(user, alloc, s.ticketPeriodDays(), time.Now())
	writeJSON(w, map[string]any{"unlimited": false, "remaining": remaining, "allocation": alloc, "period_days": s.ticketPeriodDays()})
}

func (s *Server) apiBatchJobDetail(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	job, ok := s.st.GetBatchJob(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	queued, running, succeeded, partial, failed := s.st.LiveJobCounts(id)
	items := make([]map[string]any, 0)
	for _, it := range s.st.BatchJobItems(id) {
		items = append(items, map[string]any{
			"id": it.ID, "row_index": it.RowIndex, "inputs": it.Inputs, "status": it.Status,
			"attempts": it.Attempts, "run_id": it.RunID, "error": it.Error,
			"started_at": it.StartedAt, "finished_at": it.FinishedAt,
		})
	}
	_, inProc := s.batchRunning.Load(id)
	m := jobJSON(job)
	if job.Status == "queued" {
		waiting := s.queuedItems()
		m["ahead"] = queue.Ahead(itemByID(waiting, id), waiting)
	}
	writeJSON(w, map[string]any{
		"job":                m,
		"counts":             map[string]int{"queued": queued, "running": running, "succeeded": succeeded, "partial": partial, "failed": failed},
		"running_in_process": inProc,
		"items":              items,
	})
}

func (s *Server) apiBatchJobCancel(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.CancelBatchJob(pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiBatchJobRetry requeues finished items of the given statuses (default: failed)
// and relaunches the job to process just those rows.
func (s *Server) apiBatchJobRetry(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	var in struct {
		Statuses []string `json:"statuses"`
	}
	readJSON(r, &in)
	n, err := s.st.RequeueItems(id, in.Statuses...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleTick() // re-enqueued — the scheduler re-admits it by priority
	writeJSON(w, map[string]any{"ok": true, "requeued": n})
}

// apiBatchJobReprioritize changes a job's queue priority (插队) and re-runs the
// scheduler, which may admit it immediately if it now outranks the queue.
func (s *Server) apiBatchJobReprioritize(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	var in struct {
		Priority string `json:"priority"`
	}
	if err := readJSON(r, &in); err != nil || !s.priorityRegistry().Has(in.Priority) {
		jsonError(w, http.StatusBadRequest, "unknown priority")
		return
	}
	if err := s.st.SetJobPriority(id, in.Priority); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleTick()
	writeJSON(w, map[string]any{"ok": true, "priority": in.Priority})
}

// apiBatchQueue is a lightweight queue summary for the home banner + drawer:
// waiting (due but not yet admitted), running, scheduled (定时, not yet due), and
// the concurrency budget. Not-yet-due jobs count as scheduled, never as waiting.
func (s *Server) apiBatchQueue(w http.ResponseWriter, r *http.Request, user string) {
	now := time.Now()
	scheduled := 0
	for _, j := range s.st.QueuedJobs() {
		if !runAtDue(j.RunAt, now) {
			scheduled++
		}
	}
	writeJSON(w, map[string]any{
		"waiting":   len(s.queuedItems()), // due, awaiting admission (excludes not-yet-due)
		"running":   s.st.RunningJobCount(),
		"scheduled": scheduled,
		"budget":    s.batchBudget(),
		"reserved":  s.batchReserved(),
	})
}

// apiBatchJobDelete removes a terminal job (finished/cancelled) and its rows. An
// active job (queued/running/cancelling) must be cancelled first — this only
// clears history, it never stops a run.
func (s *Server) apiBatchJobDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	job, ok := s.st.GetBatchJob(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != "finished" && job.Status != "cancelled" {
		jsonError(w, http.StatusConflict, "cancel the job before deleting it")
		return
	}
	if err := s.st.DeleteBatchJob(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiBatchJobSchedule sets or clears a queued job's one-shot start time (改时间 /
// 立即运行). An empty run_at clears the schedule so it runs on the next tick. Only
// a still-queued job can be (re)scheduled.
func (s *Server) apiBatchJobSchedule(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	job, ok := s.st.GetBatchJob(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != "queued" {
		jsonError(w, http.StatusConflict, "only a queued job can be rescheduled")
		return
	}
	var in struct {
		RunAt string `json:"run_at"`
	}
	readJSON(r, &in)
	runAt := ""
	if in.RunAt != "" {
		rt, ok := normalizeRunAt(in.RunAt)
		if !ok {
			jsonError(w, http.StatusBadRequest, "bad run_at")
			return
		}
		runAt = rt
	}
	if err := s.st.ScheduleJob(id, runAt); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleTick() // cleared/now-due → admit; still future → stays hidden
	writeJSON(w, map[string]any{"ok": true, "run_at": runAt})
}
