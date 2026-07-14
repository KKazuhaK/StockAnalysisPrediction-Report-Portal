package app

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginThrottle rate-limits failed logins per IP and per account, in memory (single binary). It
// blunts online password brute-force and the bcrypt-per-request CPU-exhaustion vector: after
// loginFailMax failures against a key within loginFailWindow, that key is refused until the window
// rolls over. Critically, the caller (apiLogin) checks the password BEFORE consulting the per-account
// key, so a correct password always succeeds and clears the counters — a per-account limit can never
// lock a legitimate owner out of their own account (only the IP key hard-blocks before bcrypt).
type loginThrottle struct {
	mu   sync.Mutex
	recs map[string]*failRec
}

type failRec struct {
	n       int
	resetAt time.Time
}

const (
	loginFailWindow = 15 * time.Minute
	loginFailMax    = 10
)

func newLoginThrottle() *loginThrottle { return &loginThrottle{recs: map[string]*failRec{}} }

// blocked reports whether key has reached the failure ceiling within the current window.
func (l *loginThrottle) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.recs[key]
	return r != nil && now.Before(r.resetAt) && r.n >= loginFailMax
}

// record counts one failed attempt against key, (re)starting the window if it had lapsed. It also
// opportunistically prunes lapsed entries so the map can't grow unbounded with distinct attacker IPs.
func (l *loginThrottle) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recs) > 4096 {
		for k, r := range l.recs {
			if now.After(r.resetAt) {
				delete(l.recs, k)
			}
		}
		// If a burst of distinct live keys still exceeds the cap (nothing lapsed to prune), drop the
		// whole table rather than run an O(n) scan under the lock on every subsequent insert. This
		// only happens under a >4096-distinct-source flood; losing partial counters then is acceptable.
		if len(l.recs) > 4096 {
			l.recs = make(map[string]*failRec)
		}
	}
	r := l.recs[key]
	if r == nil || now.After(r.resetAt) {
		l.recs[key] = &failRec{n: 1, resetAt: now.Add(loginFailWindow)}
		return
	}
	r.n++
}

// reset clears a key after a successful login.
func (l *loginThrottle) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.recs, key)
}

// clientIP is the caller IP for throttling: the RemoteAddr host. X-Forwarded-For is deliberately NOT
// trusted — it is attacker-controlled on a directly-exposed deployment, so honoring it would let an
// attacker rotate the header to mint a fresh key per request and evade the per-IP limit entirely.
// Behind a trusted reverse proxy RemoteAddr is the proxy's IP (a shared bucket, hence the account key
// and password-first check carry the real protection); such deployments should also rate-limit at
// the proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
