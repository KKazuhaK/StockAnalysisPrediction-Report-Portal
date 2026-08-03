// Package geoip resolves an IP address to a place, using a local MaxMind-format
// (.mmdb) database. Fully offline: the memory-mapped file IS the lookup, so there
// is no per-address call to anybody and no cache to keep coherent.
//
// Offline is the requirement, not a preference. The addresses being resolved are
// the portal's own visitors, including people who failed to sign in; sending them
// to a third party to be geolocated would hand that list to somebody else, which
// is the same reasoning that keeps the login page from loading a remote image.
//
// One Reader reads either schema, because the two common free databases disagree
// about the shape of the same field:
//
//   - MaxMind GeoLite2 / GeoIP2 / db-ip Lite — nested objects, where "country" is
//     a map {iso_code, names:{en:…}}, plus "city" and "subdivisions" on city-level
//     databases.
//   - ipinfo Lite — flat strings, where "country" is the country NAME and
//     "country_code" is the ISO code; country granularity only.
//
// They collide on "country" (map versus string), so a record is decoded into a
// generic map and branched on the runtime type rather than into a fixed struct.
// That also means a future MaxMind-compatible database works without a change here.
package geoip

import (
	"net"
	"strings"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

// Location is what a lookup can say about an address. Every field is optional:
// a country-level database fills only the first two, and an address that is not
// in the database fills none.
type Location struct {
	CountryCode string `json:"country_code,omitempty"` // ISO 3166-1 alpha-2
	Country     string `json:"country,omitempty"`
	Region      string `json:"region,omitempty"` // state / province
	City        string `json:"city,omitempty"`
}

// Empty reports whether the lookup found nothing worth showing.
func (l Location) Empty() bool {
	return l.CountryCode == "" && l.Country == "" && l.Region == "" && l.City == ""
}

// DBInfo describes a loaded database, for the admin status view.
type DBInfo struct {
	Type        string `json:"type"`        // e.g. "GeoLite2-City"
	BuildEpoch  uint   `json:"build_epoch"` // unix seconds the database was built
	Granularity string `json:"granularity"` // "city" or "country"
}

// Reader wraps an open .mmdb. Safe for concurrent Lookup — the underlying
// maxminddb reader is — so one Reader serves every request; the owner handles
// open, close and reload.
type Reader struct{ db *maxminddb.Reader }

// Open opens an .mmdb file.
func Open(path string) (*Reader, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &Reader{db: db}, nil
}

// Close releases the database.
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// Info reports the loaded database's metadata.
func (r *Reader) Info() DBInfo {
	if r == nil || r.db == nil {
		return DBInfo{}
	}
	m := r.db.Metadata
	g := "country"
	if strings.Contains(strings.ToLower(m.DatabaseType), "city") {
		g = "city"
	}
	return DBInfo{Type: m.DatabaseType, BuildEpoch: m.BuildEpoch, Granularity: g}
}

// Lookup resolves one address. An unparseable, private or unmapped address is an
// empty Location and NOT an error: "we do not know where this is" is the ordinary
// answer for a LAN address, and making the caller distinguish it from a failure
// would put a branch at every call site to reach the same result.
func (r *Reader) Lookup(ip string) Location {
	if r == nil || r.db == nil {
		return Location{}
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || !IsResolvable(ip) {
		return Location{}
	}
	var rec map[string]any
	if err := r.db.Lookup(parsed, &rec); err != nil {
		return Location{}
	}
	return mapRecord(rec)
}

// IsResolvable reports whether an address is a routable public one worth looking
// up. A loopback or RFC1918 address is in no database, and asking would only
// produce an empty answer more slowly.
func IsResolvable(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	return !p.IsLoopback() && !p.IsPrivate() && !p.IsUnspecified() &&
		!p.IsLinkLocalUnicast() && !p.IsLinkLocalMulticast() && !p.IsMulticast()
}

// mapRecord reads either schema out of a decoded record.
func mapRecord(rec map[string]any) Location {
	var out Location
	switch c := rec["country"].(type) {
	case map[string]any: // MaxMind / db-ip: a nested object
		out.CountryCode = str(c["iso_code"])
		out.Country = localizedName(c)
	case string: // ipinfo Lite: the NAME, with the code in its own field
		out.Country = c
		out.CountryCode = str(rec["country_code"])
	}
	if out.CountryCode == "" {
		out.CountryCode = str(rec["country_code"])
	}
	if city, ok := rec["city"].(map[string]any); ok {
		out.City = localizedName(city)
	} else {
		out.City = str(rec["city"])
	}
	// Subdivisions run outermost-first; the first is the state/province.
	if subs, ok := rec["subdivisions"].([]any); ok && len(subs) > 0 {
		if first, ok := subs[0].(map[string]any); ok {
			out.Region = localizedName(first)
		}
	} else if out.Region == "" {
		out.Region = str(rec["region"])
	}
	return out
}

// localizedName prefers English, which every MaxMind-format database carries, and
// falls back to whatever single name is present. The client localizes the COUNTRY
// itself from the ISO code, so this only has to be stable, not translated.
func localizedName(m map[string]any) string {
	names, ok := m["names"].(map[string]any)
	if !ok {
		return str(m["name"])
	}
	if v := str(names["en"]); v != "" {
		return v
	}
	for _, v := range names {
		if sv := str(v); sv != "" {
			return sv
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
