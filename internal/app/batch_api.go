package app

import (
	"encoding/json"
	"io"
	"net/http"
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

// ---------- config (admin) ----------

func (s *Server) apiBatchConfigGet(w http.ResponseWriter, r *http.Request, user string) {
	pw := s.prioWeights()
	writeJSON(w, map[string]any{
		"max_jobs":           s.batchBudget(),                                   // queue budget: jobs running at once (ADR 0004)
		"reserved_slots":     s.batchReserved(),                                 // slots held for 加急 (ADR 0004)
		"ticket_period_days": s.ticketPeriodDays(),                              // how often 加急 tickets refill (ADR 0005)
		"default_priority":   s.runDefaultPriority(),                            // base priority (0..100) for no-group runs (ADR 0008)
		"urgent_enabled":     s.urgentEnabled(),                                 // is the 加急 lane offered at all (admin toggle)
		"dify_end_user":      s.st.GetSetting("dify_end_user", "report-portal"), // Dify end-user template ([username] var)
		// Multifactor priority weights + factor tuning (ADR 0008).
		"prio_w_base":              pw.Base,
		"prio_w_age":               pw.Age,
		"prio_w_fair":              pw.Fair,
		"prio_age_hours":           s.prioAgeHours(),
		"prio_fair_halflife_hours": s.prioFairHalflifeHours(),
	})
}

func (s *Server) apiBatchConfigSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		MaxJobs          *int    `json:"max_jobs"`
		ReservedSlots    *int    `json:"reserved_slots"`
		TicketPeriodDays *int    `json:"ticket_period_days"`
		DefaultPriority  *string `json:"default_priority"`
		UrgentEnabled    *bool   `json:"urgent_enabled"` // admin toggle for the 加急 lane
		DifyEndUser      *string `json:"dify_end_user"`  // Dify end-user template ([username] var)
		// Multifactor priority tuning; pointers so an omitted field is left unchanged
		// (a weight of 0 is meaningful — it disables that factor). See ADR 0008.
		PrioWBase             *float64 `json:"prio_w_base"`
		PrioWAge              *float64 `json:"prio_w_age"`
		PrioWFair             *float64 `json:"prio_w_fair"`
		PrioAgeHours          *float64 `json:"prio_age_hours"`
		PrioFairHalflifeHours *float64 `json:"prio_fair_halflife_hours"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// The no-group default is a base number (0..100); 加急 stays ticket-gated, so it
	// can't be a silent default (groupPriorityValid rejects it).
	if in.DefaultPriority != nil {
		if p := s.groupPriorityValid(*in.DefaultPriority); p != "" {
			s.st.SetSetting("run_default_priority", p)
		}
	}
	if in.MaxJobs != nil && *in.MaxJobs >= 1 {
		s.st.SetSetting("batch_max_concurrent_jobs", strconv.Itoa(*in.MaxJobs))
	}
	if in.ReservedSlots != nil && *in.ReservedSlots >= 0 {
		s.st.SetSetting("batch_reserved_slots", strconv.Itoa(*in.ReservedSlots))
	}
	if in.TicketPeriodDays != nil && *in.TicketPeriodDays >= 1 {
		s.st.SetSetting("batch_ticket_period_days", strconv.Itoa(*in.TicketPeriodDays))
	}
	if in.UrgentEnabled != nil {
		v := "0"
		if *in.UrgentEnabled {
			v = "1"
		}
		s.st.SetSetting("batch_urgent_enabled", v)
	}
	if in.DifyEndUser != nil {
		s.st.SetSetting("dify_end_user", *in.DifyEndUser) // difyEndUser trims + defaults on read
	}
	setFloat := func(key string, v *float64, min float64) {
		if v != nil && *v >= min {
			s.st.SetSetting(key, strconv.FormatFloat(*v, 'f', -1, 64))
		}
	}
	setFloat("batch_prio_w_base", in.PrioWBase, 0) // 0 disables the factor
	setFloat("batch_prio_w_age", in.PrioWAge, 0)
	setFloat("batch_prio_w_fair", in.PrioWFair, 0)
	setFloat("batch_prio_age_hours", in.PrioAgeHours, 0.0001) // must be > 0 (divisor)
	setFloat("batch_prio_fair_halflife_hours", in.PrioFairHalflifeHours, 0.0001)
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
	firstInputs := s.st.AllJobsFirstInputs()
	now := time.Now()
	out := make([]map[string]any, 0)
	for _, j := range s.st.ListBatchJobs() {
		m := jobJSON(j)
		m["inputs"] = firstInputs[j.ID] // first row's inputs (JSON string) for a "标的" label
		// A running job's stored counts are only written at finish; fill live counts
		// so the console shows real-time progress.
		if j.Status == "running" || j.Status == "cancelling" {
			_, _, succeeded, partial, failed := s.st.LiveJobCounts(j.ID)
			m["succeeded"], m["partial"], m["failed"] = succeeded, partial, failed
			m["node"] = s.jobCurrentNode(j.ID) // live: which node the running row is on
		}
		if j.Status == "queued" {
			// A not-yet-due 定时 job is "scheduled", not "waiting"; flag it so the UI can
			// distinguish, and don't show an ahead count for it.
			if runAtDue(j.RunAt, now) {
				m["ahead"] = queue.Ahead(itemByID(waiting, j.ID), waiting)
			} else {
				m["scheduled"] = true
			}
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
	// Resolve the stored priority: an explicit 加急 escalates (ticket-gated below);
	// otherwise the base priority resolves from the submitter's group default / the
	// system default (ADR 0008). Only admins may set an explicit base number — a
	// non-admin can't hand themselves a higher priority to jump the queue. base is the
	// fallback if 加急 is denied.
	base := s.resolveBasePriority(user)
	stored := strconv.Itoa(base)
	if in.Priority != "" {
		if b, urgent := parsePriority(in.Priority); urgent {
			stored = "urgent"
		} else if s.isAdmin(user) {
			base, stored = b, strconv.Itoa(b)
		}
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
	// 加急 costs a ticket for non-admins; out of tickets → runs at its base priority.
	priority, downgraded := s.urgentAllowed(user, stored, base)
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

// apiBatchTickets reports the caller's 加急 ticket balance for the run form. Users
// in an unlimited group are exempt from ticket spending, regardless of role.
func (s *Server) apiBatchTickets(w http.ResponseWriter, r *http.Request, user string) {
	// urgent_enabled lets the run forms hide the 加急 control entirely when the lane
	// is turned off (admin toggle), independent of ticket balance.
	enabled := s.urgentEnabled()
	if s.st.UserUrgentUnlimited(user) {
		writeJSON(w, map[string]any{"unlimited": true, "urgent_enabled": enabled})
		return
	}
	alloc := s.st.UserTicketAllocation(user)
	remaining := s.st.TicketStatus(user, alloc, s.ticketPeriodDays(), time.Now())
	writeJSON(w, map[string]any{"unlimited": false, "remaining": remaining, "allocation": alloc, "period_days": s.ticketPeriodDays(), "urgent_enabled": enabled})
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
		row := map[string]any{
			"id": it.ID, "row_index": it.RowIndex, "inputs": it.Inputs, "status": it.Status,
			"attempts": it.Attempts, "run_id": it.RunID, "error": it.Error,
			"started_at": it.StartedAt, "finished_at": it.FinishedAt,
		}
		if it.Status == "running" {
			row["progress"] = s.itemNode(it.ID) // live node label
		}
		items = append(items, row)
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
	id := pathID(r, "id")
	if err := s.st.CancelBatchJob(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cancelRunningJob(id) // abort the in-flight run so the cancel is immediate
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
	norm, ok := "", false
	if err := readJSON(r, &in); err == nil {
		norm, ok = normalizePriorityInput(in.Priority)
	}
	if !ok {
		jsonError(w, http.StatusBadRequest, "bad priority")
		return
	}
	if err := s.st.SetJobPriority(id, norm); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleTick()
	writeJSON(w, map[string]any{"ok": true, "priority": norm})
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
		"waiting":     len(s.queuedItems()), // due, awaiting admission (excludes not-yet-due)
		"running":     s.st.RunningJobCount(),
		"scheduled":   scheduled,
		"budget":      s.batchBudget(),
		"reserved":    s.batchReserved(),
		"my_priority": s.resolveBasePriority(user), // the caller's resolved base priority (0..100, ADR 0008)
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
