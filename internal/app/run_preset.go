package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Preset low-peak scheduling windows (docs/adr/0014-idle-lane-and-preset-windows.md): an admin
// configures recurring windows (daily/weekly/monthly/yearly) in the run_presets table; a user
// picks one and the run is scheduled into the current-or-next occurrence, rolling to the next
// occurrence (or continuing / cancelling) if the window closes before it starts. This one file
// holds the whole small feature — the pure resolver, the run_presets persistence, and the HTTP
// surface — following the single-file convention of the other compact features (tickets, group…).
// The overrun sweep and the create-handler wiring live with the scheduler (batch_run.go) and the
// job-create handler (batch_api.go) respectively, next to the code they extend.

// ---------- pure window resolver ----------

// presetAnchor is one edge (start or stop) of a preset window. Which fields apply depends on
// the preset's freq: daily uses only Time; weekly adds Weekday (0=Sun..6=Sat, Go's convention);
// monthly adds Day (1..31, clamped to the month's length); yearly adds Month (1..12) + Day.
// Time is "HH:mm", interpreted in the panel timezone.
type presetAnchor struct {
	Weekday int    `json:"weekday,omitempty"`
	Month   int    `json:"month,omitempty"`
	Day     int    `json:"day,omitempty"`
	Time    string `json:"time"`
}

// parseHHMM parses a "HH:mm" 24-hour clock string. ok=false on anything malformed.
func parseHHMM(s string) (hour, min int, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// atClamped builds the instant year-month-day hh:mm in loc, clamping day to the month's actual
// length so day 31 in a short month (or 2/29 in a non-leap year) lands on the last valid day
// instead of spilling into the next month. (time.Date of "day 0 of next month" = last day here.)
func atClamped(year int, month time.Month, day, hh, mm int, loc *time.Location) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > last {
		day = last
	}
	if day < 1 {
		day = 1
	}
	return time.Date(year, month, day, hh, mm, 0, 0, loc)
}

// nextWindow returns the current-or-next occurrence [start, end] of a preset window relative to
// now, interpreting the anchors in loc (the panel timezone). If now is inside an occurrence, that
// occurrence is returned (start <= now < end). It handles a window whose stop precedes its start
// within the period (wraps to the next day/week/month/year) and clamps invalid month days.
// ok=false for a malformed spec. Non-overlapping occurrences mean the first end after now is the
// current-or-next one; the loop starts one period back to catch a still-open wrapped window.
func nextWindow(freq string, start, stop presetAnchor, now time.Time, loc *time.Location) (time.Time, time.Time, bool) {
	sh, sm, ok1 := parseHHMM(start.Time)
	eh, em, ok2 := parseHHMM(stop.Time)
	if !ok1 || !ok2 {
		return time.Time{}, time.Time{}, false
	}
	n := now.In(loc)

	// windowAt returns the [start, end] instants of the k-th occurrence relative to a base near
	// now; a stop at/behind the start within its period rolls the end to the next period.
	var windowAt func(k int) (time.Time, time.Time)
	switch freq {
	case "daily":
		base := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
		windowAt = func(k int) (time.Time, time.Time) {
			d := base.AddDate(0, 0, k)
			s := time.Date(d.Year(), d.Month(), d.Day(), sh, sm, 0, 0, loc)
			e := time.Date(d.Year(), d.Month(), d.Day(), eh, em, 0, 0, loc)
			if !e.After(s) {
				e = e.AddDate(0, 0, 1)
			}
			return s, e
		}
	case "weekly":
		if start.Weekday < 0 || start.Weekday > 6 || stop.Weekday < 0 || stop.Weekday > 6 {
			return time.Time{}, time.Time{}, false
		}
		day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
		back := (int(day.Weekday()) - start.Weekday + 7) % 7 // days since the most recent start weekday
		base := day.AddDate(0, 0, -back)
		span := (stop.Weekday - start.Weekday + 7) % 7 // start weekday → stop weekday, forward
		windowAt = func(k int) (time.Time, time.Time) {
			d := base.AddDate(0, 0, 7*k)
			s := time.Date(d.Year(), d.Month(), d.Day(), sh, sm, 0, 0, loc)
			ed := d.AddDate(0, 0, span)
			e := time.Date(ed.Year(), ed.Month(), ed.Day(), eh, em, 0, 0, loc)
			if !e.After(s) {
				e = e.AddDate(0, 0, 7)
			}
			return s, e
		}
	case "monthly":
		if start.Day < 1 || start.Day > 31 || stop.Day < 1 || stop.Day > 31 {
			return time.Time{}, time.Time{}, false
		}
		base := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
		windowAt = func(k int) (time.Time, time.Time) {
			m := base.AddDate(0, k, 0)
			s := atClamped(m.Year(), m.Month(), start.Day, sh, sm, loc)
			e := atClamped(m.Year(), m.Month(), stop.Day, eh, em, loc)
			if !e.After(s) {
				nm := m.AddDate(0, 1, 0)
				e = atClamped(nm.Year(), nm.Month(), stop.Day, eh, em, loc)
			}
			return s, e
		}
	case "yearly":
		if start.Month < 1 || start.Month > 12 || stop.Month < 1 || stop.Month > 12 ||
			start.Day < 1 || start.Day > 31 || stop.Day < 1 || stop.Day > 31 {
			return time.Time{}, time.Time{}, false
		}
		base := n.Year()
		windowAt = func(k int) (time.Time, time.Time) {
			y := base + k
			s := atClamped(y, time.Month(start.Month), start.Day, sh, sm, loc)
			e := atClamped(y, time.Month(stop.Month), stop.Day, eh, em, loc)
			if !e.After(s) {
				e = atClamped(y+1, time.Month(stop.Month), stop.Day, eh, em, loc)
			}
			return s, e
		}
	default:
		return time.Time{}, time.Time{}, false
	}

	for k := -1; k < 800; k++ {
		s, e := windowAt(k)
		if e.After(now) {
			return s, e, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// runPresetSnapshot is what batch_jobs.run_preset stores: the recurrence rule + the overrun
// policy + the current occurrence's end (until). It rides on the job (a snapshot taken at
// submit) rather than a reference to the mutable run_presets row, so a later edit/delete of the
// preset never rewrites an in-flight run's window (mirrors the report.name snapshot rationale).
// until is the local wall-clock "2006-01-02 15:04:05" of the occurrence end — the same basis
// runAtDue parses run_at in, so the scheduler compares it with the existing helpers.
type runPresetSnapshot struct {
	Freq      string       `json:"freq"`
	Start     presetAnchor `json:"start"`
	Stop      presetAnchor `json:"stop"`
	OnOverrun string       `json:"on_overrun"`
	Until     string       `json:"until"`
}

// fmtLocal renders an absolute instant in the local wall-clock basis run_at/until are stored in.
// Round-trips through runAtDue's time.ParseInLocation(..., time.Local): format-then-parse in
// Local recovers the same instant, so a panel-tz-computed schedule stays correct regardless of
// the server's timezone.
func fmtLocal(t time.Time) string { return t.In(time.Local).Format("2006-01-02 15:04:05") }

// resolvePresetWindow computes a job's run_at (the window start) and its run_preset snapshot for
// a preset window relative to now, interpreting anchors in loc (the panel timezone). The same
// call rolls a window forward: past the old occurrence's end, nextWindow returns the next
// occurrence. ok=false for a malformed window (the caller then skips preset scheduling).
func resolvePresetWindow(freq string, start, stop presetAnchor, onOverrun string, now time.Time, loc *time.Location) (runAt, snapshot string, ok bool) {
	s, e, ok := nextWindow(freq, start, stop, now, loc)
	if !ok {
		return "", "", false
	}
	b, _ := json.Marshal(runPresetSnapshot{Freq: freq, Start: start, Stop: stop, OnOverrun: onOverrun, Until: fmtLocal(e)})
	return fmtLocal(s), string(b), true
}

// ---------- run_presets persistence ----------
//
// The run_presets table mirrors the links / type_config admin-list CRUD; a job never references
// a preset row (it snapshots the rule into batch_jobs.run_preset), so there is no foreign key.

// RunPreset is one configured preset window. start_spec / stop_spec are JSON presetAnchor
// objects ({weekday?,month?,day?,time:"HH:mm"}); which fields apply depends on Freq
// (daily|weekly|monthly|yearly). OnOverrun is continue|next|cancel.
type RunPreset struct {
	ID        int64
	Label     string
	Freq      string
	StartSpec string
	StopSpec  string
	OnOverrun string
	Enabled   bool
	Ord       int
}

const runPresetCols = `id, COALESCE(label,''), COALESCE(freq,''), COALESCE(start_spec,''),
	COALESCE(stop_spec,''), COALESCE(on_overrun,'next'), COALESCE(enabled,1), COALESCE(ord,0)`

func scanRunPreset(sc interface{ Scan(...any) error }) (RunPreset, bool) {
	var p RunPreset
	var enabled int
	if err := sc.Scan(&p.ID, &p.Label, &p.Freq, &p.StartSpec, &p.StopSpec, &p.OnOverrun, &enabled, &p.Ord); err != nil {
		return RunPreset{}, false
	}
	p.Enabled = enabled != 0
	return p, true
}

// ListRunPresets returns every preset in display order (ord, then id).
func (s *Store) ListRunPresets() []RunPreset {
	rows, err := s.query(`SELECT ` + runPresetCols + ` FROM run_presets ORDER BY ord, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RunPreset
	for rows.Next() {
		if p, ok := scanRunPreset(rows); ok {
			out = append(out, p)
		}
	}
	return out
}

// GetRunPreset fetches one preset by id (ok=false if absent) — used to resolve a submit's
// preset_id into a concrete window to snapshot onto the job.
func (s *Store) GetRunPreset(id int64) (RunPreset, bool) {
	return scanRunPreset(s.queryRow(`SELECT `+runPresetCols+` FROM run_presets WHERE id=?`, id))
}

// CreateRunPreset inserts a preset, returning its new id. New presets sort after existing ones
// (ord defaults to 0; ties break by id, so a fresh row lands last).
func (s *Store) CreateRunPreset(p RunPreset) (int64, error) {
	return s.insertID(`INSERT INTO run_presets(label,freq,start_spec,stop_spec,on_overrun,enabled,ord)
		VALUES(?,?,?,?,?,?,?)`, p.Label, p.Freq, p.StartSpec, p.StopSpec, p.OnOverrun, boolInt(p.Enabled), p.Ord)
}

// UpdateRunPreset saves an edited preset (ord is managed by ReorderRunPresets, not here).
func (s *Store) UpdateRunPreset(p RunPreset) error {
	_, err := s.exec(`UPDATE run_presets SET label=?, freq=?, start_spec=?, stop_spec=?, on_overrun=?, enabled=? WHERE id=?`,
		p.Label, p.Freq, p.StartSpec, p.StopSpec, p.OnOverrun, boolInt(p.Enabled), p.ID)
	return err
}

// DeleteRunPreset removes a preset. In-flight jobs are unaffected — they snapshot the rule.
func (s *Store) DeleteRunPreset(id int64) error {
	_, err := s.exec(`DELETE FROM run_presets WHERE id=?`, id)
	return err
}

// ReorderRunPresets persists a new display order (drag-to-sort): each id's ord becomes its
// index in the slice, matching the links / type_config reorder convention.
func (s *Store) ReorderRunPresets(ids []int64) error {
	for i, id := range ids {
		if _, err := s.exec(`UPDATE run_presets SET ord=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return nil
}

// ---------- HTTP surface ----------
//
// The list is readable by any run_batch user (the run form's preset dropdown needs it); create /
// update / delete / reorder are admin-only (routes in server.go).

// runPresetJSON renders a stored preset for the wire, parsing the JSON anchors into objects so
// the client gets structured start/stop instead of embedded JSON strings.
func runPresetJSON(p RunPreset) map[string]any {
	var start, stop presetAnchor
	json.Unmarshal([]byte(p.StartSpec), &start)
	json.Unmarshal([]byte(p.StopSpec), &stop)
	return map[string]any{
		"id": p.ID, "label": p.Label, "freq": p.Freq,
		"start": start, "stop": stop, "on_overrun": p.OnOverrun,
		"enabled": p.Enabled, "ord": p.Ord,
	}
}

// apiRunPresets lists every configured preset (the run form filters to enabled ones; the admin
// editor shows all) plus the run-form defaults, so the run modal needs a single fetch.
func (s *Server) apiRunPresets(w http.ResponseWriter, r *http.Request, user string) {
	list := s.st.ListRunPresets()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, runPresetJSON(p))
	}
	writeJSON(w, map[string]any{
		"presets":      out,
		"default_mode": s.st.GetSetting("run_default_mode", "now"),
		"default_idle": s.st.GetSetting("run_default_idle", "0") == "1",
	})
}

// presetInput is the create/update body; start/stop are structured anchors.
type presetInput struct {
	Label     string       `json:"label"`
	Freq      string       `json:"freq"`
	Start     presetAnchor `json:"start"`
	Stop      presetAnchor `json:"stop"`
	OnOverrun string       `json:"on_overrun"`
	Enabled   bool         `json:"enabled"`
}

// normalizePreset validates freq + anchors (by resolving a window) and clamps on_overrun to a
// known policy (defaulting to 'next'), returning the RunPreset to store. ok=false → 400.
func normalizePreset(in presetInput) (RunPreset, bool) {
	onOverrun := in.OnOverrun
	switch onOverrun {
	case "continue", "next", "cancel":
	default:
		onOverrun = "next"
	}
	// A window that can't resolve (bad freq / time / anchor) is rejected up front.
	if _, _, ok := nextWindow(in.Freq, in.Start, in.Stop, time.Now(), time.UTC); !ok {
		return RunPreset{}, false
	}
	sb, _ := json.Marshal(in.Start)
	tb, _ := json.Marshal(in.Stop)
	return RunPreset{Label: in.Label, Freq: in.Freq, StartSpec: string(sb), StopSpec: string(tb),
		OnOverrun: onOverrun, Enabled: in.Enabled}, true
}

func (s *Server) apiRunPresetCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in presetInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	p, ok := normalizePreset(in)
	if !ok {
		jsonError(w, http.StatusBadRequest, "invalid preset window")
		return
	}
	id, err := s.st.CreateRunPreset(p)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiRunPresetUpdate(w http.ResponseWriter, r *http.Request, user string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	var in presetInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	p, ok := normalizePreset(in)
	if !ok {
		jsonError(w, http.StatusBadRequest, "invalid preset window")
		return
	}
	p.ID = id
	if err := s.st.UpdateRunPreset(p); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiRunPresetDelete(w http.ResponseWriter, r *http.Request, user string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.st.DeleteRunPreset(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiRunPresetReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.st.ReorderRunPresets(in.IDs); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}
