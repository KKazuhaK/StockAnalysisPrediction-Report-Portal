package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTP surface for recurring tasks (scheduled tasks; docs/adr/0018-recurring-tasks.md). All routes are
// PermRunBatch (server.go); mutations additionally check ownership here — a non-admin manages only
// their own tasks, an admin any. The channel is /api/admin/batch/*, matching the batch console
// (ADR 0018 §7); the app-bridge is irrelevant since the actual firing is a trusted server loop.

// recurringIn is the create/update request body. Rows is the job template in the exact shape the
// batch create endpoint takes (1 row = a single run, N rows = a batch).
type recurringIn struct {
	Name        string              `json:"name"`
	TargetID    int64               `json:"target_id"`
	Rows        []map[string]string `json:"rows"`
	Concurrency int                 `json:"concurrency"`
	Priority    string              `json:"priority"` // "" normal | "idle" | (admin) "urgent" | (admin) a base number 0..100
	MaxRetries  int                 `json:"max_retries"`
	Freq        string              `json:"freq"` // daily | weekly | monthly
	AtTime      string              `json:"at_time"`
	Weekday     int                 `json:"weekday"`
	Monthday    int                 `json:"monthday"`
	Enabled     bool                `json:"enabled"`
}

// validateRecurring turns a request body into a stored task, returning a non-empty error message on
// bad input. It enforces the cadence vocabulary, a resolvable target, a non-empty template, and the
// priority rules: anyone may pick blank (normal) or idle; only an admin may set urgent (top priority)
// or an explicit base number (0..100). A non-admin's attempt at either is coerced to normal.
func (s *Server) validateRecurring(in recurringIn, admin bool) (RecurringTask, string) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return RecurringTask{}, "name is required"
	}
	if _, ok := s.st.GetTarget(in.TargetID); !ok {
		return RecurringTask{}, "target not found"
	}
	if len(in.Rows) == 0 {
		return RecurringTask{}, "template has no rows"
	}
	switch in.Freq {
	case "daily", "weekly", "monthly":
	default:
		return RecurringTask{}, "freq must be daily, weekly, or monthly"
	}
	if _, _, ok := parseHHMM(in.AtTime); !ok {
		return RecurringTask{}, "bad time (want HH:MM)"
	}
	if in.Freq == "weekly" && (in.Weekday < 0 || in.Weekday > 6) {
		return RecurringTask{}, "weekday must be 0..6"
	}
	if in.Freq == "monthly" && (in.Monthday < 1 || in.Monthday > 31) {
		return RecurringTask{}, "monthday must be 1..31"
	}
	// '' (normal) resolves to the creator's group base at fire time; 'idle' is open to anyone; an
	// admin may additionally pin 'urgent' (top priority, ticketless) or an explicit base number.
	priority := ""
	if in.Priority != "" {
		b, urgent, idle := parsePriority(in.Priority)
		switch {
		case idle:
			priority = "idle"
		case urgent && admin:
			priority = "urgent"
		case !urgent && admin:
			priority = strconv.Itoa(b)
		}
	}
	conc := in.Concurrency
	if conc < 1 {
		conc = 1
	}
	maxRetries := in.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	rowsJSON, _ := json.Marshal(in.Rows)
	return RecurringTask{
		Name: name, TargetID: in.TargetID, Rows: string(rowsJSON),
		Concurrency: conc, Priority: priority, MaxRetries: maxRetries,
		Freq: in.Freq, AtTime: in.AtTime, Weekday: in.Weekday, Monthday: in.Monthday,
		Enabled: in.Enabled,
	}, ""
}

// recurringAuthorized fetches a task and confirms the caller may touch it (owner or admin).
func (s *Server) recurringAuthorized(w http.ResponseWriter, r *http.Request, user string) (RecurringTask, bool) {
	task, ok := s.st.GetRecurringTask(pathID(r, "id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "task not found")
		return RecurringTask{}, false
	}
	if !s.isAdmin(user) && task.CreatedBy != user {
		jsonError(w, http.StatusForbidden, "you can only manage your own tasks")
		return RecurringTask{}, false
	}
	return task, true
}

// recurringJSON renders a task for the wire, adding the target's current name, the template row
// count, and the computed next fire time (panel tz).
func (s *Server) recurringJSON(t RecurringTask, targetName string) map[string]any {
	var rows []map[string]string
	json.Unmarshal([]byte(t.Rows), &rows)
	m := map[string]any{
		"id": t.ID, "name": t.Name, "target_id": t.TargetID, "target_name": targetName,
		"concurrency": t.Concurrency, "priority": t.Priority, "max_retries": t.MaxRetries,
		"freq": t.Freq, "at_time": t.AtTime, "weekday": t.Weekday, "monthday": t.Monthday,
		"enabled": t.Enabled, "created_by": t.CreatedBy, "created_at": t.CreatedAt,
		"last_fired": t.LastFired, "row_count": len(rows),
	}
	if nx, ok := nextCadence(t.Freq, t.AtTime, t.Weekday, t.Monthday, time.Now(), s.panelLocation()); ok {
		m["next_run"] = nx.Format("2006-01-02 15:04:05")
	}
	return m
}

// targetNames returns an id→name lookup for the current targets (one query for a list render).
func (s *Server) targetNames() map[int64]string {
	names := map[int64]string{}
	for _, tg := range s.st.ListTargets() {
		names[tg.ID] = tg.Name
	}
	return names
}

func (s *Server) apiRecurringList(w http.ResponseWriter, r *http.Request, user string) {
	names := s.targetNames()
	admin := s.isAdmin(user)
	out := make([]map[string]any, 0)
	for _, t := range s.st.ListRecurringTasks() {
		if !admin && t.CreatedBy != user {
			continue // a non-admin sees only their own tasks
		}
		out = append(out, s.recurringJSON(t, names[t.TargetID]))
	}
	writeJSON(w, map[string]any{"tasks": out})
}

func (s *Server) apiRecurringCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in recurringIn
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	task, errMsg := s.validateRecurring(in, s.isAdmin(user))
	if errMsg != "" {
		jsonError(w, http.StatusBadRequest, errMsg)
		return
	}
	task.CreatedBy = user
	id, err := s.st.CreateRecurringTask(task)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiRecurringDetail(w http.ResponseWriter, r *http.Request, user string) {
	task, ok := s.recurringAuthorized(w, r, user)
	if !ok {
		return
	}
	names := s.targetNames()
	m := s.recurringJSON(task, names[task.TargetID])
	var rows []map[string]string
	json.Unmarshal([]byte(task.Rows), &rows)
	m["rows"] = rows // the full template, for the edit form to prefill
	m["history"] = s.st.ListRecurringRuns(task.ID, 0)
	writeJSON(w, m)
}

func (s *Server) apiRecurringUpdate(w http.ResponseWriter, r *http.Request, user string) {
	existing, ok := s.recurringAuthorized(w, r, user)
	if !ok {
		return
	}
	var in recurringIn
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	task, errMsg := s.validateRecurring(in, s.isAdmin(user))
	if errMsg != "" {
		jsonError(w, http.StatusBadRequest, errMsg)
		return
	}
	task.ID = existing.ID
	if err := s.st.UpdateRecurringTask(task); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiRecurringEnable(w http.ResponseWriter, r *http.Request, user string) {
	task, ok := s.recurringAuthorized(w, r, user)
	if !ok {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.st.SetRecurringEnabled(task.ID, in.Enabled); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiRecurringRunNow fires the task immediately (out of cadence), for a manual test/one-off. It does
// not touch last_fired, so the next scheduled fire is unaffected. A create failure is a 500 (real
// internal error); a genuine no-op (missing target / empty template) is a 400.
func (s *Server) apiRecurringRunNow(w http.ResponseWriter, r *http.Request, user string) {
	task, ok := s.recurringAuthorized(w, r, user)
	if !ok {
		return
	}
	jobID, err := s.fireRecurringTask(task)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobID == 0 {
		jsonError(w, http.StatusBadRequest, "nothing to run (missing target or empty template)")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "job_id": jobID})
}

func (s *Server) apiRecurringDelete(w http.ResponseWriter, r *http.Request, user string) {
	task, ok := s.recurringAuthorized(w, r, user)
	if !ok {
		return
	}
	if err := s.st.DeleteRecurringTask(task.ID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}
