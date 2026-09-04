package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
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
	// The keyring is one salt and one wrapped key. It lived in a single-row table on the instinct
	// that key material deserves its own place; two rows in `meta` say the same thing and are the
	// shape every other single setting already uses.
	setKeyringSalt = "keyring_salt"
	setKeyringDEK  = "keyring_wrapped_dek"
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
	return kekFrom(s.cfg.SecretKey, salt)
}

// kekFrom is kek for an arbitrary secret, which rotation needs: re-wrapping means opening the
// keyring under the key it was sealed with and closing it under the one in force now.
func kekFrom(secret string, salt []byte) ([]byte, error) {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256New, []byte(secret), salt, []byte(kekInfo)), out); err != nil {
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
		dek, err := aesOpen(kek, wrapped, []byte("dek"))
		if err == nil {
			// The keyring opened under the key in force. A previous key still configured is now
			// dead weight — harmless, but it is a second live key sitting on disk.
			if s.cfg.SecretKeyPrevious != "" {
				log.Printf("keyring: secret_key_previous is set but not needed — the keyring already opens under the current secret_key; remove it from the config")
			}
			return dek, nil
		}
		// This is what a rotated secret_key looks like, and it is the whole reason the keyring is an
		// envelope: the data key that actually encrypts the secrets never changes, so a rotation has
		// to re-wrap ONE row rather than re-encrypt everything. That was the design and nothing
		// implemented it, so rotating left the data key permanently unopenable — SSO down, captcha
		// silently failing, and no page able to repair it, because saving a secret needs the very
		// key that will not open.
		if s.cfg.SecretKeyPrevious != "" {
			if dek, rerr := s.rewrapKeyring(salt, wrapped); rerr == nil {
				return dek, nil
			} else {
				log.Printf("keyring: secret_key_previous did not open the keyring either: %v", rerr)
			}
		}
		// Named remedies, not an opaque crypto error: one of these is always the answer, and an
		// operator reading this at 3am should not have to derive them.
		return nil, fmt.Errorf("cannot unwrap the SSO key — secret_key looks rotated. "+
			"Put the OLD key in secret_key_previous (or RP_SECRET_KEY_PREVIOUS) and restart once to "+
			"re-wrap it; or, if the old key is gone, delete the two keyring rows from `meta` "+
			"(%s, %s) and re-enter the SSO secrets, which cannot be recovered without it: %w",
			setKeyringSalt, setKeyringDEK, err)
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

// rewrapKeyring opens the keyring under the previous secret_key and closes it under the current one.
//
// The data key itself is unchanged, so every secret already sealed under it stays readable and
// nothing is re-encrypted — which is the property the envelope exists for. The salt is kept too: it
// is not secret, and a new secret_key already produces a different KEK through it.
func (s *Server) rewrapKeyring(salt []byte, wrapped string) ([]byte, error) {
	oldKEK, err := kekFrom(s.cfg.SecretKeyPrevious, salt)
	if err != nil {
		return nil, err
	}
	dek, err := aesOpen(oldKEK, wrapped, []byte("dek"))
	if err != nil {
		return nil, fmt.Errorf("the previous secret_key does not open the keyring: %w", err)
	}
	newKEK, err := s.kek(salt)
	if err != nil {
		return nil, err
	}
	sealed, err := aesSeal(newKEK, dek, []byte("dek"))
	if err != nil {
		return nil, err
	}
	if err := s.st.RewrapKeyring(sealed); err != nil {
		return nil, err
	}
	log.Printf("keyring: re-wrapped under the current secret_key; remove secret_key_previous from the config")
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
	salt = s.GetSetting(setKeyringSalt, "")
	wrappedDEK = s.GetSetting(setKeyringDEK, "")
	return salt, wrappedDEK, salt != "" && wrappedDEK != ""
}

// PurgeExpiredAuthState drops expired pending logins and expired SAML replay entries. Both are
// ephemeral by construction — a pending login is single-use and short-lived, and a replay entry is
// only meaningful inside its assertion's validity window — so this is unconditional hygiene rather
// than a configurable retention policy, and it is deliberately absent from the cleanup audit counts.
// Returns how many rows each sweep removed.
func (s *Store) PurgeExpiredAuthState(now time.Time) (requests, assertions int64, err error) {
	cut := now.Unix()
	res, err := s.exec(`DELETE FROM auth_requests WHERE expires_at < ?`, cut)
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

// SaveKeyring writes the single keyring row, refusing to overwrite one that already exists.
// Replacing it would make every secret sealed under the old DEK permanently unreadable, so the
// write is create-if-absent and a caller that raced loses harmlessly.
func (s *Store) SaveKeyring(salt, wrappedDEK string) error {
	if _, _, ok := s.Keyring(); ok {
		return nil
	}
	if err := s.SetSetting(setKeyringSalt, salt); err != nil {
		return err
	}
	return s.SetSetting(setKeyringDEK, wrappedDEK)
}

// RewrapKeyring replaces the wrapped data key, deliberately bypassing SaveKeyring's create-once
// guard. That guard is right for creation — two boots racing must not each mint a data key and leave
// half the secrets unreadable — and wrong for rotation, which is the one operation that must
// overwrite. The salt is untouched, so this changes exactly one row.
func (s *Store) RewrapKeyring(wrappedDEK string) error {
	return s.SetSetting(setKeyringDEK, wrappedDEK)
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
