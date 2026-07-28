package app

import (
	"sort"
	"strings"
)

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

// FindSameDayReport looks for a report already generated today for a (symbol, subtype, version)
// request. It powers "if it was already generated today, show it instead of running".
//
// Deliberately NOT scoped to the caller (ADR 0024). This is a content lookup — does this analysis
// exist today — and the caller's entitlement is checked by reuseSameDayReport before it asks. Making
// the lookup itself viewer-scoped is what made reuse inert under per-person visibility: the person
// who has not asked before is on no viewer list, so nothing was ever found for exactly the caller
// reuse exists to serve.
//
// The newest match wins (D4) — two same-day reports for one (symbol, subtype, version) differ only
// by title, which is generator output the requester cannot predict.
func (s *Store) FindSameDayReport(symbol, subtype, version, panelToday string) (int64, bool) {
	q := `SELECT id FROM reports WHERE symbol=? AND rtype=? AND rdate=? AND version=?`
	args := []any{symbol, subtype, panelToday, version}
	var id int64
	if err := s.queryRow(q, args...).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
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

// ownerScope restricts which reports a scoped (external) viewer may READ (ADR 0024, superseding the
// ADR 0022 R1 predicate). A nil *ownerScope means no restriction — internal users, admins, and
// machine/Bearer callers — so every scoped query appends nothing and stays byte-for-byte identical.
//
// The rule is two conditions, and nothing else. The report's version must be GRANTED to this
// reader, and the version's visibility must admit them: they asked for it (owner), someone in their
// OU did (group), or it does not matter (all). What used to decide this is gone — there is no
// same-day internal pool, and no narrowing by which subtypes you may run. Read permission no longer
// derives from run permission, which is what makes a read-only client possible at all.
//
// report_viewers is the ONLY table consulted for ownership. owner_group survives on the report row
// for attribution and audit, but the security-critical filter must not have two spellings.
type ownerScope struct {
	// principals identify this reader for the ownership test: always "u:<name>", plus "g:<id>" for
	// each OU up their chain when a version shares within the OU.
	self       string
	principals []string
	// The granted versions, split by what their visibility demands, so the predicate is a plain OR
	// of at most three IN-lists rather than a per-row lookup of the registry.
	versAll, versGroup, versOwner []string
}

// where returns an AND-joinable boolean fragment (no WHERE keyword) that scopes reports to the
// viewer, plus its args. prefix qualifies the columns ("r." for aliased queries, "" for a bare
// table). A nil scope returns ("", nil) so callers append nothing and behave exactly as before.
func (sc *ownerScope) where(prefix string) (string, []any) {
	if sc == nil {
		return "", nil
	}
	var ors []string
	var args []any
	in := func(vals []string) (string, []any) {
		ph := make([]string, len(vals))
		out := make([]any, len(vals))
		for i, v := range vals {
			ph[i], out[i] = "?", v
		}
		return prefix + "version IN (" + strings.Join(ph, ",") + ")", out
	}
	// A version whose reports are open to everyone granted it needs no ownership test at all.
	if len(sc.versAll) > 0 {
		frag, a := in(sc.versAll)
		ors = append(ors, frag)
		args = append(args, a...)
	}
	// The other two differ only in WHICH principals count, so they share one shape.
	owned := func(vals []string, principals []string) {
		if len(vals) == 0 || len(principals) == 0 {
			return
		}
		frag, a := in(vals)
		ph := make([]string, len(principals))
		for i, p := range principals {
			ph[i] = "?"
			a = append(a, p)
		}
		ors = append(ors, "("+frag+" AND EXISTS(SELECT 1 FROM report_viewers rv WHERE rv.report_id="+
			prefix+"id AND rv.principal IN ("+strings.Join(ph, ",")+")))")
		args = append(args, a...)
	}
	owned(sc.versGroup, sc.principals)
	owned(sc.versOwner, []string{sc.self})
	if len(ors) == 0 {
		// Granted nothing: default-deny, spelled so it cannot be mistaken for "no filter".
		return "1=0", nil
	}
	return "(" + strings.Join(ors, " OR ") + ")", args
}

// viewerScope resolves the read scope for a browser (cookie-session) user. It returns nil — no
// restriction — for anonymous callers, admins, and any user who is not scoped, so internal behaviour
// is unchanged. A scoped user carries their granted versions, bucketed by visibility.
func (s *Server) viewerScope(user string) *ownerScope {
	if !s.isRestricted(user) {
		return nil
	}
	sc := &ownerScope{self: userPrincipal(user)}
	sc.principals = append(sc.principals, sc.self)
	for _, g := range s.st.groupChain(user) {
		sc.principals = append(sc.principals, groupPrincipal(g))
	}
	for _, name := range s.st.GrantedVersions(user) {
		v, ok := s.st.Version(name)
		if !ok {
			continue // a version nobody registered grants nothing
		}
		switch v.Visibility {
		case VisibilityAll:
			sc.versAll = append(sc.versAll, name)
		case VisibilityGroup:
			sc.versGroup = append(sc.versGroup, name)
		default:
			sc.versOwner = append(sc.versOwner, name)
		}
	}
	return sc
}

// entitledSubtypes narrows the same-day pool to the report types the user's OU may run. It returns
// nil when the OU has no allow-list at all (nothing to narrow by — the pool stays open, matching
// pre-P4 behavior), and a non-nil (possibly empty) slice once a list exists.
func (s *Server) entitledSubtypes(user string) []string {
	if len(s.st.resolveGroupTargets(user)) == 0 {
		return nil
	}
	subs := []string{}
	seen := map[string]bool{}
	for _, g := range s.st.resolveGroupTargets(user) {
		t, ok := s.st.GetTarget(g.TargetID)
		if !ok {
			continue
		}
		if sub := targetOutputSubtype(t.Config); sub != "" && !seen[sub] {
			seen[sub] = true
			subs = append(subs, sub)
		}
	}
	sort.Strings(subs) // stable order keeps the generated SQL (and its query plan) deterministic
	return subs
}
