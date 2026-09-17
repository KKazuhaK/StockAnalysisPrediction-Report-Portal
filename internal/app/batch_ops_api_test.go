package app

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// seedQueuedJob creates a bare job (no plugin needed) with n queued rows.
func seedQueuedJob(t *testing.T, s *Server, owner string, n int) int64 {
	t.Helper()
	rows := make([]map[string]string, n)
	for i := range rows {
		rows[i] = map[string]string{"symbol": fmt.Sprint(300000 + i)}
	}
	id, err := s.st.CreateBatchJob(1, 1, 0, owner, rows, "50")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// finishJob runs every row to the given outcome and closes the job out, the way the engine would.
func finishJob(t *testing.T, s *Server, id int64, outcome batch.Outcome) {
	t.Helper()
	for _, it := range s.st.BatchJobItems(id) {
		if !s.st.MarkItemRunning(it.ID) {
			t.Fatalf("mark item %d running", it.ID)
		}
		if err := s.st.FinishItem(it.ID, outcome, 1, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	s.finalizeJob(id)
}

// Plugins: the list compiles the stored manifest (so the run forms see its declared inputs), and
// delete removes by slug.
func TestBatchPluginListAndDelete(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertPlugin("p", "P", "1.0.0", batchTestSpec, "imported"); err != nil {
		t.Fatal(err)
	}

	code, out := call(t, s.apiBatchPlugins, ``, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiBatchPlugins → %d", code)
	}
	plugins, ok := out["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("plugins = %#v", out["plugins"])
	}
	p := plugins[0].(map[string]any)
	if p["slug"] != "p" || p["name"] != "P" || p["version"] != "1.0.0" {
		t.Fatalf("plugin row = %v", p)
	}
	if inputs, ok := p["inputs"].([]any); !ok || len(inputs) != 1 {
		t.Fatalf("compiled inputs = %v, want the manifest's one input", p["inputs"])
	}

	if code, _ = callPath(t, s.apiBatchPluginDelete, http.MethodPost, ``, map[string]string{"slug": "p"}, "admin"); code != http.StatusOK {
		t.Fatalf("apiBatchPluginDelete → %d", code)
	}
	if got := s.st.ListPlugins(); len(got) != 0 {
		t.Fatalf("plugins after delete = %+v", got)
	}
}

// Targets: drag-order persists, delete removes + audits, and the surfaces endpoint refuses an
// empty selection (which would mean "every surface", the opposite of what the admin meant).
func TestBatchTargetAdminHandlers(t *testing.T) {
	s := batchServer(t)
	first := seedDifyTarget(t, s, "First")
	second := seedDifyTarget(t, s, "Second")

	if code, _ := call(t, s.apiBatchTargetReorder, fmt.Sprintf(`{"ids":[%d,%d]}`, second, first), "admin"); code != http.StatusOK {
		t.Fatalf("apiBatchTargetReorder → %d", code)
	}
	ts := s.st.ListTargets()
	if len(ts) != 2 || ts[0].ID != second || ts[1].ID != first {
		t.Fatalf("target order = %+v, want second first", ts)
	}

	code, _ := callPath(t, s.apiBatchTargetSurfaces, http.MethodPost, `{"surfaces":["run","chat"]}`,
		map[string]string{"id": fmt.Sprint(first)}, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiBatchTargetSurfaces → %d", code)
	}
	got, _ := s.st.GetTarget(first)
	surfaces := TargetSurfaces(got.Surfaces)
	if len(surfaces) != 2 || !AllowsSurface(got.Surfaces, SurfaceRun) || !AllowsSurface(got.Surfaces, SurfaceChat) || AllowsSurface(got.Surfaces, SurfaceBatch) {
		t.Fatalf("surfaces = %q (%v)", got.Surfaces, surfaces)
	}
	// Empty selection is rejected rather than silently meaning "all", and so is a list whose
	// every entry is an unknown surface name.
	if code, _ = callPath(t, s.apiBatchTargetSurfaces, http.MethodPost, `{"surfaces":[]}`,
		map[string]string{"id": fmt.Sprint(first)}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("empty surfaces → %d, want 400", code)
	}
	if code, _ = callPath(t, s.apiBatchTargetSurfaces, http.MethodPost, `{"surfaces":["bogus"]}`,
		map[string]string{"id": fmt.Sprint(first)}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("all-unknown surfaces → %d, want 400", code)
	}
	if code, _ = callPath(t, s.apiBatchTargetSurfaces, http.MethodPost, `{"surfaces":["run"]}`,
		map[string]string{"id": "424242"}, "admin"); code != http.StatusNotFound {
		t.Fatalf("unknown target → %d, want 404", code)
	}
	if code, _ = callPath(t, s.apiBatchTargetSurfaces, http.MethodPost, `{"surfaces":["run"]}`,
		map[string]string{"id": "abc"}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("bad id → %d, want 400", code)
	}

	if code, _ = callPath(t, s.apiBatchTargetDelete, http.MethodPost, ``,
		map[string]string{"id": fmt.Sprint(second)}, "admin"); code != http.StatusOK {
		t.Fatalf("apiBatchTargetDelete → %d", code)
	}
	if _, ok := s.st.GetTarget(second); ok {
		t.Fatal("target survived delete")
	}
	if _, total := s.st.ListAudit(AuditFilter{Action: AuditTargetChange}); total != 1 {
		t.Fatalf("target delete audit rows = %d, want 1", total)
	}
}

// Tickets + run quota: the run form reads its remaining balance from these two endpoints.
func TestBatchTicketsAndRunQuotaEndpoints(t *testing.T) {
	s := userAdminServer(t)
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}

	// Default state: no unlimited override, 加急 lane off (the compiled-in default), nothing left.
	code, out := call(t, s.apiBatchTickets, ``, "alice")
	if code != http.StatusOK {
		t.Fatalf("apiBatchTickets → %d", code)
	}
	if out["unlimited"] != false || out["urgent_enabled"] != false {
		t.Fatalf("default tickets = %v", out)
	}
	s.st.SetSetting("batch_urgent_enabled", "1")
	_, out = call(t, s.apiBatchTickets, ``, "alice")
	if out["urgent_enabled"] != true {
		t.Fatalf("urgent_enabled = %v, want true after the admin toggle", out["urgent_enabled"])
	}

	// A group with the unlimited override flips the flag and skips the balance.
	_, g := call(t, s.apiGroupAdd, `{"name":"Urgent","urgent_unlimited":true}`, "admin")
	s.st.SetPrimaryGroup("alice", int64(g["id"].(float64)))
	_, out = call(t, s.apiBatchTickets, ``, "alice")
	if out["unlimited"] != true {
		t.Fatalf("unlimited tickets = %v", out)
	}

	// Run quota: admins are unlimited, a plain user inside a restricted quota-carrying OU is not
	// (the quota only applies to restricted tenants, ADR 0022 R2).
	if _, out = call(t, s.apiRunQuota, ``, "admin"); out["limited"] != false {
		t.Fatalf("admin run quota = %v, want unlimited", out)
	}
	if err := s.st.UpsertUser(User{Username: "bob", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	ou, err := s.st.CreateUserGroup("clients", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.st.SetGroupParent(ou, s.st.EnsureDefaultGroup())
	s.st.SetGroupRestricted(ou, true)
	s.st.SetPrimaryGroup("bob", ou)
	quota := 3
	if err := s.st.SetGroupDailyQuota(ou, &quota, QuotaDay); err != nil {
		t.Fatal(err)
	}
	_, out = call(t, s.apiRunQuota, ``, "bob")
	if out["limited"] != true || out["limit"] != float64(3) || out["remaining"] != float64(3) {
		t.Fatalf("limited run quota = %v", out)
	}
}

// Job operations: detail, per-row cancel, whole-job cancel, retry, reprioritize, clear-finished
// and delete — each with its authorization / state guard.
func TestBatchJobOperationHandlers(t *testing.T) {
	s := batchServer(t)
	ownerJob := seedQueuedJob(t, s, "alice", 2)

	// Detail carries the job, live counts and its rows.
	code, out := callPath(t, s.apiBatchJobDetail, http.MethodGet, ``, map[string]string{"id": fmt.Sprint(ownerJob)}, "alice")
	if code != http.StatusOK {
		t.Fatalf("apiBatchJobDetail → %d", code)
	}
	detail := out["job"].(map[string]any)
	counts := out["counts"].(map[string]any)
	items := out["items"].([]any)
	if int64(detail["id"].(float64)) != ownerJob || counts["queued"] != float64(2) || len(items) != 2 {
		t.Fatalf("job detail = %v", out)
	}
	if code, _ = callPath(t, s.apiBatchJobDetail, http.MethodGet, ``, map[string]string{"id": "424242"}, "alice"); code != http.StatusNotFound {
		t.Fatalf("unknown job detail → %d, want 404", code)
	}

	// A non-owner cannot cancel rows or the job; the owner can.
	if code, _ = callPath(t, s.apiBatchItemsCancel, http.MethodPost, `{"item_ids":[1]}`, map[string]string{"id": fmt.Sprint(ownerJob)}, "mallory"); code != http.StatusForbidden {
		t.Fatalf("foreign row cancel → %d, want 403", code)
	}
	rowID := int64(items[0].(map[string]any)["id"].(float64))
	code, out = callPath(t, s.apiBatchItemsCancel, http.MethodPost, fmt.Sprintf(`{"item_ids":[%d,999999]}`, rowID),
		map[string]string{"id": fmt.Sprint(ownerJob)}, "alice")
	if code != http.StatusOK || out["cancelled"] != float64(1) {
		t.Fatalf("row cancel = %d %v, want one cancellation (unknown id ignored)", code, out)
	}
	if _, status, _ := s.st.ItemJobAndStatus(rowID); status != "cancelled" {
		t.Fatalf("row status = %q, want cancelled", status)
	}

	if code, _ = callPath(t, s.apiBatchJobCancel, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(ownerJob)}, "mallory"); code != http.StatusForbidden {
		t.Fatalf("foreign job cancel → %d, want 403", code)
	}
	if code, _ = callPath(t, s.apiBatchJobCancel, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(ownerJob)}, "alice"); code != http.StatusOK {
		t.Fatalf("owner job cancel → %d", code)
	}
	if j, _ := s.st.GetBatchJob(ownerJob); j.Status != "cancelled" {
		t.Fatalf("job status after cancel = %q", j.Status)
	}
	if code, _ = callPath(t, s.apiBatchJobCancel, http.MethodPost, ``, map[string]string{"id": "424242"}, "admin"); code != http.StatusNotFound {
		t.Fatalf("unknown job cancel → %d, want 404", code)
	}

	// Retry requeues failed rows and re-enters the queue. Scheduled far ahead so the scheduler
	// does not race the assertions by admitting it immediately.
	retryJob := seedQueuedJob(t, s, "admin", 1)
	if err := s.st.ScheduleJob(retryJob, "2099-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	finishJob(t, s, retryJob, batch.Failed)
	code, out = callPath(t, s.apiBatchJobRetry, http.MethodPost, `{"statuses":["failed"]}`,
		map[string]string{"id": fmt.Sprint(retryJob)}, "admin")
	if code != http.StatusOK || out["requeued"] != float64(1) {
		t.Fatalf("job retry = %d %v", code, out)
	}
	if j, _ := s.st.GetBatchJob(retryJob); j.Status != "queued" {
		t.Fatalf("job status after retry = %q, want queued", j.Status)
	}

	// Reprioritize validates the value and stores it.
	if code, _ = callPath(t, s.apiBatchJobReprioritize, http.MethodPost, `{"priority":"nope"}`,
		map[string]string{"id": fmt.Sprint(retryJob)}, "admin"); code != http.StatusBadRequest {
		t.Fatalf("bad priority → %d, want 400", code)
	}
	code, out = callPath(t, s.apiBatchJobReprioritize, http.MethodPost, `{"priority":"80"}`,
		map[string]string{"id": fmt.Sprint(retryJob)}, "admin")
	if code != http.StatusOK || out["priority"] != "80" {
		t.Fatalf("reprioritize = %d %v", code, out)
	}
	if j, _ := s.st.GetBatchJob(retryJob); j.Priority != "80" {
		t.Fatalf("stored priority = %q, want 80", j.Priority)
	}

	// Delete refuses an active job; clear-finished leaves it alone and clears the terminal one.
	if code, _ = callPath(t, s.apiBatchJobDelete, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(retryJob)}, "admin"); code != http.StatusConflict {
		t.Fatalf("delete active job → %d, want 409", code)
	}
	doneJob := seedQueuedJob(t, s, "admin", 1)
	finishJob(t, s, doneJob, batch.Ok)
	code, out = call(t, s.apiBatchClearFinished, `{}`, "admin")
	if code != http.StatusOK || out["n"] != float64(2) { // the cancelled ownerJob + the finished doneJob
		t.Fatalf("clear finished = %d %v, want both terminal jobs cleared", code, out)
	}
	if _, ok := s.st.GetBatchJob(doneJob); ok {
		t.Fatal("finished job survived clear")
	}
	if _, ok := s.st.GetBatchJob(retryJob); !ok {
		t.Fatal("queued job was swept by clear-finished")
	}
	// A terminal job can be deleted one by one; an unknown id 404s.
	finishJob2 := seedQueuedJob(t, s, "admin", 1)
	finishJob(t, s, finishJob2, batch.Ok)
	if code, _ = callPath(t, s.apiBatchJobDelete, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(finishJob2)}, "admin"); code != http.StatusOK {
		t.Fatalf("delete finished job → %d", code)
	}
	if _, ok := s.st.GetBatchJob(finishJob2); ok {
		t.Fatal("finished job survived delete")
	}
	if _, total := s.st.ListAudit(AuditFilter{Action: AuditRunDelete}); total != 2 {
		t.Fatalf("delete audit rows = %d, want 2 (clear + one)", total)
	}
}
