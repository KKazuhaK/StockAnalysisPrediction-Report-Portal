package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Completing the audit log (the owner asked for sign-ins, runs and admin operations).
//
// The vocabulary rule these pin: auth.* is what a principal does to its OWN authentication;
// user.change is what an administrator does to somebody ELSE's account. Both carry the username as
// the target, so one target_id filter is a complete per-account timeline no matter who acted.

func auditServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: mustHash("correct-horse-battery"), Role: "user"})
	return s
}

func auditRows(t *testing.T, s *Server, action string) []AuditEntry {
	t.Helper()
	rows, _ := s.st.ListAudit(AuditFilter{Action: action})
	return rows
}

func postFrom(h http.HandlerFunc, path, body, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":51000"
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// A security log that records only successes answers half the question. This is the half that was
// missing entirely: nothing anywhere recorded a failed sign-in.
func TestFailedAndSuccessfulSignInsAreBothRecorded(t *testing.T) {
	s := auditServer(t)

	bad := postFrom(s.apiLogin, "/api/login", `{"username":"kazuha","password":"wrong"}`, "203.0.113.9")
	if bad.Code == http.StatusOK {
		t.Fatal("the wrong password signed in")
	}
	fails := auditRows(t, s, AuditLoginFailed)
	if len(fails) != 1 {
		t.Fatalf("failed sign-ins logged: %d, want 1", len(fails))
	}
	if fails[0].TargetID != "kazuha" {
		t.Errorf("target = %q, want the account that was attempted", fails[0].TargetID)
	}
	if fails[0].IP != "203.0.113.9" {
		t.Errorf("ip = %q — without it, ten failures across ten accounts are ten unrelated rows", fails[0].IP)
	}
	if !strings.Contains(fails[0].Detail, "reason") {
		t.Errorf("detail %q should name why, so bad-password and disabled are distinguishable", fails[0].Detail)
	}

	ok := postFrom(s.apiLogin, "/api/login", `{"username":"kazuha","password":"correct-horse-battery"}`, "198.51.100.4")
	if ok.Code != http.StatusOK {
		t.Fatalf("the right password did not sign in: %d %s", ok.Code, ok.Body.String())
	}
	good := auditRows(t, s, AuditLogin)
	if len(good) != 1 {
		t.Fatalf("successful sign-ins logged: %d, want 1", len(good))
	}
	if good[0].Actor != "kazuha" || good[0].IP != "198.51.100.4" {
		t.Errorf("actor/ip = %q/%q, want kazuha/198.51.100.4", good[0].Actor, good[0].IP)
	}
	var d map[string]any
	json.Unmarshal([]byte(good[0].Detail), &d)
	if d["method"] != "password" {
		t.Errorf("detail.method = %v, want password — four ways in, and they are not equivalent", d["method"])
	}
}

// A failed sign-in against a name nobody holds still records the attempt. Refusing to log it would
// hide exactly the enumeration sweep the log exists to reveal, and the row is not an oracle: it is
// only visible to an admin who can already list the accounts.
func TestFailedSignInForAnUnknownNameIsRecorded(t *testing.T) {
	s := auditServer(t)
	postFrom(s.apiLogin, "/api/login", `{"username":"nobody","password":"x"}`, "203.0.113.9")
	rows := auditRows(t, s, AuditLoginFailed)
	if len(rows) != 1 {
		t.Fatalf("logged %d, want 1", len(rows))
	}
	if rows[0].TargetID != "nobody" {
		t.Errorf("target = %q, want the name that was tried", rows[0].TargetID)
	}
}

// The IP is an equality dimension, not a substring: "which host tried nine accounts" is the query
// that turns a pile of failures into one incident.
func TestAuditFiltersByIP(t *testing.T) {
	s := auditServer(t)
	for _, n := range []string{"a", "b", "c"} {
		postFrom(s.apiLogin, "/api/login", `{"username":"`+n+`","password":"x"}`, "203.0.113.9")
	}
	postFrom(s.apiLogin, "/api/login", `{"username":"d","password":"x"}`, "198.51.100.1")

	rows, total := s.st.ListAudit(AuditFilter{IP: "203.0.113.9"})
	if total != 3 || len(rows) != 3 {
		t.Fatalf("by ip = %d/%d, want 3", len(rows), total)
	}
	for _, r := range rows {
		if r.IP != "203.0.113.9" {
			t.Errorf("row from %q leaked into the filter", r.IP)
		}
	}
}

// Signing out is the other end of a session, and "when did this session end" is part of the same
// question as "when did it start".
func TestSignOutIsRecorded(t *testing.T) {
	s := auditServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.signUserFor(*s.st.GetUser("kazuha"), 0)})
	s.apiLogout(httptest.NewRecorder(), req)
	if n := len(auditRows(t, s, AuditLogout)); n != 1 {
		t.Errorf("sign-outs logged: %d, want 1", n)
	}
}

// An admin changing somebody else's password is user.change, not auth.password_change: the actor
// and the subject are different people, and the account's timeline has to show both.
func TestPasswordChangesRecordWhoChangedWhose(t *testing.T) {
	s := auditServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})

	// The account holder changing their own.
	s.recordAuth(nil, AuditPasswordChange, "kazuha", "kazuha", nil)
	own := auditRows(t, s, AuditPasswordChange)
	if len(own) != 1 || own[0].Actor != "kazuha" || own[0].TargetID != "kazuha" {
		t.Fatalf("self-change row = %+v", own)
	}

	// An admin resetting it: a different action, and the target is still the account.
	s.recordChange(nil, "admin", AuditUserChange, "user", "kazuha", map[string]any{"field": "password"})
	byAdmin := auditRows(t, s, AuditUserChange)
	if len(byAdmin) != 1 || byAdmin[0].Actor != "admin" || byAdmin[0].TargetID != "kazuha" {
		t.Fatalf("admin-change row = %+v", byAdmin)
	}

	// And the account's whole timeline is one filter, regardless of who acted.
	_, total := s.st.ListAudit(AuditFilter{TargetID: "kazuha"})
	if total != 2 {
		t.Errorf("timeline for kazuha = %d rows, want both", total)
	}
}
