package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /healthz is a public, unauthenticated liveness probe. It must expose liveness ONLY — no build
// identity (version/commit) and no data count (report total). Either would let an anonymous
// scanner fingerprint the instance (version/commit → known CVEs) or read business volume.
func TestHealthzExposesLivenessOnly(t *testing.T) {
	srv := &Server{}
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
