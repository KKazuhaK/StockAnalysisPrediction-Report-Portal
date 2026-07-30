package app

// Helpers that only the tests need.
//
// Each of these shipped in the production binary with no production caller — a deadcode sweep found
// them. They are not dead in the sense of "delete me": the tests genuinely want them, to mint a
// session cookie without going through the login handler, or to read state the product only ever
// reads as part of a larger query. Keeping them here says which is which, and keeps them out of the
// binary. A method may be declared in a _test.go file of the same package, so nothing else changes.

// sign mints a session cookie for an existing account.
func (s *Server) sign(user string) string {
	u := s.st.GetUser(user)
	if u == nil {
		return ""
	}
	return s.signUser(*u)
}

// signUser mints a session cookie for a User value, including one no store holds — which is how the
// owner-token tests forge a session that must be rejected.
func (s *Server) signUser(u User) string { return s.signUserFor(u, sessionTTL) }

// sessionValid reports whether a cookie would still be accepted. Production never asks this
// directly; it resolves the user instead. The tests ask it to prove a cookie has been invalidated.
func (s *Server) sessionValid(cookie string) bool {
	user, rev := s.verify(cookie)
	if user == "" {
		return false
	}
	u := s.st.GetUser(user)
	return u != nil && u.Active && !s.accountExpired(u) && u.SessionRev == rev
}

// Cancelled reports whether a job is cancelling or cancelled. Production reads the status as part of
// the scheduler's own queries rather than one row at a time.
func (s *Store) Cancelled(jobID int64) (bool, error) {
	var status string
	if err := s.queryRow("SELECT status FROM batch_jobs WHERE id=?", jobID).Scan(&status); err != nil {
		return false, err
	}
	return status == "cancelling" || status == "cancelled", nil
}

// ListBatchJobs returns every job, newest first. The product always lists them filtered and paged.
func (s *Store) ListBatchJobs() []BatchJob {
	return s.queryBatchJobs(`ORDER BY b.id DESC`)
}

// CountTokens counts API tokens. The product lists them; only the tests want the bare count.
func (s *Store) CountTokens() (n int) {
	s.queryRow("SELECT COUNT(*) FROM api_tokens").Scan(&n)
	return
}
