package app

import (
	"testing"
	"time"
)

// Deleting something must take its viewer rows with it (ADR 0024).
//
// report_viewers is the table the read path consults, so an orphan is not merely wasted space. The
// dangerous direction is the principal: usernames are reusable — delete an account and the same
// address can register again — so a left-behind `u:<name>` row would hand the NEW holder of that
// name everything the old one had been given access to.

func viewerRows(t *testing.T, st *Store, where string, args ...any) int {
	t.Helper()
	var n int
	st.queryRow("SELECT COUNT(*) FROM report_viewers WHERE "+where, args...).Scan(&n)
	return n
}

// TestDeletingAReportTakesItsViewers proves the row-level cleanup, including through the bulk
// retention purge (ADR 0017), which deletes reports by its own path and would otherwise leave the
// list growing forever.
func TestDeletingAReportTakesItsViewers(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	old := time.Now().Add(-90 * 24 * time.Hour).Format("2006-01-02 15:04:05")

	single, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "单删", Version: "对外版", MD: "x"})
	bulk, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-01", RType: "投资决策",
		Title: "批删", Version: "对外版", MD: "y", Time: old})
	st.exec("UPDATE reports SET sent_at=? WHERE id=?", old, bulk)
	for _, id := range []int64{single, bulk} {
		if err := st.AddReportViewer(id, "2026-07-28", "alice", 7); err != nil {
			t.Fatal(err)
		}
	}
	if got := viewerRows(t, st, "1=1"); got != 4 { // two principals per report
		t.Fatalf("seeded %d viewer rows, want 4", got)
	}

	if _, err := st.DeleteReport(single); err != nil {
		t.Fatal(err)
	}
	if got := viewerRows(t, st, "report_id=?", single); got != 0 {
		t.Errorf("%d viewer rows outlived the report they point at", got)
	}

	if _, err := st.DeleteReportsIngestedBefore(time.Now().Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := viewerRows(t, st, "report_id=?", bulk); got != 0 {
		t.Errorf("%d viewer rows survived the retention purge", got)
	}
}

// TestDeletingAnAccountTakesItsViewers is the one with teeth. A username can be taken again — delete
// the account and the same address may register — so a surviving `u:<name>` row would grant the NEW
// holder of that name everything the previous one could read.
func TestDeletingAnAccountTakesItsViewers(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	st.UpsertUser(User{Username: "alice@corp.example", PasswordHash: "h", Role: "user"})
	st.SetUserRestricted("alice@corp.example", true)
	st.SetVersionGrants("对外版", []string{userPrincipal("alice@corp.example")})

	id, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "T", Version: "对外版", MD: "confidential to alice"})
	st.AddReportViewer(id, "2026-07-28", "alice@corp.example", 0)
	if r, _ := st.GetNew(id, s.viewerScope("alice@corp.example")); r == nil {
		t.Fatal("the original holder must be able to read it")
	}

	if err := st.DeleteUser("alice@corp.example"); err != nil {
		t.Fatal(err)
	}
	if got := viewerRows(t, st, "principal=?", userPrincipal("alice@corp.example")); got != 0 {
		t.Errorf("%d viewer rows outlived the account", got)
	}
	// The name is taken again by someone else entirely.
	st.UpsertUser(User{Username: "alice@corp.example", PasswordHash: "h2", Role: "user"})
	st.SetUserRestricted("alice@corp.example", true)
	st.SetVersionGrants("对外版", []string{userPrincipal("alice@corp.example")})
	if r, _ := st.GetNew(id, s.viewerScope("alice@corp.example")); r != nil {
		t.Error("a reused username must NOT inherit the previous holder's reports")
	}
}

// TestDeletingAnOUTakesItsGrantsAndViewers proves the same for a group principal, and that the OU's
// version grants and run allow-list go with it rather than lingering to be inherited by a new OU
// that happens to reuse the id.
func TestDeletingAnOUTakesItsGrantsAndViewers(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	root := st.EnsureDefaultGroup()
	ou, _ := st.CreateUserGroup("doomed", "", 0)
	st.SetGroupParent(ou, root)
	st.SetGroupRestricted(ou, true)
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	st.SetVersionGrants("对外版", []string{groupPrincipal(ou)})
	st.SetGroupTargets(ou, []GroupTarget{{TargetID: 42, Surfaces: ""}})

	id, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "T", Version: "对外版", MD: "x"})
	st.AddReportViewer(id, "2026-07-28", "someone", ou)

	if err := st.DeleteUserGroup(ou); err != nil {
		t.Fatal(err)
	}
	if got := viewerRows(t, st, "principal=?", groupPrincipal(ou)); got != 0 {
		t.Errorf("%d viewer rows outlived the OU", got)
	}
	if got := st.VersionGrants("对外版"); len(got) != 0 {
		t.Errorf("version grants %v outlived the OU they were made to", got)
	}
	if got := st.GroupTargets(ou); len(got) != 0 {
		t.Errorf("run allow-list %v outlived the OU", got)
	}
}
