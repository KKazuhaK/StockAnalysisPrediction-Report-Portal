package app

import "strconv"

// Run-form defaults (docs/adr/0014-idle-lane-and-preset-windows.md §4): the state the 运行分析
// dialog and the 批量执行 console open in before the user touches anything — which workflow is
// pre-selected, which button the mode toggle rests on, which preset window is pre-picked, whether
// 队列空闲 starts checked, how many failure retries the run carries, and whether the done-email is
// pre-ticked. Every one of them is a *suggestion*: the user can change any of it in the form, and
// none of them is required — "no default" is a first-class choice (id 0 / off), which is what an
// unconfigured portal has always done.
//
// They are scalars, so they stay in `meta` (no table) alongside the rest of the queue config, and
// they ride on two endpoints: `/api/admin/batch/config` (admin read/write, `run_default_` prefix)
// and `/api/admin/batch/presets` (what the run forms read, `default_` prefix) — one file so the
// two spellings and the validation rules cannot drift apart.

// runDefaultRetriesMax is the ceiling for the pre-filled failure-retry count, matching the run
// form's own InputNumber max — an admin cannot pre-set a value the user could not have typed.
const runDefaultRetriesMax = 5

// runDefaultMode is which button the run forms open on. Anything unrecognised (including a value
// written by an older/newer build) reads as immediate.
func (s *Server) runDefaultMode() string {
	switch m := s.st.GetSetting("run_default_mode", "now"); m {
	case "now", "preset", "scheduled":
		return m
	default:
		return "now"
	}
}

// runDefaultIdle is whether 队列空闲 starts checked (immediate mode only).
func (s *Server) runDefaultIdle() bool {
	return s.st.GetSetting("run_default_idle", "0") == "1"
}

// runDefaultTargetID is the workflow the run dialog pre-selects; 0 = none, which is the shipped
// default (the picker opens empty). A stored id whose target has since been deleted reads as 0:
// the forms would ignore it anyway, and reporting it back would put a bare number in the admin's
// own picker.
func (s *Server) runDefaultTargetID() int64 {
	id, err := strconv.ParseInt(s.st.GetSetting("run_default_target_id", "0"), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	if _, ok := s.st.GetTarget(id); !ok {
		return 0
	}
	return id
}

// runDefaultPresetID is the preset window pre-picked in 预设时间 mode; 0 = none. As with the
// workflow, a deleted preset reads as 0. A *disabled* preset is still reported: the forms drop it
// (they only offer enabled windows), but the admin who turned that window off should see their
// choice waiting rather than have it silently cleared on the next Save.
func (s *Server) runDefaultPresetID() int64 {
	id, err := strconv.ParseInt(s.st.GetSetting("run_default_preset_id", "0"), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	if _, ok := s.st.GetRunPreset(id); !ok {
		return 0
	}
	return id
}

// runDefaultRetries is the failure-retry count the run form pre-fills. Default 0 — a single run
// maps 1:1 to the click unless an admin says otherwise.
func (s *Server) runDefaultRetries() int {
	n, err := strconv.Atoi(s.st.GetSetting("run_default_retries", "0"))
	if err != nil || n < 0 {
		return 0
	}
	if n > runDefaultRetriesMax {
		return runDefaultRetriesMax
	}
	return n
}

// runDefaultNotify is whether "email me when done" starts ticked. It only shows for a user with
// a verified address on a portal with mail configured; elsewhere the checkbox isn't rendered and
// this has no effect.
func (s *Server) runDefaultNotify() bool {
	return s.st.GetSetting("run_default_notify", "0") == "1"
}

// runFormDefaultsJSON renders the whole block for the wire under the given key prefix: the presets
// endpoint the run forms read uses "default_", the admin config endpoint "run_default_".
func (s *Server) runFormDefaultsJSON(prefix string) map[string]any {
	return map[string]any{
		prefix + "mode":      s.runDefaultMode(),
		prefix + "idle":      s.runDefaultIdle(),
		prefix + "target_id": s.runDefaultTargetID(),
		prefix + "preset_id": s.runDefaultPresetID(),
		prefix + "retries":   s.runDefaultRetries(),
		prefix + "notify":    s.runDefaultNotify(),
	}
}

// ---------- writes ----------
//
// Each setter takes the value a save request carried; the caller only calls it when the field was
// present, so an omitted field is left untouched. A value that names nothing real (an unknown mode,
// a workflow or window that does not exist) is *ignored* rather than stored — the same shape as the
// rest of the batch config save, and it keeps a dangling default out of the settings table. 0
// always means "no default" and is always accepted: that is how an admin clears one.

func (s *Server) setRunDefaultMode(mode string) {
	switch mode {
	case "now", "preset", "scheduled":
		s.st.SetSetting("run_default_mode", mode)
	}
}

func (s *Server) setRunDefaultIdle(on bool) {
	s.st.SetSetting("run_default_idle", strconv.Itoa(boolInt(on)))
}

func (s *Server) setRunDefaultTarget(id int64) {
	if id <= 0 {
		s.st.SetSetting("run_default_target_id", "0")
		return
	}
	if _, ok := s.st.GetTarget(id); ok {
		s.st.SetSetting("run_default_target_id", strconv.FormatInt(id, 10))
	}
}

func (s *Server) setRunDefaultPreset(id int64) {
	if id <= 0 {
		s.st.SetSetting("run_default_preset_id", "0")
		return
	}
	if _, ok := s.st.GetRunPreset(id); ok {
		s.st.SetSetting("run_default_preset_id", strconv.FormatInt(id, 10))
	}
}

func (s *Server) setRunDefaultRetries(n int) {
	if n < 0 || n > runDefaultRetriesMax {
		return
	}
	s.st.SetSetting("run_default_retries", strconv.Itoa(n))
}

func (s *Server) setRunDefaultNotify(on bool) {
	s.st.SetSetting("run_default_notify", strconv.Itoa(boolInt(on)))
}

// ---------- what the run form shows ----------

// runShowPresetRule is whether the run forms print a preset window's whole rule next to its name
// ("Off-peak - daily, except 09:00-12:00 and 14:00-18:00") or just the name, leaving the rule to
// the info button beside the picker. Default off: the rule is long enough to overrun the control
// it sits in, every option in the drop-down already carries it, and the closed picker only has to
// say which window is chosen.
//
// This is not one of the defaults above — the user cannot override it in the form, it decides what
// the form *shows* — so it keeps its own key and its own name on each endpoint
// (`run_show_preset_rule` for the admin config, `show_preset_rule` for the run forms) rather than
// joining the prefixed block. Both handlers read this one getter, so the two spellings agree.
func (s *Server) runShowPresetRule() bool {
	return s.st.GetSetting("run_show_preset_rule", "0") == "1"
}

func (s *Server) setRunShowPresetRule(on bool) {
	s.st.SetSetting("run_show_preset_rule", strconv.Itoa(boolInt(on)))
}
