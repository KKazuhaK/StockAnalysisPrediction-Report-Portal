package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Hand-written reports (ADR 0026).
//
// The property the whole design rests on is that a hand-written report and a workflow's output
// cannot overwrite each other, so most of what is asserted here is about the seam between them
// rather than about the editor's own CRUD.

func editorFixture(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	st.CreateToken("tok-all", "test", "all", "")
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	st.EnsureDefaultGroup()
	st.UpsertUser(User{Username: "editor", PasswordHash: "h", Role: "editor"})
	return s
}

// editorPost/Put/Delete call the handlers directly, as the announcement tests do: PathValue is
// populated by the mux, which is not in play here.
func editorCall(t *testing.T, s *Server, method, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/reports", strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		s.apiReportCreate(rec, req, "editor")
	case http.MethodPut:
		s.apiReportSave(rec, req, "editor")
	case http.MethodDelete:
		s.apiReportDelete(rec, req, "editor")
	}
	return rec
}

func editorForm(t *testing.T, s *Server, from string) map[string]any {
	t.Helper()
	url := "/api/reports/editor"
	if from != "" {
		url += "?from=" + from
	}
	rec := httptest.NewRecorder()
	s.apiReportEditor(rec, httptest.NewRequest("GET", url, nil), "editor")
	if rec.Code != http.StatusOK {
		t.Fatalf("editor form status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("editor form not JSON: %q", rec.Body.String())
	}
	return out
}

func mustCreateManual(t *testing.T, s *Server, body string) int64 {
	t.Helper()
	rec := editorCall(t, s, http.MethodPost, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct{ ID int64 }
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.ID
}

const manualForm = `{"symbol":"600519","date":"2026-09-02","subtype":"深度分析",
	"title":"手工补充","body_md":"# 手写\n正文","audience":"all"}`

// ---------- the seam with the workflow ----------

// The reason the manual version exists. An author writes a report; the workflow that was supposed
// to produce it catches up and pushes its own — and must not land on the author's row.
func TestWorkflowIngestCannotOverwriteAHandWrittenReport(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)

	// Same identity in every component the workflow controls: code, date, subtype, title.
	machineID, created, err := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "手工补充",
		MD: "机器生成的正文"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !created || machineID == id {
		t.Fatalf("the workflow landed on the hand-written row: machine=%d manual=%d created=%v",
			machineID, id, created)
	}
	rep, _ := s.st.GetNew(id, nil)
	if rep == nil || rep.MD != "# 手写\n正文" {
		t.Fatalf("hand-written body was changed: %+v", rep)
	}
}

// ...and it is a mechanism, not a convention: an ingest that NAMES the manual version is refused,
// rather than being allowed to write the one place a person's words are supposed to be safe.
func TestIngestRefusesTheManualVersion(t *testing.T) {
	s := editorFixture(t)
	for _, name := range []string{"manual", "  manual  "} {
		body := fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析",
			"title":"t","version":%q,"body_md":"x"}`, name)
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("version=%q: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	// Nothing was written on the way to the refusal.
	if n := s.st.CountReportsOfVersion("manual"); n != 0 {
		t.Fatalf("refused ingest still wrote %d manual report(s)", n)
	}
}

// Editing a machine report never modifies it: the editor form seeds a NEW hand-written report from
// its text, and the record of the run keeps saying what the run said.
func TestEditingAMachineReportLeavesItAlone(t *testing.T) {
	s := editorFixture(t)
	machineID, _, _ := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "工作流产出",
		MD: "机器正文"})

	form := editorForm(t, s, fmt.Sprint(machineID))
	if form["manual"] != false || form["body_md"] != "机器正文" {
		t.Fatalf("form did not seed from the machine report: %v", form)
	}
	if _, ok := form["manualId"]; ok {
		t.Fatalf("no hand-written form exists yet, but one was offered: %v", form["manualId"])
	}

	manualID := mustCreateManual(t, s, `{"symbol":"600519","date":"2026-09-02","subtype":"深度分析",
		"title":"工作流产出","body_md":"人工改过的正文","audience":"all"}`)
	if manualID == machineID {
		t.Fatal("the hand-written report took over the machine report's row")
	}
	machine, _ := s.st.GetNew(machineID, nil)
	if machine.MD != "机器正文" || machine.Version != "default" {
		t.Fatalf("machine report changed: %+v", machine)
	}

	// And the two are now the two versions of one report, which is what the switcher is for.
	sibs := s.st.VersionsOfReport(*machine, nil)
	got := map[string]bool{}
	for _, v := range sibs {
		got[v.Version] = true
	}
	if !got["default"] || !got["manual"] {
		t.Fatalf("version switcher does not show both forms: %+v", sibs)
	}

	// Asked again, the form now points at the hand-written form instead of offering to fork a
	// second one.
	if again := editorForm(t, s, fmt.Sprint(machineID)); fmt.Sprint(again["manualId"]) != fmt.Sprint(manualID) {
		t.Fatalf("manualId = %v, want %d", again["manualId"], manualID)
	}
}

// ---------- editing in place ----------

// Retitling has to MOVE the row. The identity index includes the title, so writing the edit through
// the upsert path would insert a second row and leave the original behind, in every list, forever.
func TestRetitlingAHandWrittenReportMovesItRatherThanCopyingIt(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	before, _ := s.st.GetNew(id, nil)

	rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), fmt.Sprintf(
		`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":"改过的标题",
		  "body_md":"新正文","audience":"all","updated_at":%q}`, before.Time))
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := s.st.CountReportsOfVersion("manual"); n != 1 {
		t.Fatalf("retitle left %d hand-written rows, want 1", n)
	}
	after, _ := s.st.GetNew(id, nil)
	if after.Title != "改过的标题" || after.MD != "新正文" {
		t.Fatalf("edit did not land: %+v", after)
	}
}

// sent_at is the concurrency token, so a save computed against a stale one must change nothing —
// two editors on one report otherwise means the second silently discards the first's words.
func TestASaveAgainstAStaleReportIsRefused(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	loaded, _ := s.st.GetNew(id, nil)

	first := fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":"手工补充",
		"body_md":"第一个人写的","audience":"all","updated_at":%q}`, loaded.Time)
	if rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), first); rec.Code != http.StatusOK {
		t.Fatalf("first save status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The second editor still holds the token from before the first save.
	second := fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":"手工补充",
		"body_md":"第二个人写的","audience":"all","updated_at":%q}`, loaded.Time)
	rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), second)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "report_stale") {
		t.Fatalf("stale save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := s.st.GetNew(id, nil); got.MD != "第一个人写的" {
		t.Fatalf("the stale save overwrote anyway: %q", got.MD)
	}
}

// The token has to be finer than a second, or two saves inside one second each compute the other's
// token and neither is detected as stale. This is the assertion that manualInstant's precision is
// load-bearing rather than incidental.
func TestTheConcurrencyTokenDistinguishesSavesWithinOneSecond(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	first, _ := s.st.GetNew(id, nil)
	body := func(md, token string) string {
		return fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":"手工补充",
			"body_md":%q,"audience":"all","updated_at":%q}`, md, token)
	}
	if rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), body("二", first.Time)); rec.Code != http.StatusOK {
		t.Fatalf("save status=%d", rec.Code)
	}
	second, _ := s.st.GetNew(id, nil)
	if second.Time == first.Time {
		t.Fatalf("two saves in the same run produced the same token %q — a second editor would go undetected", second.Time)
	}
}

// A hand-written report cannot take an identity another one already holds, and the refusal names
// the occupant: the author almost always wants to open that one rather than invent a title.
func TestACollisionNamesTheReportItCollidedWith(t *testing.T) {
	s := editorFixture(t)
	first := mustCreateManual(t, s, manualForm)
	rec := editorCall(t, s, http.MethodPost, "", manualForm)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
		ID    int64  `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Error != "report_exists" || out.ID != first {
		t.Fatalf("collision response = %+v, want report_exists on %d", out, first)
	}
}

// ---------- what the editor may not touch ----------

func TestTheEditorCannotWriteOrDeleteAMachineReport(t *testing.T) {
	s := editorFixture(t)
	machineID, _, _ := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "工作流产出", MD: "机器正文"})

	save := editorCall(t, s, http.MethodPut, fmt.Sprint(machineID), manualForm)
	if save.Code != http.StatusConflict || !strings.Contains(save.Body.String(), "report_not_manual") {
		t.Fatalf("PUT on a machine report: status=%d body=%s", save.Code, save.Body.String())
	}
	del := editorCall(t, s, http.MethodDelete, fmt.Sprint(machineID), "")
	if del.Code != http.StatusConflict || !strings.Contains(del.Body.String(), "report_not_manual") {
		t.Fatalf("DELETE on a machine report: status=%d body=%s", del.Code, del.Body.String())
	}
	if rep, _ := s.st.GetNew(machineID, nil); rep == nil || rep.MD != "机器正文" {
		t.Fatalf("machine report was touched: %+v", rep)
	}
}

// The store refuses it too, not only the handler: the handler's check tells the editor what is
// wrong, the UPDATE's own `AND version=?` is what makes it true regardless of the caller.
func TestTheStoreRefusesToUpdateANonManualRow(t *testing.T) {
	s := editorFixture(t)
	machineID, _, _ := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "t", MD: "机器正文"})
	rep, _ := s.st.GetNew(machineID, nil)
	if _, err := s.st.UpdateManualReport(machineID, Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "t", MD: "改写"}, rep.Time); err != ErrReportStale {
		t.Fatalf("UpdateManualReport on a machine row: err=%v, want ErrReportStale", err)
	}
	if got, _ := s.st.GetNew(machineID, nil); got.MD != "机器正文" {
		t.Fatalf("body changed: %q", got.MD)
	}
}

func TestDeletingAHandWrittenReportTakesItsAudienceWithIt(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	if len(s.st.ReportViewers(id)) == 0 {
		t.Fatal("audience was not recorded")
	}
	if rec := editorCall(t, s, http.MethodDelete, fmt.Sprint(id), ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rep, _ := s.st.GetNew(id, nil); rep != nil {
		t.Fatal("report survived the delete")
	}
	// A viewer row pointing at a report that no longer exists is an access grant with nothing on the
	// other end, and ids are reused.
	if v := s.st.ReportViewers(id); len(v) != 0 {
		t.Fatalf("viewer rows left behind: %v", v)
	}
}

// ---------- validation ----------

func TestTheEditorRefusesFormsItCannotPublish(t *testing.T) {
	s := editorFixture(t)
	for name, body := range map[string]string{
		"no subject": `{"date":"2026-09-02","subtype":"深度分析","body_md":"x","audience":"all"}`,
		"bad date":   `{"title":"t","date":"2026-9-2","subtype":"深度分析","body_md":"x","audience":"all"}`,
		"no date":    `{"title":"t","subtype":"深度分析","body_md":"x","audience":"all"}`,
		"no subtype": `{"title":"t","date":"2026-09-02","body_md":"x","audience":"all"}`,
		"empty body": `{"title":"t","date":"2026-09-02","subtype":"深度分析","body_md":"   ","audience":"all"}`,
		// A targeted report with nobody in its audience is readable by no restricted account, looks
		// published in every list, and reports no problem.
		"nobody": `{"title":"t","date":"2026-09-02","subtype":"深度分析","body_md":"x","audience":"grant","viewers":[]}`,
		"unknown recipient": `{"title":"t","date":"2026-09-02","subtype":"深度分析","body_md":"x",
			"audience":"grant","viewers":["u:nobody"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := editorCall(t, s, http.MethodPost, "", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if n := s.st.CountReportsOfVersion("manual"); n != 0 {
		t.Fatalf("a refused form still wrote %d report(s)", n)
	}
}

// ---------- who can read one ----------

// The two gates, asserted together, because either alone reads as the whole rule and neither is.
// The version grant decides who may read hand-written reports at all; the per-report audience
// decides which ones. A reader needs both.
func TestBothGatesAreNeededToReadAHandWrittenReport(t *testing.T) {
	s := editorFixture(t)
	root := s.st.EnsureDefaultGroup()
	ouA, _ := s.st.CreateUserGroup("ext-A", "", 0)
	s.st.SetGroupParent(ouA, root)
	s.st.SetGroupRestricted(ouA, true)
	ouB, _ := s.st.CreateUserGroup("ext-B", "", 0)
	s.st.SetGroupParent(ouB, root)
	s.st.SetGroupRestricted(ouB, true)
	s.st.UpsertUser(User{Username: "a", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("a", ouA)
	s.st.UpsertUser(User{Username: "b", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("b", ouB)

	id := mustCreateManual(t, s, fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02",
		"subtype":"深度分析","title":"给 A 的","body_md":"正文","audience":"grant","viewers":["g:%d"]}`, ouA))

	canRead := func(user string) bool {
		rep, _ := s.st.GetNew(id, s.viewerScope(user))
		return rep != nil
	}
	// Addressed to A, but nobody is granted the version yet: default-deny holds.
	if canRead("a") {
		t.Fatal("A read a hand-written report without being granted the version")
	}
	s.st.SetVersionGrants("manual", []string{groupPrincipal(ouA), groupPrincipal(ouB)})
	if !canRead("a") {
		t.Fatal("A is granted the version and is on the audience, but cannot read it")
	}
	// B holds the same grant and is not on this report's audience. That difference is the whole
	// point of the per-report list.
	if canRead("b") {
		t.Fatal("B read a report addressed to A")
	}
	// An internal account is not scoped at all, and never was.
	if !canRead("editor") {
		t.Fatal("an internal account cannot read a hand-written report")
	}
}

// "所有人" is stored as the Default OU principal — every account's chain ends there — so it needs no
// column of its own, and reads back as "all" rather than as a group somebody has to recognise.
func TestEveryoneIsStoredAsThePrincipalEveryReaderAlreadyHas(t *testing.T) {
	s := editorFixture(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("ext", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	s.st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("ext", ou)
	s.st.SetVersionGrants("manual", []string{groupPrincipal(ou)})

	id := mustCreateManual(t, s, manualForm) // audience: all
	if rep, _ := s.st.GetNew(id, s.viewerScope("ext")); rep == nil {
		t.Fatal(`a report addressed to "所有人" was not readable by a granted external account`)
	}
	form := editorForm(t, s, fmt.Sprint(id))
	if form["audience"] != "all" {
		t.Fatalf("audience read back as %v, want all", form["audience"])
	}
	// Refused from the picker so an author who means "everyone" says so, rather than discovering
	// that a group named "默认" happens to mean it.
	rec := editorCall(t, s, http.MethodPost, "", fmt.Sprintf(`{"title":"t","date":"2026-09-03",
		"subtype":"深度分析","body_md":"x","audience":"grant","viewers":["g:%d"]}`, root))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Default OU as a recipient: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Moving a report to another date has to move its audience, because report_viewers is keyed by date
// as well as by report — a row left on the old date matches nothing and the report silently becomes
// readable by no one it was addressed to.
func TestChangingTheDateCarriesTheAudienceAlong(t *testing.T) {
	s := editorFixture(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("ext", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	s.st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("ext", ou)
	s.st.SetVersionGrants("manual", []string{groupPrincipal(ou)})

	id := mustCreateManual(t, s, fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02",
		"subtype":"深度分析","title":"t","body_md":"正文","audience":"grant","viewers":["g:%d"]}`, ou))
	loaded, _ := s.st.GetNew(id, nil)
	rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), fmt.Sprintf(
		`{"symbol":"600519","date":"2026-09-05","subtype":"深度分析","title":"t","body_md":"正文",
		  "audience":"grant","viewers":["g:%d"],"updated_at":%q}`, ou, loaded.Time))
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rep, _ := s.st.GetNew(id, s.viewerScope("ext")); rep == nil {
		t.Fatal("the report became unreadable to its audience after moving date")
	}
}

// ---------- the permission itself ----------

func TestOnlyTheEditingPermissionOpensTheseEndpoints(t *testing.T) {
	for role, want := range map[string]bool{
		"admin": true, "editor": true, "operator": false, "user": false,
	} {
		if got := can(role, PermEditReport); got != want {
			t.Errorf("can(%q, PermEditReport) = %v, want %v", role, got, want)
		}
	}
	// Running workflows and writing reports are separate authorities in both directions: 编辑员
	// spends no run quota, 执行员 publishes no words.
	if can("editor", PermRunBatch) {
		t.Error("编辑员 can run workflows")
	}
	if can("editor", PermManage) {
		t.Error("编辑员 has admin access")
	}
}

// The seeded version has to be exactly the one the write path uses, and its visibility has to be the
// one that makes the per-report audience mean anything: under VisibilityAll the audience list is
// never consulted and every granted reader sees every hand-written report.
func TestTheManualVersionIsSeededAsTheAudienceMechanismNeeds(t *testing.T) {
	st := newTestStore(t)
	v, ok := st.Version(st.ManualVersion())
	if !ok {
		t.Fatal("the manual version was not seeded")
	}
	if v.Visibility != VisibilityGroup {
		t.Fatalf("visibility = %q, want %q — the per-report audience is only consulted under group",
			v.Visibility, VisibilityGroup)
	}
	// Deleting it would leave the write path pointing at an unregistered name and every
	// hand-written report ungrantable.
	if err := st.DeleteVersion(st.ManualVersion()); err == nil {
		t.Fatal("the manual version could be deleted")
	}
}
