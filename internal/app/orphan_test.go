package app

import (
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
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

// TestDeletingAnAccountTakesEverythingKeyedToTheName widens the same argument past report_viewers.
// There are no foreign keys anywhere in this schema, so every "when the account goes, this goes too"
// is hand-written, and a reusable username turns each miss into an inheritance: the next person to
// register that address picks up what the last one left. A passkey is the worst of them — it is a
// credential, so the previous holder could sign in AS the new account, with no time limit.
func TestDeletingAnAccountTakesEverythingKeyedToTheName(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	const name = "alice@corp.example"
	st.UpsertUser(User{Username: name, PasswordHash: "h", Role: "user"})

	if err := st.AddPasskey(name, "laptop", &webauthn.Credential{ID: []byte("cred-1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConversation(7, name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRecurringTask(RecurringTask{Name: "nightly", TargetID: 7, Rows: "[]",
		Freq: "daily", AtTime: "02:00", Enabled: true, CreatedBy: name}); err != nil {
		t.Fatal(err)
	}
	st.saveTicket(name, 3, time.Now())
	if err := st.CreateAuthRequest(AuthRequest{Token: "tok-1", Kind: "verify", Username: name},
		time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	rows := func(table, col string) int {
		t.Helper()
		var n int
		st.queryRow("SELECT COUNT(*) FROM "+table+" WHERE "+col+"=?", name).Scan(&n)
		return n
	}
	for _, c := range [][2]string{{"webauthn_credentials", "username"}, {"chat_conversations", "created_by"},
		{"recurring_tasks", "created_by"}, {"priority_tickets", "username"}, {"auth_requests", "username"}} {
		if rows(c[0], c[1]) == 0 {
			t.Fatalf("failed to seed %s", c[0])
		}
	}

	if err := st.DeleteUser(name); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col, why string }{
		{"webauthn_credentials", "username", "the previous holder's passkey would authenticate the next one"},
		{"chat_conversations", "created_by", "a reused name would read the previous holder's conversations"},
		{"recurring_tasks", "created_by", "an ownerless task keeps firing and keeps spending run quota"},
		{"priority_tickets", "username", "a reused name would inherit the urgent-run allowance"},
		{"auth_requests", "username", "a pending link would act on whoever holds the name next"},
	} {
		if got := rows(c.table, c.col); got != 0 {
			t.Errorf("%d %s rows outlived the account: %s", got, c.table, c.why)
		}
	}

	// batch_jobs is deliberately NOT swept: it is the run history an operator audits, and who ran
	// what must survive the person leaving. It carries no grant, so nothing is inherited.
	if _, err := st.CreateBatchJob(7, 1, 0, name, []map[string]string{{"symbol": "600519"}}, ""); err != nil {
		t.Fatal(err)
	}
	st.DeleteUser(name)
	var jobs int
	st.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE created_by=?", name).Scan(&jobs)
	if jobs == 0 {
		t.Error("run history must outlive the account that created it")
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

// TestDeletingAnAccountDoesNotLeaveItsRunsLive is the other half of the batch_jobs decision. The
// history stays for audit — but history is all it may be. Two counters read that table as LIVE
// state, so a reused username inherited the previous holder's daily quota consumption and their
// active-run count; and jobs left queued were still dispatched to Dify, and billed, on behalf of an
// account that no longer exists.
func TestDeletingAnAccountDoesNotLeaveItsRunsLive(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	const name = "alice@corp.example"
	st.UpsertUser(User{Username: name, PasswordHash: "h", Role: "user"})

	job, err := st.CreateBatchJob(7, 1, 0, name, []map[string]string{{"symbol": "600519"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveJobCount(name) == 0 {
		t.Fatal("failed to seed an active job")
	}
	// Age the whole episode into the past — account created two hours ago, ran an hour ago — so the
	// delete-and-recreate below is a genuinely later event. Without this the recreated row's
	// created_at lands in the same second as the original's and the floor cannot tell them apart.
	stamp := func(ago time.Duration) string { return time.Now().Add(-ago).Format("2006-01-02 15:04:05") }
	st.exec("UPDATE users SET created_at=? WHERE username=?", stamp(2*time.Hour), name)
	st.exec("UPDATE batch_jobs SET created_at=? WHERE created_by=?", stamp(time.Hour), name)
	midnight := time.Now().Add(-12 * time.Hour)
	if st.RunsToday(name, midnight) == 0 {
		t.Fatal("failed to seed today's usage")
	}

	if err := st.DeleteUser(name); err != nil {
		t.Fatal(err)
	}
	// The audit row survives…
	var kept int
	st.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE created_by=?", name).Scan(&kept)
	if kept == 0 {
		t.Error("run history must outlive the account that created it")
	}
	// …but it is no longer queued, so nothing is dispatched for a departed account.
	var live int
	st.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE created_by=? AND status IN ('queued','running','cancelling')",
		name).Scan(&live)
	if live != 0 {
		t.Errorf("%d job(s) still live for a deleted account — they would be dispatched and billed", live)
	}

	// The name is taken again. The new holder starts clean on both counters.
	st.UpsertUser(User{Username: name, PasswordHash: "h2", Role: "user"})
	if got := st.ActiveJobCount(name); got != 0 {
		t.Errorf("the new holder starts with %d active runs, want 0", got)
	}
	if got := st.RunsToday(name, midnight); got != 0 {
		t.Errorf("the new holder starts having used %d runs today, want 0", got)
	}
	_ = job
}
