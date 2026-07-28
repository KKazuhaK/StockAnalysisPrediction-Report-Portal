package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSafeFetchRejectsUnsafeTargets covers the SSRF guard on every outbound SSO fetch (IdP metadata,
// OIDC discovery, JWKS, token, userinfo). These URLs are admin-supplied and the discovery document
// then supplies MORE urls, so the server must refuse to be aimed at its own infrastructure.
func TestSafeFetchRejectsUnsafeTargets(t *testing.T) {
	c := newSafeClient(false) // RFC1918 disallowed: the strict posture
	for _, tc := range []struct{ name, url string }{
		{"plain http", "http://example.com/x"},
		{"loopback by name", "https://localhost/x"},
		{"loopback by ip", "https://127.0.0.1/x"},
		{"ipv6 loopback", "https://[::1]/x"},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data/"},
		{"link-local", "https://169.254.1.1/x"},
		{"private", "https://10.0.0.5/x"},
		{"private 192.168", "https://192.168.1.1/x"},
		{"CGNAT", "https://100.64.0.1/x"},
		{"ipv6 ULA", "https://[fd00::1]/x"},
		{"not a url", "::::"},
		{"no host", "https:///x"},
	} {
		if _, err := c.fetch(tc.url, 1<<20); err == nil {
			t.Errorf("%s (%s) must be refused", tc.name, tc.url)
		}
	}
}

// TestSafeFetchAllowsPrivateWhenEnabled proves the escape hatch: a self-hosted Keycloak or ADFS on
// an internal network is the common case, so RFC1918 is admin-selectable — but loopback and the
// cloud metadata address stay blocked unconditionally, since nothing legitimate lives there.
func TestSafeFetchAllowsPrivateWhenEnabled(t *testing.T) {
	c := newSafeClient(true)
	if err := c.checkURL("https://10.0.0.5/x"); err != nil {
		t.Errorf("RFC1918 must be permitted when enabled: %v", err)
	}
	for _, u := range []string{"https://127.0.0.1/x", "https://169.254.169.254/x"} {
		if err := c.checkURL(u); err == nil {
			t.Errorf("%s must stay blocked even with private networks enabled", u)
		}
	}
}

// TestSafeFetchReadsAndBoundsBody proves a normal fetch works and that an oversized response is
// truncated rather than read into memory unbounded.
func TestSafeFetchReadsAndBoundsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()

	// The test server is plain http on loopback, so exercise the transport through the escape
	// hatch used for tests rather than weakening the real policy.
	c := newSafeClient(true)
	c.allowInsecureForTest = true

	body, err := c.fetch(srv.URL, 1<<20)
	if err != nil || len(body) != 5000 {
		t.Fatalf("fetch = %d bytes, %v; want 5000 bytes", len(body), err)
	}
	if body, err = c.fetch(srv.URL, 100); err != nil || len(body) != 100 {
		t.Fatalf("bounded fetch = %d bytes, %v; want the read capped at 100", len(body), err)
	}
}
