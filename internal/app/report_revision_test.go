package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The edit history of a hand-written report (ADR 0026).
//
// The property everything else rests on is that history is written by the same transaction that
// overwrites the report — so a save that is refused leaves no trace, and a save that lands cannot
// fail to be recorded. Most of what is asserted here is that seam.

func revisionCall(t *testing.T, s *Server, method, id, rev, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/reports/"+id+"/revisions", strings.NewReader(body))
	req.SetPathValue("id", id)
	if rev != "" {
		req.SetPathValue("rev", rev)
	}
	rec := httptest.NewRecorder()
	switch {
	case method == http.MethodPost:
		s.apiReportRevisionRestore(rec, req, "editor")
	case rev != "":
		s.apiReportRevision(rec, req, "editor")
	default:
		s.apiReportRevisions(rec, req, "editor")
	}
	return rec
}

// save edits a hand-written report through the real handler, so every test goes the way the editor
// does rather than through the store directly.
func save(t *testing.T, s *Server, id int64, title, md string) {
	t.Helper()
	cur, _ := s.st.GetNew(id, nil)
	body := fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":%q,
		"body_md":%q,"audience":"all","updated_at":%q}`, title, md, cur.Time)
	if rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), body); rec.Code != http.StatusOK {
		t.Fatalf("save %q: status=%d body=%s", title, rec.Code, rec.Body.String())
	}
}

func revisionBodies(t *testing.T, s *Server, id int64) []string {
	t.Helper()
	var out []string
	for _, v := range s.st.Revisions(id) {
		full, ok := s.st.Revision(id, v.ID)
		if !ok {
			t.Fatalf("revision %d vanished", v.ID)
		}
		out = append(out, full.MD)
	}
	return out
}

// ---------- the seam with the save ----------

// The history is what the report USED to say. The current text lives on the report and is never
// stored twice, which is the whole reason a save snapshots the pre-image rather than appending the
// post-image.
func TestHistoryHoldsWhatTheReportUsedToSayAndNotWhatItSaysNow(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm) // body "# 手写\n正文"

	if n := s.st.countRevisions(id); n != 0 {
		t.Fatalf("a newly written report already has %d revision(s) — nothing has been superseded", n)
	}
	save(t, s, id, "手工补充", "第二版")
	save(t, s, id, "手工补充", "第三版")

	// Newest first, and the current text is absent from the log.
	if got := revisionBodies(t, s, id); strings.Join(got, "|") != "第二版|# 手写\n正文" {
		t.Fatalf("history = %q", got)
	}
	cur, _ := s.st.GetNew(id, nil)
	if cur.MD != "第三版" {
		t.Fatalf("current body = %q", cur.MD)
	}
}

// A refused save must leave the log exactly as it was. The snapshot sits inside the transaction and
// after the staleness check precisely so that a conflict cannot deposit a half-history.
func TestARefusedSaveWritesNoHistory(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	loaded, _ := s.st.GetNew(id, nil)
	save(t, s, id, "手工补充", "第二版") // moves the token on

	stale := fmt.Sprintf(`{"symbol":"600519","date":"2026-09-02","subtype":"深度分析","title":"手工补充",
		"body_md":"来自过期编辑器","audience":"all","updated_at":%q}`, loaded.Time)
	rec := editorCall(t, s, http.MethodPut, fmt.Sprint(id), stale)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := revisionBodies(t, s, id); len(got) != 1 || got[0] != "# 手写\n正文" {
		t.Fatalf("a refused save changed the history: %q", got)
	}
}

// Pressing save twice, or editing only the audience, must not bury the version the author wants
// under identical copies of the one they are looking at.
func TestASaveThatChangedNothingWritesNoRevision(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	save(t, s, id, "手工补充", "# 手写\n正文") // byte-identical to what is there
	save(t, s, id, "手工补充", "# 手写\n正文")
	if n := s.st.countRevisions(id); n != 0 {
		t.Fatalf("%d revision(s) recorded for saves that changed nothing", n)
	}
	// ...and the token still moves, so the editor is not left holding a stale one.
	save(t, s, id, "手工补充", "真的改了")
	if n := s.st.countRevisions(id); n != 1 {
		t.Fatalf("a real change recorded %d revision(s), want 1", n)
	}
}

// The identity fields ride along, or a restore recovers the words under the wrong title — a
// different report wearing the old text.
func TestARevisionCarriesTheIdentityAndNotOnlyTheBody(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	save(t, s, id, "改过的标题", "新正文")

	revs := s.st.Revisions(id)
	if len(revs) != 1 {
		t.Fatalf("want 1 revision, got %d", len(revs))
	}
	if revs[0].Title != "手工补充" || revs[0].Author != "editor" || revs[0].RType != "深度分析" {
		t.Fatalf("revision did not capture the identity: %+v", revs[0])
	}
	// The list is an index, not a pile of documents: bodies are counted, not shipped.
	if revs[0].MD != "" || revs[0].Bytes == 0 {
		t.Fatalf("the list carried a body: md=%q bytes=%d", revs[0].MD, revs[0].Bytes)
	}
}

// ---------- the cap ----------

func TestTheCapBoundsHistoryPerReportAndZeroMeansKeepEverything(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)

	// Unlimited is the shipped state, and it is a stored 0 rather than an absent key — the one
	// place in the settings where 0 is an answer instead of "unset".
	if keep := s.reportRevisionsKeep(); keep != 0 {
		t.Fatalf("unconfigured cap = %d, want 0 (unlimited)", keep)
	}
	for i := 0; i < 5; i++ {
		save(t, s, id, "手工补充", fmt.Sprintf("第 %d 版", i))
	}
	if n := s.st.countRevisions(id); n != 5 {
		t.Fatalf("unlimited kept %d of 5", n)
	}

	s.st.SetSetting(setReportRevisionsKeep, "2")
	save(t, s, id, "手工补充", "封顶后的一版")
	if n := s.st.countRevisions(id); n != 2 {
		t.Fatalf("cap of 2 left %d revisions", n)
	}
	// The two kept are the NEWEST two, which is what an undo is for.
	got := revisionBodies(t, s, id)
	if strings.Join(got, "|") != "第 4 版|第 3 版" {
		t.Fatalf("cap kept the wrong two: %q", got)
	}

	// A second report's history is untouched by the first's trim: the ring is per report.
	other := mustCreateManual(t, s, `{"symbol":"600520","date":"2026-09-02","subtype":"深度分析",
		"title":"另一篇","body_md":"甲","audience":"all"}`)
	cur, _ := s.st.GetNew(other, nil)
	editorCall(t, s, http.MethodPut, fmt.Sprint(other), fmt.Sprintf(
		`{"symbol":"600520","date":"2026-09-02","subtype":"深度分析","title":"另一篇","body_md":"乙",
		  "audience":"all","updated_at":%q}`, cur.Time))
	if n := s.st.countRevisions(other); n != 1 {
		t.Fatalf("the other report has %d revisions", n)
	}
	if n := s.st.countRevisions(id); n != 2 {
		t.Fatalf("the first report's history changed to %d", n)
	}
}

func TestTheCapIsRefusedRatherThanClampedWhenItMakesNoSense(t *testing.T) {
	s := editorFixture(t)
	for _, body := range []string{`{"revisions_keep":-1}`, `{"revisions_keep":100000}`,
		`{"revisions_days":1}`} {
		req := httptest.NewRequest("POST", "/api/admin/cleanup/config", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.apiCleanupConfigSave(rec, req, "admin")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
	}
	// Nothing was persisted on the way to any of those refusals.
	if got := s.st.GetSetting(setReportRevisionsKeep, ""); got != "" {
		t.Fatalf("a refused save still wrote the cap: %q", got)
	}
}

// The cap rides the cleanup console's config endpoint, whose per-field merge is the only reason it
// needed no endpoint of its own — so that property is pinned rather than assumed.
func TestTheCapSurvivesAnUnrelatedCleanupSave(t *testing.T) {
	s := editorFixture(t)
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.apiCleanupConfigSave(rec, httptest.NewRequest("POST", "/api/admin/cleanup/config",
			strings.NewReader(body)), "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
	}
	post(`{"revisions_keep":20}`)
	post(`{"reports_days":800}`)
	if keep := s.reportRevisionsKeep(); keep != 20 {
		t.Fatalf("cap = %d after an unrelated save, want 20", keep)
	}
	if days := s.cleanupConfigLoad().ReportsDays; days != 800 {
		t.Fatalf("reports_days = %d, want 800", days)
	}
}

// ---------- retention and deletion ----------

func TestTheStorageConsoleAgesRevisionsIndependentlyOfTheirReport(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	save(t, s, id, "手工补充", "第二版")

	// Backdate the one revision past the cutoff. The report itself is untouched — and could not age
	// out anyway, because every save rewrites its sent_at.
	old := time.Now().UTC().AddDate(0, 0, -400).Format(time.RFC3339Nano)
	s.st.exec("UPDATE report_revisions SET saved_at=? WHERE report_id=?", old, id)

	cut := time.Now().UTC().AddDate(0, 0, -30)
	if n, _ := s.st.CountRevisionsBefore(cut); n != 1 {
		t.Fatalf("preview counted %d eligible revisions, want 1", n)
	}
	n, err := s.st.DeleteRevisionsBefore(cut)
	if err != nil || n != 1 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if got, _ := s.st.GetNew(id, nil); got == nil {
		t.Fatal("the report was deleted along with its history")
	}
	// Preview and delete share one predicate, so a second pass finds nothing.
	if n, _ := s.st.CountRevisionsBefore(cut); n != 0 {
		t.Fatalf("%d revisions survived the pass the preview counted", n)
	}
}

// The target ships off, like every other one: nothing starts deleting on upgrade.
func TestTheRevisionTargetShipsDisabled(t *testing.T) {
	s := editorFixture(t)
	c := s.cleanupConfigLoad()
	if c.RevisionsEnabled {
		t.Fatal("the edit-history retention target is enabled on a fresh portal")
	}
	if c.scheduledTargets().Revisions {
		t.Fatal("a scheduled pass would act on revisions with nobody having asked")
	}
	// And a hand-edited meta value cannot drop below the floor on read.
	s.st.SetSetting("cleanup_revisions_days", "1")
	if got := s.cleanupConfigLoad().RevisionsDays; got != minRevisionsRetentionDays {
		t.Fatalf("hand-edited retention read back as %d, want the floor %d", got, minRevisionsRetentionDays)
	}
}

func TestDeletingAReportTakesItsHistoryWithIt(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	save(t, s, id, "手工补充", "第二版")
	if s.st.countRevisions(id) == 0 {
		t.Fatal("no history to lose")
	}
	if rec := editorCall(t, s, http.MethodDelete, fmt.Sprint(id), ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Whole prior copies of a body, under an id nothing points at and ids are reassigned.
	if n := s.st.countRevisions(id); n != 0 {
		t.Fatalf("%d revision(s) left behind by the delete", n)
	}
}

// The other delete path, which does not go through DeleteReport and is the easy one to miss.
func TestTheStorageSweepAlsoTakesTheHistory(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	save(t, s, id, "手工补充", "第二版")
	// Age the report itself past the reports cutoff.
	s.st.exec("UPDATE reports SET sent_at=? WHERE id=?",
		time.Now().UTC().AddDate(-3, 0, 0).Format(time.RFC3339), id)

	if n, err := s.st.DeleteReportsIngestedBefore(time.Now().UTC().AddDate(-1, 0, 0)); err != nil || n != 1 {
		t.Fatalf("sweep deleted %d, %v", n, err)
	}
	if n := s.st.countRevisions(id); n != 0 {
		t.Fatalf("the chunked sweep left %d revision(s) behind", n)
	}
}

// ---------- restore ----------

// Restore is a save, and that is what makes the button safe to press: the version it replaces
// becomes the newest entry, so restoring is undone by restoring again.
func TestRestoringIsItselfRecorded(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm) // "# 手写\n正文"
	save(t, s, id, "手工补充", "误改的版本")

	revs := s.st.Revisions(id)
	cur, _ := s.st.GetNew(id, nil)
	rec := revisionCall(t, s, http.MethodPost, fmt.Sprint(id), fmt.Sprint(revs[0].ID),
		fmt.Sprintf(`{"updated_at":%q}`, cur.Time))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	back, _ := s.st.GetNew(id, nil)
	if back.MD != "# 手写\n正文" {
		t.Fatalf("restore did not put the body back: %q", back.MD)
	}
	// The mistake is still there to go back to.
	if got := revisionBodies(t, s, id); strings.Join(got, "|") != "误改的版本|# 手写\n正文" {
		t.Fatalf("history after restore = %q", got)
	}
	// A fresh token comes back, so the editor can keep working without a reload.
	var out struct {
		UpdatedAt string `json:"updated_at"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.UpdatedAt == "" || out.UpdatedAt == cur.Time {
		t.Fatalf("restore returned token %q (was %q)", out.UpdatedAt, cur.Time)
	}
}

// A restore computed from a history list drawn before somebody else saved must be refused, not win.
func TestRestoringWithAStaleTokenIsRefused(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm)
	stale, _ := s.st.GetNew(id, nil)
	save(t, s, id, "手工补充", "别人的改动")

	revs := s.st.Revisions(id)
	rec := revisionCall(t, s, http.MethodPost, fmt.Sprint(id), fmt.Sprint(revs[0].ID),
		fmt.Sprintf(`{"updated_at":%q}`, stale.Time))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "report_stale") {
		t.Fatalf("stale restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := s.st.GetNew(id, nil); got.MD != "别人的改动" {
		t.Fatalf("the stale restore landed anyway: %q", got.MD)
	}
}

// Restoring an old title is a rename, and can collide exactly as a rename can.
func TestRestoringOntoATakenIdentityReportsTheCollision(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, manualForm) // title 手工补充
	save(t, s, id, "改名了", "正文二")
	// Somebody else takes the title this report used to hold.
	other := mustCreateManual(t, s, manualForm)

	revs := s.st.Revisions(id)
	cur, _ := s.st.GetNew(id, nil)
	rec := revisionCall(t, s, http.MethodPost, fmt.Sprint(id), fmt.Sprint(revs[0].ID),
		fmt.Sprintf(`{"updated_at":%q}`, cur.Time))
	if rec.Code != http.StatusConflict {
		t.Fatalf("colliding restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Code string `json:"code"`
		ID   int64  `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Code != "report_exists" || out.ID != other {
		t.Fatalf("collision = %+v, want report_exists on %d", out, other)
	}
}

// ---------- what the history is not ----------

// A workflow's report has no history and is not editable, so the endpoints refuse rather than
// answering with an empty list — an empty list reads as "nothing has been edited yet".
func TestAMachineReportHasNoHistorySurface(t *testing.T) {
	s := editorFixture(t)
	machineID, _, _ := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "深度分析", Title: "工作流产出", MD: "机器正文"})
	for _, rec := range []*httptest.ResponseRecorder{
		revisionCall(t, s, http.MethodGet, fmt.Sprint(machineID), "", ""),
		revisionCall(t, s, http.MethodGet, fmt.Sprint(machineID), "1", ""),
		revisionCall(t, s, http.MethodPost, fmt.Sprint(machineID), "1", `{"updated_at":""}`),
	} {
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "report_not_manual") {
			t.Fatalf("machine report: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// A revision id is looked up WITH its report, so one report's history cannot be read through
// another report the caller happens to be allowed to open.
func TestARevisionCannotBeReadThroughTheWrongReport(t *testing.T) {
	s := editorFixture(t)
	a := mustCreateManual(t, s, manualForm)
	save(t, s, a, "手工补充", "甲的第二版")
	b := mustCreateManual(t, s, `{"symbol":"600520","date":"2026-09-02","subtype":"深度分析",
		"title":"乙","body_md":"乙一","audience":"all"}`)

	revOfA := s.st.Revisions(a)[0].ID
	rec := revisionCall(t, s, http.MethodGet, fmt.Sprint(b), fmt.Sprint(revOfA), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-report revision read: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The history endpoint answers the question a history is opened to ask, using the same section diff
// the report comparison uses rather than a second implementation of "what changed".
func TestOpeningARevisionCarriesBothTheDiffAndTheDocument(t *testing.T) {
	s := editorFixture(t)
	id := mustCreateManual(t, s, `{"symbol":"600519","date":"2026-09-02","subtype":"深度分析",
		"title":"t","body_md":"# 一\n甲\n\n# 二\n乙","audience":"all"}`)
	save(t, s, id, "t", "# 一\n甲\n\n# 二\n丙")

	revs := s.st.Revisions(id)
	rec := revisionCall(t, s, http.MethodGet, fmt.Sprint(id), fmt.Sprint(revs[0].ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Revision Revision      `json:"revision"`
		Sections []SectionDiff `json:"sections"`
		Changed  int           `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if out.Revision.MD != "# 一\n甲\n\n# 二\n乙" {
		t.Fatalf("body not served: %q", out.Revision.MD)
	}
	if out.Changed != 1 || len(out.Sections) != 2 {
		t.Fatalf("sections=%d changed=%d, want 2 sections and 1 changed", len(out.Sections), out.Changed)
	}
}
