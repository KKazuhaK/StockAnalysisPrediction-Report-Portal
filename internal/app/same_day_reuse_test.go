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

// TestFindSameDayReportIsAContentLookup pins the split ADR 0024 introduced. The store answers only
// "does this analysis exist today" — symbol, subtype, version, date — and does NOT ask who may read
// it. Scoping the lookup itself was the original mistake: under per-person visibility a first-time
// requester is on no viewer list, so a scoped lookup found nothing for exactly the caller reuse
// exists to serve, and every request ran for real. The entitlement check moved to the caller, which
// is where TestReuseRefusesAnUngrantedVersion holds it.
func TestFindSameDayReportIsAContentLookup(t *testing.T) {
	st, s, _, _, _ := readScopeFixture(t)
	today := time.Now().Format("2006-01-02")
	def := st.DefaultVersion()

	// The fixture's own-OU report is on the published version; the internal ones are on the default.
	if _, ok := st.FindSameDayReport("600000", "val", "对外版", today); !ok {
		t.Error("a same-day report of that version must be found")
	}
	if _, ok := st.FindSameDayReport("600000", "val", def, today); !ok {
		t.Error("the internal report of the same symbol+subtype is a different version, and exists")
	}
	// Version is part of the lookup: asking for a version nobody produced finds nothing, even though
	// the symbol and subtype both exist today.
	if _, ok := st.FindSameDayReport("600000", "val", "客户版", today); ok {
		t.Error("a version that was never generated must not be found")
	}
	// Pinned to the civil date.
	yest := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if _, ok := st.FindSameDayReport("600000", "val", "对外版", yest); ok {
		t.Error("the lookup must be pinned to the given civil date")
	}
	_ = s
}

// TestReuseRefusesAnUngrantedVersion proves reuse skips the RUN, never the grant. Without this,
// "already generated today" would become a way to receive a form of a report the caller was never
// entitled to read — the run allow-list would gate generating it while reuse handed it over.
func TestReuseRefusesAnUngrantedVersion(t *testing.T) {
	s, org, _, tgtA, _ := ouFixture(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	st.UpdateTarget(tgtA, "A", `{"output_subtype":"val","symbol_input":"code","output_version":"对外版"}`)
	st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: ""}})
	today := s.panelToday()
	st.UpsertReport(Rep{Symbol: "600000", Date: today, RType: "val", Title: "已生成", Version: "对外版"})

	rows := []map[string]string{{"code": "600000"}}
	if _, _, ok := s.reuseSameDayReport("ext", tgtA, rows); ok {
		t.Error("a report of an ungranted version must not be handed back")
	}
	// Granted: it is reused, and the requester joins its viewer list — which is what makes strict
	// per-person visibility cost no duplicate generation.
	st.SetVersionGrants("对外版", []string{groupPrincipal(org)})
	id, _, ok := s.reuseSameDayReport("ext", tgtA, rows)
	if !ok {
		t.Fatal("a granted version's same-day report must be reused")
	}
	if r, _ := st.GetNew(id, s.viewerScope("ext")); r == nil {
		t.Error("the reused report must be readable by the requester afterwards")
	}
}

// TestApiBatchJobCreateReusesSameDayReport is the end-to-end contract for R1's reuse gate: when a
// restricted member requests a report that already exists today, the portal returns it instead of
// running — creating no job and consuming no quota — and falls through to a real run otherwise.
func TestApiBatchJobCreateReusesSameDayReport(t *testing.T) {
	s, org, _, tgtA, _ := ouFixture(t)
	st := s.st
	// Declare what the target produces and which input carries the stock code; reuse needs both.
	st.UpdateTarget(tgtA, "A", `{"output_subtype":"val","symbol_input":"code"}`)
	st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: ""}})
	st.SetGroupDailyQuota(org, quotaPtr(2))
	// The target declares no version, so it produces the default one — and reuse only hands back a
	// version the requester is granted (ADR 0024), so the OU is granted it here.
	st.SetVersionGrants(st.DefaultVersion(), []string{groupPrincipal(org)})

	submit := func(user, symbol string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"target_id":%d,"rows":[{"code":%q}]}`, tgtA, symbol)
		rec := httptest.NewRecorder()
		s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/api/admin/batch/jobs", strings.NewReader(body)), user)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}

	// Nothing exists yet → a real run, which costs quota.
	first := decode(submit("ext", "600000"))
	if first["reused"] == true {
		t.Fatal("with no existing report the submit must actually run")
	}
	if st.RunsToday("ext", s.panelMidnight(time.Now())) != 1 {
		t.Fatal("the real run must consume quota")
	}

	// Now an internal report for the same (symbol, subtype) lands today.
	today := time.Now().Format("2006-01-02")
	repID, _, err := st.UpsertReport(Rep{Symbol: "600000", Date: today, RType: "val", Title: "已生成"})
	if err != nil {
		t.Fatal(err)
	}

	rec := submit("ext", "600000")
	if rec.Code != http.StatusOK {
		t.Fatalf("reuse submit → %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decode(rec)
	if body["reused"] != true {
		t.Errorf("a same-day report must be reused, got %v", body)
	}
	if int64(body["report_id"].(float64)) != repID {
		t.Errorf("reused report_id = %v, want %d", body["report_id"], repID)
	}
	// The group key lets the client open the report directly, without re-deriving the route.
	if want := "600000|" + today; body["key"] != want {
		t.Errorf("reused key = %v, want %q", body["key"], want)
	}
	if body["job_id"] != nil {
		t.Errorf("reuse must create NO job, got job_id %v", body["job_id"])
	}
	if n := st.RunsToday("ext", s.panelMidnight(time.Now())); n != 1 {
		t.Errorf("reuse must consume no quota (still %d rows today, want 1)", n)
	}

	// A different symbol has no same-day report → runs for real.
	if decode(submit("ext", "000001"))["reused"] == true {
		t.Error("a symbol with no same-day report must run, not reuse")
	}
}

// TestSameDayReuseNeedsDeclaredTargetFields proves the fail-safe: without BOTH output_subtype and
// symbol_input declared, the portal runs rather than risk handing back the wrong report — and a
// symbol-less (thematic) request never reuses.
func TestSameDayReuseNeedsDeclaredTargetFields(t *testing.T) {
	s, org, _, tgtA, _ := ouFixture(t)
	st := s.st
	st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: ""}})
	// Reuse hands back only a granted version (ADR 0024); this test is about the target's DECLARED
	// fields, so the grant is in place and the declarations are the variable under test.
	st.SetVersionGrants(st.DefaultVersion(), []string{groupPrincipal(org)})
	today := time.Now().Format("2006-01-02")
	st.UpsertReport(Rep{Symbol: "600000", Date: today, RType: "val", Title: "已生成"})

	reused := func(config, symbol string) bool {
		st.UpdateTarget(tgtA, "A", config)
		body := fmt.Sprintf(`{"target_id":%d,"rows":[{"code":%q}]}`, tgtA, symbol)
		rec := httptest.NewRecorder()
		s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/api/admin/batch/jobs", strings.NewReader(body)), "ext")
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m["reused"] == true
	}

	if reused(`{}`, "600000") {
		t.Error("an undeclared target must never reuse (it cannot know what it produces)")
	}
	if reused(`{"output_subtype":"val"}`, "600000") {
		t.Error("without symbol_input the portal cannot tell which input is the code — must not reuse")
	}
	if reused(`{"output_subtype":"val","symbol_input":"code"}`, "") {
		t.Error("a symbol-less (thematic) request must never reuse")
	}
	if !reused(`{"output_subtype":"val","symbol_input":"code"}`, "600000") {
		t.Error("with both declared and a real symbol, reuse must happen")
	}
}
