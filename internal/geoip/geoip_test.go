package geoip

import "testing"

// The two free databases disagree about the shape of the same field: MaxMind nests
// "country" as an object, ipinfo Lite makes it the country's NAME as a plain string.
// Decoding into a fixed struct picks one and silently reads nothing from the other,
// which looks exactly like "this address is not in the database".

func TestReadsTheMaxMindShape(t *testing.T) {
	got := mapRecord(map[string]any{
		"country": map[string]any{
			"iso_code": "CN",
			"names":    map[string]any{"en": "China", "zh-CN": "中国"},
		},
		"city": map[string]any{"names": map[string]any{"en": "Shenzhen"}},
		"subdivisions": []any{
			map[string]any{"names": map[string]any{"en": "Guangdong"}},
		},
	})
	want := Location{CountryCode: "CN", Country: "China", Region: "Guangdong", City: "Shenzhen"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadsTheIpinfoLiteShape(t *testing.T) {
	got := mapRecord(map[string]any{"country": "China", "country_code": "CN"})
	want := Location{CountryCode: "CN", Country: "China"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Outermost first: the state, not the county below it.
func TestTakesTheOutermostSubdivision(t *testing.T) {
	got := mapRecord(map[string]any{
		"subdivisions": []any{
			map[string]any{"names": map[string]any{"en": "California"}},
			map[string]any{"names": map[string]any{"en": "Santa Clara County"}},
		},
	})
	if got.Region != "California" {
		t.Errorf("region = %q, want California", got.Region)
	}
}

// A record with no English name must still produce something. Returning "" would
// render as a bare flag with no place next to it.
func TestFallsBackToAnyName(t *testing.T) {
	got := mapRecord(map[string]any{
		"country": map[string]any{"iso_code": "JP", "names": map[string]any{"ja": "日本"}},
	})
	if got.Country != "日本" || got.CountryCode != "JP" {
		t.Errorf("got %+v, want the Japanese name and JP", got)
	}
}

func TestEmptyRecordIsEmpty(t *testing.T) {
	if !mapRecord(map[string]any{}).Empty() {
		t.Error("an empty record produced a location")
	}
}

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
