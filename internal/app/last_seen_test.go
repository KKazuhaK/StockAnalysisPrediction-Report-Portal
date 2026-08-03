package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// "Last activity" and "last login" are different facts, and the admin list was showing the second
// under a heading people read as the first. Someone who signed in on Monday and used the portal all
// week looked identical to someone who signed in on Monday and never came back — which is the one
// distinction the column is consulted for.

func TestLastSeenIsStampedByOrdinaryRequests(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: "h", Role: "user"})
	if u := s.st.GetUser("kazuha"); u.LastSeen != "" {
		t.Fatalf("a fresh account has no activity yet, got %q", u.LastSeen)
	}

	s.touchSeen("kazuha", time.Now())
	u := s.st.GetUser("kazuha")
	if u.LastSeen == "" {
		t.Fatal("an authenticated request did not record activity")
	}
	// And it is NOT the login stamp: signing in is one kind of activity, not the definition of it.
	if u.LastLogin != "" {
		t.Errorf("activity overwrote the login time (%q); they are separate facts", u.LastLogin)
	}
}

// A write per request would be a write per page load per user. The stamp is throttled, and the
// throttle is what makes recording activity affordable at all.
func TestLastSeenIsThrottled(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: "h", Role: "user"})

	base := time.Now()
	s.touchSeen("kazuha", base)
	first := s.st.GetUser("kazuha").LastSeen

	// A burst inside the window writes nothing further.
	for i := 0; i < 50; i++ {
		s.touchSeen("kazuha", base.Add(time.Duration(i)*time.Second))
	}
	if got := s.st.GetUser("kazuha").LastSeen; got != first {
		t.Errorf("a burst rewrote the stamp: %q -> %q", first, got)
	}

	// Past the window it moves again, or the column would freeze at the first request forever.
	s.touchSeen("kazuha", base.Add(lastSeenInterval+time.Minute))
	if got := s.st.GetUser("kazuha").LastSeen; got == first {
		t.Error("the stamp never moved after the throttle window")
	}
}

// Each account is throttled on its own: one busy user must not suppress everyone else's stamp.
func TestLastSeenThrottleIsPerAccount(t *testing.T) {
	s := tenancyServer(t)
	for _, n := range []string{"a", "b"} {
		s.st.UpsertUser(User{Username: n, PasswordHash: "h", Role: "user"})
	}
	now := time.Now()
	s.touchSeen("a", now)
	s.touchSeen("b", now)
	if s.st.GetUser("b").LastSeen == "" {
		t.Error("the second account was suppressed by the first one's throttle")
	}
}

// The middleware is where it hooks in, so every authenticated SPA call counts rather than a list
// of endpoints somebody has to remember to extend.
func TestRequireUserJSONRecordsActivity(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: "h", Role: "user"})

	h := s.requireUserJSON(func(w http.ResponseWriter, r *http.Request, user string) { writeJSON(w, okJSON) })
	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.signUserFor(*s.st.GetUser("kazuha"), time.Hour)})
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("request → %d (%s)", rec.Code, rec.Body.String())
	}
	if s.st.GetUser("kazuha").LastSeen == "" {
		t.Error("an authenticated request through the middleware recorded no activity")
	}
}

// An unauthenticated request has nobody to stamp, and must not create or touch anything.
func TestRejectedRequestRecordsNothing(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: "h", Role: "user"})
	h := s.requireUserJSON(func(w http.ResponseWriter, r *http.Request, user string) { writeJSON(w, okJSON) })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie → %d, want 401", rec.Code)
	}
	if s.st.GetUser("kazuha").LastSeen != "" {
		t.Error("a rejected request stamped somebody")
	}
}
