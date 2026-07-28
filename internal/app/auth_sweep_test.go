package app

import (
	"testing"
	"time"
)

// TestPurgeExpiredAuthState proves the ephemeral auth tables self-clean: expired pending logins and
// expired replay-cache entries go, live ones stay. These rows are worthless once expired, so unlike
// the configurable retention targets this sweep is unconditional and is not an admin setting.
func TestPurgeExpiredAuthState(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Unix()

	mkReq := func(token string, exp int64) {
		if _, err := st.exec(`INSERT INTO sso_auth_requests(token,kind,created_at,expires_at) VALUES(?,?,?,?)`,
			token, "oidc", now-60, exp); err != nil {
			t.Fatal(err)
		}
	}
	mkSeen := func(key string, exp int64) {
		if _, err := st.exec(`INSERT INTO sso_assertion_seen(seen_key,expires_at) VALUES(?,?)`, key, exp); err != nil {
			t.Fatal(err)
		}
	}
	mkReq("live", now+300)
	mkReq("stale", now-300)
	mkSeen("live-assertion", now+300)
	mkSeen("stale-assertion", now-300)

	reqs, seen, err := st.PurgeExpiredAuthState(time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredAuthState: %v", err)
	}
	if reqs != 1 || seen != 1 {
		t.Errorf("purged %d requests / %d assertions, want 1 / 1", reqs, seen)
	}

	count := func(table string) int {
		var n int
		st.queryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		return n
	}
	if count("sso_auth_requests") != 1 || count("sso_assertion_seen") != 1 {
		t.Error("a live pending login or replay entry was purged early")
	}
	// A live pending login must still be there by name — purging the wrong row would silently
	// break in-flight logins rather than fail loudly.
	var tok string
	st.queryRow(`SELECT token FROM sso_auth_requests`).Scan(&tok)
	if tok != "live" {
		t.Errorf("surviving pending login = %q, want the unexpired one", tok)
	}
}
