package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The mux's only catch-all is `GET /` → the SPA, which answers index.html for any unrouted
// path so React can own deep links. That is right for /stock/300750 and dead wrong for
// /api/*: an unrouted API path would come back as the app shell with 200, and a caller
// parsing that as success is a far worse failure than a clean error.
//
// This stopped being hypothetical when the legacy machine API was retired: GET /api/reports,
// /api/report, /api/runs and /api/tracking went from live routes to unrouted paths in one
// commit. A stale caller must be told they are gone. (The write verbs are already safe —
// POST/DELETE/PATCH match no pattern and the mux answers 405 by itself.)
func TestUnknownAPIPathIs404NotTheSPAShell(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")

	// Every path the legacy removal orphaned, plus a never-existed one.
	for _, p := range []string{
		"/api/reports", "/api/reports/manifest", "/api/report", "/api/runs", "/api/tracking",
		"/api/definitely-not-a-route",
	} {
		t.Run(p, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 — body was %q", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "spa shell") {
				t.Fatal("served the SPA shell: a stale API caller would parse HTML as a 200 answer")
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json — API callers get JSON, not HTML", ct)
			}
			var m map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
			}
			if m["ok"] != false {
				t.Errorf("body = %v, want the ok:false error envelope", m)
			}
		})
	}
}

// The guard must not eat the SPA's real job: deep links still render the shell, and a path
// that merely starts with the letters "api" is not an API path.
func TestSPADeepLinksStillServeTheShell(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")

	for _, p := range []string{"/stock/300750", "/manage/tokens", "/apidocs", "/"} {
		t.Run(p, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — deep links must reach the SPA", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "spa shell") {
				t.Errorf("body = %q, want the SPA shell", rec.Body.String())
			}
		})
	}
}
