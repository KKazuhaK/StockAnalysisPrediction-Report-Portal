package app

import (
	"net/http"
	"strings"
)

// HTTP handlers for the enterprise user admin: organizational groups and bulk
// actions over the user list. Admin-only (wired with requireAdminJSON).

func userGroupsJSON(gs []UserGroup) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "description": g.Description, "weight": g.Weight, "members": g.Members})
	}
	return out
}

func (s *Server) apiAdminGroups(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"groups": userGroupsJSON(s.st.ListUserGroups())})
}

func (s *Server) apiGroupAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Weight      int    `json:"weight"`
	}
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "group name required")
		return
	}
	id, err := s.st.CreateUserGroup(name, strings.TrimSpace(in.Description), clampWeight(in.Weight))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "group name already exists")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiGroupSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Weight      int    `json:"weight"`
	}
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "group name required")
		return
	}
	if err := s.st.UpdateUserGroup(pathID(r, "id"), name, strings.TrimSpace(in.Description), clampWeight(in.Weight)); err != nil {
		jsonError(w, http.StatusBadRequest, "group name already exists")
		return
	}
	writeJSON(w, okJSON)
}

// clampWeight keeps a group's 加急 allowance in a sane range.
func clampWeight(w int) int {
	if w < 0 {
		return 0
	}
	if w > 999 {
		return 999
	}
	return w
}

func (s *Server) apiGroupDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteUserGroup(pathID(r, "id"))
	writeJSON(w, okJSON)
}

// apiUsersBulk applies one action to many users at once (enable / disable / delete /
// set_role / add_group / remove_group), honouring the same last-admin and no-self-
// lockout guards as the single-user endpoints. Returns how many were affected.
func (s *Server) apiUsersBulk(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Action    string   `json:"action"`
		Usernames []string `json:"usernames"`
		Role      string   `json:"role"`
		GroupID   int64    `json:"group_id"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	n := 0
	for _, name := range in.Usernames {
		u := s.st.GetUser(name)
		if u == nil {
			continue
		}
		// A destructive action on yourself or the last admin is skipped.
		protected := name == user || (u.IsAdmin() && s.st.CountAdmins() <= 1)
		switch in.Action {
		case "enable":
			s.st.SetUserActive(name, true)
			n++
		case "disable":
			if protected {
				continue
			}
			s.st.SetUserActive(name, false)
			n++
		case "delete":
			if protected {
				continue
			}
			s.st.DeleteUser(name)
			n++
		case "set_role":
			role := validRole(in.Role)
			if role != "admin" && u.IsAdmin() && s.st.CountAdmins() <= 1 {
				continue
			}
			s.st.SetUserRole(name, role)
			n++
		case "add_group":
			if in.GroupID == 0 {
				continue
			}
			s.st.SetUserGroups(name, addID(s.st.GroupsOf(name), in.GroupID))
			n++
		case "remove_group":
			s.st.SetUserGroups(name, removeID(s.st.GroupsOf(name), in.GroupID))
			n++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "n": n})
}

// addID appends id if not already present; removeID drops it.
func addID(ids []int64, id int64) []int64 {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

func removeID(ids []int64, id int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}
