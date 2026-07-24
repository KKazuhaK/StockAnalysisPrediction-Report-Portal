package app

import "testing"

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
	if err := st.SetGroupDailyQuota(org, quotaPtr(2)); err != nil {
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
	st.SetGroupDailyQuota(team, quotaPtr(5))
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
