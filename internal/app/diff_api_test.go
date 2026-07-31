package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Comparing any two reports the caller may read.
//
// Deliberately an arbitrary PAIR rather than "this one and its predecessor": the useful comparisons
// are not all chronological — an internal edition against the external one, this quarter against
// the same quarter last year. The UI can default to the previous report of the same kind; the
// capability underneath does not need to care.

func diffGET(t *testing.T, s *Server, user string, a, b int64) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/reports/diff?a="+itoa(a)+"&b="+itoa(b), nil)
	s.apiReportDiff(rec, r, user)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestDiffAPIComparesTwoReports(t *testing.T) {
	s := tenancyServer(t)
	older, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-06-30", RType: "投资决策",
		Title: "六月", MD: "# 结论\n买入\n\n## 估值\n目标价 48 元\n"})
	newer, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-31", RType: "投资决策",
		Title: "七月", MD: "# 结论\n买入\n\n## 估值\n目标价 55 元\n"})

	code, out := diffGET(t, s, "admin", older, newer)
	if code != http.StatusOK {
		t.Fatalf("diff → %d", code)
	}
	// Both sides are described, so the UI can label the columns without another request.
	for _, side := range []string{"a", "b"} {
		m, _ := out[side].(map[string]any)
		if m["title"] == nil || m["date"] == nil {
			t.Errorf("side %q lacks its context: %v", side, m)
		}
	}
	secs, _ := out["sections"].([]any)
	if len(secs) != 2 {
		t.Fatalf("sections = %d, want 2", len(secs))
	}
	if out["changed"] != float64(1) {
		t.Errorf("changed = %v, want 1 — the UI leads with this", out["changed"])
	}
}

// The security property. A diff endpoint that accepts two ids is a read of both, so both have to be
// checked — and a report the caller may not read must be indistinguishable from one that is not
// there, or the endpoint becomes a way to ask which report ids exist.
func TestDiffAPIRefusesAReportYouCannotRead(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	internal, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-06-30", RType: "投资决策",
		Title: "内部", MD: "# 结论\n内部结论\n"})
	external, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-31", RType: "投资决策",
		Title: "对外", Version: "对外版", MD: "# 结论\n对外结论\n"})

	st.UpsertUser(User{Username: "client@corp.example", PasswordHash: "h", Role: "user"})
	st.SetUserRestricted("client@corp.example", true)
	st.SetVersionGrants("对外版", []string{userPrincipal("client@corp.example")})
	st.AddReportViewer(external, "2026-07-31", "client@corp.example", 0)

	// One side readable, one not.
	if code, _ := diffGET(t, s, "client@corp.example", internal, external); code != http.StatusNotFound {
		t.Errorf("diffing against an unreadable report → %d, want 404", code)
	}
	// And the other way round, so the refusal does not depend on which slot it lands in.
	if code, _ := diffGET(t, s, "client@corp.example", external, internal); code != http.StatusNotFound {
		t.Errorf("with the operands swapped → %d, want 404", code)
	}
	// A missing id answers identically, so the endpoint cannot be used to probe which ids exist.
	codeMissing, _ := diffGET(t, s, "client@corp.example", external, 999999)
	codeForbidden, _ := diffGET(t, s, "client@corp.example", external, internal)
	if codeMissing != codeForbidden {
		t.Errorf("missing id → %d but forbidden id → %d; that difference is an oracle",
			codeMissing, codeForbidden)
	}
	// Comparing something readable with itself is allowed and reports no changes.
	code, out := diffGET(t, s, "client@corp.example", external, external)
	if code != http.StatusOK || out["changed"] != float64(0) {
		t.Errorf("self-diff → %d changed=%v", code, out["changed"])
	}
}
