package app

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func secretServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
}

// TestSealOpenRoundTrip locks the envelope encryption used for every stored auth secret (ADR 0023):
// a value round-trips, ciphertext is recognisable and never contains the plaintext, and each seal is
// distinct (a fresh nonce), so identical secrets don't produce identical ciphertext.
func TestSealOpenRoundTrip(t *testing.T) {
	s := secretServer(t)
	const secret = "app-super-secret-client-value"

	box, err := s.sealSecret("acme", "oidc_client_secret", secret)
	if err != nil {
		t.Fatalf("sealSecret: %v", err)
	}
	if !strings.HasPrefix(box, "enc:v1:") {
		t.Errorf("sealed value %q must carry the enc:v1: prefix so plaintext stays distinguishable", box)
	}
	if strings.Contains(box, secret) {
		t.Fatal("the plaintext leaked into the sealed value")
	}
	got, err := s.openSecret("acme", "oidc_client_secret", box)
	if err != nil || got != secret {
		t.Fatalf("openSecret = %q, %v; want the original", got, err)
	}

	again, err := s.sealSecret("acme", "oidc_client_secret", secret)
	if err != nil {
		t.Fatal(err)
	}
	if again == box {
		t.Error("sealing the same value twice must not produce identical ciphertext (nonce reuse)")
	}
}

// TestSealIsBoundToItsContext proves the AAD binding: a sealed secret cannot be moved to another
// provider or another field, so a database edit can't promote one provider's client secret into
// another's — or a low-value field into a high-value one.
func TestSealIsBoundToItsContext(t *testing.T) {
	s := secretServer(t)
	box, err := s.sealSecret("acme", "oidc_client_secret", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.openSecret("other", "oidc_client_secret", box); err == nil {
		t.Error("a secret sealed for one provider must not open for another")
	}
	if _, err := s.openSecret("acme", "saml_sp_key", box); err == nil {
		t.Error("a secret sealed for one field must not open as another")
	}
}

// TestOpenRejectsTampering proves an altered ciphertext fails loudly rather than returning garbage —
// AES-GCM authenticates, and the caller must be able to tell "needs re-entering" from "wrong value".
func TestOpenRejectsTampering(t *testing.T) {
	s := secretServer(t)
	box, err := s.sealSecret("acme", "saml_sp_key", "private-key-material")
	if err != nil {
		t.Fatal(err)
	}
	bad := box[:len(box)-2] + "AA"
	if _, err := s.openSecret("acme", "saml_sp_key", bad); err == nil {
		t.Error("a tampered ciphertext must not decrypt")
	}
	if _, err := s.openSecret("acme", "saml_sp_key", "not-even-sealed"); err == nil {
		t.Error("a non-sealed value must be rejected, not treated as plaintext")
	}
	if _, err := s.openSecret("acme", "saml_sp_key", ""); err == nil {
		t.Error("an empty value must be rejected")
	}
}

// TestKeyringIsStableAndLazy proves the DEK is created once on first use and then reused, so
// restarting the server (or a second Server value against the same store) can still open existing
// secrets — the failure mode that would otherwise silently invalidate every stored credential.
func TestKeyringIsStableAndLazy(t *testing.T) {
	st := newTestStore(t)
	cfg := &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	s1 := &Server{st: st, cfg: cfg}

	var n int
	st.queryRow(`SELECT COUNT(*) FROM sso_keyring`).Scan(&n)
	if n != 0 {
		t.Fatalf("the keyring must not be created until a secret is sealed, found %d rows", n)
	}
	box, err := s1.sealSecret("acme", "saml_sp_key", "k")
	if err != nil {
		t.Fatal(err)
	}
	st.queryRow(`SELECT COUNT(*) FROM sso_keyring`).Scan(&n)
	if n != 1 {
		t.Fatalf("keyring rows = %d, want exactly 1", n)
	}

	// A fresh Server over the same store — i.e. after a restart — must still open it.
	s2 := &Server{st: st, cfg: cfg}
	if got, err := s2.openSecret("acme", "saml_sp_key", box); err != nil || got != "k" {
		t.Fatalf("after restart openSecret = %q, %v; want the original", got, err)
	}

	// A different secret_key must NOT open it — the DEK is wrapped under a key derived from it.
	s3 := &Server{st: st, cfg: &config.Config{SecretKey: "ffffffffffffffffffffffffffffffff"}}
	if _, err := s3.openSecret("acme", "saml_sp_key", box); err == nil {
		t.Error("a rotated secret_key must not silently open existing secrets")
	}
}
