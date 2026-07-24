package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// TestUserExpiryRoundTrip locks the store round-trip for the account validity cutoff (ADR 0022 R4):
// a fresh user has none, a set date reads back, and clearing it returns to "never".
func TestUserExpiryRoundTrip(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if u := st.GetUser("bob"); u.ExpiresAt != "" {
		t.Fatalf("new user should have no expiry, got %q", u.ExpiresAt)
	}
	if err := st.SetUserExpiry("bob", "2030-01-01"); err != nil {
		t.Fatal(err)
	}
	if u := st.GetUser("bob"); u.ExpiresAt != "2030-01-01" {
		t.Fatalf("expiry = %q, want 2030-01-01", u.ExpiresAt)
	}
	if err := st.SetUserExpiry("bob", ""); err != nil {
		t.Fatal(err)
	}
	if u := st.GetUser("bob"); u.ExpiresAt != "" {
		t.Fatalf("cleared expiry = %q, want empty", u.ExpiresAt)
	}
}

// TestAccountExpiredBlocksSession proves an already-issued session dies the moment the account's
// panel-tz validity date passes — and that the account stays valid THROUGH the expiry day itself.
func TestAccountExpiredBlocksSession(t *testing.T) {
	st := newTestStore(t)
	st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	cookie := s.sign("bob")
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		return r
	}
	if got := s.currentActiveUser(req()); got != "bob" {
		t.Fatalf("no-expiry session = %q, want bob", got)
	}

	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	st.SetUserExpiry("bob", yesterday)
	if got := s.currentActiveUser(req()); got != "" {
		t.Fatalf("yesterday-expiry session = %q, want blocked", got)
	}
	st.SetUserExpiry("bob", today) // valid THROUGH the expiry day
	if got := s.currentActiveUser(req()); got != "bob" {
		t.Fatalf("today-expiry session = %q, want still valid through today", got)
	}
	st.SetUserExpiry("bob", tomorrow)
	if got := s.currentActiveUser(req()); got != "bob" {
		t.Fatalf("future-expiry session = %q, want valid", got)
	}
}

// TestApiLoginRejectsExpiredAccount proves login is refused (403) for an expired account even with
// the correct password, and still succeeds (200) before expiry.
func TestApiLoginRejectsExpiredAccount(t *testing.T) {
	st := newTestStore(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	st.UpsertUser(User{Username: "bob", PasswordHash: string(h), Role: "user"})
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}

	login := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "bob", "password": "secret123"})
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		s.apiLogin(w, r)
		return w
	}

	if w := login(); w.Code != http.StatusOK {
		t.Fatalf("valid login code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	st.SetUserExpiry("bob", time.Now().AddDate(0, 0, -1).Format("2006-01-02"))
	w := login()
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired login code = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "过期") {
		t.Fatalf("expired login body = %s, want an expiry message", w.Body.String())
	}
}
