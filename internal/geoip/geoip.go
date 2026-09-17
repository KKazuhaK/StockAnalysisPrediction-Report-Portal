// Package geoip resolves an IP address to a place, using a local MaxMind-format
// (.mmdb) database. Fully offline: the memory-mapped file IS the lookup, so there
// is no per-address call to anybody and no cache to keep coherent.
//
// Offline is the requirement, not a preference. The addresses being resolved are
// the portal's own visitors, including people who failed to sign in; sending them
// to a third party to be geolocated would hand that list to somebody else, which
// is the same reasoning that keeps the login page from loading a remote image.
//
// This package is a thin adapter over github.com/KazuhaHub/authcore/geoip, which
// does the actual schema decoding (MaxMind vs. ipinfo Lite) and file reading. It
// exists only to keep the package path and the pre-migration signatures stable for
// internal/app, which still calls Open/Reader/Location/DBInfo exactly as before.
//
// One behavioral seam is worth calling out: authcore's Reader.Lookup returns
// (Location, error), where the error is set only when the database ITSELF could
// not be read (a corrupt file, for example) — an unmapped or private address is
// still a zero Location with a nil error there, same as here. This package's
// Lookup predates that split and has always folded "database unreadable" into the
// same empty-Location answer as "not found" (the original implementation called
// maxminddb.Reader.Lookup directly and treated any error, including "unmapped
// record", as an empty result). Adapting here means discarding the error, which
// reproduces the exact old behavior with zero change in observable output.
package geoip

import (
	authcoregeoip "github.com/KazuhaHub/authcore/geoip"
)

// Location and DBInfo are aliased, not redeclared, so the JSON shape internal/app
// already depends on (audit.go's AuditEntry.Geo field, the admin status view) is
// authcore's own type and can never drift out of sync with it.
type (
	Location = authcoregeoip.Location
	DBInfo   = authcoregeoip.DBInfo
)

// IsResolvable reports whether an address is a routable public one worth looking
// up. A loopback or RFC1918 address is in no database, and asking would only
// produce an empty answer more slowly.
func IsResolvable(ip string) bool { return authcoregeoip.IsResolvable(ip) }

// Reader wraps an open .mmdb. Safe for concurrent Lookup — the underlying authcore
// Reader is — so one Reader serves every request; the owner handles open, close and
// reload.
type Reader struct{ r *authcoregeoip.Reader }

// Open opens an .mmdb file.
func Open(path string) (*Reader, error) {
	r, err := authcoregeoip.Open(path)
	if err != nil {
		return nil, err
	}
	return &Reader{r: r}, nil
}

// Close releases the database. Safe to call on a nil Reader.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	return r.r.Close()
}

// Info reports the loaded database's metadata.
func (r *Reader) Info() DBInfo {
	if r == nil {
		return DBInfo{}
	}
	return r.r.Info()
}

// Lookup resolves one address. An unparseable, private, unmapped, or (here) unreadable-
// database address is an empty Location and NOT an error: see the package doc comment
// for why the underlying error is discarded rather than propagated.
func (r *Reader) Lookup(ip string) Location {
	if r == nil {
		return Location{}
	}
	loc, _ := r.r.Lookup(ip)
	return loc
}
