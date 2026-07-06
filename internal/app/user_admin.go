package app

import (
	"database/sql"
	"fmt"
)

// User-admin persistence: extended profile attributes (display name / email /
// active / last login) and organizational groups. Groups are labels only —
// permissions still come from the role. The `users` table is never altered; these
// live in additive tables (user_profiles, user_groups, user_group_members).

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
}

// ---------- profile ----------

// SetUserProfile upserts a user's display name and email (leaving active/last_login).
func (s *Store) SetUserProfile(username, displayName, email string) error {
	_, err := s.exec(`INSERT INTO user_profiles(username,display_name,email) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name, email=excluded.email`,
		username, displayName, email)
	return err
}

// SetUserActive enables or disables a user (disabled accounts cannot log in).
func (s *Store) SetUserActive(username string, active bool) error {
	_, err := s.exec(`INSERT INTO user_profiles(username,active) VALUES(?,?)
		ON CONFLICT(username) DO UPDATE SET active=excluded.active`, username, boolInt(active))
	return err
}

// TouchLastLogin stamps the user's last successful login time.
func (s *Store) TouchLastLogin(username string) error {
	_, err := s.exec(`INSERT INTO user_profiles(username,last_login) VALUES(?,?)
		ON CONFLICT(username) DO UPDATE SET last_login=excluded.last_login`, username, nowStr())
	return err
}

// deleteUserExtras removes a user's profile row and all group memberships (called
// from DeleteUser so a removed account leaves nothing behind).
func (s *Store) deleteUserExtras(username string) {
	s.exec("DELETE FROM user_profiles WHERE username=?", username)
	s.exec("DELETE FROM user_group_members WHERE username=?", username)
	s.exec("DELETE FROM user_primary_group WHERE username=?", username)
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

// DeleteUserGroup removes a group, its priority row, and any primary-group pointers to
// it (its former primary members fall back to the Default group). Any group flagged
// is_default is never deletable — the resolution depends on it. We check the row's own
// flag (not just DefaultGroupID) so even a stray duplicate default can't be removed.
func (s *Store) DeleteUserGroup(id int64) error {
	var isDefault sql.NullInt64
	s.queryRow("SELECT is_default FROM user_groups WHERE id=?", id).Scan(&isDefault)
	if isDefault.Int64 != 0 {
		return fmt.Errorf("the Default group cannot be deleted")
	}
	s.exec("DELETE FROM user_group_members WHERE group_id=?", id)
	s.exec("DELETE FROM user_primary_group WHERE group_id=?", id)
	s.exec("DELETE FROM group_priority WHERE group_id=?", id)
	_, err := s.exec("DELETE FROM user_groups WHERE id=?", id)
	return err
}

// SetGroupPriority sets a group's base priority override (ADR 0007). An empty priority
// clears it (a non-default group then inherits the system default).
func (s *Store) SetGroupPriority(groupID int64, priority string) error {
	if priority == "" {
		_, err := s.exec("DELETE FROM group_priority WHERE group_id=?", groupID)
		return err
	}
	_, err := s.exec(`INSERT INTO group_priority(group_id,priority) VALUES(?,?)
		ON CONFLICT(group_id) DO UPDATE SET priority=excluded.priority`, groupID, priority)
	return err
}

// GroupPriority returns a group's base-priority override, or "" if it inherits.
func (s *Store) GroupPriority(groupID int64) string {
	var p sql.NullString
	s.queryRow("SELECT priority FROM group_priority WHERE group_id=?", groupID).Scan(&p)
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
			COALESCE(g.is_default,0), g.weight, g.urgent_unlimited, COALESCE(gp.priority,''), COUNT(pg.username)
		FROM user_groups g
		LEFT JOIN user_primary_group pg ON pg.group_id=g.id
		LEFT JOIN group_priority gp ON gp.group_id=g.id
		GROUP BY g.id, g.name, g.description, g.created_at, g.is_default, g.weight, g.urgent_unlimited, gp.priority
		ORDER BY g.is_default DESC, g.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UserGroup
	for rows.Next() {
		var g UserGroup
		var isDefault int
		var weight, urgent sql.NullInt64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Created, &isDefault, &weight, &urgent, &g.Priority, &g.Members); err != nil {
			continue
		}
		g.IsDefault = isDefault != 0
		g.Weight, g.WeightInherit = int(weight.Int64), !weight.Valid && !g.IsDefault
		g.UrgentFree, g.UrgentInherit = urgent.Int64 != 0, !urgent.Valid && !g.IsDefault
		out = append(out, g)
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
		_, err := s.exec("DELETE FROM user_primary_group WHERE username=?", username)
		return err
	}
	_, err := s.exec(`INSERT INTO user_primary_group(username,group_id) VALUES(?,?)
		ON CONFLICT(username) DO UPDATE SET group_id=excluded.group_id`, username, groupID)
	return err
}

// PrimaryGroupOf returns a user's primary group id, or 0 if they inherit the Default.
func (s *Store) PrimaryGroupOf(username string) int64 {
	var id sql.NullInt64
	s.queryRow("SELECT group_id FROM user_primary_group WHERE username=?", username).Scan(&id)
	return id.Int64
}

// AllPrimaryGroups returns username → primary group id for every assigned user, so a
// user list can be enriched with one query instead of N.
func (s *Store) AllPrimaryGroups() map[string]int64 {
	m := map[string]int64{}
	rows, err := s.query("SELECT username, group_id FROM user_primary_group")
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

// EffectiveTicketSettings resolves a user's urgent-ticket settings through group model
// B: their primary group's per-field override where set, otherwise the Default group's
// baseline. Returns (weight, urgentUnlimited); (0,false) if no Default group exists.
func (s *Store) EffectiveTicketSettings(username string) (int, bool) {
	dw, du := s.defaultBaselines()
	gid := s.PrimaryGroupOf(username)
	if gid == 0 {
		return dw, du
	}
	var w, u sql.NullInt64 // raw (un-coalesced) so NULL means inherit
	s.queryRow("SELECT weight, urgent_unlimited FROM user_groups WHERE id=?", gid).Scan(&w, &u)
	weight, urgent := dw, du
	if w.Valid {
		weight = int(w.Int64)
	}
	if u.Valid {
		urgent = u.Int64 != 0
	}
	return weight, urgent
}

// defaultBaselines returns the Default group's concrete weight + urgent-unlimited.
func (s *Store) defaultBaselines() (int, bool) {
	var w, u sql.NullInt64
	s.queryRow("SELECT COALESCE(weight,0), COALESCE(urgent_unlimited,0) FROM user_groups WHERE is_default=1 ORDER BY id LIMIT 1").Scan(&w, &u)
	return int(w.Int64), u.Int64 != 0
}
