package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Behind an unconfigured reverse proxy, every audit row records the proxy's own address.
//
// clientIP is right to do that: believing X-Forwarded-For from a peer nobody vouched for would let
// any visitor claim any address, which would make the whole column worse than absent. The defect is
// that the portal KNOWS it is in this situation — a request carrying X-Forwarded-For from an
// untrusted peer is exactly that — and said nothing, so the log filled with a plausible-looking
// address that is the same for everyone.

func TestForwardedRequestFromAnUntrustedPeerIsNoticed(t *testing.T) {
	s := auditServer(t) // no trusted_proxies configured, like the deployment that reported this
	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.RemoteAddr = "172.20.0.1:41234" // a Docker bridge gateway: the proxy, not the visitor
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := s.auditIP(req); got != "172.20.0.1" {
		t.Fatalf("recorded %q; an untrusted peer's XFF must NOT be believed", got)
	}
	if !s.proxyHint() {
		t.Error("the portal saw a forwarded request it could not trust and did not flag it")
	}
}

// No header, no hint: a portal reached directly must not nag about a proxy it does not have.
func TestDirectRequestsRaiseNoHint(t *testing.T) {
	s := auditServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.RemoteAddr = "203.0.113.9:41234"
	s.auditIP(req)
	if s.proxyHint() {
		t.Error("a direct request was mistaken for a misconfigured proxy")
	}
}

// Once the proxy IS trusted the header is believed, the recorded address is the visitor's, and
// there is nothing left to warn about.
func TestATrustedProxyRecordsTheVisitorAndClearsTheHint(t *testing.T) {
	s := auditServer(t)
	nets, err := parseTrustedProxies([]string{"172.20.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	s.trustedNets = nets

	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.RemoteAddr = "172.20.0.1:41234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := s.auditIP(req); got != "203.0.113.9" {
		t.Fatalf("recorded %q, want the visitor's address", got)
	}
	if s.proxyHint() {
		t.Error("a correctly configured proxy still raises the hint")
	}
}

// The console has to say it, or the admin never finds out: the column looks populated either way.
func TestAuditResponseCarriesTheProxyHint(t *testing.T) {
	s := auditServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})

	bad := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	bad.RemoteAddr = "172.20.0.1:5000"
	bad.Header.Set("X-Forwarded-For", "203.0.113.9")
	s.auditIP(bad)

	rec := httptest.NewRecorder()
	s.apiAdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil), "admin")
	var out struct {
		ProxyHint bool `json:"proxy_hint"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.ProxyHint {
		t.Error("the console is not told that every recorded address is the proxy's")
	}
}
