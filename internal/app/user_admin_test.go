package app

import (
	"reflect"
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

	gid, err := st.CreateUserGroup("Research", "The research desk", 0)
	if err != nil {
		t.Fatalf("CreateUserGroup: %v", err)
	}
	gid2, _ := st.CreateUserGroup("Ops", "", 0)

	// Membership: alice in both groups, bob in Ops only.
	if err := st.SetUserGroups("alice", []int64{gid, gid2}); err != nil {
		t.Fatalf("SetUserGroups: %v", err)
	}
	st.SetUserGroups("bob", []int64{gid2})

	if got := st.GroupsOf("alice"); !reflect.DeepEqual(got, []int64{gid, gid2}) {
		t.Fatalf("GroupsOf(alice) = %v, want [%d %d]", got, gid, gid2)
	}
	all := st.AllUserGroups()
	if len(all["alice"]) != 2 || len(all["bob"]) != 1 {
		t.Fatalf("AllUserGroups = %v", all)
	}

	// Member counts.
	groups := st.ListUserGroups()
	counts := map[string]int{}
	urgentFree := map[string]bool{}
	for _, g := range groups {
		counts[g.Name] = g.Members
		urgentFree[g.Name] = g.UrgentFree
	}
	if counts["Research"] != 1 || counts["Ops"] != 2 {
		t.Fatalf("member counts = %v, want Research:1 Ops:2", counts)
	}
	if urgentFree["Research"] || urgentFree["Ops"] {
		t.Fatalf("new groups should not be unlimited by default: %v", urgentFree)
	}

	if err := st.UpdateUserGroup(gid, "Research", "The research desk", 0, true); err != nil {
		t.Fatalf("UpdateUserGroup urgent unlimited: %v", err)
	}
	groups = st.ListUserGroups()
	urgentFree = map[string]bool{}
	for _, g := range groups {
		urgentFree[g.Name] = g.UrgentFree
	}
	if !urgentFree["Research"] {
		t.Fatalf("updated group did not persist urgent unlimited: %v", urgentFree)
	}

	// Re-assigning replaces (not appends).
	st.SetUserGroups("alice", []int64{gid})
	if got := st.GroupsOf("alice"); !reflect.DeepEqual(got, []int64{gid}) {
		t.Fatalf("after replace GroupsOf(alice) = %v, want [%d]", got, gid)
	}

	// Deleting a group drops its memberships.
	st.DeleteUserGroup(gid2)
	if got := st.GroupsOf("bob"); len(got) != 0 {
		t.Fatalf("bob still in a deleted group: %v", got)
	}

	// Deleting a user cleans up profile + memberships.
	st.SetUserProfile("alice", "A", "a@x")
	st.DeleteUser("alice")
	if len(st.GroupsOf("alice")) != 0 {
		t.Fatal("deleted user's memberships survived")
	}
	if u := st.GetUser("alice"); u != nil {
		t.Fatal("deleted user still present")
	}
}
