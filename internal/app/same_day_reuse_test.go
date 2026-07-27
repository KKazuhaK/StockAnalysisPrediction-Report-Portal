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

// TestFindSameDayReportRespectsScope proves the reuse lookup only ever returns a report the viewer
// may actually read: today's own-OU or internal report, never another OU's, never an older one.
func TestFindSameDayReportRespectsScope(t *testing.T) {
	st, s, _, ouA, _ := readScopeFixture(t)
	today := time.Now().Format("2006-01-02")
	sc := s.viewerScope("ext")

	// The fixture has, for 600000/val/today: own(ouA), internal(NULL) and another OU's report.
	id, ok := st.FindSameDayReport("600000", "val", today, sc)
	if !ok {
		t.Fatal("a visible same-day report must be found")
	}
	if r, _ := st.GetNew(id, sc); r == nil {
		t.Error("the reused report must be readable by that same viewer")
	}
	// A subtype only another OU has today must NOT be reusable.
	if _, ok := st.FindSameDayReport("600000", "secret-type", today, sc); ok {
		t.Error("another OU's same-day report must not be reusable")
	}
	// Yesterday is not today.
	yest := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if _, ok := st.FindSameDayReport("600000", "val", yest, sc); ok {
		t.Error("the lookup must be pinned to the given civil date")
	}
	// Unscoped (internal) callers can see the other OU's subtype.
	if _, ok := st.FindSameDayReport("600000", "secret-type", today, nil); !ok {
		t.Error("an unscoped lookup must find any same-day report")
	}
	_ = ouA
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
