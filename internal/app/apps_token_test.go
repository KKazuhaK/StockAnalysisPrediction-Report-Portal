package app

import (
	"testing"
	"time"
)

func TestAppTokens(t *testing.T) {
	at := newAppTokens(30 * time.Minute)
	t0 := time.Unix(1_700_000_000, 0)

	tok := at.mint([]string{"query"}, t0)
	if tok == "" {
		t.Fatal("mint returned empty token")
	}
	if !at.valid(tok, "query", t0) {
		t.Fatal("fresh token should be valid for its scope")
	}
	if at.valid(tok, "ingest", t0) {
		t.Fatal("a query token must not cover ingest")
	}
	if at.valid("bogus", "query", t0) {
		t.Fatal("unknown token must be invalid")
	}
	if at.valid("", "query", t0) {
		t.Fatal("empty token must be invalid")
	}
	if at.valid(tok, "query", t0.Add(31*time.Minute)) {
		t.Fatal("expired token must be invalid")
	}
}

// Only read/query scope may be granted in this phase; write scopes are dropped.
func TestGrantableScopes(t *testing.T) {
	g := grantableScopes([]string{"query", "ingest", "webhooks", "query"})
	if len(g) != 1 || g[0] != "query" {
		t.Fatalf("grantableScopes = %v, want [query]", g)
	}
	if len(grantableScopes(nil)) != 0 {
		t.Fatal("nil scopes should grant nothing")
	}
}
