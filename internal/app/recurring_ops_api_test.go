package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Recurring-task management endpoints: detail, update, enable toggle and run-now, with the
// ownership guard and the no-op path (target gone) that must answer 400 rather than fire nothing.
func TestRecurringTaskOperationHandlers(t *testing.T) {
	dify := difyServer(t)
	defer dify.Close()
	s := userAdminServer(t)
	if err := s.st.UpsertUser(User{Username: "bob", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	post(t, s.apiBatchPluginImport, batchTestSpec)
	added := post(t, s.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, dify.URL))
	tgt := int64(added["id"].(float64))

	id := seedRecurring(t, s.st, tgt, "admin", RecurringTask{Name: "nightly", Freq: "daily", AtTime: "09:00", Enabled: true})
	path := map[string]string{"id": itoa(id)}

	// Detail returns the full template plus the target's name and empty history.
	code, out := callPath(t, s.apiRecurringDetail, http.MethodGet, ``, path, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiRecurringDetail → %d", code)
	}
	if out["name"] != "nightly" || out["target_name"] != "t" || out["row_count"] != float64(1) {
		t.Fatalf("detail = %v", out)
	}
	if rows, ok := out["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("detail rows = %v", out["rows"])
	}
	if hist, _ := out["history"].([]any); len(hist) != 0 {
		t.Fatalf("detail history = %v, want empty", out["history"])
	}

	// A non-owner cannot read, edit, toggle or fire it.
	if code, _ = callPath(t, s.apiRecurringDetail, http.MethodGet, ``, path, "bob"); code != http.StatusForbidden {
		t.Fatalf("foreign detail → %d, want 403", code)
	}
	if code, _ = callPath(t, s.apiRecurringEnable, http.MethodPost, `{"enabled":false}`, path, "bob"); code != http.StatusForbidden {
		t.Fatalf("foreign enable → %d, want 403", code)
	}
	if code, _ = callPath(t, s.apiRecurringRunNow, http.MethodPost, ``, path, "bob"); code != http.StatusForbidden {
		t.Fatalf("foreign run-now → %d, want 403", code)
	}
	// An unknown id 404s before the ownership check can matter.
	if code, _ = callPath(t, s.apiRecurringDetail, http.MethodGet, ``, map[string]string{"id": "424242"}, "admin"); code != http.StatusNotFound {
		t.Fatalf("unknown task → %d, want 404", code)
	}

	// Update rewrites every cadence field.
	code, _ = callPath(t, s.apiRecurringUpdate, http.MethodPost,
		`{"name":"weekly","target_id":`+itoa(tgt)+`,"rows":[{"code":"a"},{"code":"b"}],"concurrency":0,"max_retries":-2,"freq":"weekly","at_time":"07:30","weekday":3,"priority":"urgent","enabled":true}`, path, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiRecurringUpdate → %d", code)
	}
	task, _ := s.st.GetRecurringTask(id)
	if task.Name != "weekly" || task.Freq != "weekly" || task.Weekday != 3 || task.AtTime != "07:30" || task.Priority != "urgent" {
		t.Fatalf("updated task = %+v", task)
	}
	// Concurrency and retries are clamped to sane floors.
	if task.Concurrency != 1 || task.MaxRetries != 0 {
		t.Fatalf("clamped task = concurrency %d retries %d, want 1/0", task.Concurrency, task.MaxRetries)
	}
	// Bad input is refused without touching the stored task.
	if code, _ = callPath(t, s.apiRecurringUpdate, http.MethodPost,
		`{"name":"x","target_id":`+itoa(tgt)+`,"rows":[{"code":"a"}],"freq":"hourly","at_time":"07:30"}`, path, "admin"); code != http.StatusBadRequest {
		t.Fatalf("bad freq update → %d, want 400", code)
	}
	if task, _ = s.st.GetRecurringTask(id); task.Freq != "weekly" {
		t.Fatalf("refused update changed the task: %+v", task)
	}

	// Enable toggle persists.
	if code, _ = callPath(t, s.apiRecurringEnable, http.MethodPost, `{"enabled":false}`, path, "admin"); code != http.StatusOK {
		t.Fatalf("apiRecurringEnable → %d", code)
	}
	if task, _ = s.st.GetRecurringTask(id); task.Enabled {
		t.Fatal("task still enabled")
	}

	// Run-now fires out of cadence: a job is created and its recurring history records the run.
	code, out = callPath(t, s.apiRecurringRunNow, http.MethodPost, ``, path, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiRecurringRunNow → %d", code)
	}
	jobID := int64(out["job_id"].(float64))
	if jobID == 0 {
		t.Fatal("run-now did not create a job")
	}
	waitForJobDone(t, s.st, jobID)
	if runs := s.st.ListRecurringRuns(id, 0); len(runs) != 1 {
		t.Fatalf("recurring history = %d runs, want 1", len(runs))
	}
	// Disabled does not block a deliberate manual fire.
	if task, _ = s.st.GetRecurringTask(id); task.Enabled {
		t.Fatal("run-now re-enabled the task")
	}

	// A task whose target vanished is a no-op: 400, not 500 and not a job.
	tgt2 := seedDifyTarget(t, s, "Doomed")
	orphan := seedRecurring(t, s.st, tgt2, "admin", RecurringTask{Name: "orphan", Freq: "daily", AtTime: "09:00"})
	if err := s.st.DeleteTarget(tgt2); err != nil {
		t.Fatal(err)
	}
	if code, _ = callPath(t, s.apiRecurringRunNow, http.MethodPost, ``, map[string]string{"id": itoa(orphan)}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("run-now with missing target → %d, want 400", code)
	}
}
