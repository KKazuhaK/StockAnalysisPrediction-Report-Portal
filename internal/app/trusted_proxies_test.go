package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Which upstreams may speak for a client.
//
// The default matters more than the parser does. Trusting nothing means that a portal behind nginx
// on the same host — the overwhelmingly common deployment — records the proxy's address for every
// visitor and looks like it is working. Loopback is the default the sibling panel uses, and it is
// the one that makes that deployment correct with no configuration at all.

func peerWithXFF(peer, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// net/http always brackets an IPv6 literal in RemoteAddr; building it unbracketed here would
	// test a string no real server produces.
	r.RemoteAddr = net.JoinHostPort(peer, "40000")
	r.Header.Set("X-Forwarded-For", xff)
	return r
}

func TestUnsetTrustsLoopbackOnly(t *testing.T) {
	nets, err := parseTrustedProxies(nil)
	if err != nil {
		t.Fatal(err)
	}
	// A proxy on the same host is believed…
	if got := clientIP(peerWithXFF("127.0.0.1", "203.0.113.9"), nets); got != "203.0.113.9" {
		t.Errorf("loopback proxy: got %q, want the forwarded address", got)
	}
	if got := clientIP(peerWithXFF("::1", "203.0.113.9"), nets); got != "203.0.113.9" {
		t.Errorf("IPv6 loopback proxy: got %q, want the forwarded address", got)
	}
	// …and nobody else is. A visitor who sets the header themselves is still recorded as themselves.
	if got := clientIP(peerWithXFF("198.51.100.7", "203.0.113.9"), nets); got != "198.51.100.7" {
		t.Errorf("a stranger's own header was believed: %q", got)
	}
	// Including a Docker bridge gateway, which is the case that started this: it is NOT loopback,
	// so it still needs naming — but now the portal says so rather than staying silent.
	if got := clientIP(peerWithXFF("172.20.0.1", "203.0.113.9"), nets); got != "172.20.0.1" {
		t.Errorf("an unnamed bridge gateway was believed: %q", got)
	}
}

// "none" is how an operator asks for the old behaviour: believe nobody, not even loopback.
func TestNoneTrustsNobody(t *testing.T) {
	nets, err := parseTrustedProxies([]string{"none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 0 {
		t.Fatalf("none produced %d networks", len(nets))
	}
	if got := clientIP(peerWithXFF("127.0.0.1", "203.0.113.9"), nets); got != "127.0.0.1" {
		t.Errorf("none still believed loopback: %q", got)
	}
}

// "all" is for a listener that cannot be reached except through the proxy — a Docker network, a
// unix socket. It is the right answer there and a hole anywhere else, so it is opt-in and loud.
func TestAllTrustsEverything(t *testing.T) {
	for _, tok := range []string{"all", "*", "ALL"} {
		nets, err := parseTrustedProxies([]string{tok})
		if err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
		if got := clientIP(peerWithXFF("172.20.0.1", "203.0.113.9"), nets); got != "203.0.113.9" {
			t.Errorf("%s: got %q, want the forwarded address", tok, got)
		}
		if got := clientIP(peerWithXFF("198.51.100.7", "2001:db8::1"), nets); got != "2001:db8::1" {
			t.Errorf("%s: IPv6 forwarded address not taken: %q", tok, got)
		}
	}
}

// A token mixed with real networks is a contradiction, and silently picking one meaning would give
// an operator a trust policy they did not write.
func TestTokensCannotBeMixedWithNetworks(t *testing.T) {
	for _, entries := range [][]string{{"all", "10.0.0.0/8"}, {"10.0.0.0/8", "none"}} {
		if _, err := parseTrustedProxies(entries); err == nil {
			t.Errorf("%v was accepted", entries)
		}
	}
}

// Explicit networks still work, and still exclude everything else.
func TestExplicitNetworks(t *testing.T) {
	nets, err := parseTrustedProxies([]string{"172.16.0.0/12", "10.1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if got := clientIP(peerWithXFF("172.20.0.1", "203.0.113.9"), nets); got != "203.0.113.9" {
		t.Errorf("a named bridge gateway was not believed: %q", got)
	}
	if got := clientIP(peerWithXFF("10.1.2.3", "203.0.113.9"), nets); got != "203.0.113.9" {
		t.Errorf("a named single address was not believed: %q", got)
	}
	// Loopback is NOT implied once an explicit list is given: the list is the policy.
	if got := clientIP(peerWithXFF("127.0.0.1", "203.0.113.9"), nets); got != "127.0.0.1" {
		t.Errorf("loopback was believed although it was not listed: %q", got)
	}
	if _, err := parseTrustedProxies([]string{"not-an-address"}); err == nil {
		t.Error("rubbish was accepted as a network")
	}
}

func TestTrustAllIsReportedSoItCanBeWarnedAbout(t *testing.T) {
	all, _ := parseTrustedProxies([]string{"all"})
	if !trustsEverything(all) {
		t.Error("trust-all was not recognised, so no warning would be logged")
	}
	loopback, _ := parseTrustedProxies(nil)
	if trustsEverything(loopback) {
		t.Error("the loopback default was mistaken for trust-all")
	}
	var none []*net.IPNet
	if trustsEverything(none) {
		t.Error("an empty list was mistaken for trust-all")
	}
}
