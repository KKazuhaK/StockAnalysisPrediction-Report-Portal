package app

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestPasswordChangeInvalidatesExistingSession(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "alice", PasswordHash: "old", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	oldCookie := s.sign("alice")
	requestWith := func(value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: value})
		return r
	}
	if got := s.currentActiveUser(requestWith(oldCookie)); got != "alice" {
		t.Fatalf("fresh session user = %q", got)
	}
	legacyMsg := "alice|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	legacy := encodeSessionMessage(legacyMsg) + "." + s.hmac(legacyMsg)
	if got := s.currentActiveUser(requestWith(legacy)); got != "alice" {
		t.Fatalf("legacy session user = %q", got)
	}
	if err := st.SetUserPassword("alice", "new"); err != nil {
		t.Fatal(err)
	}
	if got := s.currentActiveUser(requestWith(oldCookie)); got != "" {
		t.Fatalf("old session survived password change as %q", got)
	}
	if got := s.currentActiveUser(requestWith(s.sign("alice"))); got != "alice" {
		t.Fatalf("new session user = %q", got)
	}
	if got := s.currentActiveUser(requestWith(legacy)); got != "" {
		t.Fatalf("legacy session survived password change as %q", got)
	}
}
