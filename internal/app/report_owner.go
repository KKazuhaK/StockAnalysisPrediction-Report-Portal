package app

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
