package app

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// /healthz answers "should this instance be receiving traffic?", which is a question about the
// database as much as about the process.
//
// It used to answer `{"ok":true}` unconditionally, which is a probe that can only ever report the
// one failure an orchestrator can already see for itself — the process being gone. With Postgres in
// its own container (the shipped compose stack), the interesting failure is precisely the one it
// could not see: the portal up, the database unreachable, every page an error, and the healthcheck
// green the whole time.
//
// Two properties it must have, because the endpoint is public and unauthenticated:
//
//   - Cheap under repetition. The verdict is cached for healthCacheFor, so a probe every second (or
//     a deliberate flood) costs at most one `SELECT 1` per interval. That also bounds the SQLite
//     case, where the pool is a single connection and an uncached probe would queue behind whatever
//     write is in flight.
//   - Silent about detail. A failure logs the driver's message for the operator and returns a bare
//     "unreachable" to the caller: the error text can carry the DSN's host, user and database name.
const (
	healthTimeout  = 3 * time.Second
	healthCacheFor = 2 * time.Second
)

// healthCache memoizes the last database verdict, so repeated probes do not each become a query.
type healthCache struct {
	mu   sync.Mutex
	at   time.Time
	err  error
	seen bool
}

// dbHealth pings the database, reusing a verdict younger than healthCacheFor.
func (s *Server) dbHealth() error {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.seen && time.Since(s.health.at) < healthCacheFor {
		return s.health.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	err := s.st.Ping(ctx)
	s.health.at, s.health.err, s.health.seen = time.Now(), err, true
	return err
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.dbHealth(); err != nil {
		// Logged, not returned: an operator needs the driver's message and an anonymous caller must
		// not have it. Every probe while the database is down logs a line, which is the point —
		// the outage should be legible in `docker compose logs` without anyone asking for it.
		log.Printf("healthz: database is not reachable: %v", err)
		w.Header().Set("Cache-Control", "no-store")
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": "unreachable"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"ok": true, "db": "ok"})
}

// Ping checks that the database is answering. Separate from database/sql's own Ping so the store
// stays the only thing that knows there is a *sql.DB in here.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}
