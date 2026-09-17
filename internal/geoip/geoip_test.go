package geoip

import "testing"

// mapRecord and the schema-decoding behavior it implements (MaxMind's nested
// "country" object vs. ipinfo Lite's flat string, outermost-subdivision-first,
// name-fallback-without-English, empty-record handling) used to be tested here
// directly, because this package used to own that decoding.
//
// It doesn't anymore: this package is now a thin adapter over
// github.com/KazuhaHub/authcore/geoip, which owns decoding and is tested there
// (authcore's geoip/geoip_test.go and geoip/fixture_test.go), with coverage that
// is a superset of what lived here — the authcore suite adds a country-code-only
// schema variant, a plain-string city/region variant, and an end-to-end test built
// against real constructed .mmdb fixtures, none of which this package ever had.
// Re-testing mapRecord's behavior from this side would just be testing authcore's
// internals through a second door.
//
// What stays here is behavior this adapter package itself is still responsible
// for: nil-safety and the public IsResolvable/Lookup contract it exposes to
// internal/app.

// Addresses that are in no database, so asking is only slower.
func TestPrivateAndBogusAddressesAreNotResolvable(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "192.168.0.7", "172.16.5.5",
		"169.254.1.1", "0.0.0.0", "224.0.0.1", "", "not-an-ip", "203.0.113.9.9",
	} {
		if IsResolvable(ip) {
			t.Errorf("%q was treated as a public address", ip)
		}
	}
	for _, ip := range []string{"203.0.113.9", "8.8.8.8", "2001:4860:4860::8888"} {
		if !IsResolvable(ip) {
			t.Errorf("%q is public and should be resolvable", ip)
		}
	}
}

// A nil reader is the normal state when no database has been installed. It must
// answer "unknown" rather than panic, because the audit page renders either way.
func TestNilReaderIsUsable(t *testing.T) {
	var r *Reader
	if !r.Lookup("8.8.8.8").Empty() {
		t.Error("a nil reader returned a location")
	}
	if r.Info().Type != "" {
		t.Error("a nil reader claimed to have a database")
	}
	if err := r.Close(); err != nil {
		t.Errorf("closing a nil reader: %v", err)
	}
}
