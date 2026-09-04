package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// /healthz is a public, unauthenticated liveness probe. It must expose liveness ONLY — no build
// identity (version/commit) and no data count (report total). Either would let an anonymous
// scanner fingerprint the instance (version/commit → known CVEs) or read business volume.
//
// It reports the DATABASE too as of v0.4.48, which is a fact about reachability and deliberately
// not about contents: "ok" or "unreachable", never a driver message (those can carry the DSN's
// host, user and database name) and never a count.
func TestHealthzExposesLivenessOnly(t *testing.T) {
	srv := &Server{st: newTestStore(t)}
	rec := httptest.NewRecorder()
	srv.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("healthz body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if got["ok"] != true {
		t.Errorf("healthz ok = %v, want true", got["ok"])
	}
	for _, leak := range []string{"version", "commit", "buildDate", "new", "newCount", "count", "reports", "total"} {
		if _, present := got[leak]; present {
			t.Errorf("public /healthz must not expose %q; body = %s", leak, rec.Body.String())
		}
	}
}

func healthGET(t *testing.T, s *Server) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestHealthzChecksTheDatabase is the whole point of the endpoint. It used to answer ok
// unconditionally, which reports only the failure an orchestrator can already see for itself — the
// process being gone — and stays green through the one it cannot: the portal up with its database
// unreachable, serving an error on every page.
func TestHealthzChecksTheDatabase(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	if code, out := healthGET(t, s); code != http.StatusOK || out["ok"] != true || out["db"] != "ok" {
		t.Fatalf("healthy portal → %d %v; want 200 and db ok", code, out)
	}

	// Losing the database is what the probe exists to notice.
	st.Close()
	s.health = healthCache{} // drop the cached verdict; the caching is tested separately below
	code, out := healthGET(t, s)
	if code != http.StatusServiceUnavailable || out["ok"] != false {
		t.Fatalf("unreachable database → %d %v; want 503 and ok false", code, out)
	}
	// The driver's message can carry the DSN's host, user and database name, and this endpoint is
	// public. It goes to the log, never to the caller.
	if out["db"] != "unreachable" {
		t.Errorf("db = %v; want the opaque \"unreachable\" on a public endpoint", out["db"])
	}
	for _, leak := range []string{"sql:", "database is closed", "dsn", "password"} {
		if strings.Contains(strings.ToLower(fmt.Sprint(out)), leak) {
			t.Errorf("the failure response leaked %q: %v", leak, out)
		}
	}
}

// TestHealthzCachesItsVerdict keeps an unauthenticated endpoint from being a query amplifier — and,
// on SQLite, from queueing behind whatever write holds the single pooled connection.
func TestHealthzCachesItsVerdict(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	if code, _ := healthGET(t, s); code != http.StatusOK {
		t.Fatalf("first probe → %d", code)
	}
	// With the database gone, a probe inside the cache window must still answer from the cached
	// verdict rather than query — which is exactly what makes a flood cheap.
	st.Close()
	if code, _ := healthGET(t, s); code != http.StatusOK {
		t.Errorf("a probe inside the cache window → %d; the verdict should have been reused", code)
	}
	// And the window must expire, or the endpoint would keep reporting a state that has passed.
	s.health.at = time.Now().Add(-healthCacheFor - time.Second)
	if code, _ := healthGET(t, s); code != http.StatusServiceUnavailable {
		t.Errorf("after the cache window → %d; want the fresh 503", code)
	}
}

// /api/version carries build identity (version/commit/buildDate) for the signed-in footer, so it
// is session-gated: an anonymous request must be rejected and must not leak any build field.
func TestVersionRequiresSession(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.requireUserJSON(srv.handleVersion)(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/version = %d, want 401", rec.Code)
	}
	for _, leak := range []string{"version", "commit", "buildDate"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("unauthenticated /api/version leaked %q: %s", leak, rec.Body.String())
		}
	}
}
