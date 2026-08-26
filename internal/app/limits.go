package app

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How long a session lasts and how often a caller may knock — the three ceilings an operator has a
// reason to move, edited on Manage -> Login protection with the rest of the login policy.
//
// All three were constants compiled into the binary. They are scalars, so they stay in `meta` (no
// table, no migration) and every one of them defaults to exactly what the constant did: an
// unconfigured portal behaves as it always has, and the settings only exist for the operator who
// wants something else.
//
// The rule throughout is that a value which says nothing leaves the working one alone. A save is
// refused rather than coerced, and a stored value that will not parse (hand-edited, or written by a
// build whose range differed) reads as the default. Neither may resolve to zero: a session lifetime
// of zero signs everybody out and a request ceiling of zero refuses every call, so "unset" and
// "forbid everything" must never be the same number. The one exception is the machine API's
// ceiling, where 0 genuinely means "no ceiling" — that is its shipped state, and turning a limit on
// for a portal that never asked for one would break a working integration on upgrade.
const (
	setSessionTTLHours    = "session_ttl_hours"
	setLoginFailMax       = "login_fail_max"
	setLoginFailWindowMin = "login_fail_window_min"
	setAPIV1RatePerMin    = "apiv1_rate_per_min"
)

// The shipped behaviour, and the fallback whenever a stored value cannot be read.
const (
	defLoginFailMax    = 10
	defLoginFailWindow = 15 * time.Minute
	// Ranges wide enough for any real policy and narrow enough that a typo cannot express one
	// nobody meant: an hour to a year of session, and a login window of a minute to a day.
	maxSessionTTLHours    = 24 * 365
	maxLoginFailMax       = 1000
	maxLoginFailWindowMin = 24 * 60
	maxAPIV1RatePerMin    = 100000
)

// settingInt reads a meta scalar as an int inside [lo, hi]. Anything else — absent, blank, not a
// number, out of range — is the default, never zero.
func (s *Server) settingInt(key string, def, lo, hi int) int {
	// Total by construction. These getters sit under cookie minting and under the request path, so
	// they answer for a server that has no store to ask — the same shape as the `s.loginThr != nil`
	// guards elsewhere. No configuration available reads as the shipped behaviour, which is what
	// the default is.
	if s.st == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.st.GetSetting(key, "")))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// sessionTTL is how long a portal session lasts. An SSO provider may still shorten it for its own
// users (session_hours), which is why signing takes a duration at all.
func (s *Server) sessionTTL() time.Duration {
	return time.Duration(s.settingInt(setSessionTTLHours, int(defaultSessionTTL/time.Hour), 1, maxSessionTTLHours)) * time.Hour
}

// loginLimits is the failed-login ceiling and the window it is counted in. It is handed to the
// throttle as a function rather than as two numbers, so a change on the settings page takes effect
// on the next attempt instead of at the next restart.
func (s *Server) loginLimits() (int, time.Duration) {
	max := s.settingInt(setLoginFailMax, defLoginFailMax, 1, maxLoginFailMax)
	window := s.settingInt(setLoginFailWindowMin, int(defLoginFailWindow/time.Minute), 1, maxLoginFailWindowMin)
	return max, time.Duration(window) * time.Minute
}

// apiV1RatePerMin is how many /api/v1 requests one source may make per minute. 0 = no ceiling,
// which is what an unconfigured portal has.
func (s *Server) apiV1RatePerMin() int {
	return s.settingInt(setAPIV1RatePerMin, 0, 0, maxAPIV1RatePerMin)
}

// ---------- the machine API's request ceiling ----------

// rateLimiter is a fixed-window request counter, in memory (single binary), shaped like the login
// throttle next to it. A fixed window can pass up to twice the ceiling across a window boundary;
// that is the accepted cost of a counter with no per-caller state beyond an int and a deadline, and
// it is the right trade for a ceiling whose job is to stop a runaway integration rather than to
// meter a paid API.
type rateLimiter struct {
	mu   sync.Mutex
	recs map[string]*rateRec
}

type rateRec struct {
	n       int
	resetAt time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{recs: map[string]*rateRec{}} }

// allow counts one request against key and reports whether it fits under limit in the current
// window, starting a fresh window when the old one has lapsed.
func (l *rateLimiter) allow(key string, limit int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// The same bound the login throttle keeps, for the same reason: a flood of distinct sources
	// must not grow this table without end. Prune what has lapsed; if a live burst still exceeds
	// the cap, drop the table rather than scan it under the lock on every insert.
	if len(l.recs) > 4096 {
		for k, r := range l.recs {
			if now.After(r.resetAt) {
				delete(l.recs, k)
			}
		}
		if len(l.recs) > 4096 {
			l.recs = make(map[string]*rateRec)
		}
	}
	r := l.recs[key]
	if r == nil || now.After(r.resetAt) {
		l.recs[key] = &rateRec{n: 1, resetAt: now.Add(window)}
		return true
	}
	if r.n >= limit {
		return false
	}
	r.n++
	return true
}

// rateLimitV1 caps how often one source may call the machine API, answering 429 above the ceiling.
//
// It runs BEFORE the handler's own token check, which is the point: a ceiling that only applied to
// authenticated callers would leave the unauthenticated flood — the one worth shedding — to do its
// work first. That is also why the key is the source address and not the presented token: a token
// nobody has validated yet is a string an attacker chooses, and keying on it would hand out a fresh
// budget per made-up token. Each source gets its own counter, so one busy integration cannot spend
// everybody else's allowance.
func (s *Server) rateLimitV1(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := s.apiV1RatePerMin()
		if limit > 0 && s.v1Rate != nil &&
			!s.v1Rate.allow("ip:"+clientIP(r, s.trustedNets), limit, time.Minute, time.Now()) {
			w.Header().Set("Retry-After", "60")
			jsonErrorCode(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		h(w, r)
	}
}

// ---------- admin wire ----------

// limitsJSON is the block the security page reads. Every value is resolved rather than raw, so the
// form shows what the portal is actually doing — including on a portal that has never saved these.
func (s *Server) limitsJSON() map[string]any {
	max, window := s.loginLimits()
	return map[string]any{
		"session_ttl_hours":     int(s.sessionTTL() / time.Hour),
		"login_fail_max":        max,
		"login_fail_window_min": int(window / time.Minute),
		"apiv1_rate_per_min":    s.apiV1RatePerMin(),
	}
}

// limitsInput is the save block. Pointers throughout: this endpoint clears most of its booleans by
// omission, which is right for a captcha toggle and wrong for a session lifetime, so a field that
// was not sent leaves the stored one alone.
type limitsInput struct {
	SessionTTLHours    *int `json:"session_ttl_hours"`
	LoginFailMax       *int `json:"login_fail_max"`
	LoginFailWindowMin *int `json:"login_fail_window_min"`
	APIV1RatePerMin    *int `json:"apiv1_rate_per_min"`
}

// applyLimits validates and stores the block, returning false once it has answered 400. Out of
// range is refused rather than clamped: an admin who typed 0 into the session lifetime asked for
// something the portal will not do, and silently storing 1 hour instead would leave them believing
// a policy they never wrote.
func (s *Server) applyLimits(w http.ResponseWriter, in *limitsInput) bool {
	type field struct {
		key      string
		val      *int
		lo, hi   int
		what     string
		zeroIsOK bool
	}
	fields := []field{
		{setSessionTTLHours, in.SessionTTLHours, 1, maxSessionTTLHours, "session_ttl_hours", false},
		{setLoginFailMax, in.LoginFailMax, 1, maxLoginFailMax, "login_fail_max", false},
		{setLoginFailWindowMin, in.LoginFailWindowMin, 1, maxLoginFailWindowMin, "login_fail_window_min", false},
		{setAPIV1RatePerMin, in.APIV1RatePerMin, 0, maxAPIV1RatePerMin, "apiv1_rate_per_min", true},
	}
	// Validate the whole block before storing any of it, so a bad fourth field cannot leave the
	// first three saved and the form showing a mixture of old and new.
	for _, f := range fields {
		if f.val == nil {
			continue
		}
		if *f.val < f.lo || *f.val > f.hi {
			jsonErrorCode(w, http.StatusBadRequest, "bad_limit", "无效的安全限制取值："+f.what)
			return false
		}
		if *f.val == 0 && !f.zeroIsOK {
			jsonErrorCode(w, http.StatusBadRequest, "bad_limit", "无效的安全限制取值："+f.what)
			return false
		}
	}
	for _, f := range fields {
		if f.val != nil {
			s.st.SetSetting(f.key, strconv.Itoa(*f.val))
		}
	}
	return true
}
