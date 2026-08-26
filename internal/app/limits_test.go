package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The session lifetime and the two request ceilings were constants compiled into the binary. These
// tests are about what happens once they are settings: an unconfigured portal must behave exactly
// as it did before, a saved value must actually reach the code that enforces it, and a value that
// says nothing (junk, out of range, an omitted block) must leave the working one alone rather than
// resolve to zero — a session lifetime of zero logs everybody out, and a request ceiling of zero
// refuses every call.

func limitsServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle(), v1Rate: newRateLimiter()}
	s.loginThr.limits = s.loginLimits
	return s
}

func saveSecurity(t *testing.T, s *Server, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSecuritySave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/security", strings.NewReader(body)), "boss")
	return rec.Code
}

func TestLimitsDefaultToWhatTheBinaryUsedToDo(t *testing.T) {
	s := limitsServer(t)

	if got := s.sessionTTL(); got != defaultSessionTTL {
		t.Errorf("session TTL = %v, want %v", got, defaultSessionTTL)
	}
	max, window := s.loginLimits()
	if max != defLoginFailMax || window != defLoginFailWindow {
		t.Errorf("login limits = %d/%v, want %d/%v", max, window, defLoginFailMax, defLoginFailWindow)
	}
	if got := s.apiV1RatePerMin(); got != 0 {
		t.Errorf("api rate = %d, want 0 (off): a portal that never asked for a ceiling must not get one", got)
	}
}

func TestLimitsRoundTrip(t *testing.T) {
	s := limitsServer(t)

	if code := saveSecurity(t, s, `{"limits":{"session_ttl_hours":12,"login_fail_max":4,"login_fail_window_min":30,"apiv1_rate_per_min":100}}`); code != http.StatusOK {
		t.Fatalf("save → %d, want 200", code)
	}
	if got := s.sessionTTL(); got != 12*time.Hour {
		t.Errorf("session TTL = %v, want 12h", got)
	}
	max, window := s.loginLimits()
	if max != 4 || window != 30*time.Minute {
		t.Errorf("login limits = %d/%v, want 4/30m", max, window)
	}
	if got := s.apiV1RatePerMin(); got != 100 {
		t.Errorf("api rate = %d, want 100", got)
	}

	rec := httptest.NewRecorder()
	s.apiAdminSecurity(rec, httptest.NewRequest(http.MethodGet, "/api/admin/security", nil), "boss")
	var body struct {
		Limits struct {
			SessionTTLHours    int `json:"session_ttl_hours"`
			LoginFailMax       int `json:"login_fail_max"`
			LoginFailWindowMin int `json:"login_fail_window_min"`
			APIV1RatePerMin    int `json:"apiv1_rate_per_min"`
		} `json:"limits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the security config: %v", err)
	}
	if body.Limits.SessionTTLHours != 12 || body.Limits.LoginFailMax != 4 ||
		body.Limits.LoginFailWindowMin != 30 || body.Limits.APIV1RatePerMin != 100 {
		t.Errorf("GET did not report what was saved: %+v", body.Limits)
	}
}

func TestLimitsSurviveABodyThatDoesNotMentionThem(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setSessionTTLHours, "12")

	// The security page posts captcha and registration together; a body that carries neither this
	// block nor a login block must not reset a lifetime somebody configured.
	if code := saveSecurity(t, s, `{"captcha":{"provider":"image"},"registration":{"enabled":false}}`); code != http.StatusOK {
		t.Fatalf("save → %d, want 200", code)
	}
	if got := s.sessionTTL(); got != 12*time.Hour {
		t.Errorf("session TTL = %v after an unrelated save, want 12h", got)
	}
}

func TestNonsenseLimitsAreRefusedRatherThanStored(t *testing.T) {
	s := limitsServer(t)
	for _, body := range []string{
		`{"limits":{"session_ttl_hours":0}}`,
		`{"limits":{"session_ttl_hours":-1}}`,
		`{"limits":{"login_fail_max":0}}`,
		`{"limits":{"login_fail_window_min":0}}`,
		`{"limits":{"apiv1_rate_per_min":-5}}`,
	} {
		if code := saveSecurity(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", body, code)
		}
	}
	// Nothing landed: the portal still behaves the way it shipped.
	if got := s.sessionTTL(); got != defaultSessionTTL {
		t.Errorf("a refused save changed the session TTL to %v", got)
	}
	if got := s.apiV1RatePerMin(); got != 0 {
		t.Errorf("a refused save turned the api ceiling on: %d", got)
	}
}

// A stored value the code would choke on — hand-edited, or written by a build that allowed a range
// this one does not — reads as the default rather than as zero.
func TestAnUnreadableStoredLimitReadsAsTheDefault(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setSessionTTLHours, "not a number")
	s.st.SetSetting(setLoginFailMax, "0")
	if got := s.sessionTTL(); got != defaultSessionTTL {
		t.Errorf("session TTL = %v, want the default", got)
	}
	if max, _ := s.loginLimits(); max != defLoginFailMax {
		t.Errorf("login ceiling = %d, want the default", max)
	}
}

func TestSessionCookieFollowsTheConfiguredLifetime(t *testing.T) {
	s := limitsServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.SetSetting(setSessionTTLHours, "2")
	u := *s.st.GetUser("alice")

	rec := httptest.NewRecorder()
	s.setSessionCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), u)
	cookie := rec.Result().Cookies()[0]
	if cookie.MaxAge > 2*3600 {
		t.Errorf("cookie MaxAge = %d, want at most 2h", cookie.MaxAge)
	}
	// The signed token has to expire with it — MaxAge alone is a hint to a browser, and whoever
	// holds the cookie value is not obliged to be one.
	if exp := sessionExpiry(t, cookie.Value); exp.After(time.Now().Add(3 * time.Hour)) {
		t.Errorf("signed expiry %v is beyond the configured 2h", exp)
	}
}

func TestLoginCeilingFollowsTheSetting(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setLoginFailMax, "2")
	now := time.Now()

	s.loginThr.record("ip:1.2.3.4", now)
	if s.loginThr.blocked("ip:1.2.3.4", now) {
		t.Error("one failure blocked a caller under a ceiling of two")
	}
	s.loginThr.record("ip:1.2.3.4", now)
	if !s.loginThr.blocked("ip:1.2.3.4", now) {
		t.Error("the configured ceiling of two was not enforced")
	}
}

func TestLoginWindowFollowsTheSetting(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setLoginFailMax, "1")
	s.st.SetSetting(setLoginFailWindowMin, "1")
	now := time.Now()

	s.loginThr.record("ip:5.6.7.8", now)
	if !s.loginThr.blocked("ip:5.6.7.8", now) {
		t.Fatal("the ceiling was not enforced")
	}
	if s.loginThr.blocked("ip:5.6.7.8", now.Add(2*time.Minute)) {
		t.Error("the block outlived the configured one-minute window")
	}
}

// ---------- the machine API's request ceiling ----------

func TestAPIV1RateLimitIsOffUntilConfigured(t *testing.T) {
	s := limitsServer(t)
	h := s.rateLimitV1(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d → %d with no ceiling configured, want 200", i, rec.Code)
		}
	}
}

func TestAPIV1RateLimitRefusesOverTheCeiling(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setAPIV1RatePerMin, "3")
	served := 0
	h := s.rateLimitV1(func(w http.ResponseWriter, r *http.Request) { served++; w.WriteHeader(http.StatusOK) })

	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		h(rec, req)
		return rec
	}
	for i := 0; i < 3; i++ {
		if rec := call(); rec.Code != http.StatusOK {
			t.Fatalf("call %d → %d, want 200 (still inside the ceiling)", i+1, rec.Code)
		}
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth call → %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 with no Retry-After leaves a client guessing")
	}
	if served != 3 {
		t.Errorf("the handler ran %d times, want 3 — a refused call must not reach it", served)
	}
}

func TestAPIV1RateLimitCountsEachCallerSeparately(t *testing.T) {
	s := limitsServer(t)
	s.st.SetSetting(setAPIV1RatePerMin, "1")
	h := s.rateLimitV1(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	call := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
		req.RemoteAddr = ip + ":9000"
		h(rec, req)
		return rec.Code
	}
	if code := call("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first caller → %d, want 200", code)
	}
	// A second source has its own budget: one busy integration must not lock out everyone else.
	if code := call("198.51.100.2"); code != http.StatusOK {
		t.Errorf("a different source → %d, want 200", code)
	}
	if code := call("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Errorf("the first caller's second call → %d, want 429", code)
	}
}

func TestRateLimiterWindowRolls(t *testing.T) {
	l := newRateLimiter()
	now := time.Now()
	if !l.allow("k", 1, time.Minute, now) {
		t.Fatal("the first call inside an empty window was refused")
	}
	if l.allow("k", 1, time.Minute, now.Add(time.Second)) {
		t.Fatal("the ceiling was not enforced inside the window")
	}
	if !l.allow("k", 1, time.Minute, now.Add(2*time.Minute)) {
		t.Error("the window never rolled over")
	}
}

// The table cannot be grown without bound by a flood of distinct sources.
func TestRateLimiterPrunes(t *testing.T) {
	l := newRateLimiter()
	now := time.Now()
	for i := 0; i < 5000; i++ {
		l.allow(fmt.Sprintf("k%d", i), 10, time.Minute, now)
	}
	l.allow("later", 10, time.Minute, now.Add(2*time.Minute))
	l.mu.Lock()
	n := len(l.recs)
	l.mu.Unlock()
	if n > 4096 {
		t.Errorf("the limiter holds %d keys after a flood, want it pruned", n)
	}
}
