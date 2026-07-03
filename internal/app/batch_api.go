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
		"max_jobs":           s.batchBudget(),      // queue budget: jobs running at once (ADR 0004)
		"reserved_slots":     s.batchReserved(),    // slots held for the urgent tier
		"ticket_period_days": s.ticketPeriodDays(), // how often 加急 tickets refill (ADR 0005)
		"market_index_url":   s.marketIndexURL(),
	})
}

func (s *Server) apiBatchConfigSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		MaxConcurrency   int    `json:"max_concurrency"`
		MaxJobs          int    `json:"max_jobs"`
		ReservedSlots    int    `json:"reserved_slots"`
		TicketPeriodDays int    `json:"ticket_period_days"`
		MarketIndexURL   string `json:"market_index_url"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
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
	}
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
		Rows        []map[string]string `json:"rows"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !s.priorityRegistry().Has(in.Priority) {
		jsonError(w, http.StatusBadRequest, "unknown priority")
		return
	}
	if len(in.Rows) == 0 {
		jsonError(w, http.StatusBadRequest, "no rows to run")
		return
	}
	tgt, ok := s.st.GetTarget(in.TargetID)
	if !ok {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	plug, ok := s.st.GetPlugin(tgt.PluginSlug)
	if !ok {
		jsonError(w, http.StatusBadRequest, "target's plugin is missing")
		return
	}
	if _, err := batch.Compile([]byte(plug.Spec)); err != nil {
		jsonError(w, http.StatusBadRequest, "target's plugin manifest is invalid: "+err.Error())
		return
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
	s.scheduleTick() // enqueued — admit now if the budget allows, else it waits in the queue
	writeJSON(w, map[string]any{"ok": true, "job_id": jobID, "concurrency": conc, "priority": priority, "downgraded": downgraded})
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
