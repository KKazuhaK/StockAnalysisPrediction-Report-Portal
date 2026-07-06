package app

import (
	"net/http"
	"strings"
)

// HTTP handlers for the enterprise user admin: organizational groups (group model B)
// and bulk actions over the user list. Admin-only (wired with requireAdminJSON).

func userGroupsJSON(gs []UserGroup) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		// A nil weight / urgent_unlimited on a non-default group means "inherit the
		// Default group's value"; the UI renders that as an inherit toggle.
		var weight any = g.Weight
		var urgent any = g.UrgentFree
		var allowUrgent any = g.AllowUrgent
		var maxQueued any = g.MaxQueued
		var runWindow any = g.RunWindow
		if g.WeightInherit {
			weight = nil
		}
		if g.UrgentInherit {
			urgent = nil
		}
		if g.AllowUrgentInherit {
			allowUrgent = nil
		}
		if g.MaxQueuedInherit {
			maxQueued = nil
		}
		if g.RunWindowInherit {
			runWindow = nil
		}
		out = append(out, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description,
			"is_default": g.IsDefault, "weight": weight, "urgent_unlimited": urgent,
			"allow_urgent": allowUrgent, "max_queued": maxQueued, "run_window": runWindow,
			"priority": g.Priority, "members": g.Members,
		})
	}
	return out
}

func (s *Server) apiAdminGroups(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"groups": userGroupsJSON(s.st.ListUserGroups())})
}

// groupInput is the create/update body. Weight and UrgentUnlimited are pointers so a
// JSON null means "inherit the Default group" and a value means "override".
type groupInput struct {
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Weight          *int    `json:"weight"`
	UrgentUnlimited *bool   `json:"urgent_unlimited"`
	AllowUrgent     *bool   `json:"allow_urgent"`
	MaxQueued       *int    `json:"max_queued"`
	RunWindow       *string `json:"run_window"`
	Priority        string  `json:"priority"`
}

// overrides normalizes the input's inherit/override fields for storage. The Default
// group cannot inherit (it is the baseline), so a null there is coerced to concrete.
func (in groupInput) overrides(isDefault bool) (*int, *bool) {
	w, u := in.Weight, in.UrgentUnlimited
	if w != nil {
		cw := clampWeight(*w)
		w = &cw
	} else if isDefault {
		zero := 0
		w = &zero
	}
	if u == nil && isDefault {
		f := false
		u = &f
	}
	return w, u
}

// governance normalizes the allow-urgent / max-queued / run-window fields. On the Default
// group a null is coerced to the permissive baseline (urgent allowed, no cap, any hour).
func (in groupInput) governance(isDefault bool) (*bool, *int, *string) {
	allow, mq, rw := in.AllowUrgent, in.MaxQueued, in.RunWindow
	if mq != nil {
		v := *mq
		if v < 0 {
			v = 0
		}
		mq = &v
	}
	if rw != nil {
		v := normalizeRunWindow(*rw)
		rw = &v
	}
	if isDefault {
		if allow == nil {
			def := true
			allow = &def
		}
		if mq == nil {
			zero := 0
			mq = &zero
		}
		if rw == nil {
			empty := ""
			rw = &empty
		}
	}
	return allow, mq, rw
}

// normalizeRunWindow keeps a valid "H1-H2" window, else "" (no restriction).
func normalizeRunWindow(win string) string {
	if _, _, ok := parseRunWindow(win); ok {
		return strings.TrimSpace(win)
	}
	return ""
}

func (s *Server) apiGroupAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in groupInput
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "group name required")
		return
	}
	weight, urgent := in.overrides(false) // new groups are never the Default
	initWeight := 0
	if weight != nil {
		initWeight = *weight
	}
	id, err := s.st.CreateUserGroup(name, strings.TrimSpace(in.Description), initWeight)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "group name already exists")
		return
	}
	// Re-apply as nullable so an omitted weight/urgent is stored as inherit (NULL).
	s.st.UpdateGroup(id, name, strings.TrimSpace(in.Description), weight, urgent)
	allow, mq, rw := in.governance(false)
	s.st.SetGroupGovernance(id, allow, mq, rw)
	s.st.SetGroupPriority(id, s.groupPriorityValid(in.Priority))
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiGroupSave(w http.ResponseWriter, r *http.Request, user string) {
	var in groupInput
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "group name required")
		return
	}
	id := pathID(r, "id")
	isDefault := id == s.st.DefaultGroupID()
	weight, urgent := in.overrides(isDefault)
	if err := s.st.UpdateGroup(id, name, strings.TrimSpace(in.Description), weight, urgent); err != nil {
		jsonError(w, http.StatusBadRequest, "group name already exists")
		return
	}
	allow, mq, rw := in.governance(isDefault)
	s.st.SetGroupGovernance(id, allow, mq, rw)
	// The Default group carries no priority override (its members use the system
	// default); force it clear so a stored value can't mislead.
	if isDefault {
		s.st.SetGroupPriority(id, "")
	} else {
		s.st.SetGroupPriority(id, s.groupPriorityValid(in.Priority))
	}
	writeJSON(w, okJSON)
}

// clampWeight keeps a group's urgent-ticket allowance in a sane range.
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
	if err := s.st.DeleteUserGroup(pathID(r, "id")); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiUsersBulk applies one action to many users at once (enable / disable / delete /
// set_role / set_group / clear_group), honouring the same last-admin and no-self-
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
		case "set_group":
			if in.GroupID == 0 {
				continue
			}
			s.st.SetPrimaryGroup(name, in.GroupID)
			n++
		case "clear_group":
			s.st.SetPrimaryGroup(name, 0)
			n++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "n": n})
}
