package app

import (
	"testing"
)

// Version grants (ADR 0024): who may READ a version. Default-deny, decoupled from who may RUN
// anything, and addressable at an account as well as at an OU — a portal with no OU tree configured
// has to be a first-class case, not a workaround.

func grantFixture(t *testing.T) (s *Server, root, parent, child int64) {
	t.Helper()
	s = tenancyServer(t)
	root = s.st.EnsureDefaultGroup()
	parent, _ = s.st.CreateUserGroup("clients", "", 0)
	s.st.SetGroupParent(parent, root)
	s.st.SetGroupRestricted(parent, true)
	child, _ = s.st.CreateUserGroup("client-A", "", 0)
	s.st.SetGroupParent(child, parent)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	s.st.SaveVersion(ReportVersion{Name: "客户版", Ord: 2, Visibility: VisibilityOwner})
	return s, root, parent, child
}

// TestGrantsAreDefaultDeny proves an account nobody configured reads nothing. The half-configured
// state is exactly when a disclosure rule must be closed, and it is exactly when the previous
// design was open: a restricted OU with no run allow-list saw every internal report from today.
func TestGrantsAreDefaultDeny(t *testing.T) {
	s, _, _, child := grantFixture(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("alice", child)

	if got := s.st.GrantedVersions("alice"); len(got) != 0 {
		t.Errorf("an unconfigured account is granted %v, want nothing", got)
	}
}

// TestGrantsInheritDownTheOUTree proves a sub-OU does not have to be configured separately —
// otherwise every new client sub-OU starts invisible, and the failure looks to an admin like the
// portal is broken rather than like a missing tick box.
func TestGrantsInheritDownTheOUTree(t *testing.T) {
	s, root, parent, child := grantFixture(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("alice", child)

	s.st.SetVersionGrants("对外版", []string{groupPrincipal(parent)})
	if got := s.st.GrantedVersions("alice"); len(got) != 1 || got[0] != "对外版" {
		t.Errorf("child OU granted %v, want the parent's [对外版]", got)
	}

	// NEAREST wins, not union. In this project's tree the external OUs hang off the Default OU, so a
	// union would push whatever the root was granted down into every tenant — the internal version
	// included. Nearest-wins means configuring a sub-OU is how you narrow it.
	s.st.SetVersionGrants("客户版", []string{groupPrincipal(child)})
	got := s.st.GrantedVersions("alice")
	if len(got) != 1 || got[0] != "客户版" {
		t.Errorf("granted %v, want only the child's own [客户版] — a nearer grant replaces an ancestor's", got)
	}
	// And a grant on the root does not reach a tenant that has its own configuration.
	s.st.SetVersionGrants(s.st.DefaultVersion(), []string{groupPrincipal(root)})
	if got := s.st.GrantedVersions("alice"); contains(got, s.st.DefaultVersion()) {
		t.Errorf("granted %v — a root-OU grant must not flow into a configured tenant", got)
	}
}

// TestAccountGrantsWorkWithoutAnyOU is the case the principal column exists for: an external person
// with no OU tree set up at all.
func TestAccountGrantsWorkWithoutAnyOU(t *testing.T) {
	s := tenancyServer(t)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1})
	s.st.UpsertUser(User{Username: "solo", PasswordHash: "h", Role: "user"})

	if got := s.st.GrantedVersions("solo"); len(got) != 0 {
		t.Fatalf("before configuration: %v, want nothing", got)
	}
	s.st.SetVersionGrants("对外版", []string{userPrincipal("solo")})
	if got := s.st.GrantedVersions("solo"); len(got) != 1 || got[0] != "对外版" {
		t.Errorf("granted %v, want [对外版] with no OU involved at all", got)
	}
	// An account grant takes precedence over the OU chain, so a single person can be given something
	// their OU is not, and can be pinned to less than their OU has.
	root := s.st.EnsureDefaultGroup()
	s.st.SetPrimaryGroup("solo", root)
	s.st.SaveVersion(ReportVersion{Name: "内部版", Ord: 0})
	s.st.SetVersionGrants("内部版", []string{groupPrincipal(root)})
	if got := s.st.GrantedVersions("solo"); len(got) != 1 || got[0] != "对外版" {
		t.Errorf("granted %v, want the account's own grant to win over the OU's", got)
	}
}

// TestAccountLevelRestrictedNeedsNoOU proves the scoping switch can be thrown on one account. Until
// now "restricted" was purely an OU property, so a portal that never built an OU tree had no way to
// scope anybody — which is the setup an external user is most likely to arrive into first.
func TestAccountLevelRestrictedNeedsNoOU(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "solo", PasswordHash: "h", Role: "user"})
	if s.isRestricted("solo") {
		t.Fatal("an ordinary account is not restricted")
	}
	if err := s.st.SetUserRestricted("solo", true); err != nil {
		t.Fatal(err)
	}
	if !s.isRestricted("solo") {
		t.Error("an account marked restricted must be scoped, with no OU configured")
	}
	if u := s.st.GetUser("solo"); u == nil || !u.Restricted {
		t.Error("the flag must round-trip on the user row")
	}
	// The OU flag still works, and the two OR together: an OU-restricted member cannot be
	// un-restricted by leaving their own account flag off.
	ext, _ := s.st.CreateUserGroup("ext", "", 0)
	s.st.SetGroupParent(ext, s.st.EnsureDefaultGroup())
	s.st.SetGroupRestricted(ext, true)
	s.st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("bob", ext)
	if !s.isRestricted("bob") {
		t.Error("an OU-restricted member must still be scoped")
	}
	// An admin is never scoped — otherwise nobody can diagnose a tenancy problem.
	s.st.UpsertUser(User{Username: "root", PasswordHash: "h", Role: "admin"})
	s.st.SetUserRestricted("root", true)
	if s.isRestricted("root") {
		t.Error("an admin must not be scoped")
	}
}
