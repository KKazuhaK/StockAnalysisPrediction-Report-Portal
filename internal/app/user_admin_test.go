package app

import (
	"testing"
)

func mkUser(t *testing.T, st *Store, name, role string) {
	t.Helper()
	if err := st.UpsertUser(User{Username: name, PasswordHash: "h", Role: role}); err != nil {
		t.Fatalf("UpsertUser %s: %v", name, err)
	}
}

// A user with no profile row reads as enabled with an empty display name; setting
// the profile, active flag, and last login all round-trip via the join.
func TestUserProfileDefaultsAndRoundTrip(t *testing.T) {
	st := newTestStore(t)
	mkUser(t, st, "alice", "user")

	u := st.GetUser("alice")
	if u == nil || !u.Active || u.DisplayName != "" || u.Name() != "alice" {
		t.Fatalf("default profile = %+v (Name=%q), want enabled/empty/alice", u, u.Name())
	}

	if err := st.SetUserProfile("alice", "Alice Anderson", "alice@x.com"); err != nil {
		t.Fatalf("SetUserProfile: %v", err)
	}
	if err := st.SetUserActive("alice", false); err != nil {
		t.Fatalf("SetUserActive: %v", err)
	}
	if err := st.TouchLastLogin("alice"); err != nil {
		t.Fatalf("TouchLastLogin: %v", err)
	}
	u = st.GetUser("alice")
	if u.DisplayName != "Alice Anderson" || u.Email != "alice@x.com" || u.Active || u.LastLogin == "" {
		t.Fatalf("after updates = %+v, want name/email set, disabled, last_login set", u)
	}
	if u.Name() != "Alice Anderson" {
		t.Fatalf("Name() = %q, want the display name", u.Name())
	}

	// The profile updates are independent: re-enabling must not wipe name/email.
	st.SetUserActive("alice", true)
	if u = st.GetUser("alice"); !u.Active || u.DisplayName != "Alice Anderson" {
		t.Fatalf("re-enable clobbered profile: %+v", u)
	}
}

func TestUserGroupsCRUDAndMembership(t *testing.T) {
	st := newTestStore(t)
	mkUser(t, st, "alice", "user")
	mkUser(t, st, "bob", "operator")

	// The Default group is bootstrapped and can't be deleted.
	def := st.DefaultGroupID()
	if def == 0 {
		t.Fatal("Default group was not bootstrapped")
	}
	if err := st.DeleteUserGroup(def); err == nil {
		t.Fatal("the Default group must not be deletable")
	}

	gid, err := st.CreateUserGroup("Research", "The research desk", 0)
	if err != nil {
		t.Fatalf("CreateUserGroup: %v", err)
	}
	gid2, _ := st.CreateUserGroup("Ops", "", 0)

	// Primary group: alice → Research, bob → Ops.
	if err := st.SetPrimaryGroup("alice", gid); err != nil {
		t.Fatalf("SetPrimaryGroup: %v", err)
	}
	st.SetPrimaryGroup("bob", gid2)

	if got := st.PrimaryGroupOf("alice"); got != gid {
		t.Fatalf("PrimaryGroupOf(alice) = %d, want %d", got, gid)
	}
	all := st.AllPrimaryGroups()
	if all["alice"] != gid || all["bob"] != gid2 {
		t.Fatalf("AllPrimaryGroups = %v", all)
	}

	// Member counts reflect primary membership.
	counts := map[string]int{}
	for _, g := range st.ListUserGroups() {
		counts[g.Name] = g.Members
	}
	if counts["Research"] != 1 || counts["Ops"] != 1 {
		t.Fatalf("member counts = %v, want Research:1 Ops:1", counts)
	}

	// The Default group is never marked inherit (it is the baseline).
	for _, g := range st.ListUserGroups() {
		if g.IsDefault && (g.WeightInherit || g.UrgentInherit) {
			t.Fatal("the Default group must never be marked inherit")
		}
	}

	// Override urgent-unlimited on Research (concrete shim), then confirm it sticks.
	if err := st.UpdateUserGroup(gid, "Research", "The research desk", 0, true); err != nil {
		t.Fatalf("UpdateUserGroup urgent unlimited: %v", err)
	}
	if !st.UserUrgentUnlimited("alice") {
		t.Fatal("override did not grant alice unlimited urgent")
	}

	// Inherit resolution: make Ops inherit (NULL overrides), raise the Default group's
	// weight, and a member of the inheriting group picks up the Default baseline.
	st.UpdateGroup(gid2, "Ops", "", nil, nil)
	for _, g := range st.ListUserGroups() {
		if g.Name == "Ops" && (!g.WeightInherit || !g.UrgentInherit) {
			t.Fatalf("Ops should inherit after NULL overrides: %+v", g)
		}
	}
	defFalse := false
	st.UpdateGroup(def, "Default", "", ptrInt(5), &defFalse)
	if a := st.UserTicketAllocation("bob"); a != 5 { // bob's Ops group inherits
		t.Fatalf("inheriting member allocation = %d, want 5 (Default baseline)", a)
	}

	// Switching primary is a replace (single-valued).
	st.SetPrimaryGroup("alice", gid2)
	if got := st.PrimaryGroupOf("alice"); got != gid2 {
		t.Fatalf("after switch PrimaryGroupOf(alice) = %d, want %d", got, gid2)
	}

	// Deleting a group drops its primary pointers (members fall back to Default).
	st.DeleteUserGroup(gid2)
	if got := st.PrimaryGroupOf("bob"); got != 0 {
		t.Fatalf("bob still points at a deleted group: %d", got)
	}

	// Deleting a user cleans up profile + primary group.
	st.SetPrimaryGroup("alice", gid)
	st.SetUserProfile("alice", "A", "a@x")
	st.DeleteUser("alice")
	if st.PrimaryGroupOf("alice") != 0 {
		t.Fatal("deleted user's primary group survived")
	}
	if u := st.GetUser("alice"); u != nil {
		t.Fatal("deleted user still present")
	}
}

func ptrInt(v int) *int { return &v }

// The Default group is created once and reused; a concrete override on a user's
// primary group beats the Default baseline, and clearing the primary falls back.
func TestGroupModelResolution(t *testing.T) {
	st := newTestStore(t)
	mkUser(t, st, "u", "user")

	def := st.DefaultGroupID()
	if def == 0 || st.EnsureDefaultGroup() != def {
		t.Fatal("EnsureDefaultGroup is not idempotent")
	}
	defaults := 0
	for _, g := range st.ListUserGroups() {
		if g.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("want exactly one Default group, got %d", defaults)
	}

	// Default baseline weight 4, urgent off.
	off := false
	st.UpdateGroup(def, "Default", "", ptrInt(4), &off)

	// No primary group → inherit the Default baseline.
	if w := st.UserTicketAllocation("u"); w != 4 {
		t.Fatalf("no-primary allocation = %d, want 4 (Default)", w)
	}

	// Primary group overrides weight to 9 and grants unlimited urgent.
	on := true
	g, _ := st.CreateUserGroup("VIP", "", 9)
	st.UpdateGroup(g, "VIP", "", ptrInt(9), &on)
	st.SetPrimaryGroup("u", g)
	if w := st.UserTicketAllocation("u"); w != 9 {
		t.Fatalf("override allocation = %d, want 9", w)
	}
	if !st.UserUrgentUnlimited("u") {
		t.Fatal("override did not grant unlimited urgent")
	}

	// Clearing the primary group falls back to the Default baseline again.
	st.SetPrimaryGroup("u", 0)
	if w := st.UserTicketAllocation("u"); w != 4 || st.UserUrgentUnlimited("u") {
		t.Fatalf("after clear = (%d, %v), want (4, false)", st.UserTicketAllocation("u"), st.UserUrgentUnlimited("u"))
	}

	// A non-existent group id is not stored (it would dangle); the user inherits Default.
	if err := st.SetPrimaryGroup("u", 999999); err != nil {
		t.Fatalf("SetPrimaryGroup(bad id): %v", err)
	}
	if got := st.PrimaryGroupOf("u"); got != 0 {
		t.Fatalf("stale primary id was stored: %d, want 0", got)
	}

	// Deleting a group also clears its primary members; a stray duplicate default (if one
	// ever existed) is likewise protected — the guard checks the row's own is_default flag.
	st.SetPrimaryGroup("u", g)
	if err := st.DeleteUserGroup(g); err != nil {
		t.Fatalf("DeleteUserGroup(VIP): %v", err)
	}
	if got := st.PrimaryGroupOf("u"); got != 0 {
		t.Fatalf("primary pointer to a deleted group survived: %d", got)
	}
}
