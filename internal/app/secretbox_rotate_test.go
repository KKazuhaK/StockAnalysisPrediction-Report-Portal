package app

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

const (
	rotOldKey   = "0123456789abcdef0123456789abcdef"
	rotNewKey   = "fedcba9876543210fedcba9876543210"
	rotWrongKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// sealedUnder creates a keyring under `key` and returns the store plus two secrets sealed with it,
// which is the state every rotation test starts from.
func sealedUnder(t *testing.T, key string) (*Store, string, string) {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: key}}
	oidc, err := s.sealSecret("acme", "oidc_client_secret", "client-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	saml, err := s.sealSecret("acme", "saml_sp_key", "private-key-material")
	if err != nil {
		t.Fatal(err)
	}
	return st, oidc, saml
}

// TestRotateSecretKeyRewrapsKeyring is the property the envelope exists for and that nothing
// implemented until now: rotating secret_key with the old key named in secret_key_previous re-wraps
// the ONE keyring row, leaves the data key itself untouched, and so keeps every already-sealed
// secret readable — no re-encryption, no re-entering.
func TestRotateSecretKeyRewrapsKeyring(t *testing.T) {
	st, oidc, saml := sealedUnder(t, rotOldKey)
	_, wrappedBefore, _ := st.Keyring()
	saltBefore, _, _ := st.Keyring()

	s := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotOldKey}}
	if got, err := s.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("after rotation openSecret = %q, %v; want the original", got, err)
	}
	if got, err := s.openSecret("acme", "saml_sp_key", saml); err != nil || got != "private-key-material" {
		t.Fatalf("the second secret must survive the same re-wrap: %q, %v", got, err)
	}

	saltAfter, wrappedAfter, ok := st.Keyring()
	if !ok {
		t.Fatal("the keyring must still exist after a re-wrap")
	}
	if wrappedAfter == wrappedBefore {
		t.Error("the wrapped data key must have been rewritten under the new secret_key")
	}
	if saltAfter != saltBefore {
		t.Error("the salt must be kept: it is not secret, and a new secret_key already yields a different KEK through it")
	}
}

// TestRotationPersistsForTheNextBoot proves the re-wrap is durable rather than a per-process repair:
// once one boot has run with secret_key_previous set, the NEXT boot opens the keyring with the new
// key alone — which is what lets an operator delete secret_key_previous from the config.
func TestRotationPersistsForTheNextBoot(t *testing.T) {
	st, oidc, _ := sealedUnder(t, rotOldKey)

	rewrap := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotOldKey}}
	if _, err := rewrap.dek(); err != nil {
		t.Fatalf("the re-wrap boot must succeed: %v", err)
	}

	next := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey}}
	if got, err := next.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("the boot after the re-wrap must open the keyring with the new key alone: %q, %v", got, err)
	}

	// And the old key must no longer work — a re-wrap is a rotation, not a second accepted key.
	stale := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
	if _, err := stale.dek(); err == nil {
		t.Error("the retired secret_key must stop opening the keyring once it has been re-wrapped")
	}
}

// TestRotationWithoutPreviousKeyIsRefusedAndNamesTheRemedies covers the operator's actual failure:
// secret_key changed and nothing else. It must fail — anything else would be a silent new data key
// orphaning the stored secrets — and the error must name both ways out, because at that point no
// admin page can help (saving a secret needs the very key that will not open).
func TestRotationWithoutPreviousKeyIsRefusedAndNamesTheRemedies(t *testing.T) {
	st, oidc, _ := sealedUnder(t, rotOldKey)
	saltBefore, wrappedBefore, _ := st.Keyring()

	s := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey}}
	_, err := s.dek()
	if err == nil {
		t.Fatal("a rotated secret_key with no previous key must not silently mint a new keyring")
	}
	for _, want := range []string{"secret_key_previous", setKeyringSalt, setKeyringDEK} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q so the remedy is readable at 3am; got: %v", want, err)
		}
	}

	saltAfter, wrappedAfter, ok := st.Keyring()
	if !ok || saltAfter != saltBefore || wrappedAfter != wrappedBefore {
		t.Fatal("a refused rotation must leave the keyring exactly as it was")
	}
	// The decisive part: the old key still opens everything, so putting it back is a real recovery.
	back := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
	if got, err := back.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("restoring the old secret_key must recover the secrets: %q, %v", got, err)
	}
}

// TestRotationWithTheWrongPreviousKeyIsRefused proves the re-wrap authenticates rather than trusting
// the config: a mistyped or unrelated previous key must fail, and must not overwrite the keyring
// with something derived from it.
func TestRotationWithTheWrongPreviousKeyIsRefused(t *testing.T) {
	st, oidc, _ := sealedUnder(t, rotOldKey)
	saltBefore, wrappedBefore, _ := st.Keyring()

	s := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotWrongKey}}
	if _, err := s.dek(); err == nil {
		t.Fatal("a previous key that does not open the keyring must be refused")
	}

	saltAfter, wrappedAfter, _ := st.Keyring()
	if saltAfter != saltBefore || wrappedAfter != wrappedBefore {
		t.Fatal("a failed re-wrap must not touch the keyring")
	}
	back := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
	if got, err := back.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("the real key must still work after a wrong-previous-key attempt: %q, %v", got, err)
	}
}

// TestPreviousKeyIsHarmlessWhenNotNeeded covers the config an operator leaves behind after a
// successful rotation: secret_key_previous still set, but the keyring already opens under the current
// key. That must be a no-op (with a warning in the log), never a re-wrap back to the old key.
func TestPreviousKeyIsHarmlessWhenNotNeeded(t *testing.T) {
	st, oidc, _ := sealedUnder(t, rotOldKey)
	_, wrappedBefore, _ := st.Keyring()

	s := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey, SecretKeyPrevious: rotWrongKey}}
	if got, err := s.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("a stale secret_key_previous must not disturb a keyring that already opens: %q, %v", got, err)
	}
	if _, wrappedAfter, _ := st.Keyring(); wrappedAfter != wrappedBefore {
		t.Error("nothing should have been rewritten: the current key already opened the keyring")
	}
}

// TestRotationLeavesTheDataKeyUnchanged is the reason the design is an envelope at all. A secret
// sealed BEFORE the rotation and one sealed AFTER it must both open under the same server, which can
// only be true if the data key survived the re-wrap unchanged.
func TestRotationLeavesTheDataKeyUnchanged(t *testing.T) {
	st, oidc, _ := sealedUnder(t, rotOldKey)

	s := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotOldKey}}
	fresh, err := s.sealSecret("acme", "saml_sp_key", "sealed-after-the-rotation")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.openSecret("acme", "oidc_client_secret", oidc); err != nil || got != "client-secret-value" {
		t.Fatalf("pre-rotation secret: %q, %v", got, err)
	}
	if got, err := s.openSecret("acme", "saml_sp_key", fresh); err != nil || got != "sealed-after-the-rotation" {
		t.Fatalf("post-rotation secret: %q, %v", got, err)
	}
	// Both must also survive the next boot, under the new key alone.
	next := &Server{st: st, cfg: &config.Config{SecretKey: rotNewKey}}
	if _, err := next.openSecret("acme", "oidc_client_secret", oidc); err != nil {
		t.Errorf("pre-rotation secret after reboot: %v", err)
	}
	if _, err := next.openSecret("acme", "saml_sp_key", fresh); err != nil {
		t.Errorf("post-rotation secret after reboot: %v", err)
	}
}
