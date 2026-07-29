package app

import (
	"database/sql"
	"fmt"
)

// User-admin persistence: extended profile attributes (display name / email / active /
// last login) and the single primary group are columns on the `users` table (folded from
// the former user_profiles + user_primary_group side tables, ADR 0013); organizational-group
// settings live in user_groups. Groups are labels only — permissions still come from the role.

// UserGroup is an organizational group whose settings decide its members' run
// behavior (group model B, docs/adr/0010-group-model.md). Every user has at most one
// primary group; users without one inherit the Default group. A non-default group can
// override each field or inherit it from the Default group (the *Inherit flags below
// say which); the Default group always holds concrete baselines.
type UserGroup struct {
	ID          int64
	Name        string
	Description string
	Created     string
	IsDefault   bool
	Weight      int  // urgent tickets granted per period (see docs/adr/0005-priority-tickets.md); 0 when inherited
	UrgentFree  bool // members may run urgent without spending tickets; false when inherited
	// Inherit flags: true means the field is unset on this group and resolves to the
	// Default group's value. Always false on the Default group itself.
	WeightInherit bool
	UrgentInherit bool
	Priority      string // base priority override ("" = inherit the system default)
	Members       int    // primary-member count, filled by ListUserGroups
	// Per-group governance (group model B). Value fields carry the effective/permissive
	// value when inherited; the *Inherit flags say whether this group sets them.
	AllowUrgent        bool // may members use the urgent lane
	AllowUrgentInherit bool
	MaxQueued          int // active-run cap; 0 = unlimited
	MaxQueuedInherit   bool
	RunWindow          string // "" = any hour, else "H1-H2" (panel timezone)
	RunWindowInherit   bool
	// External-user tenancy (ADR 0022). Restricted marks an external OU (owner-scoped reads +
	// run allow-list); it is sticky down the OU tree, so RestrictedEffective reports whether an
	// ancestor already restricts this group even when its own flag is off.
	Restricted          bool
	RestrictedEffective bool
	ParentID            int64 // 0 = a root OU; the tree the inherited settings resolve along
	DailyRunQuota       int // runs/day cap for members; 0 = unlimited
	DailyQuotaInherit   bool
}

// ---------- profile ----------

// SetUserProfile sets a user's display name and email (leaving active/last_login/group_id).
// The user row always pre-exists (created by UpsertUser before any profile edit).
func (s *Store) SetUserProfile(username, displayName, email string) error {
	_, err := s.exec(`UPDATE users SET display_name=?, email=? WHERE username=?`,
		displayName, email, username)
	return err
}

// SetUserActive enables or disables a user (disabled accounts cannot log in).
func (s *Store) SetUserActive(username string, active bool) error {
	_, err := s.exec(`UPDATE users SET active=? WHERE username=?`, boolInt(active), username)
	return err
}

// SetUserExpiry sets a user's account validity cutoff (a panel-tz civil date "YYYY-MM-DD"); an
// empty string stores NULL = never expires. The caller validates/normalizes the date format.
func (s *Store) SetUserExpiry(username, expiresAt string) error {
	var v any = expiresAt
	if expiresAt == "" {
		v = nil // NULL = never
	}
	_, err := s.exec(`UPDATE users SET expires_at=? WHERE username=?`, v, username)
	return err
}

// TouchLastLogin stamps the user's last successful login time.
func (s *Store) TouchLastLogin(username string) error {
	_, err := s.exec(`UPDATE users SET last_login=? WHERE username=?`, nowStr(), username)
	return err
}

// ---------- groups ----------

// nullWeight/nullUrgent build the override arguments for an insert/update: a nil
// pointer stores NULL (inherit from the Default group), a value stores the override.
func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullBoolInt(p *bool) any {
	if p == nil {
		return nil
	}
	return boolInt(*p)
}

// CreateUserGroup adds a group and returns its id. The variadic urgentFree keeps the
// old concrete-value call sites working; weight is stored as a concrete override.
func (s *Store) CreateUserGroup(name, description string, weight int, urgentFree ...bool) (int64, error) {
	uf := len(urgentFree) > 0 && urgentFree[0]
	return s.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,is_default) VALUES(?,?,?,?,?,0)`,
		name, description, nowStr(), weight, boolInt(uf))
}

// UpdateGroup renames / re-describes a group and sets its per-field overrides. A nil
// weight/urgent stores NULL (inherit the Default group's value); a value overrides it.
func (s *Store) UpdateGroup(id int64, name, description string, weight *int, urgent *bool) error {
	_, err := s.exec("UPDATE user_groups SET name=?, description=?, weight=?, urgent_unlimited=? WHERE id=?",
		name, description, nullInt(weight), nullBoolInt(urgent), id)
	return err
}

// UpdateUserGroup is the concrete-value shim kept for existing call sites/tests.
func (s *Store) UpdateUserGroup(id int64, name, description string, weight int, urgentFree ...bool) error {
	w := weight
	if len(urgentFree) == 0 {
		return s.UpdateGroup(id, name, description, &w, nil)
	}
	uf := urgentFree[0]
	return s.UpdateGroup(id, name, description, &w, &uf)
}

// DeleteUserGroup removes a group and reassigns its former primary members to the Default
// group (users.group_id back to NULL). Its priority rides on the group row, so it goes with
// it. Any group flagged is_default is never deletable — the resolution depends on it. We check
// the row's own flag (not just DefaultGroupID) so even a stray duplicate default can't be removed.
func (s *Store) DeleteUserGroup(id int64) error {
	var isDefault sql.NullInt64
	s.queryRow("SELECT is_default FROM user_groups WHERE id=?", id).Scan(&isDefault)
	if isDefault.Int64 != 0 {
		return fmt.Errorf("the Default group cannot be deleted")
	}
	s.exec("UPDATE users SET group_id=NULL WHERE group_id=?", id)
	// Everything keyed to this OU goes with it. Group ids are assigned by the database and a
	// deleted one can come back around, so a lingering grant or viewer row would be inherited by
	// whichever OU is created next (ADR 0022 / 0024).
	s.exec("DELETE FROM report_viewers WHERE principal=?", groupPrincipal(id))
	s.exec("DELETE FROM version_grants WHERE principal=?", groupPrincipal(id))
	s.exec("DELETE FROM group_targets WHERE group_id=?", id)
	s.exec("UPDATE user_groups SET parent_id=NULL WHERE parent_id=?", id)
	_, err := s.exec("DELETE FROM user_groups WHERE id=?", id)
	return err
}

// SetGroupPriority sets a group's base priority override (ADR 0007). An empty priority stores
// NULL, so a non-default group then inherits the system default. The group row always exists.
func (s *Store) SetGroupPriority(groupID int64, priority string) error {
	var val any // nil -> NULL (inherit), matching the old delete-the-side-row semantics
	if priority != "" {
		val = priority
	}
	_, err := s.exec("UPDATE user_groups SET priority=? WHERE id=?", val, groupID)
	return err
}

// GroupPriority returns a group's base-priority override, or "" if it inherits.
func (s *Store) GroupPriority(groupID int64) string {
	var p sql.NullString
	s.queryRow("SELECT priority FROM user_groups WHERE id=?", groupID).Scan(&p)
	return p.String
}

// DefaultGroupID returns the id of the Default (fallback) group, or 0 if none exists.
func (s *Store) DefaultGroupID() int64 {
	var id sql.NullInt64
	s.queryRow("SELECT id FROM user_groups WHERE is_default=1 ORDER BY id LIMIT 1").Scan(&id)
	return id.Int64
}

// EnsureDefaultGroup creates the Default group if none exists and returns its id. It is
// idempotent and safe to call on every boot. The Default group holds concrete baselines
// (weight 0, urgent-unlimited off) that every other group inherits from.
func (s *Store) EnsureDefaultGroup() int64 {
	if id := s.DefaultGroupID(); id != 0 {
		return id
	}
	// Pick a free name (the column is UNIQUE): "Default", then "Default (1)", ...
	base := "Default"
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s (%d)", base, i)
		}
		id, err := s.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,is_default) VALUES(?,?,?,0,0,1)`,
			name, "Fallback group — users without an assigned group inherit these settings.", nowStr())
		if err == nil {
			return id
		}
	}
	return s.DefaultGroupID()
}

// ListUserGroups returns all groups (Default first, then by name) with their primary-
// member counts, per-field override values, and inherit flags.
func (s *Store) ListUserGroups() []UserGroup {
	rows, err := s.query(`SELECT g.id, g.name, COALESCE(g.description,''), COALESCE(g.created_at,''),
			COALESCE(g.is_default,0), g.weight, g.urgent_unlimited, g.allow_urgent, g.max_queued, g.run_window,
			COALESCE(g.priority,''), COALESCE(g.restricted,0), g.daily_run_quota, g.parent_id, COUNT(u.username)
		FROM user_groups g
		LEFT JOIN users u ON u.group_id=g.id
		GROUP BY g.id, g.name, g.description, g.created_at, g.is_default, g.weight, g.urgent_unlimited,
			g.allow_urgent, g.max_queued, g.run_window, g.priority, g.restricted, g.daily_run_quota, g.parent_id
		ORDER BY g.is_default DESC, g.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UserGroup
	parents := map[int64]int64{}
	restrictedOwn := map[int64]bool{}
	for rows.Next() {
		var g UserGroup
		var isDefault, restricted int
		var weight, urgent, allowUrgent, maxQueued, dailyQuota, parent sql.NullInt64
		var runWindow sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Created, &isDefault, &weight, &urgent,
			&allowUrgent, &maxQueued, &runWindow, &g.Priority, &restricted, &dailyQuota, &parent, &g.Members); err != nil {
			continue
		}
		g.IsDefault = isDefault != 0
		g.Weight, g.WeightInherit = int(weight.Int64), !weight.Valid && !g.IsDefault
		g.UrgentFree, g.UrgentInherit = urgent.Int64 != 0, !urgent.Valid && !g.IsDefault
		// Value fields carry the permissive default when unset, so the UI reads sensibly.
		g.AllowUrgent, g.AllowUrgentInherit = !allowUrgent.Valid || allowUrgent.Int64 != 0, !allowUrgent.Valid && !g.IsDefault
		g.MaxQueued, g.MaxQueuedInherit = int(maxQueued.Int64), !maxQueued.Valid && !g.IsDefault
		g.RunWindow, g.RunWindowInherit = runWindow.String, !runWindow.Valid && !g.IsDefault
		g.Restricted = restricted != 0
		g.DailyRunQuota, g.DailyQuotaInherit = int(dailyQuota.Int64), !dailyQuota.Valid && !g.IsDefault
		g.ParentID = parent.Int64
		parents[g.ID], restrictedOwn[g.ID] = parent.Int64, g.Restricted
		out = append(out, g)
	}
	// Restriction is sticky down the OU tree, so a group is effectively restricted when it or ANY
	// ancestor sets the flag — this is what the admin UI shows as "inherited from the parent OU".
	for i := range out {
		seen := map[int64]bool{}
		for id := out[i].ID; id != 0 && !seen[id]; id = parents[id] {
			seen[id] = true
			if restrictedOwn[id] {
				out[i].RestrictedEffective = true
				break
			}
		}
	}
	return out
}

// ---------- primary group (membership) ----------

// SetPrimaryGroup sets a user's primary group; a groupID of 0 clears it (the user then
// falls back to the Default group). A non-existent group id is treated as a clear so the
// stored pointer is never left dangling (e.g. a stale UI targeting a just-deleted group).
func (s *Store) SetPrimaryGroup(username string, groupID int64) error {
	if groupID != 0 {
		var exists sql.NullInt64
		s.queryRow("SELECT 1 FROM user_groups WHERE id=?", groupID).Scan(&exists)
		if exists.Int64 == 0 {
			groupID = 0
		}
	}
	if groupID == 0 {
		_, err := s.exec("UPDATE users SET group_id=NULL WHERE username=?", username)
		return err
	}
	_, err := s.exec("UPDATE users SET group_id=? WHERE username=?", groupID, username)
	return err
}

// PrimaryGroupOf returns a user's primary group id, or 0 if they inherit the Default (a NULL
// group_id, or a missing user, reads back as 0).
func (s *Store) PrimaryGroupOf(username string) int64 {
	var id sql.NullInt64
	s.queryRow("SELECT group_id FROM users WHERE username=?", username).Scan(&id)
	return id.Int64
}

// AllPrimaryGroups returns username → primary group id for every assigned user, so a
// user list can be enriched with one query instead of N.
func (s *Store) AllPrimaryGroups() map[string]int64 {
	m := map[string]int64{}
	rows, err := s.query("SELECT username, group_id FROM users WHERE group_id IS NOT NULL")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			continue
		}
		m[name] = id
	}
	return m
}

// GroupSettings is a user's fully-resolved run governance (group model B / OU tree, ADR 0022).
type GroupSettings struct {
	Weight          int    // urgent tickets granted per period
	UrgentUnlimited bool   // may run urgent without spending tickets
	AllowUrgent     bool   // may use the urgent lane at all
	MaxQueued       int    // cap on active (queued+running) jobs per user; 0 = unlimited
	RunWindow       string // "" = any hour, else "startHour-endHour" (panel timezone)
	Restricted      bool   // external OU: owner-scoped reads + run allow-list apply (sticky down the tree)
	DailyRunQuota   int    // cap on runs/day for a restricted user; 0 = unlimited
}

// rawGroupSettings holds one group's un-coalesced governance columns (NULL = unset).
type rawGroupSettings struct {
	weight, urgent, allowUrgent, maxQueued, restricted, dailyQuota sql.NullInt64
	runWindow                                                      sql.NullString
}

func (s *Store) rawGroupSettings(id int64) rawGroupSettings {
	var g rawGroupSettings
	s.queryRow("SELECT weight, urgent_unlimited, allow_urgent, max_queued, run_window, restricted, daily_run_quota FROM user_groups WHERE id=?", id).
		Scan(&g.weight, &g.urgent, &g.allowUrgent, &g.maxQueued, &g.runWindow, &g.restricted, &g.dailyQuota)
	return g
}

// EffectiveGroupSettings resolves a user's run governance by layering along the OU tree, in order:
// the permissive baseline, then each OU from the root (Default) down to the user's primary group,
// so a set field on a nearer (deeper) OU wins over its ancestors (NULL = inherit). `restricted` is
// STICKY rather than last-wins: a restricted ancestor restricts its whole subtree, and a descendant
// can never un-restrict itself (ADR 0022).
func (s *Store) EffectiveGroupSettings(username string) GroupSettings {
	res := GroupSettings{AllowUrgent: true} // permissive baseline: no cap, any hour, urgent ok
	apply := func(g rawGroupSettings) {
		if g.weight.Valid {
			res.Weight = int(g.weight.Int64)
		}
		if g.urgent.Valid {
			res.UrgentUnlimited = g.urgent.Int64 != 0
		}
		if g.allowUrgent.Valid {
			res.AllowUrgent = g.allowUrgent.Int64 != 0
		}
		if g.maxQueued.Valid {
			res.MaxQueued = int(g.maxQueued.Int64)
		}
		if g.runWindow.Valid {
			res.RunWindow = g.runWindow.String
		}
		if g.restricted.Valid && g.restricted.Int64 != 0 {
			res.Restricted = true // sticky OR: never un-set by a descendant
		}
		if g.dailyQuota.Valid {
			res.DailyRunQuota = int(g.dailyQuota.Int64)
		}
	}
	for _, gid := range s.groupChain(username) {
		apply(s.rawGroupSettings(gid))
	}
	return res
}

// groupParent returns a group's parent OU id, or 0 for a root (NULL parent_id) or missing group.
func (s *Store) groupParent(id int64) int64 {
	var p sql.NullInt64
	s.queryRow("SELECT parent_id FROM user_groups WHERE id=?", id).Scan(&p)
	return p.Int64
}

// groupChain returns the OU ancestry a user's settings resolve through, ordered root→leaf so the
// caller applies ancestors first and the leaf (the user's primary group, or the Default group when
// unassigned) last. The Default/root baseline is always included even if a broken parent_id chain
// never reaches it. Cycle- and depth-guarded.
func (s *Store) groupChain(username string) []int64 {
	def := s.DefaultGroupID()
	leaf := s.PrimaryGroupOf(username)
	if leaf == 0 {
		leaf = def
	}
	var up []int64
	seen := map[int64]bool{}
	for gid := leaf; gid != 0 && !seen[gid] && len(up) < 64; gid = s.groupParent(gid) {
		seen[gid] = true
		up = append(up, gid)
	}
	if def != 0 && !seen[def] {
		up = append(up, def) // guarantee the baseline is applied even off an orphaned chain
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i] // reverse to root→leaf
	}
	return up
}

// SetGroupParent moves an OU under a parent (0 = make it a root; NULL parent_id). No cycle check
// here — the admin API guards that before calling; groupChain is itself cycle-guarded at read time.
func (s *Store) SetGroupParent(id, parent int64) error {
	var v any = parent
	if parent == 0 {
		v = nil
	}
	_, err := s.exec(`UPDATE user_groups SET parent_id=? WHERE id=?`, v, id)
	return err
}

// SetGroupRestricted flags (or clears) an OU as external/restricted. Restriction is sticky down the
// tree at resolution time, so clearing it here only affects OUs with no restricted ancestor.
func (s *Store) SetGroupRestricted(id int64, restricted bool) error {
	_, err := s.exec(`UPDATE user_groups SET restricted=? WHERE id=?`, boolInt(restricted), id)
	return err
}

// SetGroupDailyQuota sets an OU's per-day run cap; nil stores NULL (inherit the parent), 0 = unlimited.
func (s *Store) SetGroupDailyQuota(id int64, quota *int) error {
	_, err := s.exec(`UPDATE user_groups SET daily_run_quota=? WHERE id=?`, nullInt(quota), id)
	return err
}

// SetGroupGovernance writes a group's allow-urgent / max-queued / run-window overrides.
// A nil pointer stores NULL (inherit the Default group); the Default group is coerced to
// concrete by the API before calling this.
func (s *Store) SetGroupGovernance(id int64, allowUrgent *bool, maxQueued *int, runWindow *string) error {
	var rw any
	if runWindow != nil {
		rw = *runWindow
	}
	_, err := s.exec("UPDATE user_groups SET allow_urgent=?, max_queued=?, run_window=? WHERE id=?",
		nullBoolInt(allowUrgent), nullInt(maxQueued), rw, id)
	return err
}

// GroupRestrictedEffective reports whether an OU is external once inheritance is applied — its own
// flag, or any ancestor's, since restriction is sticky down the tree.
func (s *Store) GroupRestrictedEffective(id int64) bool {
	for _, g := range s.ListUserGroups() {
		if g.ID == id {
			return g.RestrictedEffective
		}
	}
	return false
}
