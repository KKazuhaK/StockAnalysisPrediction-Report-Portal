package app

import (
	"context"
	"time"
)

// samlReplayCacheMaxTTL bounds how long a replay row may live regardless of what the assertion's
// own NotOnOrAfter claims. Carried over unchanged from the pre-authcore implementation
// (assertionExpiry in the old saml.go): a signed but carelessly- or maliciously-configured IdP
// could otherwise pin a row in sso_assertion_seen indefinitely. authcore/saml's own expiry
// calculation (Provider.expiryFor) has no such ceiling — it is a lower bound only — so this
// package supplies the clamp itself rather than relying on authcore for it.
const samlReplayCacheMaxTTL = 30 * time.Minute

// samlReplayCache adapts the Store's persistent, hashed replay table to authcore/saml.ReplayCache.
//
// It is NOT a package-level singleton. samlProvider builds one per call, scoped to the IdP entity
// id of the provider row being served, exactly the way the pre-authcore code called
// s.st.MarkAssertionSeen(sp.IDPMetadata.EntityID, ...) inline. authcore/saml.ReplayCache.SeenOrAdd
// receives only the bare assertion id — it has no notion of tenancy — so keeping the id space
// separated by IdP is entirely this adapter's responsibility. Sharing one samlReplayCache (or, worse,
// authcore's own in-memory default) across providers would let one tenant's assertion ids collide
// with another's, which is the same class of mistake that sank the audit-package migration.
//
// The underlying store call is a persistent, hashed, insert-or-nothing write (authreq.go,
// MarkAssertionSeen) — it survives a process restart and is safe for two concurrent callers
// racing the same assertion id, which is exactly what authcore/saml.ReplayCache requires of any
// implementation. authcore's own NewMemoryReplayCache is deliberately never used here: it is an
// in-process map that empties on every restart, which would let an assertion captured just before
// a restart be replayed just after one — a real capability regression this migration must not
// introduce.
type samlReplayCache struct {
	st          *Store
	idpEntityID string
}

// SeenOrAdd implements authcore/saml.ReplayCache.
func (c samlReplayCache) SeenOrAdd(_ context.Context, id string, expiresAt, now time.Time) (seen bool, err error) {
	if max := now.Add(samlReplayCacheMaxTTL); expiresAt.After(max) {
		expiresAt = max
	}
	if c.st.MarkAssertionSeen(c.idpEntityID, id, expiresAt) {
		return false, nil // first sighting: recorded
	}
	return true, nil // already present and unexpired: replay
}
