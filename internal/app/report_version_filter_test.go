package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// homeAs calls the browse feed with a query string, as the SPA does.
func homeAs(t *testing.T, s *Server, query, user string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiHome(rec, httptest.NewRequest("GET", "/api/home"+query, nil), user)
	if rec.Code != 200 {
		t.Fatalf("apiHome%s → %d %s", query, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("apiHome body: %v", err)
	}
	return out
}

// homeTitles lists the report titles the feed returned, flattened out of its per-run cards.
func homeTitles(t *testing.T, out map[string]any) []string {
	t.Helper()
	var titles []string
	groups, _ := out["groups"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		items, _ := gm["members"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			if s, ok := im["title"].(string); ok {
				titles = append(titles, s)
			}
		}
	}
	return titles
}

func versionFilterFixture(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	st.EnsureDefaultGroup()
	st.UpsertReport(Rep{Symbol: "600519", Date: "2026-09-04", RType: "深度分析", Title: "工作流写的", MD: "机器"})
	if _, err := st.CreateManualReport(Rep{
		Symbol: "600519", Date: "2026-09-04", RType: "深度分析", Title: "人写的", MD: "人工", Author: "editor"}); err != nil {
		t.Fatalf("CreateManualReport: %v", err)
	}
	return s
}

// TestBrowseFiltersByVersion is what makes the hand-written reports a set rather than a pile: they
// are the manual version, so a version filter is the whole answer — and it generalizes to the
// internal/external forms ADR 0024 already allows, instead of hard-coding one special case.
func TestBrowseFiltersByVersion(t *testing.T) {
	s := versionFilterFixture(t)

	all := homeTitles(t, homeAs(t, s, "", "admin"))
	if len(all) != 2 {
		t.Fatalf("unfiltered browse = %v; want both reports", all)
	}

	manual := homeTitles(t, homeAs(t, s, "?version=manual", "admin"))
	if len(manual) != 1 || manual[0] != "人写的" {
		t.Errorf("version=manual = %v; want only the hand-written report", manual)
	}
	def := homeTitles(t, homeAs(t, s, "?version=default", "admin"))
	if len(def) != 1 || def[0] != "工作流写的" {
		t.Errorf("version=default = %v; want only the workflow's report", def)
	}
	if got := homeTitles(t, homeAs(t, s, "?version=nosuchversion", "admin")); len(got) != 0 {
		t.Errorf("an unknown version must match nothing, not everything; got %v", got)
	}
}

// TestBrowseOffersTheVersionsItCanSee covers the filter's options. They come from the data, so a
// version nobody has written in is not offered — and the filter is withheld entirely while there is
// only one written form, because a filter whose every setting means the same thing is noise.
func TestBrowseOffersTheVersionsItCanSee(t *testing.T) {
	s := versionFilterFixture(t)

	names := versionNames(homeAs(t, s, "", "admin"))
	if len(names) != 2 || !containsString(names, "manual") || !containsString(names, "default") {
		t.Fatalf("versions = %v; want both written forms present in the data", names)
	}

	// A portal with only machine reports gets no filter at all.
	bare := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	bare.names = LoadNames(t.TempDir(), bare.st)
	bare.names.fetch = func(string) string { return "" }
	bare.st.EnsureDefaultGroup()
	bare.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-09-04", RType: "深度分析", Title: "只有一种", MD: "x"})
	if got := versionNames(homeAs(t, bare, "", "admin")); len(got) != 0 {
		t.Errorf("one written form must offer no version filter; got %v", got)
	}
}

// TestVersionFilterCannotWidenAScope is the one that matters for disclosure. The filter narrows what
// a reader may already see; naming a version they were never granted must return nothing rather than
// reaching past their scope — and the options must not name it either, or the filter becomes a
// directory of what exists.
func TestVersionFilterCannotWidenAScope(t *testing.T) {
	s := versionFilterFixture(t)
	s.st.UpsertUser(User{Username: "client", PasswordHash: "h", Role: "user"})
	if err := s.st.SetUserRestricted("client", true); err != nil {
		t.Fatalf("SetUserRestricted: %v", err)
	}
	// Granted the manual version and nothing else. It is VisibilityGroup, so the account also has
	// to be on the report's viewer list — which is exactly how a per-report audience is expressed.
	if err := s.st.SetVersionGrants(manualVersionName, []string{userPrincipal("client")}); err != nil {
		t.Fatalf("SetVersionGrants: %v", err)
	}
	manualID := manualReportID(t, s)
	if err := s.st.AddReportViewer(manualID, "2026-09-04", "client", 0); err != nil {
		t.Fatalf("AddReportViewer: %v", err)
	}
	// The grant works: the reader can see their one version, which is what makes the refusals below
	// meaningful rather than a scope that denies everything.
	if got := homeTitles(t, homeAs(t, s, "?version=manual", "client")); len(got) != 1 {
		t.Fatalf("the granted version must be readable; got %v", got)
	}

	if got := homeTitles(t, homeAs(t, s, "?version=default", "client")); len(got) != 0 {
		t.Errorf("a version the reader was never granted must yield nothing; got %v", got)
	}
	if got := versionNames(homeAs(t, s, "", "client")); containsString(got, "default") {
		t.Errorf("the options must not name a version this reader cannot read; got %v", got)
	}
}

func versionNames(out map[string]any) []string {
	var names []string
	vs, _ := out["versions"].([]any)
	for _, v := range vs {
		if m, ok := v.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

func manualReportID(t *testing.T, s *Server) int64 {
	t.Helper()
	var id int64
	if err := s.st.queryRow(`SELECT id FROM reports WHERE version=?`, manualVersionName).Scan(&id); err != nil {
		t.Fatalf("find the manual report: %v", err)
	}
	return id
}
