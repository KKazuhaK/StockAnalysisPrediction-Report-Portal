package app

import (
	"fmt"
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
		var dailyQuota any = g.DailyRunQuota
		if g.DailyQuotaInherit {
			dailyQuota = nil
		}
		out = append(out, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description,
			"is_default": g.IsDefault, "weight": weight, "urgent_unlimited": urgent,
			"allow_urgent": allowUrgent, "max_queued": maxQueued, "run_window": runWindow,
			"priority": g.Priority, "members": g.Members,
			// External-user tenancy (ADR 0022): restricted is this group's OWN flag, while
			// restricted_effective also accounts for a restricted ancestor (sticky down the tree).
			"restricted": g.Restricted, "restricted_effective": g.RestrictedEffective,
			"daily_run_quota":  dailyQuota,
			"run_quota_period": g.QuotaPeriod,
			// parent_id so the admin UI can render the tree it is editing (ADR 0022).
			"parent_id": g.ParentID,
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
	// External-user tenancy (ADR 0022). Restricted is a plain flag (absent = leave as-is);
	// DailyRunQuota is a pointer so null means "inherit the parent OU" and 0 means unlimited.
	Restricted    *bool `json:"restricted"`
	DailyRunQuota *int  `json:"daily_run_quota"`
	// The window the cap is measured over. Only meaningful alongside a quota, so it is written in
	// the same call rather than separately — a period with no number configures nothing.
	RunQuotaPeriod *string `json:"run_quota_period"`
	// ParentID places this OU in the tree. A pointer because absent means "leave it where it is" —
	// every pre-existing caller of this endpoint omits the field — while an explicit 0 detaches it
	// to a root. Without this the tree could only be built by hand in SQL, which made every
	// inherited behaviour (restricted stickiness, quota inheritance, the run allow-list's and the
	// version grants' nearest-ancestor resolution) unreachable through the product.
	ParentID *int64 `json:"parent_id"`
}

// applyParent moves an OU, refusing a move that would make it its own ancestor. groupChain survives
// a cycle — it dedupes and caps — so this is not a hang; but an OU resolving its settings from a ring
// is a configuration no admin can reason about, and the refusal has to be at the door.
func (s *Server) applyParent(id int64, parent *int64) error {
	if parent == nil {
		return nil
	}
	p := *parent
	if p == id {
		return fmt.Errorf("an OU cannot be its own parent")
	}
	for seen, g := map[int64]bool{}, p; g != 0 && !seen[g]; g = s.st.groupParent(g) {
		seen[g] = true
		if g == id {
			return fmt.Errorf("that would place the OU under itself")
		}
	}
	return s.st.SetGroupParent(id, p)
}

// tenancy normalizes the external-tenancy fields for storage: a negative quota is clamped to 0
// (unlimited), and the Default group (the root baseline) can neither be restricted nor inherit.
func (in groupInput) tenancy(isDefault bool) (*bool, *int) {
	restricted, quota := in.Restricted, in.DailyRunQuota
	if quota != nil && *quota < 0 {
		zero := 0
		quota = &zero
	}
	if isDefault {
		if restricted == nil || *restricted {
			no := false
			restricted = &no // the root OU is the internal baseline; never restricted
		}
		if quota == nil {
			zero := 0
			quota = &zero
		}
	}
	return restricted, quota
}

// quotaPeriod is the window the cap is measured over, defaulting to the day — which is both the
// pre-existing behaviour and the narrowest window, so an omitted or unrecognized value can never
// widen an allowance.
func (in groupInput) quotaPeriod() string {
	if in.RunQuotaPeriod == nil {
		return QuotaDay
	}
	return *in.RunQuotaPeriod
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
	if restricted, quota := in.tenancy(false); restricted != nil || quota != nil {
		if restricted != nil {
			s.st.SetGroupRestricted(id, *restricted)
		}
		if quota != nil {
			s.st.SetGroupDailyQuota(id, quota, in.quotaPeriod())
		}
	}
	// Placing the OU in one step, so creating a sub-OU is one action rather than create-then-move.
	if err := s.applyParent(id, in.ParentID); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordChange(r, user, AuditGroupCreate, "group", itoa64(id),
		map[string]any{"name": name, "parent": in.ParentID})
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
	restricted, quota := in.tenancy(isDefault)
	if restricted != nil {
		s.st.SetGroupRestricted(id, *restricted)
	}
	s.st.SetGroupDailyQuota(id, quota, in.quotaPeriod()) // nil quota = inherit the parent OU
	if err := s.applyParent(id, in.ParentID); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// An OU's settings decide what its members may run and what they may see, so this is an
	// access change even when it looks like a rename. The fields that carry consequences are
	// named; the row says which knobs moved, not their whole prior state.
	s.recordChange(r, user, AuditGroupChange, "group", itoa64(id), map[string]any{
		"name": name, "parent": in.ParentID, "restricted": restricted,
		"quota": quota, "quota_period": in.quotaPeriod(), "priority": in.Priority})
	writeJSON(w, okJSON)
}

// apiGroupTargets returns one OU's run allow-list (ADR 0022 R3) alongside every target, so the
// admin matrix can render "which workflows may this OU run, and on which surfaces".
func (s *Server) apiGroupTargets(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	granted := make([]map[string]any, 0)
	for _, g := range s.st.GroupTargets(id) {
		granted = append(granted, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	targets := make([]map[string]any, 0)
	for _, t := range s.st.ListTargets() {
		targets = append(targets, map[string]any{"id": t.ID, "name": t.Name,
			"surfaces": TargetSurfaces(t.Surfaces), "output_subtype": targetOutputSubtype(t.Config)})
	}
	writeJSON(w, map[string]any{"granted": granted, "targets": targets})
}

// apiGroupTargetsSave replaces an OU's allow-list atomically. An empty list means "this OU may run
// nothing" — default-deny is the point, so it is a legitimate save, not an error.
func (s *Server) apiGroupTargetsSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Granted []struct {
			TargetID int64    `json:"target_id"`
			Surfaces []string `json:"surfaces"`
		} `json:"granted"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	rows := make([]GroupTarget, 0, len(in.Granted))
	for _, g := range in.Granted {
		// An all-surfaces selection is stored as "" so the row keeps tracking the target's own
		// surfaces if an admin later narrows them.
		sub := strings.Join(g.Surfaces, ",")
		if len(TargetSurfaces(sub)) == len(TargetSurfaces("")) {
			sub = ""
		}
		rows = append(rows, GroupTarget{TargetID: g.TargetID, Surfaces: sub})
	}
	gid := pathID(r, "id")
	// Both sides, like every other grant row: the current allow-list answers "what may this OU
	// run now", and only the log answers "when did it gain that, and who granted it".
	before := make([]map[string]any, 0)
	for _, g := range s.st.GroupTargets(gid) {
		before = append(before, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	if err := s.st.SetGroupTargets(gid, rows); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after := make([]map[string]any, 0, len(rows))
	for _, g := range rows {
		after = append(after, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	s.recordChange(r, user, AuditGrantChange, "group", itoa64(gid),
		map[string]any{"kind": "run_allowlist", "before": before, "after": after})
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
	id := pathID(r, "id")
	// Read before the delete: afterwards there is no name to record, and an id alone means
	// nothing to whoever reads the row later.
	name := ""
	for _, g := range s.st.ListUserGroups() {
		if g.ID == id {
			name = g.Name
			break
		}
	}
	if err := s.st.DeleteUserGroup(id); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Deleting an OU takes its grants and viewer rows with it, so this is an access change too.
	s.recordChange(r, user, AuditGroupDelete, "group", itoa64(id), map[string]any{"name": name})
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
		// Moving an account between OUs is a policy change, not bookkeeping: the OU decides what
		// that person may read (ADR 0022/0024) and, since ADR 0025, what they are TOLD. The
		// single-user path has always audited it; this one did not, so the bulk form of the same
		// change left no trail at all — the shape of gap that is only noticed while investigating.
		case "set_group":
			if in.GroupID == 0 {
				continue
			}
			s.st.SetPrimaryGroup(name, in.GroupID)
			s.recordChange(r, user, AuditUserChange, "user", name,
				map[string]any{"op": "set_group", "group_id": in.GroupID, "bulk": true})
			n++
		case "clear_group":
			s.st.SetPrimaryGroup(name, 0)
			s.recordChange(r, user, AuditUserChange, "user", name,
				map[string]any{"op": "clear_group", "bulk": true})
			n++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "n": n})
}
