package app

import (
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWorthLoggingPolicy pins the rule on its own, because it is the whole policy and a policy
// buried in an if-chain inside a middleware is one nobody can test.
//
// The shape it has to have: nothing that was logged before stops being logged, every failure and
// every mutation is logged, and the SPA's timer-driven polling — the only reason /api/ was ever
// skipped — is the one thing that stays quiet.
func TestWorthLoggingPolicy(t *testing.T) {
	fast, slow := 5*time.Millisecond, 2*time.Second
	cases := []struct {
		name         string
		path, method string
		status       int
		took         time.Duration
		want         bool
	}{
		{"the polling reads that made /api/ noisy", "/api/home", "GET", 200, fast, false},
		{"announcement poll", "/api/announcements", "GET", 304, fast, false},
		{"a failing read is never silent", "/api/home", "GET", 500, fast, true},
		{"a refused read is never silent", "/api/reports/9/revisions", "GET", 403, fast, true},
		{"a slow read is the complaint waiting to happen", "/api/home", "GET", 200, slow, true},
		{"every mutation is somebody doing something", "/api/reports", "POST", 200, fast, true},
		{"including deletes", "/api/reports/9", "DELETE", 200, fast, true},
		{"and machine ingests", "/api/v1/reports", "POST", 200, fast, true},
		{"page loads stay logged, as before", "/", "GET", 200, fast, true},
		{"a report download stays logged", "/report/9/pdf", "GET", 200, fast, true},
		{"a 404 page stays logged", "/nope", "GET", 404, fast, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := worthLogging(c.path, c.method, c.status, c.took); got != c.want {
				t.Errorf("worthLogging(%q, %s, %d, %v) = %v, want %v", c.path, c.method, c.status, c.took, got, c.want)
			}
		})
	}
}

// captureLog redirects the standard logger for one test and returns what was written.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf strings.Builder
	old := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	fn()
	return buf.String()
}

// TestAccessLogRecordsApiFailures is the gap this closes. The middleware skipped /api/ outright,
// with a comment claiming those endpoints logged themselves; a handful log an event apiece and the
// rest log nothing, so a 500 on an unaudited endpoint left no trace anywhere at all.
func TestAccessLogRecordsApiFailures(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/boom", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("/api/home", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := s.logMiddleware(mux)

	out := captureLog(t, func() {
		r := httptest.NewRequest("GET", "/api/boom", nil)
		r.RemoteAddr = "203.0.113.7:5555"
		h.ServeHTTP(httptest.NewRecorder(), r)
	})
	if !strings.Contains(out, "500") || !strings.Contains(out, "/api/boom") {
		t.Errorf("a failing API request must be logged; got %q", out)
	}
	if !strings.Contains(out, "203.0.113.7") {
		t.Errorf("the log must name the caller; got %q", out)
	}

	// And the polling that made this endpoint prefix noisy in the first place stays quiet.
	quiet := captureLog(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/home", nil))
	})
	if quiet != "" {
		t.Errorf("a fast successful API read must not be logged; got %q", quiet)
	}
}

// TestAccessLogSkipsAssets keeps the high-volume, uninformative traffic out. Logging it is how an
// access log becomes something nobody reads.
func TestAccessLogSkipsAssets(t *testing.T) {
	s := &Server{}
	h := s.logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, p := range []string{"/assets/index-abc.js", "/app-assets/demo/x.png", "/site-assets/logo.png",
		"/healthz", "/favicon.svg", "/favicon.ico", "/manifest.webmanifest", "/pwa-icon", "/sw.js"} {
		out := captureLog(t, func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil)) })
		if out != "" {
			t.Errorf("%s must not be logged; got %q", p, out)
		}
	}
}

// TestAccessLogTrustsOnlyConfiguredProxies matters because the address in the log is evidence. An
// untrusted upstream must not be able to write someone else's address into it by asking.
func TestAccessLogTrustsOnlyConfiguredProxies(t *testing.T) {
	line := func(s *Server) string {
		h := s.logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
		return captureLog(t, func() {
			r := httptest.NewRequest("GET", "/api/x", nil)
			r.RemoteAddr = "10.1.2.3:4444"
			r.Header.Set("X-Forwarded-For", "198.51.100.9")
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}

	untrusted := line(&Server{})
	if strings.Contains(untrusted, "198.51.100.9") {
		t.Errorf("an unconfigured upstream must not be able to claim an address; got %q", untrusted)
	}
	if !strings.Contains(untrusted, "10.1.2.3") {
		t.Errorf("the peer address must be logged instead; got %q", untrusted)
	}

	nets, err := parseTrustedProxies([]string{"10.1.2.3"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	trusted := line(&Server{trustedNets: nets})
	if !strings.Contains(trusted, "198.51.100.9") {
		t.Errorf("behind a configured proxy the visitor must be logged, not the gateway; got %q", trusted)
	}
}
