package app

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestCreateTokenStoresOnlyDigestAndPrefix(t *testing.T) {
	st := newTestStore(t)
	const raw = "0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := st.CreateToken(raw, "test", "query", ""); err != nil {
		t.Fatal(err)
	}
	var plaintext, digest, prefix string
	if err := st.queryRow(`SELECT COALESCE(token,''),token_hash,token_prefix FROM api_tokens WHERE name='test'`).Scan(&plaintext, &digest, &prefix); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(raw))
	if plaintext != "" || digest != hex.EncodeToString(wantDigest[:]) || prefix != raw[:8] {
		t.Fatalf("stored token = plaintext:%q digest:%q prefix:%q", plaintext, digest, prefix)
	}
	if !st.TokenValid(raw, "query") {
		t.Fatal("digest-backed token was rejected")
	}
	listed := st.ListTokens()
	if len(listed) != 1 || listed[0].Prefix != raw[:8] {
		t.Fatalf("listed tokens = %+v", listed)
	}
}

func TestLegacyPlaintextTokenRemainsUntouchedAndValid(t *testing.T) {
	st := newTestStore(t)
	const raw = "legacy-token-kept-in-place"
	if _, err := st.exec(`INSERT INTO api_tokens(token,name,scope,created_at) VALUES(?,?,?,?)`,
		raw, "legacy", "query", nowStr()); err != nil {
		t.Fatal(err)
	}
	if !st.TokenValid(raw, "query") {
		t.Fatal("legacy plaintext token was rejected")
	}
	var plaintext string
	var digest, prefix *string
	if err := st.queryRow(`SELECT token,token_hash,token_prefix FROM api_tokens WHERE name='legacy'`).Scan(
		&plaintext, &digest, &prefix); err != nil {
		t.Fatal(err)
	}
	if plaintext != raw || digest != nil || prefix != nil {
		t.Fatalf("legacy token was rewritten: plaintext=%q digest=%v prefix=%v", plaintext, digest, prefix)
	}
	listed := st.ListTokens()
	if len(listed) != 1 || listed[0].Prefix != raw[:8] {
		t.Fatalf("listed legacy token = %+v", listed)
	}
}

func TestTokenValidThrottlesLastUsedWrites(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateToken("token", "test", "query", ""); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-10 * time.Second).Format("2006-01-02 15:04:05")
	if _, err := st.exec("UPDATE api_tokens SET last_used_at=? WHERE token_hash=?", recent, tokenDigest("token")); err != nil {
		t.Fatal(err)
	}
	if !st.TokenValid("token", "query") {
		t.Fatal("valid token was rejected")
	}
	var got string
	if err := st.queryRow("SELECT last_used_at FROM api_tokens WHERE token_hash=?", tokenDigest("token")).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != recent {
		t.Fatalf("last_used_at changed inside throttle interval: got %q, want %q", got, recent)
	}

	stale := time.Now().Add(-2 * tokenLastUsedWriteInterval).Format("2006-01-02 15:04:05")
	if _, err := st.exec("UPDATE api_tokens SET last_used_at=? WHERE token_hash=?", stale, tokenDigest("token")); err != nil {
		t.Fatal(err)
	}
	if !st.TokenValid("token", "query") {
		t.Fatal("valid stale token was rejected")
	}
	if err := st.queryRow("SELECT last_used_at FROM api_tokens WHERE token_hash=?", tokenDigest("token")).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == stale {
		t.Fatal("last_used_at was not refreshed after throttle interval")
	}
}
