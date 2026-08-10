package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		"Referrer-Policy":        "same-origin",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// Everything under /api/ and /report/ is per-user and answered against a session cookie. None of
// it may sit in a shared cache: the portal runs behind whatever proxy the operator puts in front
// of it, and a response with no Cache-Control at all is one a cache is free to make its own
// decision about. Static, non-personal paths keep deciding for themselves.
func TestPrivateResponsesAreNotStored(t *testing.T) {
	for _, path := range []string{"/api/me", "/api/v1/reports", "/report/7/pdf", "/report/day.zip"} {
		w := httptest.NewRecorder()
		h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}

	// A handler that has a considered answer of its own — the immutable hashed assets, the
	// five-minute site assets — must win, since the middleware runs before it.
	w := httptest.NewRecorder()
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset Cache-Control = %q, want the handler's own value", got)
	}
}
