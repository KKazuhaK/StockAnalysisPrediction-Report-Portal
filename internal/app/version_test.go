package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Report versions (ADR 0024). One analysis is published in several written forms — one carrying the
// scoring table and the weights, another carrying only the conclusion — and each form is produced by
// its OWN run. A report therefore exists in whichever versions have actually been generated, and a
// missing version is a normal state rather than an error.
//
// Version is a column on the report, not a second report type. The two are orthogonal dimensions:
// encoding one inside the other costs types × versions rows in the registry and degrades exactly as
// versions multiply, which is the direction this is expected to grow.
//
// These tests also pin the reason the feature exists. Same-day reuse (ADR 0022 R1) hands a
// restricted OU any NULL-owner report generated today, and every internal report is NULL-owner, so
// before versions an entitled external user read the internal analysis verbatim.

// TestVersionSeparatesReportsThatWouldHaveCollided is the identity contract. Two versions of one
// report can share a symbol, a date, a subtype and even a title, so without version in the identity
// tuple the second ingest silently OVERWRITES the first — publishing the external version would
// destroy the internal one.
func TestVersionSeparatesReportsThatWouldHaveCollided(t *testing.T) {
	s := tenancyServer(t)
	ingest := func(version, body string) int64 {
		id, _, err := s.st.UpsertReport(Rep{
			Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
			Title: "投资决策 600519", Version: version, MD: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	full := ingest("", "scoring table, weights, prompt notes")
	pub := ingest("对外版", "score 78/100, hold")

	if full == pub {
		t.Fatal("two versions of one report must be two rows, not one overwriting the other")
	}
	if r, _ := s.st.GetNew(full, nil); r == nil || r.MD != "scoring table, weights, prompt notes" {
		t.Error("publishing the external version must not disturb the internal one")
	}
	// Re-ingesting the same (identity, version) still overwrites in place, as it always has.
	if again := ingest("对外版", "score 80/100, buy"); again != pub {
		t.Errorf("re-ingesting one version must overwrite its own row, got %d want %d", again, pub)
	}
	if r, _ := s.st.GetNew(pub, nil); r == nil || r.MD != "score 80/100, buy" {
		t.Error("the re-ingest must have replaced the body")
	}
}

// TestVersionlessIngestIsStable is the migration safety argument, asserted rather than reasoned
// about. Every row that predates versions resolves to the SAME version, so
// (symbol,date,rtype,title,default) is unique exactly when (symbol,date,rtype,title) was —
// rebuilding the unique index can neither merge two reports nor fork one, which is the failure mode
// that cost 626 reports in v0.3.0. A producer that never heard of versions keeps overwriting in
// place, as it does today.
func TestVersionlessIngestIsStable(t *testing.T) {
	s := tenancyServer(t)
	base := Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策", Title: "T"}
	first := base
	first.MD = "first"
	id, created, err := s.st.UpsertReport(first)
	if err != nil || !created {
		t.Fatalf("first ingest: id=%d created=%v err=%v", id, created, err)
	}
	second := base
	second.MD = "second"
	again, created, err := s.st.UpsertReport(second)
	if err != nil || created || again != id {
		t.Errorf("a version-less re-ingest must overwrite the same row: id=%d created=%v err=%v", again, created, err)
	}
	// It is stored as the default version rather than as an empty string, so every downstream
	// filter, grant and switcher speaks one vocabulary with no "" special case to forget.
	if r, _ := s.st.GetNew(id, nil); r == nil || r.Version != s.st.DefaultVersion() {
		t.Errorf("version = %q, want the default %q", r.Version, s.st.DefaultVersion())
	}
	// And naming the default explicitly is the same row, not a second one.
	explicit := base
	explicit.Version, explicit.MD = s.st.DefaultVersion(), "third"
	if third, _, _ := s.st.UpsertReport(explicit); third != id {
		t.Errorf("naming the default version explicitly must hit the same row, got %d want %d", third, id)
	}
}

// TestVersionRegistryIsAdminManaged proves versions are registered names rather than free-form
// strings invented by whichever workflow ran, and that the default cannot be removed out from under
// the reports resolving to it.
func TestVersionRegistryIsAdminManaged(t *testing.T) {
	s := tenancyServer(t)
	if def := s.st.DefaultVersion(); def == "" {
		t.Fatal("a default version must exist on a fresh database, or a version-less ingest has nowhere to land")
	}
	if err := s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner}); err != nil {
		t.Fatal(err)
	}
	got := map[string]ReportVersion{}
	for _, v := range s.st.Versions() {
		got[v.Name] = v
	}
	if _, ok := got["对外版"]; !ok {
		t.Fatalf("registry = %v, want the registered version", got)
	}
	if got["对外版"].Visibility != VisibilityOwner {
		t.Errorf("visibility = %q, want it stored", got["对外版"].Visibility)
	}
	if err := s.st.DeleteVersion(s.st.DefaultVersion()); err == nil {
		t.Error("deleting the default version must be refused — every version-less report resolves to it")
	}
	if err := s.st.DeleteVersion("对外版"); err != nil {
		t.Errorf("deleting a registered version must work: %v", err)
	}
}

// TestNewVersionDefaultsToTheNarrowestVisibility proves a version created without anyone thinking
// about disclosure starts closed. The failure direction of a forgotten setting has to be "too few
// people see it".
func TestNewVersionDefaultsToTheNarrowestVisibility(t *testing.T) {
	s := tenancyServer(t)
	if err := s.st.SaveVersion(ReportVersion{Name: "客户版", Ord: 2}); err != nil {
		t.Fatal(err)
	}
	for _, v := range s.st.Versions() {
		if v.Name == "客户版" && v.Visibility != VisibilityOwner {
			t.Errorf("a version saved with no visibility set = %q, want the narrowest (%q)",
				v.Visibility, VisibilityOwner)
		}
	}
}

// TestV1IngestAcceptsVersion is the producer's side of the contract: a workflow declares which
// written form it just generated. Omitting it stays exactly what it means today — the default
// version — so every existing workflow keeps working untouched.
func TestV1IngestAcceptsVersion(t *testing.T) {
	s := newV1Server(t)
	post := func(body string) map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		if m["ok"] != true {
			t.Fatalf("ingest failed: %s", rec.Body.String())
		}
		return m
	}
	const base = `{"symbol":"600519","date":"2026-07-28","subtype":"投资决策","title":"投资决策 600519"`
	internal := int64(post(base + `,"body_md":"scoring table"}`)["id"].(float64))
	public := int64(post(base + `,"version":"对外版","body_md":"conclusion only"}`)["id"].(float64))

	if internal == public {
		t.Fatal("a second version must be its own report, not an overwrite of the first")
	}
	got, _ := s.st.GetNew(internal, nil)
	if got == nil || got.Version != s.st.DefaultVersion() {
		t.Errorf("a version-less ingest = %q, want the default", got.Version)
	}
	if got.MD != "scoring table" {
		t.Errorf("publishing the second version disturbed the first: %q", got.MD)
	}
	if got, _ = s.st.GetNew(public, nil); got == nil || got.Version != "对外版" {
		t.Errorf("declared version = %q, want 对外版", got.Version)
	}
	// A version nobody registered is registered on sight, so a workflow's output is never lost
	// behind a 400 nobody is watching — and it is granted to nobody, so it discloses nothing.
	if v, ok := s.st.Version("对外版"); !ok {
		t.Error("an unregistered version must be registered on sight")
	} else if v.Visibility != VisibilityOwner {
		t.Errorf("auto-registered visibility = %q, want the narrowest", v.Visibility)
	}
	// The report a reader gets back over the API carries its version, so the UI can label it.
	req := httptest.NewRequest("GET", "/api/v1/reports/"+fmt.Sprint(public), nil)
	req.Header.Set("Authorization", "Bearer tok-all")
	req.SetPathValue("id", fmt.Sprint(public))
	rec := httptest.NewRecorder()
	s.v1GetReport(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["version"] != "对外版" {
		t.Errorf("v1 read response version = %v, want 对外版 so a consumer can label what it got", out["version"])
	}
}
