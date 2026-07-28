package app

import (
	"testing"
)

// The read rule, whole (ADR 0024). A scoped reader may see a report when BOTH hold:
//
//	the report's version is granted to them, AND
//	the version's visibility admits them — they asked for it (owner), someone in their OU did
//	(group), or it does not matter (all).
//
// Everything else that used to decide this is gone. There is no same-day internal pool, no
// narrowing by which subtypes you may run, and no second ownership mechanism: report_viewers is the
// only table the read path consults, because the security-critical filter must not have two
// spellings. What you may RUN no longer implies anything about what you may READ.

type readFixture struct {
	s              *Server
	ou, mate       int64
	internal, pub  int64 // report ids
	mateReport     int64
	strangerReport int64
}

// seedReadFixture builds the shape the whole feature exists for: an internal report generated today
// with no owner, plus reports of a published version asked for by three different people.
func seedReadFixture(t *testing.T, visibility Visibility) readFixture {
	t.Helper()
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("client-A", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	other, _ := s.st.CreateUserGroup("client-B", "", 0)
	s.st.SetGroupParent(other, root)
	s.st.SetGroupRestricted(other, true)

	for _, u := range []struct {
		name string
		g    int64
	}{{"alice", ou}, {"colleague", ou}, {"stranger", other}} {
		s.st.UpsertUser(User{Username: u.name, PasswordHash: "h", Role: "user"})
		s.st.SetPrimaryGroup(u.name, u.g)
	}
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: visibility})

	today := s.panelToday()
	mk := func(version, title, asker string, askerOU int64) int64 {
		id, _, err := s.st.UpsertReport(Rep{Symbol: "600519", Date: today, RType: "投资决策",
			Title: title, Version: version, MD: "body of " + title})
		if err != nil {
			t.Fatal(err)
		}
		if asker != "" {
			if err := s.st.AddReportViewer(id, today, asker, askerOU); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	f := readFixture{s: s, ou: ou, mate: other}
	// The internal analysis: no owner, generated today. This is the row that used to be handed to
	// an entitled external reader verbatim.
	f.internal = mk(s.st.DefaultVersion(), "内部投资决策", "", 0)
	f.pub = mk("对外版", "对外投资决策 alice", "alice", ou)
	f.mateReport = mk("对外版", "对外投资决策 colleague", "colleague", ou)
	f.strangerReport = mk("对外版", "对外投资决策 stranger", "stranger", other)
	s.st.SetVersionGrants("对外版", []string{groupPrincipal(ou), groupPrincipal(other)})
	return f
}

func (f readFixture) canRead(t *testing.T, user string, id int64) bool {
	t.Helper()
	r, err := f.s.st.GetNew(id, f.s.viewerScope(user))
	if err != nil {
		t.Fatal(err)
	}
	return r != nil
}

// TestVisibilityOwnerSeesOnlyWhatTheyAskedFor is the narrowest mode, and the default for a new
// version.
func TestVisibilityOwnerSeesOnlyWhatTheyAskedFor(t *testing.T) {
	f := seedReadFixture(t, VisibilityOwner)
	if !f.canRead(t, "alice", f.pub) {
		t.Error("alice must read the report she asked for")
	}
	if f.canRead(t, "alice", f.mateReport) {
		t.Error("owner visibility must not show a colleague's report")
	}
	if f.canRead(t, "alice", f.strangerReport) {
		t.Error("another tenant's report must never be visible")
	}
	// The leak this feature exists to close: an internal report, generated today, no owner.
	if f.canRead(t, "alice", f.internal) {
		t.Error("the internal analysis must not be readable — no grant covers its version")
	}
}

// TestVisibilityGroupSharesWithinTheOU covers the company-client case, where colleagues are expected
// to see each other's requests but other tenants are not.
func TestVisibilityGroupSharesWithinTheOU(t *testing.T) {
	f := seedReadFixture(t, VisibilityGroup)
	if !f.canRead(t, "alice", f.pub) || !f.canRead(t, "alice", f.mateReport) {
		t.Error("group visibility must share within the OU")
	}
	if f.canRead(t, "alice", f.strangerReport) {
		t.Error("group visibility must not cross into another OU")
	}
	if f.canRead(t, "alice", f.internal) {
		t.Error("an ungranted version stays invisible whatever the visibility mode")
	}
}

// TestVisibilityAllIsABrowsableLibrary covers the widest mode: every report of the version, whoever
// asked — a client onboarded today sees the back catalogue.
func TestVisibilityAllIsABrowsableLibrary(t *testing.T) {
	f := seedReadFixture(t, VisibilityAll)
	for _, id := range []int64{f.pub, f.mateReport, f.strangerReport} {
		if !f.canRead(t, "alice", id) {
			t.Errorf("visibility=all must show report %d whoever asked for it", id)
		}
	}
	if f.canRead(t, "alice", f.internal) {
		t.Error("visibility=all widens WHOSE reports, never WHICH versions")
	}
}

// TestUngrantedAccountReadsNothing proves default-deny end to end: being restricted with no grant is
// not "see the internal pool", it is "see nothing".
func TestUngrantedAccountReadsNothing(t *testing.T) {
	f := seedReadFixture(t, VisibilityAll)
	f.s.st.SetVersionGrants("对外版", nil) // revoke
	for _, id := range []int64{f.pub, f.internal, f.mateReport} {
		if f.canRead(t, "alice", id) {
			t.Errorf("report %d readable with no grant", id)
		}
	}
	reps, _ := f.s.st.SearchNew(Filters{}, f.s.viewerScope("alice"))
	if len(reps) != 0 {
		t.Errorf("listing returned %d rows with no grant", len(reps))
	}
}

// TestInternalReadersAreUnaffected is the property that keeps this deployable: an unrestricted user
// gets a nil scope, so their SQL is byte-for-byte what it was and the money path does not change.
func TestInternalReadersAreUnaffected(t *testing.T) {
	f := seedReadFixture(t, VisibilityOwner)
	f.s.st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"}) // Default OU, unrestricted
	if sc := f.s.viewerScope("staff"); sc != nil {
		t.Fatalf("an internal reader must have no scope, got %+v", sc)
	}
	for _, id := range []int64{f.internal, f.pub, f.strangerReport} {
		if !f.canRead(t, "staff", id) {
			t.Errorf("an internal reader must still read report %d", id)
		}
	}
}

// TestAccountScopedWithNoOU proves the whole rule works for a lone account, with no OU tree at all —
// grants addressed to the person, ownership recorded against the person.
func TestAccountScopedWithNoOU(t *testing.T) {
	s := tenancyServer(t)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	s.st.UpsertUser(User{Username: "solo", PasswordHash: "h", Role: "user"})
	s.st.SetUserRestricted("solo", true)
	s.st.SetVersionGrants("对外版", []string{userPrincipal("solo")})

	today := s.panelToday()
	mine, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: today, RType: "投资决策",
		Title: "mine", Version: "对外版", MD: "x"})
	theirs, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: today, RType: "投资决策",
		Title: "theirs", Version: "对外版", MD: "y"})
	s.st.AddReportViewer(mine, today, "solo", 0)

	sc := s.viewerScope("solo")
	if r, _ := s.st.GetNew(mine, sc); r == nil {
		t.Error("a lone account must read what it asked for, with no OU involved")
	}
	if r, _ := s.st.GetNew(theirs, sc); r != nil {
		t.Error("owner visibility must hold for a lone account too")
	}
}

// TestOwnRunBecomesReadable closes the loop between generating and reading. A scoped user's own run
// must leave them on the report's viewer list, or under owner visibility they cannot read the very
// report they paid for. The attribution token therefore carries the PERSON, not only their OU —
// under per-person visibility the OU alone is not enough to identify a reader.
func TestOwnRunBecomesReadable(t *testing.T) {
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("client-A", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("alice", ou)
	s.st.UpsertUser(User{Username: "colleague", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("colleague", ou)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	s.st.SetVersionGrants("对外版", []string{groupPrincipal(ou)})

	// The run: the portal injects a signed token naming who asked, the workflow echoes it back.
	inputs := s.runInputs(BatchJob{CreatedBy: "alice"}, map[string]string{"code": "600519"})
	token := inputs[ownerTokenInput]
	if token == "" {
		t.Fatal("a scoped user's run must carry an attribution token")
	}
	gotOU, gotUser, ok := s.ownerFromToken(token)
	if !ok || gotOU != ou || gotUser != "alice" {
		t.Fatalf("token carries (ou=%d user=%q ok=%v), want (%d, alice, true)", gotOU, gotUser, ok, ou)
	}
	// A tampered token attributes nothing rather than attributing wrongly.
	if _, _, ok := s.ownerFromToken(token + "x"); ok {
		t.Error("a tampered attribution token must be refused")
	}

	today := s.panelToday()
	id, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: today, RType: "投资决策",
		Title: "T", Version: "对外版", MD: "body"})
	s.st.StampReportOwner(id, gotOU)
	if err := s.st.AddReportViewer(id, today, gotUser, gotOU); err != nil {
		t.Fatal(err)
	}

	if r, _ := s.st.GetNew(id, s.viewerScope("alice")); r == nil {
		t.Error("the requester must be able to read the report their own run produced")
	}
	if r, _ := s.st.GetNew(id, s.viewerScope("colleague")); r != nil {
		t.Error("owner visibility must not extend to a colleague who did not ask")
	}
}

// TestVersionSwitcherListsOnlyGeneratedAndPermittedForms backs the reading page. It groups by
// (symbol, date, subtype) and NOT by title on purpose: each form comes from its own run of its own
// workflow, so two forms of one analysis will almost never carry a byte-identical title, and
// requiring that would make the switcher silently fail to group exactly the reports it exists for.
func TestVersionSwitcherListsOnlyGeneratedAndPermittedForms(t *testing.T) {
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("client-A", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("alice", ou)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	s.st.SaveVersion(ReportVersion{Name: "客户版", Ord: 2, Visibility: VisibilityGroup}) // registered, never run

	today := s.panelToday()
	mk := func(version, title string) int64 {
		id, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: today, RType: "投资决策",
			Title: title, Version: version, MD: "body"})
		s.st.AddReportViewer(id, today, "alice", ou)
		return id
	}
	// Deliberately DIFFERENT titles: two workflows, two generations.
	internal := mk(s.st.DefaultVersion(), "贵州茅台投资决策分析")
	public := mk("对外版", "茅台：投资决策结论")

	// An internal reader sees both forms grouped as one report despite the titles differing.
	all := s.st.VersionsOfReport(Rep{Symbol: "600519", Date: today, RType: "投资决策"}, nil)
	if len(all) != 2 {
		t.Fatalf("internal switcher = %d forms, want 2 despite the differing titles: %+v", len(all), all)
	}
	if all[0].Version != s.st.DefaultVersion() || all[1].Version != "对外版" {
		t.Errorf("switcher order = %q,%q, want registry order", all[0].Version, all[1].Version)
	}

	// alice is granted only the published form, so that is all her switcher offers — the internal
	// one must not even be advertised as existing.
	s.st.SetVersionGrants("对外版", []string{groupPrincipal(ou)})
	got := s.st.VersionsOfReport(Rep{Symbol: "600519", Date: today, RType: "投资决策"}, s.viewerScope("alice"))
	if len(got) != 1 || got[0].ID != public {
		t.Errorf("scoped switcher = %+v, want only the published form", got)
	}
	_ = internal
}
