package app

// User-admin persistence: extended profile attributes (display name / email /
// active / last login) and organizational groups. Groups are labels only —
// permissions still come from the role. The `users` table is never altered; these
// live in additive tables (user_profiles, user_groups, user_group_members).

// UserGroup is an organizational group (a team / department label).
type UserGroup struct {
	ID          int64
	Name        string
	Description string
	Created     string
	Weight      int // 加急 tickets granted per period to each member (see docs/adr/0005-priority-tickets.md)
	Members     int // member count, filled by ListUserGroups
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
}

// ---------- groups ----------

// CreateUserGroup adds a group and returns its id.
func (s *Store) CreateUserGroup(name, description string, weight int) (int64, error) {
	return s.insertID(`INSERT INTO user_groups(name,description,created_at,weight) VALUES(?,?,?,?)`, name, description, nowStr(), weight)
}

// UpdateUserGroup renames / re-describes a group and sets its weight.
func (s *Store) UpdateUserGroup(id int64, name, description string, weight int) error {
	_, err := s.exec("UPDATE user_groups SET name=?, description=?, weight=? WHERE id=?", name, description, weight, id)
	return err
}

// DeleteUserGroup removes a group and all of its memberships.
func (s *Store) DeleteUserGroup(id int64) error {
	s.exec("DELETE FROM user_group_members WHERE group_id=?", id)
	_, err := s.exec("DELETE FROM user_groups WHERE id=?", id)
	return err
}

// ListUserGroups returns all groups with their member counts, by name.
func (s *Store) ListUserGroups() []UserGroup {
	rows, err := s.query(`SELECT g.id, g.name, COALESCE(g.description,''), COALESCE(g.created_at,''), COALESCE(g.weight,0), COUNT(m.username)
		FROM user_groups g LEFT JOIN user_group_members m ON m.group_id=g.id
		GROUP BY g.id, g.name, g.description, g.created_at, g.weight ORDER BY g.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UserGroup
	for rows.Next() {
		var g UserGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Created, &g.Weight, &g.Members); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out
}

// ---------- membership ----------

// SetUserGroups replaces a user's group membership with the given group ids.
func (s *Store) SetUserGroups(username string, ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM user_group_members WHERE username=?"), username); err != nil {
		return err
	}
	stmt, err := tx.Prepare(s.bind("INSERT INTO user_group_members(group_id,username) VALUES(?,?)"))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GroupsOf returns the group ids a user belongs to.
func (s *Store) GroupsOf(username string) []int64 {
	rows, err := s.query("SELECT group_id FROM user_group_members WHERE username=? ORDER BY group_id", username)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// AllUserGroups returns username → group ids for every membership, so a user list
// can be enriched with one query instead of N.
func (s *Store) AllUserGroups() map[string][]int64 {
	m := map[string][]int64{}
	rows, err := s.query("SELECT username, group_id FROM user_group_members")
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
		m[name] = append(m[name], id)
	}
	return m
}
