package app

import (
	"sync"
	"time"
)

// appTokenScopes is the set of API scopes the iframe bridge may grant an app in
// this phase. Only read/query access is exposed; write scopes (ingest, …) are
// deferred to the install-time permission prompt in phase 2 of
// docs/adr/0003-downloadable-apps.md, so an installed app can never mutate data.
var appTokenScopes = map[string]bool{"query": true}

// grantableScopes filters an app's requested scopes down to the ones this phase
// permits (deduplicated). An app that declares only unsupported scopes gets none.
func grantableScopes(req []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range req {
		if appTokenScopes[s] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// appTokens is an in-memory registry of short-lived, scoped bearer tokens minted
// when a user opens an app. The token authorizes the host-mediated /api/v1 bridge;
// it is held by the trusted host page and never handed to the sandboxed iframe.
// Tokens are ephemeral by design — they live only in memory (a restart drops them,
// the host simply re-mints on next open) and never touch the api_tokens table.
type appTokens struct {
	mu  sync.Mutex
	m   map[string]appTokenEntry
	ttl time.Duration
}

type appTokenEntry struct {
	scopes  map[string]bool
	expires time.Time
}

func newAppTokens(ttl time.Duration) *appTokens {
	return &appTokens{m: map[string]appTokenEntry{}, ttl: ttl}
}

// mint issues a token limited to the given scopes and returns its string. now is
// injected so tests are deterministic; callers pass time.Now().
func (a *appTokens) mint(scopes []string, now time.Time) string {
	tok := randToken()
	set := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		set[s] = true
	}
	a.mu.Lock()
	a.pruneLocked(now)
	a.m[tok] = appTokenEntry{scopes: set, expires: now.Add(a.ttl)}
	a.mu.Unlock()
	return tok
}

// valid reports whether tok is live and covers the needed scope at time now.
func (a *appTokens) valid(tok, need string, now time.Time) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.m[tok]
	if !ok || now.After(e.expires) {
		if ok {
			delete(a.m, tok)
		}
		return false
	}
	return need == "" || e.scopes[need]
}

func (a *appTokens) pruneLocked(now time.Time) {
	for k, e := range a.m {
		if now.After(e.expires) {
			delete(a.m, k)
		}
	}
}
