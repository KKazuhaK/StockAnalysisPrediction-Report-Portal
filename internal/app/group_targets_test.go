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

// ouFixture builds root → restricted OU (org) → its sub-team, plus a restricted user in the team,
// an internal user, and two targets. It is the shared setup for the P4 allow-list tests.
func ouFixture(t *testing.T) (*Server, int64, int64, int64, int64) {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	root := st.EnsureDefaultGroup()
	org, _ := st.CreateUserGroup("ext-org", "", 0)
	st.SetGroupParent(org, root)
	st.SetGroupRestricted(org, true)
	team, _ := st.CreateUserGroup("ext-team", "", 0)
	st.SetGroupParent(team, org)

	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "operator"})
	st.SetPrimaryGroup("ext", team)
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "operator"})

	st.UpsertPlugin("p", "P", "1.0.0", batchTestSpec, "test")
	tgtA, _ := st.CreateTarget("p", "A", "{}")
	tgtB, _ := st.CreateTarget("p", "B", "{}")
	return s, org, team, tgtA, tgtB
}

// TestGroupTargetsCRUD locks the allow-list store: rows round-trip per OU with their surface
// subset, and replacing an OU's list is atomic.
func TestGroupTargetsCRUD(t *testing.T) {
	s, org, _, tgtA, tgtB := ouFixture(t)
	st := s.st

	if got := st.GroupTargets(org); len(got) != 0 {
		t.Fatalf("a fresh OU must have no allow-list rows, got %v", got)
	}
	if err := st.SetGroupTargets(org, []GroupTarget{
		{TargetID: tgtA, Surfaces: "run"},
		{TargetID: tgtB, Surfaces: ""},
	}); err != nil {
		t.Fatal(err)
	}
	got := st.GroupTargets(org)
	if len(got) != 2 {
		t.Fatalf("GroupTargets = %d rows, want 2", len(got))
	}
	bySurface := map[int64]string{}
	for _, g := range got {
		bySurface[g.TargetID] = g.Surfaces
	}
	if bySurface[tgtA] != "run" || bySurface[tgtB] != "" {
		t.Errorf("round-trip = %v, want A=run and B='' (inherit the target's own surfaces)", bySurface)
	}
	// Replacing is atomic: the previous rows are gone.
	if err := st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtB, Surfaces: "batch"}}); err != nil {
		t.Fatal(err)
	}
	got = st.GroupTargets(org)
	if len(got) != 1 || got[0].TargetID != tgtB || got[0].Surfaces != "batch" {
		t.Errorf("after replace = %v, want only B=batch", got)
	}
}

// TestRunAllowedResolvesUpTheOuTree is the enforcement contract: a restricted member is default-deny,
// inherits the nearest ancestor OU's allow-list, and is bounded by the surface subset; internal users
// and admins are unaffected.
func TestRunAllowedResolvesUpTheOuTree(t *testing.T) {
	s, org, team, tgtA, tgtB := ouFixture(t)
	st := s.st

	// Default-deny: a restricted OU with no allow-list anywhere may run nothing.
	if s.runAllowed("ext", tgtA, SurfaceRun) {
		t.Error("a restricted member with an empty allow-list must be denied (default-deny)")
	}
	// Internal users and admins ignore the allow-list entirely.
	if !s.runAllowed("staff", tgtA, SurfaceRun) {
		t.Error("an internal user must not be gated by the allow-list")
	}

	// Grant on the ANCESTOR org: the sub-team inherits it.
	st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: "run"}})
	if !s.runAllowed("ext", tgtA, SurfaceRun) {
		t.Error("a sub-team must inherit its parent OU's allow-list")
	}
	if s.runAllowed("ext", tgtA, SurfaceBatch) {
		t.Error("a surface outside the granted subset must be denied")
	}
	if s.runAllowed("ext", tgtB, SurfaceRun) {
		t.Error("a target absent from the allow-list must be denied")
	}

	// A nearer OU's own list overrides the ancestor's entirely.
	st.SetGroupTargets(team, []GroupTarget{{TargetID: tgtB, Surfaces: ""}})
	if s.runAllowed("ext", tgtA, SurfaceRun) {
		t.Error("the nearest OU's allow-list must override the ancestor's (A no longer granted)")
	}
	if !s.runAllowed("ext", tgtB, SurfaceRun) || !s.runAllowed("ext", tgtB, SurfaceChat) {
		t.Error("an empty surface subset means every surface the target itself allows")
	}
}

// TestApiBatchJobCreateEnforcesAllowList proves the allow-list is enforced SERVER-side at submit —
// the gate that defeats a crafted request for a workflow the UI never offered — and that a granted
// target on a non-granted surface is refused too.
func TestApiBatchJobCreateEnforcesAllowList(t *testing.T) {
	s, org, _, tgtA, tgtB := ouFixture(t)
	st := s.st
	st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: "run"}})

	submit := func(user string, target int64, surface string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"target_id":%d,"surface":%q,"rows":[{"code":"600000"}]}`, target, surface)
		rec := httptest.NewRecorder()
		s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/api/admin/batch/jobs", strings.NewReader(body)), user)
		return rec
	}

	if rec := submit("ext", tgtA, SurfaceRun); rec.Code != http.StatusOK {
		t.Errorf("granted target+surface → %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if rec := submit("ext", tgtB, SurfaceRun); rec.Code != http.StatusForbidden {
		t.Errorf("target outside the allow-list → %d, want 403 (crafted-request bypass)", rec.Code)
	}
	if rec := submit("ext", tgtA, SurfaceBatch); rec.Code != http.StatusForbidden {
		t.Errorf("granted target on a non-granted surface → %d, want 403", rec.Code)
	}
	if rec := submit("staff", tgtB, SurfaceRun); rec.Code != http.StatusOK {
		t.Errorf("internal user → %d (%s), want 200 (never gated)", rec.Code, rec.Body.String())
	}
}

// TestApiBatchTargetsFiltersForRestricted proves the run form is only offered what the submit gate
// would accept: a restricted OU sees just its allow-listed targets, with surfaces intersected.
func TestApiBatchTargetsFiltersForRestricted(t *testing.T) {
	s, org, _, tgtA, _ := ouFixture(t)
	s.st.SetGroupTargets(org, []GroupTarget{{TargetID: tgtA, Surfaces: "run"}})

	list := func(user string) []map[string]any {
		rec := httptest.NewRecorder()
		s.apiBatchTargets(rec, httptest.NewRequest("GET", "/api/admin/batch/targets", nil), user)
		var resp struct {
			Targets []map[string]any `json:"targets"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp.Targets
	}

	got := list("ext")
	if len(got) != 1 {
		t.Fatalf("restricted target list = %d entries, want 1 (only the allow-listed one)", len(got))
	}
	if surfaces, _ := got[0]["surfaces"].([]any); len(surfaces) != 1 || surfaces[0] != SurfaceRun {
		t.Errorf("surfaces = %v, want [run] (intersected with the grant)", got[0]["surfaces"])
	}
	if n := len(list("staff")); n != 2 {
		t.Errorf("internal target list = %d, want 2 (unfiltered)", n)
	}
}
