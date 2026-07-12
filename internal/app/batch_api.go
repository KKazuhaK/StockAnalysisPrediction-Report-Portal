package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// difyManualReconcileWait bounds a manual reconcile so the HTTP request stays responsive. A run
// that already finished on Dify settles on the first status fetch; one still genuinely running is
// reported as such rather than polled for the full run timeout.
const difyManualReconcileWait = 60 * time.Second

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
		"max_jobs":                 s.batchBudget(),                                   // queue budget: jobs running at once (ADR 0004)
		"reserved_slots":           s.batchReserved(),                                 // slots held for 加急 (ADR 0004)
		"ticket_period_days":       s.ticketPeriodDays(),                              // how often 加急 tickets refill (ADR 0005)
		"default_priority":         s.runDefaultPriority(),                            // base priority (0..100) for no-group runs (ADR 0008)
		"urgent_enabled":           s.urgentEnabled(),                                 // is the 加急 lane offered at all (admin toggle)
		"dify_end_user":            s.st.GetSetting("dify_end_user", "report-portal"), // Dify end-user template ([username] var)
		"dify_poll_seconds":        s.difyPollSeconds(),                               // 0 = streaming; >0 = poll the run status every N s (proxy-friendly)
		"dify_run_timeout_minutes": int(s.difyRunTimeoutDur() / time.Minute),          // cap on one run: portal HTTP client + reconcile poll window
		"run_default_mode":         s.st.GetSetting("run_default_mode", "now"),        // run form default: now|preset|scheduled (ADR 0014)
		"run_default_idle":         s.st.GetSetting("run_default_idle", "0") == "1",   // pre-check "run when queue idle" (immediate mode only)
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
		MaxJobs               *int    `json:"max_jobs"`
		ReservedSlots         *int    `json:"reserved_slots"`
		TicketPeriodDays      *int    `json:"ticket_period_days"`
		DefaultPriority       *string `json:"default_priority"`
		UrgentEnabled         *bool   `json:"urgent_enabled"`           // admin toggle for the 加急 lane
		DifyEndUser           *string `json:"dify_end_user"`            // Dify end-user template ([username] var)
		DifyPollSeconds       *int    `json:"dify_poll_seconds"`        // 0 = streaming; >0 = poll the run status every N s
		DifyRunTimeoutMinutes *int    `json:"dify_run_timeout_minutes"` // cap on one run (HTTP client + reconcile window)
		RunDefaultMode        *string `json:"run_default_mode"`         // run form default button: now|preset|scheduled
		RunDefaultIdle        *bool   `json:"run_default_idle"`         // pre-check "run when queue idle" (immediate mode)
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
	if in.DifyPollSeconds != nil && *in.DifyPollSeconds >= 0 {
		s.st.SetSetting("dify_poll_seconds", strconv.Itoa(*in.DifyPollSeconds))
	}
	if in.DifyRunTimeoutMinutes != nil && *in.DifyRunTimeoutMinutes >= 1 {
		s.st.SetSetting("dify_run_timeout_minutes", strconv.Itoa(*in.DifyRunTimeoutMinutes))
	}
	if in.DifyEndUser != nil {
		s.st.SetSetting("dify_end_user", *in.DifyEndUser) // difyEndUser trims + defaults on read
	}
	if in.RunDefaultMode != nil {
		switch *in.RunDefaultMode {
		case "now", "preset", "scheduled":
			s.st.SetSetting("run_default_mode", *in.RunDefaultMode)
		}
	}
	if in.RunDefaultIdle != nil {
		s.st.SetSetting("run_default_idle", strconv.Itoa(boolInt(*in.RunDefaultIdle)))
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
		m["mode"] = difyTargetMode(t.Config) // "" / "workflow" / "chat"
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

// apiBatchTargetReorder persists the admin's drag-to-sort order of targets: it stores each
// id's position, so the list keeps that order everywhere ListTargets is read.
func (s *Server) apiBatchTargetReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	for i, id := range in.IDs {
		s.st.SetTargetOrder(id, i)
	}
	writeJSON(w, okJSON)
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
	// cancelled rows are terminal but neither success nor failure; derive them so the
	// progress bar can treat them as done (total − ok − partial − failed). A running
	// job's live cancelled count is filled in by apiBatchJobs from LiveJobCounts.
	cancelled := j.Total - j.Succeeded - j.Partial - j.Failed
	if cancelled < 0 {
		cancelled = 0
	}
	return map[string]any{
		"id": j.ID, "target_id": j.TargetID, "status": j.Status, "priority": j.Priority,
		"concurrency": j.Concurrency, "max_retries": j.MaxRetries,
		"total": j.Total, "succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed, "cancelled": cancelled,
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
			_, _, succeeded, partial, failed, cancelled := s.st.LiveJobCounts(j.ID)
			m["succeeded"], m["partial"], m["failed"], m["cancelled"] = succeeded, partial, failed, cancelled
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
		RunAt       string              `json:"run_at"`    // one-shot 定时运行; "" = run now
		PresetID    int64               `json:"preset_id"` // preset low-peak window to schedule into (ADR 0014); 0 = none
		Notify      bool                `json:"notify"`    // email the submitter when the job finishes
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
		if b, urgent, idle := parsePriority(in.Priority); urgent {
			stored = "urgent"
		} else if idle {
			stored = "idle" // run-when-queue-idle bottom lane; anyone may pick it (it only lowers priority, ADR 0014)
		} else if s.isAdmin(user) {
			base, stored = b, strconv.Itoa(b)
		}
	}
	if len(in.Rows) == 0 {
		jsonError(w, http.StatusBadRequest, "no rows to run")
		return
	}
	// Resolve the optional schedule up front so a bad time never leaves an orphan job. A preset
	// window (preset_id) resolves to run_at + a run_preset snapshot in the panel timezone (ADR
	// 0014); an explicit run_at is the plain 定时 path. They're mutually exclusive (preset wins).
	runAt, runPreset := "", ""
	if in.PresetID != 0 {
		p, ok := s.st.GetRunPreset(in.PresetID)
		if !ok || !p.Enabled {
			jsonError(w, http.StatusBadRequest, "unknown or disabled preset")
			return
		}
		var intervals []presetInterval
		json.Unmarshal([]byte(p.Intervals), &intervals)
		ra, snap, ok := resolvePresetWindow(p.Freq, intervals, p.OnOverrun, p.Invert, time.Now(), s.panelLocation())
		if !ok {
			jsonError(w, http.StatusBadRequest, "preset window is misconfigured")
			return
		}
		runAt, runPreset = ra, snap
	} else if in.RunAt != "" {
		rt, ok := normalizeRunAt(in.RunAt)
		if !ok {
			jsonError(w, http.StatusBadRequest, "bad run_at")
			return
		}
		runAt = rt
	}
	// Per-group governance (group model B): non-admins are held to their group's run
	// window and active-run cap. Admins are exempt (operational). allow-urgent is enforced
	// separately, inside urgentAllowed. The window is checked against the effective run
	// time — a scheduled job's run_at hour, not submit time — so a run can't be scheduled
	// to slip outside the window (nor wrongly rejected for a submit that lands outside it).
	if !s.isAdmin(user) {
		hour := time.Now().In(s.panelLocation()).Hour()
		if runAt != "" {
			if rt, err := time.ParseInLocation("2006-01-02 15:04:05", runAt, time.Local); err == nil {
				hour = rt.In(s.panelLocation()).Hour()
			}
		}
		if open, win := s.runWindowOpenAt(user, hour); !open {
			jsonError(w, http.StatusForbidden, "runs are only allowed during "+win+":00 (panel time)")
			return
		}
		if cap := s.st.EffectiveGroupSettings(user).MaxQueued; cap > 0 && s.st.ActiveJobCount(user) >= cap {
			jsonError(w, http.StatusConflict, "you already have the maximum number of active runs; wait for one to finish")
			return
		}
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
	if runPreset != "" {
		s.st.SetJobWindow(jobID, runAt, runPreset) // preset window: run_at + snapshot (ADR 0014)
	} else if runAt != "" {
		s.st.ScheduleJob(jobID, runAt) // hidden from admission until run_at passes
	}
	if in.Notify {
		s.jobNotify.Store(jobID, true) // email the submitter on finish (best-effort, in-memory)
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
	queued, running, succeeded, partial, failed, cancelled := s.st.LiveJobCounts(id)
	items := make([]map[string]any, 0)
	for _, it := range s.st.BatchJobItems(id) {
		row := map[string]any{
			"id": it.ID, "row_index": it.RowIndex, "inputs": it.Inputs, "status": it.Status,
			"attempts": it.Attempts, "run_id": it.RunID, "conversation_id": it.ConvID, "task_id": it.TaskID,
			"error": it.Error, "started_at": it.StartedAt, "finished_at": it.FinishedAt,
		}
		items = append(items, row)
	}
	_, inProc := s.jobRuns.Load(id) // a job with an active run scope is executing here
	m := jobJSON(job)
	if job.Status == "queued" {
		waiting := s.queuedItems()
		m["ahead"] = queue.Ahead(itemByID(waiting, id), waiting)
	}
	writeJSON(w, map[string]any{
		"job":                m,
		"counts":             map[string]int{"queued": queued, "running": running, "succeeded": succeeded, "partial": partial, "failed": failed, "cancelled": cancelled},
		"running_in_process": inProc,
		"items":              items,
	})
}

// apiBatchItemReconcile settles ONE row by reconciling its persisted Dify handle WITHOUT
// re-running the workflow — the admin's manual counterpart to the restart-time reconcile. It
// refuses a row with no run/conversation id (nothing to reconcile) and a row currently running
// here (its own drop-reconcile owns the outcome). Bounded by difyManualReconcileWait so a run
// still executing on Dify is reported "running" instead of hanging the request.
func (s *Server) apiBatchItemReconcile(w http.ResponseWriter, r *http.Request, user string) {
	itemID := pathID(r, "id")
	ref, status, ok := s.st.ItemReconcileRef(itemID)
	if !ok {
		jsonError(w, http.StatusNotFound, "item not found")
		return
	}
	if ref.RunID == "" && ref.ConvID == "" {
		jsonError(w, http.StatusBadRequest, "no dify run or conversation id to reconcile")
		return
	}
	if _, live := s.itemCancels.Load(itemID); live && status == "running" {
		jsonError(w, http.StatusConflict, "item is running here; it reconciles itself on drop")
		return
	}
	job, ok := s.st.GetBatchJob(ref.JobID)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	prov, err := s.providerFor(job, nil, nil)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec, ok := prov.(batch.Reconciler)
	if !ok {
		jsonError(w, http.StatusBadRequest, "this target's provider cannot reconcile")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), difyManualReconcileWait)
	defer cancel()
	res, err := rec.Reconcile(ctx, ref.RunID, ref.ConvID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// Still executing on Dify — leave the row untouched and report it (don't mark failed).
			writeJSON(w, map[string]any{"ok": true, "status": "running", "note": "still running on dify"})
			return
		}
		jsonError(w, http.StatusBadGateway, "reconcile: "+err.Error())
		return
	}
	if err := s.st.FinishItem(itemID, res.Status, 1, res.RunID, res.Detail); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.afterItem(ref.JobID)
	writeJSON(w, map[string]any{"ok": true, "status": itemStatus(res.Status), "run_id": res.RunID, "detail": res.Detail})
}

// apiBatchItemsCancel cancels one or more individual rows of a job (ADR 0011): a queued
// row is skipped (never runs), a running row's Dify call is aborted; both land 'cancelled'
// (not 'failed'). It backs both the per-row ⊘ (one id) and multi-select (many). Auth
// mirrors job cancel: a non-admin may only cancel rows of their own job.
func (s *Server) apiBatchItemsCancel(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	job, ok := s.st.GetBatchJob(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	if !s.isAdmin(user) && job.CreatedBy != user {
		jsonError(w, http.StatusForbidden, "you can only cancel your own runs")
		return
	}
	var in struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	n := 0
	for _, itemID := range in.ItemIDs {
		jobID, status, ok := s.st.ItemJobAndStatus(itemID)
		if !ok || jobID != id { // ignore ids that aren't this job's rows
			continue
		}
		switch status {
		case "queued":
			if s.st.CancelQueuedItem(itemID) { // skip a not-yet-run row
				n++
			}
		case "running":
			s.cancelRunningItem(itemID) // abort the in-flight run; its goroutine marks it cancelled
			n++
		}
	}
	// Cancelling queued rows may have left the job with nothing more to run.
	s.finalizeJob(id)
	writeJSON(w, map[string]any{"cancelled": n})
}

func (s *Server) apiBatchJobCancel(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	job, ok := s.st.GetBatchJob(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	// A non-admin may cancel only their own run; admins may cancel anyone's. All other
	// queue mutations (delete / retry / reschedule / reprioritize) are admin-only routes.
	if !s.isAdmin(user) && job.CreatedBy != user {
		jsonError(w, http.StatusForbidden, "you can only cancel your own runs")
		return
	}
	if err := s.st.CancelBatchJob(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cancelRunningJob(id) // abort the in-flight run so the cancel is immediate
	// Close it out now: if the job had no in-flight run (its rows were parked behind a
	// saturated budget), no finishing run will ever finalize it — without this it would
	// strand in 'cancelling'. finalizeJob is a no-op if runs are still in flight (their
	// afterItem finalizes later) or the job was already terminal.
	s.finalizeJob(id)
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
		"waiting":      len(s.queuedItems()), // due, awaiting admission (excludes not-yet-due)
		"running":      s.st.RunningJobCount(),
		"running_rows": s.st.RunningItemCount(), // concurrent runs (rows) — what the run cap governs
		"scheduled":    scheduled,
		"budget":       s.batchBudget(),
		"reserved":     s.batchReserved(),
		"my_priority":  s.resolveBasePriority(user), // the caller's resolved base priority (0..100, ADR 0008)
	})
}

// apiBatchClearFinished deletes every terminal (finished/cancelled) job at once. Admin-
// only (like single delete); active jobs are left running.
func (s *Server) apiBatchClearFinished(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"ok": true, "n": s.st.DeleteFinishedJobs()})
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
