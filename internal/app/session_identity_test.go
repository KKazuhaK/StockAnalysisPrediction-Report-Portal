package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A session must not outlive the ACCOUNT it was issued for — not merely the username.
//
// session_rev protects a live account: change the password and old cookies stop working. It cannot
// protect across a deletion, because a recreated row is a fresh INSERT and its revision starts at
// zero again. A username is re-registerable, so alice's still-valid cookie authenticated her as
// whoever took the address next, with their OU, their role and their reports.
//
// users.created_at was a declared column nobody wrote. Stamping it on INSERT (never on the conflict
// branch, or every profile save would sign everyone out) gives each account instance an identity the
// signed cookie can name, and costs no schema change.

func TestASessionDiesWithTheAccountNotTheName(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	cookie := s.sign("alice")
	if cookie == "" {
		t.Fatal("no cookie minted")
	}
	if got := s.currentActiveUser(reqWith(cookie)); got != "alice" {
		t.Fatalf("the original holder is not authenticated: %q", got)
	}

	// The account is deleted and the address is taken by somebody else entirely.
	if err := s.st.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "different", Role: "user"})

	if got := s.currentActiveUser(reqWith(cookie)); got != "" {
		t.Errorf("the previous holder's cookie authenticated them as the NEW account (%q)", got)
	}
	// The new holder signs in normally.
	if fresh := s.sign("alice"); s.currentActiveUser(reqWith(fresh)) != "alice" {
		t.Error("the new holder cannot use their own session")
	}
}

// A profile save must not log anyone out: created_at is stamped on INSERT only.
func TestAnUpsertOfALiveAccountKeepsItsSessions(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	before := s.st.GetUser("alice").CreatedAt
	if before == "" {
		t.Fatal("created_at was not stamped on insert")
	}
	s.st.SetUserProfile("alice", "Alice", "alice@example.com")
	if after := s.st.GetUser("alice").CreatedAt; after != before {
		t.Errorf("created_at changed on a profile save: %q → %q", before, after)
	}
}

// An account that predates the stamping keeps its sessions, so upgrading does not sign the world
// out. It gains the protection the moment it is recreated, which is the case that matters.
func TestSessionsPredatingTheStampStillWork(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "old", PasswordHash: "h", Role: "user"})
	s.st.exec("UPDATE users SET created_at='' WHERE username=?", "old")

	cookie := s.sign("old")
	if got := s.currentActiveUser(reqWith(cookie)); got != "old" {
		t.Errorf("an unstamped account lost its session on upgrade: %q", got)
	}
}

func reqWith(cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	return r
}
