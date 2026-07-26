package app

import "time"

// Report ownership (ADR 0022 R1). A report is stamped with the OU that generated it so a restricted
// external viewer sees only its own OU's reports (plus the same-day internal pool). Ownership is
// stamped server-side from a signed token (mint/verify live on Server), never from a client field.

// OwnerGroupOf resolves the OU that owns a user's output: their primary group, or the Default group
// when unassigned. Returns 0 only if there is no Default group (which EnsureDefaultGroup prevents).
func (s *Store) OwnerGroupOf(username string) int64 {
	if gid := s.PrimaryGroupOf(username); gid != 0 {
		return gid
	}
	return s.DefaultGroupID()
}

// StampReportOwner sets a report's owning OU first-writer-wins: it writes only while owner_group is
// still NULL, so a re-ingest of the shared identity row — or a second OU racing the same request —
// never reassigns an already-attributed report. Reports whether this call did the stamping. ou 0
// (no resolvable OU) is a no-op.
func (s *Store) StampReportOwner(id, ou int64) (bool, error) {
	if ou == 0 {
		return false, nil
	}
	res, err := s.exec(`UPDATE reports SET owner_group=? WHERE id=? AND owner_group IS NULL`, ou, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ownerTokenInput is the reserved Dify input key that carries the owner-attribution token into a
// restricted OU's run. The instrumented workflow passes it straight through into its
// /api/v1/reports payload as owner_token. Injected ONLY for restricted OUs, so internal runs and
// their (possibly undeclared-variable-strict) workflows never see it.
const ownerTokenInput = "_rp_owner_token"

// runInputs returns the Dify inputs to send for a job's item. For a restricted OU's run it adds the
// signed owner-attribution token (mintOwnerToken) under ownerTokenInput, so the produced report is
// stamped to that OU at ingest (ADR 0022 R1). For every internal run it returns the caller's inputs
// unchanged — the money-path stays byte-for-byte identical — and it never mutates the caller's map.
func (s *Server) runInputs(job BatchJob, inputs map[string]string) map[string]string {
	if !s.st.EffectiveGroupSettings(job.CreatedBy).Restricted {
		return inputs
	}
	tok := s.mintOwnerToken(s.st.OwnerGroupOf(job.CreatedBy))
	if tok == "" {
		return inputs
	}
	out := make(map[string]string, len(inputs)+1)
	for k, v := range inputs {
		out[k] = v
	}
	out[ownerTokenInput] = tok
	return out
}

// ownerScope restricts which reports a restricted (external) viewer may READ (ADR 0022 R1). A nil
// *ownerScope means no restriction — internal users, admins, and machine/Bearer callers — so every
// scoped query appends nothing and stays byte-for-byte identical. For a restricted viewer only its
// own OU's reports plus the same-day INTERNAL pool are visible. Because the owner token is injected
// for restricted OUs only (see runInputs), an internal run never stamps an owner: every internal
// report is NULL-owner, so the same-day internal pool is exactly `owner_group IS NULL` and needs no
// internal-OU id set (the reason this can be a 2-field predicate, not an IN-list).
type ownerScope struct {
	myOU       int64
	panelToday string // panel-tz civil date "YYYY-MM-DD"
}

// where returns an AND-joinable boolean fragment (no WHERE keyword) that scopes reports to the
// viewer, plus its args. prefix qualifies the columns ("r." for aliased queries, "" for a bare
// table). A nil scope returns ("", nil) so callers append nothing and behave exactly as before.
func (sc *ownerScope) where(prefix string) (string, []any) {
	if sc == nil {
		return "", nil
	}
	frag := "(" + prefix + "owner_group = ? OR (" + prefix + "rdate = ? AND " + prefix + "owner_group IS NULL))"
	return frag, []any{sc.myOU, sc.panelToday}
}

// viewerScope resolves the read scope for a browser (cookie-session) user. It returns nil — no
// restriction — for anonymous callers, admins (PermManage-exempt, matching the run-governance
// exemption), and any user whose effective OU is not restricted, so internal behavior is unchanged.
// A restricted user is pinned to their own OU (OwnerGroupOf) and the panel-tz civil "today".
func (s *Server) viewerScope(user string) *ownerScope {
	if user == "" || s.isAdmin(user) {
		return nil
	}
	if !s.st.EffectiveGroupSettings(user).Restricted {
		return nil
	}
	return &ownerScope{
		myOU:       s.st.OwnerGroupOf(user),
		panelToday: time.Now().In(s.panelLocation()).Format("2006-01-02"),
	}
}
