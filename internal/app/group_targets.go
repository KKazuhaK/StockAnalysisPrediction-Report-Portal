package app

import (
	"encoding/json"
	"strings"
)

// Per-OU run allow-list — ADR 0022 R3. A restricted OU may run only the targets its allow-list
// grants, on only the surfaces that row permits. Internal (unrestricted) OUs ignore the table
// entirely, so nothing here changes behavior until a restricted OU exists.

// GroupTarget is one allow-list row: this OU may run TargetID, limited to Surfaces (a comma-
// separated subset of run|batch|recurring|chat; "" = every surface the target itself allows).
type GroupTarget struct {
	TargetID int64
	Surfaces string
}

// GroupTargets returns one OU's own allow-list rows (no inheritance — see resolveGroupTargets).
func (s *Store) GroupTargets(groupID int64) []GroupTarget {
	rows, err := s.query(`SELECT target_id, COALESCE(surfaces,'') FROM group_targets WHERE group_id=? ORDER BY target_id`, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []GroupTarget
	for rows.Next() {
		var g GroupTarget
		if rows.Scan(&g.TargetID, &g.Surfaces) == nil {
			out = append(out, g)
		}
	}
	return out
}

// SetGroupTargets replaces an OU's whole allow-list in one transaction, so a save is atomic and
// can never leave a half-applied grant.
func (s *Store) SetGroupTargets(groupID int64, rows []GroupTarget) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind(`DELETE FROM group_targets WHERE group_id=?`), groupID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(s.bind(`INSERT INTO group_targets(group_id,target_id,surfaces) VALUES(?,?,?)`),
			groupID, r.TargetID, strings.TrimSpace(r.Surfaces)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// resolveGroupTargets returns the allow-list that applies to a user: the nearest OU on their
// ancestry chain (leaf → root) that defines one wins outright — a nearer OU's list is an override,
// not an addition, so a sub-team can be narrowed without touching its parent. Returns nil when no
// OU on the chain defines a list, which for a restricted user means default-deny.
func (s *Store) resolveGroupTargets(username string) []GroupTarget {
	chain := s.groupChain(username) // root → leaf
	for i := len(chain) - 1; i >= 0; i-- {
		if rows := s.GroupTargets(chain[i]); len(rows) > 0 {
			return rows
		}
	}
	return nil
}

// runAllowed reports whether a user may run a target on a surface. Unrestricted users (internal
// staff, admins) always may — the allow-list is a restricted-OU concept. A restricted user is
// default-deny: the target must appear in the resolved allow-list, and the surface must be in both
// that row's subset and the target's own global surfaces.
func (s *Server) runAllowed(user string, targetID int64, surface string) bool {
	if s.viewerScope(user) == nil {
		return true
	}
	for _, g := range s.st.resolveGroupTargets(user) {
		if g.TargetID != targetID {
			continue
		}
		if g.Surfaces != "" && !AllowsSurface(g.Surfaces, surface) {
			return false
		}
		t, ok := s.st.GetTarget(targetID)
		return ok && AllowsSurface(t.Surfaces, surface)
	}
	return false
}

// targetOutputSubtype reads the report subtype a target produces. It lives in the target's existing
// config JSON rather than a new column (ADR 0022 D6), and feeds both the same-day reuse key and the
// restricted read filter, keeping "what you may run" and "what you may see today" consistent.
func targetOutputSubtype(config string) string {
	var m struct {
		OutputSubtype string `json:"output_subtype"`
	}
	json.Unmarshal([]byte(config), &m)
	return strings.TrimSpace(m.OutputSubtype)
}

// allowedSubtypes lists the report subtypes a restricted user's OU is entitled to. nil means "no
// restriction" — an internal user/admin, or a restricted OU with no allow-list at all (nothing to
// narrow by). Restricted callers share entitledSubtypes with the read filter, so "what you may run"
// and "what you may see today" can never drift apart.
func (s *Server) allowedSubtypes(user string) []string {
	if s.viewerScope(user) == nil {
		return nil
	}
	return s.entitledSubtypes(user)
}
