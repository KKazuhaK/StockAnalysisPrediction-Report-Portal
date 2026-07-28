package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

func sha256New() hash.Hash { return sha256.New() }

// Envelope encryption for stored authentication secrets — the OIDC client secret and the SAML SP
// private key (ADR 0023). Two levels on purpose: a random data key (DEK) encrypts the secrets, and
// the DEK itself is wrapped under a key derived from config secret_key. Rotating secret_key then
// re-wraps ONE row instead of re-encrypting every stored secret.
//
// Everything here is stdlib plus x/crypto/hkdf, which is already a dependency.

const (
	sealPrefix = "enc:v1:" // keeps a sealed value distinguishable from a legacy plaintext one
	kekInfo    = "report-portal/sso/kek/v1"
)

// errNotSealed is returned for a value that is not a sealed box at all, so a caller can tell
// "this needs re-entering" from "this decrypted to the wrong thing".
var errNotSealed = errors.New("value is not sealed")

// dekCache memoizes the unwrapped DEK per Server. Unwrapping is cheap, but the secrets are read on
// every SSO login, and this keeps the key material in exactly one place.
type dekCache struct {
	once sync.Once
	key  []byte
	err  error
}

// kek derives the key-encryption key from config secret_key and the keyring's stored salt. HKDF
// (rather than using secret_key directly) gives domain separation from the session HMAC, which is
// keyed by the same secret.
func (s *Server) kek(salt []byte) ([]byte, error) {
	if s.cfg == nil || len(s.cfg.SecretKey) == 0 {
		return nil, errors.New("secret_key is not configured")
	}
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256New, []byte(s.cfg.SecretKey), salt, []byte(kekInfo)), out); err != nil {
		return nil, err
	}
	return out, nil
}

// dek returns the unwrapped data key, creating the keyring row on first use. Creation is lazy so a
// deployment that never enables SSO never grows a keyring.
func (s *Server) dek() ([]byte, error) {
	s.dekOnce.once.Do(func() { s.dekOnce.key, s.dekOnce.err = s.loadOrCreateDEK() })
	return s.dekOnce.key, s.dekOnce.err
}

func (s *Server) loadOrCreateDEK() ([]byte, error) {
	saltB64, wrapped, ok := s.st.Keyring()
	if ok {
		salt, err := base64.StdEncoding.DecodeString(saltB64)
		if err != nil {
			return nil, fmt.Errorf("keyring salt is corrupt: %w", err)
		}
		kek, err := s.kek(salt)
		if err != nil {
			return nil, err
		}
		// A failure here almost always means secret_key was rotated. Say so, so the operator is
		// told to re-enter the SSO secrets rather than seeing an opaque crypto error.
		dek, err := aesOpen(kek, wrapped, []byte("dek"))
		if err != nil {
			return nil, fmt.Errorf("cannot unwrap the SSO key (was secret_key rotated?): %w", err)
		}
		return dek, nil
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	kek, err := s.kek(salt)
	if err != nil {
		return nil, err
	}
	sealed, err := aesSeal(kek, dek, []byte("dek"))
	if err != nil {
		return nil, err
	}
	if err := s.st.SaveKeyring(base64.StdEncoding.EncodeToString(salt), sealed); err != nil {
		return nil, err
	}
	return dek, nil
}

// sealSecret encrypts a secret for one provider and one field. The (slug, purpose) pair is
// authenticated as additional data, so a sealed value cannot be moved to a different provider or a
// different column by editing the database.
func (s *Server) sealSecret(slug, purpose, plaintext string) (string, error) {
	dek, err := s.dek()
	if err != nil {
		return "", err
	}
	box, err := aesSeal(dek, []byte(plaintext), aad(slug, purpose))
	if err != nil {
		return "", err
	}
	return sealPrefix + box, nil
}

// openSecret reverses sealSecret. It fails loudly on a tampered, truncated, foreign-context or
// unsealed value — never returning a partial or plausible-looking result.
func (s *Server) openSecret(slug, purpose, sealed string) (string, error) {
	if !strings.HasPrefix(sealed, sealPrefix) {
		return "", errNotSealed
	}
	dek, err := s.dek()
	if err != nil {
		return "", err
	}
	out, err := aesOpen(dek, strings.TrimPrefix(sealed, sealPrefix), aad(slug, purpose))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// aad binds a ciphertext to where it is stored.
func aad(slug, purpose string) []byte { return []byte(slug + "\x00" + purpose) }

// Keyring returns the stored salt and wrapped DEK. ok=false when the keyring has not been created
// yet (no auth secret has ever been sealed).
func (s *Store) Keyring() (salt, wrappedDEK string, ok bool) {
	var sa, wd sql.NullString
	if err := s.queryRow(`SELECT salt, wrapped_dek FROM sso_keyring WHERE id=1`).Scan(&sa, &wd); err != nil {
		return "", "", false
	}
	if sa.String == "" || wd.String == "" {
		return "", "", false
	}
	return sa.String, wd.String, true
}

// PurgeExpiredAuthState drops expired pending logins and expired SAML replay entries. Both are
// ephemeral by construction — a pending login is single-use and short-lived, and a replay entry is
// only meaningful inside its assertion's validity window — so this is unconditional hygiene rather
// than a configurable retention policy, and it is deliberately absent from the cleanup audit counts.
// Returns how many rows each sweep removed.
func (s *Store) PurgeExpiredAuthState(now time.Time) (requests, assertions int64, err error) {
	cut := now.Unix()
	res, err := s.exec(`DELETE FROM sso_auth_requests WHERE expires_at < ?`, cut)
	if err != nil {
		return 0, 0, err
	}
	requests, _ = res.RowsAffected()
	res, err = s.exec(`DELETE FROM sso_assertion_seen WHERE expires_at < ?`, cut)
	if err != nil {
		return requests, 0, err
	}
	assertions, _ = res.RowsAffected()
	return requests, assertions, nil
}

// SaveKeyring writes the single keyring row. It never overwrites an existing one: doing so would
// orphan every secret already sealed under the current DEK.
func (s *Store) SaveKeyring(salt, wrappedDEK string) error {
	_, err := s.exec(`INSERT INTO sso_keyring(id,salt,wrapped_dek,kek_version,created_at)
		VALUES(1,?,?,1,?) ON CONFLICT(id) DO NOTHING`, salt, wrappedDEK, nowStr())
	return err
}

func aesSeal(key, plaintext, additional []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plaintext, additional)), nil
}

func aesOpen(key []byte, box string, additional []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(box)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("sealed value is truncated")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], additional)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
