package app

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"log"
	"time"
)

// Pending interactive-auth state (ADR 0023), shared by every ceremony: the SAML AuthnRequest, the
// OIDC nonce + PKCE verifier, the 2FA pending-login step and the WebAuthn challenge.
//
// Server-side rather than a sealed cookie, for three reasons that a cookie cannot satisfy: it
// survives a restart, it works across the several instances production runs against one Postgres,
// and consumption is GLOBALLY single-use. That last one is the security property — it is a
// conditional DELETE whose RowsAffected decides the winner, never select-then-delete, so two
// concurrent callbacks replaying one token cannot both mint a session.

// AuthRequest is one pending ceremony.
type AuthRequest struct {
	Token      string // opaque, 128-bit; also the SAML RelayState and the OIDC state
	ProviderID int64
	Kind       string // "oidc" | "saml" | "2fa" | "webauthn"
	ReqID      string // SAML AuthnRequest ID, echoed back as InResponseTo
	Nonce      string // OIDC nonce
	Verifier   string // PKCE code verifier
	Username   string // 2FA / WebAuthn: who the pending step belongs to
	Target     string // where to land afterwards; a relative PATH, never a URL
}

// newAuthToken mints the opaque token. 16 bytes base64url is 22 characters, which fits inside
// SAML's 80-byte RelayState limit with room to spare.
func newAuthToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateAuthRequest stores a pending ceremony with an absolute expiry.
func (s *Store) CreateAuthRequest(r AuthRequest, expires time.Time) error {
	_, err := s.exec(`INSERT INTO sso_auth_requests(token,provider_id,kind,req_id,nonce,verifier,username,target,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.Token, r.ProviderID, r.Kind, r.ReqID, r.Nonce, r.Verifier, r.Username, r.Target,
		time.Now().Unix(), expires.Unix())
	return err
}

// ConsumeAuthRequest atomically claims a pending ceremony, returning it only to the single caller
// that won. Expiry is part of the same statement, so a request cannot lapse between a check and a
// delete. ok=false for unknown, expired and already-consumed alike — the caller must not be able to
// tell those apart either.
func (s *Store) ConsumeAuthRequest(token string, now time.Time) (AuthRequest, bool) {
	if token == "" {
		return AuthRequest{}, false
	}
	// Read first so the winner gets the payload, then let the conditional DELETE decide whether
	// this caller is in fact the winner. A read that loses the race is discarded.
	var r AuthRequest
	var provID sql.NullInt64
	var kind, reqID, nonce, verifier, username, target sql.NullString
	err := s.queryRow(`SELECT provider_id,kind,req_id,nonce,verifier,username,target
		FROM sso_auth_requests WHERE token=? AND expires_at>?`, token, now.Unix()).
		Scan(&provID, &kind, &reqID, &nonce, &verifier, &username, &target)
	if err != nil {
		return AuthRequest{}, false
	}
	res, err := s.exec(`DELETE FROM sso_auth_requests WHERE token=? AND expires_at>?`, token, now.Unix())
	if err != nil {
		return AuthRequest{}, false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return AuthRequest{}, false // someone else claimed it first
	}
	r.Token, r.ProviderID, r.Kind = token, provID.Int64, kind.String
	r.ReqID, r.Nonce, r.Verifier = reqID.String, nonce.String, verifier.String
	r.Username, r.Target = username.String, target.String
	return r, true
}

// PeekAuthRequest reads a pending ceremony without claiming it. Used where a later step is still
// allowed to fail — starting a WebAuthn ceremony, say — so a user is not thrown back to the
// password screen because their authenticator was slow. The claim still happens exactly once, at
// the point of no return.
func (s *Store) PeekAuthRequest(token string, now time.Time) (AuthRequest, bool) {
	if token == "" {
		return AuthRequest{}, false
	}
	var r AuthRequest
	var provID sql.NullInt64
	var kind, reqID, nonce, verifier, username, target sql.NullString
	err := s.queryRow(`SELECT provider_id,kind,req_id,nonce,verifier,username,target
		FROM sso_auth_requests WHERE token=? AND expires_at>?`, token, now.Unix()).
		Scan(&provID, &kind, &reqID, &nonce, &verifier, &username, &target)
	if err != nil {
		return AuthRequest{}, false
	}
	r.Token, r.ProviderID, r.Kind = token, provID.Int64, kind.String
	r.ReqID, r.Nonce, r.Verifier = reqID.String, nonce.String, verifier.String
	r.Username, r.Target = username.String, target.String
	return r, true
}

// MarkAssertionSeen records a SAML assertion id and reports whether this is its FIRST sighting.
// A false return means replay, and the caller must refuse before minting anything.
//
// The key is a hash of the IdP entity id and the assertion id, so one provider cannot pre-poison
// another's id space, and no raw identifier is stored. The insert-or-nothing is atomic for the same
// reason consumption above is: two concurrent POSTs of one assertion must produce exactly one win.
func (s *Store) MarkAssertionSeen(idpEntityID, assertionID string, expires time.Time) bool {
	if idpEntityID == "" || assertionID == "" {
		return false
	}
	sum := sha256.Sum256([]byte(idpEntityID + "\x00" + assertionID))
	res, err := s.exec(`INSERT INTO sso_assertion_seen(seen_key,expires_at) VALUES(?,?)
		ON CONFLICT(seen_key) DO NOTHING`, hex.EncodeToString(sum[:]), expires.Unix())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// authStateSweepInterval is how often expired pending logins and replay entries are dropped.
const authStateSweepInterval = 15 * time.Minute

// authSweepLoop drops expired auth state on its own always-on tick.
//
// This deliberately does NOT ride along with the storage-retention pass (ADR 0017): that pass only
// runs when an admin has configured retention, so in the shipped default configuration the replay
// cache and the pending-login table would grow without bound forever. Expiring ephemeral rows is
// hygiene the operator never opted into and should not have to.
func (s *Server) authSweepLoop() {
	for {
		if reqs, seen, err := s.st.PurgeExpiredAuthState(time.Now()); err != nil {
			log.Printf("auth sweep: %v", err)
		} else if reqs+seen > 0 {
			log.Printf("auth sweep: dropped %d expired pending logins, %d replay entries", reqs, seen)
		}
		time.Sleep(authStateSweepInterval)
	}
}
