package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// Regression for the account-lockout DoS: an attacker failing an account past the ceiling must NOT
// prevent the real owner (correct password, from a different IP) from logging in.
func TestLoginNoAccountLockout(t *testing.T) {
	st := newTestStore(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("right"), bcrypt.MinCost)
	if err := st.UpsertUser(User{Username: "victim", PasswordHash: string(hash), Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, loginThr: newLoginThrottle(), cfg: &config.Config{SecretKey: "k"}}
	login := func(ip, pw string) int {
		req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"Username":"victim","Password":"`+pw+`"}`))
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		s.apiLogin(rec, req)
		return rec.Code
	}
	// Attacker from one IP trips the per-account key.
	for i := 0; i < loginFailMax+2; i++ {
		login("10.0.0.1", "wrong")
	}
	// A wrong guess (even from a clean IP) is now rate-limited on the account key.
	if code := login("10.0.0.9", "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("wrong password on a hot account = %d; want 429", code)
	}
	// But the real owner, correct password, from a clean IP, still gets in — no lockout.
	if code := login("10.0.0.2", "right"); code != http.StatusOK {
		t.Fatalf("correct password from a clean IP = %d; want 200 (must never be locked out)", code)
	}
}

// The throttle blocks a key after loginFailMax failures within the window, a success resets it, and
// the window rolls over after loginFailWindow.
func TestLoginThrottle(t *testing.T) {
	l := newLoginThrottle()
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	key := "u:admin"

	for i := 0; i < loginFailMax-1; i++ {
		l.record(key, now)
	}
	if l.blocked(key, now) {
		t.Fatalf("blocked after %d failures; ceiling is %d", loginFailMax-1, loginFailMax)
	}
	l.record(key, now) // hits the ceiling
	if !l.blocked(key, now) {
		t.Fatalf("not blocked after %d failures", loginFailMax)
	}
	// A success clears the key.
	l.reset(key)
	if l.blocked(key, now) {
		t.Fatal("still blocked after reset")
	}
	// Window rollover: failures then wait past the window → no longer blocked.
	for i := 0; i < loginFailMax; i++ {
		l.record(key, now)
	}
	if !l.blocked(key, now) {
		t.Fatal("expected blocked before window rollover")
	}
	if l.blocked(key, now.Add(loginFailWindow+time.Second)) {
		t.Fatal("should unblock after the window lapses")
	}
}

// clientIP uses the RemoteAddr host (port stripped) and deliberately IGNORES a spoofable
// X-Forwarded-For, so an attacker can't rotate the header to evade the per-IP limit.
func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("RemoteAddr host = %q; want 203.0.113.9", got)
	}
	// A spoofed XFF must NOT change the throttle key.
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("XFF must be ignored; got %q, want 203.0.113.9", got)
	}
}

// ListQueueJobs returns every active job plus only the most recent termLimit terminal jobs, newest
// first, and the true total — so a large finished history is bounded but no pending job is hidden.
func TestListQueueJobs(t *testing.T) {
	st := newTestStore(t)
	// 5 terminal jobs (ids 1..5) + 1 active queued job (id 6) + 1 old-ish active running (id 7).
	for i := 0; i < 5; i++ {
		seedJob(t, st, "finished", agoLocal(i+1))
	}
	active1 := seedJob(t, st, "queued", "")
	active2 := seedJob(t, st, "running", "")

	jobs, total := st.ListQueueJobs(2) // keep only 2 terminal
	if total != 7 {
		t.Errorf("total = %d; want 7", total)
	}
	// Expect: 2 active + 2 most-recent terminal = 4, newest-first by id.
	if len(jobs) != 4 {
		t.Fatalf("returned %d jobs; want 4 (2 active + 2 terminal)", len(jobs))
	}
	got := map[int64]bool{}
	for i := 1; i < len(jobs); i++ {
		if jobs[i-1].ID < jobs[i].ID {
			t.Errorf("not sorted newest-first: %d before %d", jobs[i-1].ID, jobs[i].ID)
		}
	}
	for _, j := range jobs {
		got[j.ID] = true
	}
	if !got[active1] || !got[active2] {
		t.Error("an active job was hidden by the terminal limit")
	}
}
