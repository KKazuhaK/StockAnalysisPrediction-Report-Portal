package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quotaPtr(n int) *int { return &n }

// TestEffectiveGroupSettingsOuTree locks OU-tree resolution (ADR 0022): settings layer root→leaf
// along parent_id so a leaf overrides its ancestors, `restricted` is sticky (a restricted ancestor
// restricts the whole subtree), and daily_run_quota inherits unless overridden. Internal users in
// the (unrestricted) Default/root resolve to restricted=false with no quota — unchanged behavior.
func TestEffectiveGroupSettingsOuTree(t *testing.T) {
	st := newTestStore(t)
	root := st.EnsureDefaultGroup()

	org, err := st.CreateUserGroup("ext-org", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(org, root); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupRestricted(org, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupDailyQuota(org, quotaPtr(2), QuotaDay); err != nil {
		t.Fatal(err)
	}
	team, _ := st.CreateUserGroup("ext-team", "", 0)
	st.SetGroupParent(team, org) // inherits restricted + quota from org

	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", team)

	gs := st.EffectiveGroupSettings("ext")
	if !gs.Restricted {
		t.Error("a sub-team of a restricted org must be restricted (sticky down the tree)")
	}
	if gs.DailyRunQuota != 2 {
		t.Errorf("quota = %d, want 2 inherited from the org", gs.DailyRunQuota)
	}

	// A leaf override wins over the inherited value.
	st.SetGroupDailyQuota(team, quotaPtr(5), QuotaDay)
	if gs := st.EffectiveGroupSettings("ext"); gs.DailyRunQuota != 5 {
		t.Errorf("overridden quota = %d, want 5", gs.DailyRunQuota)
	}

	// An internal user (Default/root group) stays unrestricted with no quota — behavior unchanged.
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"})
	gs2 := st.EffectiveGroupSettings("staff")
	if gs2.Restricted {
		t.Error("a Default-group user must not be restricted")
	}
	if gs2.DailyRunQuota != 0 {
		t.Errorf("default-group quota = %d, want 0 (unlimited)", gs2.DailyRunQuota)
	}
}

// The OU tree has to be buildable through the product (ADR 0022). Every inherited behaviour —
// restricted stickiness, quota inheritance, the run allow-list's nearest-ancestor resolution and
// version grants (ADR 0024) — reads the tree, so with no way to set a parent every OU is a root and
// the whole inheritance story is decorative. SetGroupParent existed in the store with no API caller
// and no UI; these tests are that gap closed.

func ouAPIServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	s.st.EnsureDefaultGroup()
	return s
}

func postGroup(t *testing.T, s *Server, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiGroupAdd(rec, httptest.NewRequest(http.MethodPost, "/api/admin/groups", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("create group → %d %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

func putGroup(s *Server, id int64, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/admin/groups/1", strings.NewReader(body))
	r.SetPathValue("id", itoa(id))
	s.apiGroupSave(rec, r, "admin")
	return rec
}

func TestOUParentIsSettableThroughTheAPI(t *testing.T) {
	s := ouAPIServer(t)
	root := s.st.EnsureDefaultGroup()

	parent := int64(postGroup(t, s, `{"name":"客户A","restricted":true}`)["id"].(float64))
	if rec := putGroup(s, parent, `{"name":"客户A","restricted":true,"parent_id":`+itoa(root)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("setting a parent → %d %s", rec.Code, rec.Body.String())
	}
	child := int64(postGroup(t, s, `{"name":"客户A-子部门","parent_id":`+itoa(parent)+`}`)["id"].(float64))

	// Created with a parent in one step, and it took effect.
	if got := s.st.groupParent(child); got != parent {
		t.Errorf("child's parent = %d, want %d — create must accept parent_id", got, parent)
	}
	// The inherited behaviour the tree exists for: restricted is sticky downward.
	s.st.UpsertUser(User{Username: "member", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("member", child)
	if !s.st.EffectiveGroupSettings("member").Restricted {
		t.Error("a member of a child OU under a restricted parent must be restricted — this is what the tree is for")
	}
	// And the list response carries it, or the UI cannot render a tree it cannot see.
	rec := httptest.NewRecorder()
	s.apiAdminGroups(rec, httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil), "admin")
	var resp struct {
		Groups []map[string]any `json:"groups"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	for _, g := range resp.Groups {
		if int64(g["id"].(float64)) == child {
			if g["parent_id"] == nil || int64(g["parent_id"].(float64)) != parent {
				t.Errorf("list response parent_id = %v, want %d", g["parent_id"], parent)
			}
		}
	}
}

// TestOUParentRefusesACycle proves an OU cannot be moved under its own descendant. groupChain
// survives a cycle (it caps and dedupes), so this is not a hang — but an OU that is its own ancestor
// resolves settings from a ring, which no admin can reason about.
func TestOUParentRefusesACycle(t *testing.T) {
	s := ouAPIServer(t)
	a := int64(postGroup(t, s, `{"name":"A"}`)["id"].(float64))
	b := int64(postGroup(t, s, `{"name":"B","parent_id":`+itoa(a)+`}`)["id"].(float64))
	c := int64(postGroup(t, s, `{"name":"C","parent_id":`+itoa(b)+`}`)["id"].(float64))

	if rec := putGroup(s, a, `{"name":"A","parent_id":`+itoa(c)+`}`); rec.Code == http.StatusOK {
		t.Error("moving an OU under its own descendant must be refused")
	}
	if got := s.st.groupParent(a); got != 0 {
		t.Errorf("A's parent = %d after a refused move, want it unchanged (0)", got)
	}
	if rec := putGroup(s, a, `{"name":"A","parent_id":`+itoa(a)+`}`); rec.Code == http.StatusOK {
		t.Error("an OU cannot be its own parent")
	}
	// A legitimate move still works.
	if rec := putGroup(s, c, `{"name":"C","parent_id":`+itoa(a)+`}`); rec.Code != http.StatusOK {
		t.Errorf("a legitimate re-parent → %d %s", rec.Code, rec.Body.String())
	}
}

// TestOUParentOmittedLeavesItAlone proves a save that says nothing about the parent does not
// silently orphan the OU — every existing caller of this endpoint omits the field.
func TestOUParentOmittedLeavesItAlone(t *testing.T) {
	s := ouAPIServer(t)
	root := s.st.EnsureDefaultGroup()
	g := int64(postGroup(t, s, `{"name":"X","parent_id":`+itoa(root)+`}`)["id"].(float64))
	if rec := putGroup(s, g, `{"name":"X renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename → %d", rec.Code)
	}
	if got := s.st.groupParent(g); got != root {
		t.Errorf("parent = %d after a save that omitted it, want it untouched (%d)", got, root)
	}
	// An explicit 0 detaches it, which is how the UI offers "no parent".
	if rec := putGroup(s, g, `{"name":"X renamed","parent_id":0}`); rec.Code != http.StatusOK {
		t.Fatalf("detach → %d", rec.Code)
	}
	if got := s.st.groupParent(g); got != 0 {
		t.Errorf("parent = %d after an explicit 0, want detached", got)
	}
}
