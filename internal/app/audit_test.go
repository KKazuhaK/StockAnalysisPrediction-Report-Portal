package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The audit log.
//
// Two audiences in one table. "Who read this report" is the question a client asks, and it is the
// only one the portal can answer with evidence rather than assurance; "who changed this grant" is
// the question an operator asks when something is visible that should not be. Both are the same
// shape — actor, action, target, when — so they are one table with one query.
//
// actor_ou is stamped at write time on purpose. People move between OUs, so resolving it later from
// users.group_id would answer "which OU are they in NOW", which is not what an audit line means.

func TestAuditRecordsWhoDidWhatToWhich(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.WriteAudit(AuditEntry{Actor: "alice", ActorOU: 7, Action: "report.read",
		TargetType: "report", TargetID: "42", Detail: `{"symbol":"600519"}`})
	st.WriteAudit(AuditEntry{Actor: "admin", Action: "grant.change",
		TargetType: "version", TargetID: "对外版", Detail: `{"added":["u:bob"]}`})

	rows, total := st.ListAudit(AuditFilter{})
	if total != 2 || len(rows) != 2 {
		t.Fatalf("got %d rows (total %d), want 2", len(rows), total)
	}
	// Newest first: an audit page is read from the top.
	if rows[0].Action != "grant.change" {
		t.Errorf("not newest-first: %q", rows[0].Action)
	}
	if rows[1].ActorOU != 7 {
		t.Errorf("actor_ou was not stored: %+v", rows[1])
	}

	// The three questions the indexes exist for.
	if _, n := st.ListAudit(AuditFilter{Actor: "alice"}); n != 1 {
		t.Errorf("by actor = %d, want 1", n)
	}
	if _, n := st.ListAudit(AuditFilter{Action: "report.read"}); n != 1 {
		t.Errorf("by action = %d, want 1", n)
	}
	if _, n := st.ListAudit(AuditFilter{TargetType: "report", TargetID: "42"}); n != 1 {
		t.Errorf("by target = %d, want 1", n)
	}
}

// A machine caller has no username; the line must still be recorded rather than dropped, or an
// ingest token becomes the way to act unobserved.
func TestAuditRecordsMachineActors(t *testing.T) {
	st := tenancyServer(t).st
	st.WriteAudit(AuditEntry{Actor: "", Action: "report.ingest", TargetType: "report", TargetID: "9"})
	rows, total := st.ListAudit(AuditFilter{})
	if total != 1 {
		t.Fatalf("a machine action was not recorded")
	}
	if rows[0].Actor != "" {
		t.Errorf("actor = %q, want empty for a token caller", rows[0].Actor)
	}
}

// Retention is the storage-cleanup subsystem's Target D, so "never delete" is simply the target
// being off — no new concept, and no way for a hand-edited setting to half-apply it.
func TestAuditRetentionDeletesOnlyWhatIsOldEnough(t *testing.T) {
	st := tenancyServer(t).st
	old := time.Now().Add(-100 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	st.WriteAudit(AuditEntry{Actor: "a", Action: "report.read", TargetType: "report", TargetID: "1"})
	st.WriteAudit(AuditEntry{Actor: "b", Action: "report.read", TargetType: "report", TargetID: "2"})
	st.exec("UPDATE audit_log SET at=? WHERE target_id=?", old, "1")

	n, err := st.DeleteAuditBefore(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}
	if _, total := st.ListAudit(AuditFilter{}); total != 1 {
		t.Errorf("%d rows left, want the recent one", total)
	}
}

// A search over the detail, because "what happened to this client" is asked in words, not ids.
func TestAuditSearchesTheDetail(t *testing.T) {
	st := tenancyServer(t).st
	st.WriteAudit(AuditEntry{Actor: "admin", Action: "grant.change", TargetType: "version",
		TargetID: "对外版", Detail: `{"added":["u:client@corp.example"]}`})
	if _, n := st.ListAudit(AuditFilter{Q: "client@corp.example"}); n != 1 {
		t.Errorf("detail search found nothing")
	}
	if _, n := st.ListAudit(AuditFilter{Q: "nobody"}); n != 0 {
		t.Errorf("detail search matched something it should not")
	}
}

// Paging, because this is the one table expected to grow without bound.
func TestAuditPages(t *testing.T) {
	st := tenancyServer(t).st
	for i := 0; i < 25; i++ {
		st.WriteAudit(AuditEntry{Actor: "a", Action: "report.read", TargetType: "report",
			TargetID: strings.TrimSpace(itoa(int64(i)))})
	}
	rows, total := st.ListAudit(AuditFilter{Limit: 10, Offset: 20})
	if total != 25 || len(rows) != 5 {
		t.Errorf("page = %d rows of %d total, want 5 of 25", len(rows), total)
	}
}

// The events are only worth having if they are actually emitted. These drive the real handlers
// rather than the store, because a hook that exists but is never reached logs nothing.

func TestReadingAReportIsRecorded(t *testing.T) {
	s := tenancyServer(t)
	id, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-31", RType: "投资决策",
		Title: "内部", MD: "x"})
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	if rep := s.loadRep("alice", id); rep == nil {
		t.Fatal("the fixture report should be readable")
	}
	rows, total := s.st.ListAudit(AuditFilter{Action: AuditReportRead})
	if total != 1 {
		t.Fatalf("a report read logged %d lines, want 1", total)
	}
	if rows[0].Actor != "alice" || rows[0].TargetID != itoa(id) {
		t.Errorf("wrong actor/target: %+v", rows[0])
	}
	if !strings.Contains(rows[0].Detail, "600519") {
		t.Errorf("detail lacks the context that makes the line readable: %q", rows[0].Detail)
	}

	// A refusal is not a read. Logging those here would fill the table from any stale link, and it
	// is a different question from "who saw this".
	s.st.SetUserRestricted("alice", true)
	if rep := s.loadRep("alice", id); rep != nil {
		t.Fatal("a restricted account with no grant must not read it")
	}
	if _, n := s.st.ListAudit(AuditFilter{Action: AuditReportRead}); n != 1 {
		t.Errorf("a refused read was logged; total is now %d", n)
	}
}

func TestChangingAGrantIsRecordedWithBothSides(t *testing.T) {
	s := tenancyServer(t)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})
	s.st.SetVersionGrants("对外版", []string{"u:old@corp.example"})

	rec := httptest.NewRecorder()
	s.apiAdminVersionSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/versions",
		strings.NewReader(`{"name":"对外版","visibility":"owner","grants":["u:new@corp.example"]}`)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("saving the version → %d", rec.Code)
	}

	rows, total := s.st.ListAudit(AuditFilter{Action: AuditGrantChange})
	if total != 1 {
		t.Fatalf("a grant change logged %d lines, want 1", total)
	}
	// Both sides: the current state answers "who can read this now"; only the log answers "when did
	// they gain it, and who granted it".
	if !strings.Contains(rows[0].Detail, "old@corp.example") ||
		!strings.Contains(rows[0].Detail, "new@corp.example") {
		t.Errorf("detail does not carry both sides: %q", rows[0].Detail)
	}
}

// Only an admin may read the log — but gated on the PERMISSION, so a role granted it later works
// without touching the handler. That is the seam for per-OU audit visibility.
func TestAuditIsAdminOnly(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "worker", PasswordHash: "h", Role: "user"})
	s.st.WriteAudit(AuditEntry{Actor: "worker", Action: AuditReportRead, TargetType: "report", TargetID: "1"})

	rec := httptest.NewRecorder()
	s.requirePermJSON(PermManage, s.apiAdminAudit)(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated read → %d, want 401", rec.Code)
	}
	// The gate is the permission, not the literal role: whoever holds PermManage passes.
	if !can("admin", PermManage) || can("user", PermManage) {
		t.Error("the role registry no longer says what this handler assumes")
	}
}
