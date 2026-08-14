package app

import (
	"fmt"
	"testing"
)

// The run-form defaults are the only settings in the batch config that name another row (a workflow,
// a preset window). That reference is what these tests are about: it has to survive a round trip,
// be clearable, be refused when it names nothing, and stop being reported once the thing it names
// is gone — a default the run form would ignore must not sit in the admin's picker looking live.

// seedRunPreset creates one enabled daily window and returns its id.
func seedRunPreset(t *testing.T, srv *Server) int64 {
	t.Helper()
	pr := post(t, srv.apiRunPresetCreate,
		`{"label":"低峰期","freq":"daily","intervals":[{"start":{"time":"09:00"},"stop":{"time":"12:00"}}],"on_overrun":"next","enabled":true}`)
	return int64(pr["id"].(float64))
}

func TestRunDefaultsRoundTrip(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	presetID := seedRunPreset(t, srv)

	post(t, srv.apiBatchConfigSave, fmt.Sprintf(
		`{"run_default_target_id":%d,"run_default_preset_id":%d,"run_default_mode":"preset","run_default_idle":true,"run_default_retries":3,"run_default_notify":true}`,
		targetID, presetID))

	cfg := post(t, srv.apiBatchConfigGet, "{}")
	if got := int64(cfg["run_default_target_id"].(float64)); got != targetID {
		t.Errorf("default target = %d, want %d", got, targetID)
	}
	if got := int64(cfg["run_default_preset_id"].(float64)); got != presetID {
		t.Errorf("default preset = %d, want %d", got, presetID)
	}
	if got := cfg["run_default_mode"]; got != "preset" {
		t.Errorf("default mode = %v, want preset", got)
	}
	if cfg["run_default_idle"] != true || cfg["run_default_notify"] != true {
		t.Errorf("idle/notify did not round-trip: %v / %v", cfg["run_default_idle"], cfg["run_default_notify"])
	}
	if got := int(cfg["run_default_retries"].(float64)); got != 3 {
		t.Errorf("default retries = %d, want 3", got)
	}

	// The run forms read the same block off the presets endpoint, under the shorter prefix — one
	// source, two spellings, and they must agree or the dialog opens on something the admin never set.
	ps := post(t, srv.apiRunPresets, "{}")
	if got := int64(ps["default_target_id"].(float64)); got != targetID {
		t.Errorf("presets endpoint default target = %d, want %d", got, targetID)
	}
	if got := int64(ps["default_preset_id"].(float64)); got != presetID {
		t.Errorf("presets endpoint default preset = %d, want %d", got, presetID)
	}
	if got := int(ps["default_retries"].(float64)); got != 3 {
		t.Errorf("presets endpoint default retries = %d, want 3", got)
	}
}

// "No default" is a choice, not an unset field: 0 has to clear a stored default rather than read
// as "leave it alone" (which is what an omitted field means).
func TestRunDefaultsZeroClears(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d}`, targetID))

	post(t, srv.apiBatchConfigSave, `{"run_default_target_id":0}`)
	if got := srv.runDefaultTargetID(); got != 0 {
		t.Fatalf("default target after clearing = %d, want 0", got)
	}
	// An omitted field is still untouched — the save stays partial.
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d}`, targetID))
	post(t, srv.apiBatchConfigSave, `{"run_default_retries":2}`)
	if got := srv.runDefaultTargetID(); got != targetID {
		t.Fatalf("default target was overwritten by an unrelated save: %d", got)
	}
}

// A default naming a workflow or window that does not exist is never stored: the forms would
// ignore it, and storing it would make the admin's Save look like it took.
func TestRunDefaultsRejectUnknownRows(t *testing.T) {
	srv := batchServer(t)
	post(t, srv.apiBatchConfigSave, `{"run_default_target_id":404,"run_default_preset_id":404,"run_default_mode":"whenever","run_default_retries":99}`)

	if got := srv.runDefaultTargetID(); got != 0 {
		t.Errorf("unknown target was stored: %d", got)
	}
	if got := srv.runDefaultPresetID(); got != 0 {
		t.Errorf("unknown preset was stored: %d", got)
	}
	if got := srv.runDefaultMode(); got != "now" {
		t.Errorf("unknown mode was stored: %q", got)
	}
	if got := srv.runDefaultRetries(); got != 0 {
		t.Errorf("out-of-range retries were stored: %d", got)
	}
}

// Deleting the workflow or window a default points at leaves the setting dangling. It must read as
// "no default" from then on, which is what every form will actually do with it.
func TestRunDefaultsForgetDeletedRows(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	presetID := seedRunPreset(t, srv)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d,"run_default_preset_id":%d}`, targetID, presetID))

	if err := srv.st.DeleteTarget(targetID); err != nil {
		t.Fatal(err)
	}
	if err := srv.st.DeleteRunPreset(presetID); err != nil {
		t.Fatal(err)
	}
	if got := srv.runDefaultTargetID(); got != 0 {
		t.Errorf("deleted target still reported as the default: %d", got)
	}
	if got := srv.runDefaultPresetID(); got != 0 {
		t.Errorf("deleted preset still reported as the default: %d", got)
	}
}

// A window switched off is a different case from a deleted one: the run form won't offer it, but
// the admin who turned it off should find their choice waiting rather than silently cleared.
func TestRunDefaultsKeepDisabledPreset(t *testing.T) {
	srv := batchServer(t)
	presetID := seedRunPreset(t, srv)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_preset_id":%d}`, presetID))

	p, ok := srv.st.GetRunPreset(presetID)
	if !ok {
		t.Fatal("preset vanished")
	}
	p.Enabled = false
	if err := srv.st.UpdateRunPreset(p); err != nil {
		t.Fatal(err)
	}
	if got := srv.runDefaultPresetID(); got != presetID {
		t.Fatalf("disabled preset was dropped as the default: %d, want %d", got, presetID)
	}
}
