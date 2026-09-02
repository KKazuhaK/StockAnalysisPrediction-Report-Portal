package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Site announcements (ADR 0025): the reader-facing feed and the admin CRUD surface.
//
// The single most important line in this file is the auth wrapper on the reader endpoint. The
// announcement used to ride along on GET /api/site, which is public — it powers the login page's
// brand title before anyone has signed in — so an announcement's text was readable by any
// anonymous visitor, and /login re-fetched it every 60 seconds. That was survivable while there
// was exactly one announcement meant for everybody. It stops being survivable the moment a row can
// be addressed to one OU: "who is being told what" is itself disclosure. So the feed moved behind
// requireUserJSON and the five keys left siteSettingsJSON, which is what site_settings_test's
// frozen key set is there to keep true.

const (
	maxAnnouncementTitleRunes   = 160
	maxAnnouncementContentRunes = 2000
)

// There is deliberately no ceiling on the number of announcements. A hard limit would refuse a save
// at the moment an operator most needs to broadcast, to prevent a problem — readers ignoring an
// overcrowded band — that a refusal does not actually solve. The console warns instead, counting
// what readers actually face (enabled and inside its window, not the draft pile), and the reader
// side already folds the overflow behind a counter. If a runaway script ever makes this a real
// risk, the answer is a rate limit on creation, not a cap on the total.

func validAnnouncementLevel(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "notice", "success", "warning", "error":
		return true
	default:
		return false
	}
}

func normalizeAnnouncementLevel(raw string) string {
	level := strings.ToLower(strings.TrimSpace(raw))
	if validAnnouncementLevel(level) && level != "" {
		return level
	}
	return "notice"
}

// normalizeAnnouncementScope falls back to home — the narrower of the two, and what every row
// written before the column existed meant.
func normalizeAnnouncementScope(raw string) string {
	if strings.ToLower(strings.TrimSpace(raw)) == "app" {
		return "app"
	}
	return "home"
}

// normalizeAnnouncementAudience falls back to all. That is the permissive answer, and it is the
// right one HERE only because an unrecognized value can reach this function from exactly one
// place — a row written by an older or hand-edited database, where "all" is what it meant. The
// read path never derives default-deny from this; it checks Audience == "grant" explicitly.
func normalizeAnnouncementAudience(raw string) string {
	if strings.ToLower(strings.TrimSpace(raw)) == "grant" {
		return "grant"
	}
	return "all"
}

// announcementWindowOpen reports whether now falls inside [starts_at, ends_at]. An empty or
// unparseable bound is treated as unbounded on that side: a row whose timestamp somebody hand-edited
// into nonsense should keep showing, not vanish silently.
func announcementWindowOpen(a Announcement, now time.Time) bool {
	if t, err := time.Parse(time.RFC3339, a.StartsAt); err == nil && now.Before(t) {
		return false
	}
	if t, err := time.Parse(time.RFC3339, a.EndsAt); err == nil && !now.Before(t) {
		return false
	}
	return true
}

// announcementPrincipals answers "who is this reader", in the same encoding announcement_grants
// stores: their own account, plus every OU from the root down to their own.
//
// This is the UNION along the chain, and it is deliberately the opposite of GrantedVersions, which
// takes the nearest ancestor with rows and stops. Both are right for what they model. A version
// grant is a right, so a child OU that has its own grants must not also inherit the root's. An
// announcement is a broadcast, so a notice addressed to a parent OU has to reach the whole subtree
// — and a child OU having its own announcement must never suppress the company-wide one.
//
// It also does not go through viewerScope/isRestricted. Those resolve run governance, cost ~19
// queries for a restricted user, and — the real reason — return "unrestricted" for every admin and
// every internal account, so deriving an audience from them would broadcast a targeted
// announcement to exactly the people it was not addressed to.
func (s *Server) announcementPrincipals(user string) []string {
	if user == "" {
		return nil
	}
	out := []string{userPrincipal(user)}
	for _, gid := range s.st.groupChain(user) {
		out = append(out, groupPrincipal(gid))
	}
	return out
}

// visibleAnnouncements filters the table down to what one reader may see, right now.
func (s *Server) visibleAnnouncements(user string) []Announcement {
	return s.visibleFor(s.announcementPrincipals(user))
}

// visibleFor is the filter itself, over an already-resolved principal set. The preview endpoint
// calls it with a synthetic set, which is the point of the split: "what would this OU see" must be
// answered by the same function that answers it for real, so the two can never drift.
func (s *Server) visibleFor(readerPrincipals []string) []Announcement {
	all := s.st.Announcements()
	if len(all) == 0 {
		return nil
	}
	principals := map[string]bool{}
	for _, p := range readerPrincipals {
		principals[p] = true
	}
	var grants map[int64][]string
	now := time.Now().UTC()
	var out []Announcement
	for _, a := range all {
		if !a.Enabled || (a.Title == "" && a.Content == "") {
			continue
		}
		if !announcementWindowOpen(a, now) {
			continue
		}
		if a.Audience == "grant" {
			// Default-deny, written out. Not expressed as an empty IN-list (a syntax error on both
			// drivers) and never as "no filter means no narrowing" — that inversion is how a
			// scoping bug becomes a disclosure.
			if len(principals) == 0 {
				continue
			}
			if grants == nil {
				grants = s.st.AllAnnouncementGrants()
			}
			matched := false
			for _, p := range grants[a.ID] {
				if principals[p] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// readerAnnouncementJSON is what a reader is allowed to know about an announcement. It carries
// neither audience nor grants: telling a reader that a targeted announcement exists — even without
// naming the target — leaks the shape of the OU tree. It carries no ord and no enabled either;
// those are admin state, and the list already arrives in order.
//
// endsAt IS included, because startVisiblePoll stops polling in a hidden tab: without it, a tab
// left open behind others would keep painting an incident banner hours after it expired.
func readerAnnouncementJSON(a Announcement) map[string]any {
	return map[string]any{
		"id": a.ID, "level": a.Level, "title": a.Title, "content": a.Content,
		"popup": a.Popup, "dismissible": a.Dismissible, "scope": a.Scope, "endsAt": a.EndsAt,
	}
}

// apiAnnouncements is the reader feed: every announcement addressed to the caller, in display
// order. Polled, so it answers 304 with an empty body while nothing changes.
// announcementCollapse reports whether the reader-facing bands fold their overflow behind a
// counter. It defaults to FALSE — show everything — because folding costs a click to read something
// an operator decided was worth interrupting people with, and because a band that hides its own
// contents behind a chevron is one readers stop trusting. It is a switch rather than a fixed rule
// because the right answer depends on how many announcements a portal actually runs at once, which
// only its operator knows.
func (s *Server) announcementCollapse() bool {
	return settingBool(s.st.GetSetting("announcement_collapse", ""), false)
}

func (s *Server) apiAnnouncements(w http.ResponseWriter, r *http.Request, user string) {
	list := s.visibleAnnouncements(user)
	items := make([]map[string]any, 0, len(list))
	for _, a := range list {
		items = append(items, readerAnnouncementJSON(a))
	}
	writeJSONIfChanged(w, r, map[string]any{"items": items, "collapse": s.announcementCollapse()})
}

// adminAnnouncementJSON is the full row, grants included — the admin console is where an operator
// diagnoses "did this reach anyone", so it withholds nothing.
func adminAnnouncementJSON(a Announcement) map[string]any {
	grants := a.Grants
	if grants == nil {
		grants = []string{}
	}
	return map[string]any{
		"id": a.ID, "level": a.Level, "title": a.Title, "content": a.Content,
		"ord": a.Ord, "enabled": a.Enabled, "popup": a.Popup, "dismissible": a.Dismissible,
		"scope": a.Scope, "audience": a.Audience, "grants": grants,
		"startsAt": a.StartsAt, "endsAt": a.EndsAt,
		"createdAt": a.CreatedAt, "createdBy": a.CreatedBy, "updatedAt": a.UpdatedAt,
	}
}

// maxPickerUsers bounds the account list the editor offers. VersionsPage puts every account into a
// Select with no paging, which is fine at fifty and unusable at five thousand; here the list is
// capped, the page says so, and an admin who needs an account past the cap types the principal.
const maxPickerUsers = 200

// apiAdminAnnouncements lists every row for the management page, plus the principals a row can be
// addressed to — one round trip, because the page cannot render the audience of an existing
// announcement without the names to render it with.
//
// Times go out as UTC instants and the editor renders them in the operator's own clock, the way
// every other instant in this app is shown (lib/datetime.ts): the panel timezone governs
// scheduling, not display.
func (s *Server) apiAdminAnnouncements(w http.ResponseWriter, r *http.Request, user string) {
	list := s.st.AnnouncementsWithGrants()
	items := make([]map[string]any, 0, len(list))
	for _, a := range list {
		items = append(items, adminAnnouncementJSON(a))
	}
	// The Default OU is on every account's chain, so addressing it is addressing everybody — which
	// audience='all' already says. The save path refuses it; leaving it out of the picker means an
	// admin never has to discover that by being rejected.
	groups, users, truncated := s.recipientPicker(false)
	writeJSON(w, map[string]any{
		"items": items, "groups": groups, "users": users,
		"usersTruncated": truncated,
		"collapse":       s.announcementCollapse(),
	})
}

// announcementImpact describes what deleting a principal would do to the announcements addressed to
// it. `orphaned` is the sharp end: this principal is the ONLY recipient, so once its grants are
// swept the announcement reaches nobody and says nothing about it.
type announcementImpact struct {
	affected []Announcement
	orphaned []int64
}

func (s *Server) announcementImpactOf(principal string) announcementImpact {
	out := announcementImpact{}
	for _, a := range s.st.AnnouncementsGrantedTo(principal) {
		out.affected = append(out.affected, a)
		if len(a.Grants) == 1 {
			out.orphaned = append(out.orphaned, a.ID)
		}
	}
	return out
}

func announcementImpactJSON(im announcementImpact) []map[string]any {
	orphan := map[int64]bool{}
	for _, id := range im.orphaned {
		orphan[id] = true
	}
	items := make([]map[string]any, 0, len(im.affected))
	for _, a := range im.affected {
		items = append(items, map[string]any{
			"id": a.ID, "title": a.Title, "level": a.Level,
			"enabled": a.Enabled, "orphaned": orphan[a.ID],
		})
	}
	return items
}

// apiAnnouncementPreview answers "what would this principal actually see".
//
// It exists because admins are NOT exempt from the audience filter (see announcementPrincipals), so
// a targeted announcement fails silently: the admin who wrote it cannot see it, the people who
// should receive it do not know to expect it, and "addressed correctly" and "addressed to nobody"
// look identical from the console. This is the only cheap way to tell them apart before publishing.
//
// It reuses visibleFor rather than reimplementing the predicate, so the preview cannot drift from
// the real answer — a preview that lies is worse than no preview.
func (s *Server) apiAnnouncementPreview(w http.ResponseWriter, r *http.Request, user string) {
	raw := strings.TrimSpace(r.URL.Query().Get("principal"))
	var principals []string
	switch {
	case strings.HasPrefix(raw, "g:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "g:"), 10, 64)
		if err != nil || !s.userGroupExists(id) {
			jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
			return
		}
		// A member of this OU, not the OU alone: an announcement addressed to a parent reaches the
		// whole subtree, so the preview has to carry the ancestry the read path would have built.
		for _, gid := range s.st.groupAncestry(id, s.st.DefaultGroupID()) {
			principals = append(principals, groupPrincipal(gid))
		}
	case strings.HasPrefix(raw, "u:"):
		name := strings.TrimSpace(strings.TrimPrefix(raw, "u:"))
		if !s.st.UsernameTaken(name) {
			jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
			return
		}
		principals = s.announcementPrincipals(name)
	default:
		jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
		return
	}
	list := s.visibleFor(principals)
	items := make([]map[string]any, 0, len(list))
	for _, a := range list {
		items = append(items, readerAnnouncementJSON(a))
	}
	writeJSON(w, map[string]any{"items": items})
}

// announcementInput is the create/update body. Every optional field is a pointer for the usual
// reason — an omitted field means "leave it", not "clear it" — but Grants is a pointer for a
// sharper one: a nil there has to keep the existing principals, so that no partial write can
// silently empty an announcement's audience.
type announcementInput struct {
	Level, Title, Content, Scope, Audience, StartsAt, EndsAt, UpdatedAt *string
	Enabled, Popup, Dismissible                                         *bool
	Grants                                                              *[]string
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// parseAnnouncementBound normalizes one end of the time window to an RFC3339 UTC instant. The
// input is an instant too (the picker sends one), so this is a re-render, not a zone conversion:
// storing the operator's civil string instead would silently change what the row means the next
// time somebody edits the panel timezone.
func parseAnnouncementBound(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}

// validateAnnouncement checks the whole input and returns the row to write. Everything is
// validated before anything is written, which is the invariant the existing settings tests pin:
// a rejected save must not half-apply.
func (s *Server) validateAnnouncement(w http.ResponseWriter, in announcementInput, cur *Announcement) (Announcement, bool) {
	a := Announcement{}
	if cur != nil {
		a = *cur
	}
	if in.Level != nil {
		if !validAnnouncementLevel(*in.Level) {
			jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_level", "无效的公告级别")
			return a, false
		}
		a.Level = normalizeAnnouncementLevel(*in.Level)
	} else if a.Level == "" {
		a.Level = "notice"
	}
	if in.Title != nil {
		if len([]rune(str(in.Title))) > maxAnnouncementTitleRunes {
			jsonErrorCode(w, http.StatusBadRequest, "announcement_title_too_long", "公告标题过长")
			return a, false
		}
		a.Title = str(in.Title)
	}
	if in.Content != nil {
		if len([]rune(str(in.Content))) > maxAnnouncementContentRunes {
			jsonErrorCode(w, http.StatusBadRequest, "announcement_content_too_long", "公告内容过长")
			return a, false
		}
		a.Content = str(in.Content)
	}
	if in.Scope != nil {
		if v := strings.ToLower(str(in.Scope)); v != "home" && v != "app" {
			jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_scope", "无效的显示范围")
			return a, false
		}
		a.Scope = normalizeAnnouncementScope(*in.Scope)
	} else if a.Scope == "" {
		a.Scope = "home"
	}
	if in.Audience != nil {
		if v := strings.ToLower(str(in.Audience)); v != "all" && v != "grant" {
			jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_audience", "无效的公告受众")
			return a, false
		}
		a.Audience = normalizeAnnouncementAudience(*in.Audience)
	} else if a.Audience == "" {
		a.Audience = "all"
	}
	starts, ok := parseAnnouncementBound(str(in.StartsAt))
	if in.StartsAt != nil {
		if !ok {
			jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_window", "无效的展示时间段")
			return a, false
		}
		a.StartsAt = starts
	}
	ends, ok := parseAnnouncementBound(str(in.EndsAt))
	if in.EndsAt != nil {
		if !ok {
			jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_window", "无效的展示时间段")
			return a, false
		}
		a.EndsAt = ends
	}
	// A window that has already closed before it opens shows the announcement to nobody, ever.
	// That is never what the operator meant, and it is invisible after saving.
	if a.StartsAt != "" && a.EndsAt != "" && a.EndsAt <= a.StartsAt {
		jsonErrorCode(w, http.StatusBadRequest, "bad_announcement_window", "结束时间必须晚于开始时间")
		return a, false
	}
	a.Enabled = boolOr(in.Enabled, cur == nil || a.Enabled)
	a.Popup = boolOr(in.Popup, cur != nil && a.Popup)
	a.Dismissible = boolOr(in.Dismissible, cur != nil && a.Dismissible)
	return a, true
}

// userGroupExists resolves an OU id off the full list. There is no by-id store lookup and this
// runs once per grant on a save, over a table with a handful of rows.
func (s *Server) userGroupExists(id int64) bool {
	for _, g := range s.st.ListUserGroups() {
		if g.ID == id {
			return true
		}
	}
	return false
}

// normalizePrincipals validates every principal and re-encodes it. A hand-written "u:Alice" stored
// verbatim would never match the lower-cased principal the read path builds: an audience nobody is
// in, with nothing anywhere to say so.
//
// Shared with hand-written reports (report_edit_api.go), which address a reader through the same
// "g:<id>" / "u:<name>" encoding. Deliberately one function: this validates the input to a
// disclosure decision, and a second copy of it is a second thing that has to stay right.
//
// allowDefaultOU is the one difference between the two callers. groupChain always appends the
// Default OU, so that principal matches every account there is; an announcement refuses it and
// points at its own "everyone" setting, while a report's audience has no separate setting and
// spells "everyone" as exactly this principal.
func (s *Server) normalizePrincipals(w http.ResponseWriter, in []string, allowDefaultOU bool) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	defaultOU := s.st.DefaultGroupID()
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(raw, "g:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(raw, "g:"), 10, 64)
			if err != nil || !s.userGroupExists(id) {
				jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
				return nil, false
			}
			// groupChain always appends the Default OU, so granting it matches every account —
			// including every external tenant hanging beneath it. audience='all' already says
			// that, out loud. Refusing here rather than only hiding it in the picker, because an
			// admin who types it deserves the answer, not the outcome.
			if id == defaultOU && !allowDefaultOU {
				jsonErrorCode(w, http.StatusBadRequest, "announcement_default_ou_audience",
					"默认分组等同于全体用户，请改用「所有人」")
				return nil, false
			}
			raw = groupPrincipal(id)
		case strings.HasPrefix(raw, "u:"):
			// UsernameTaken, not GetUser: it folds case the same way userPrincipal does, so an
			// account stored as `Alice` validates against the `u:alice` the read path will build.
			// An exact-match lookup would reject a principal that is in fact reachable.
			name := strings.TrimSpace(strings.TrimPrefix(raw, "u:"))
			if !s.st.UsernameTaken(name) {
				jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
				return nil, false
			}
			raw = userPrincipal(name)
		default:
			jsonErrorCode(w, http.StatusBadRequest, "unknown_principal", "投放对象不存在："+raw)
			return nil, false
		}
		if !seen[raw] {
			seen[raw] = true
			out = append(out, raw)
		}
	}
	return out, true
}

// audienceOK rejects a targeted announcement with nobody in its audience. Saving it would store a
// row that reaches no one and reports no problem — the standard path to an operator concluding the
// feature is broken and switching everything back to "everyone".
func audienceOK(w http.ResponseWriter, a Announcement, grants []string) bool {
	if a.Audience == "grant" && len(grants) == 0 {
		jsonErrorCode(w, http.StatusBadRequest, "announcement_audience_empty", "请至少选择一个投放对象")
		return false
	}
	return true
}

func (s *Server) apiAnnouncementAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in announcementInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	a, ok := s.validateAnnouncement(w, in, nil)
	if !ok {
		return
	}
	grants := []string{}
	if in.Grants != nil {
		if grants, ok = s.normalizePrincipals(w, *in.Grants, false); !ok {
			return
		}
	}
	if !audienceOK(w, a, grants) {
		return
	}
	a.CreatedBy = user
	id, err := s.st.AddAnnouncement(a)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(grants) > 0 {
		if err := s.st.SetAnnouncementGrants(id, grants); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.recordChange(r, user, AuditPolicyChange, "announcement", strconv.FormatInt(id, 10),
		map[string]any{"op": "create", "title": a.Title, "level": a.Level, "scope": a.Scope})
	if a.Audience == "grant" {
		s.recordChange(r, user, AuditGrantChange, "announcement", strconv.FormatInt(id, 10),
			map[string]any{"before": []string{}, "after": grants})
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiAnnouncementSave(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	cur := s.st.GetAnnouncement(id)
	if cur == nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	var in announcementInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Optimistic concurrency, and the reason is disclosure rather than tidiness: with two admins
	// on the page, one editing the audience and one editing the text, a last-write-wins PUT
	// reverts the audience change AND records it against the wrong person in the audit log.
	if in.UpdatedAt != nil && str(in.UpdatedAt) != cur.UpdatedAt {
		jsonErrorCode(w, http.StatusConflict, "announcement_stale", "公告已被他人修改，请刷新后重试")
		return
	}
	a, ok := s.validateAnnouncement(w, in, cur)
	if !ok {
		return
	}
	before := s.st.AllAnnouncementGrants()[id]
	grants := before
	if in.Grants != nil { // nil = leave the audience alone; see announcementInput
		if grants, ok = s.normalizePrincipals(w, *in.Grants, false); !ok {
			return
		}
	}
	if !audienceOK(w, a, grants) {
		return
	}
	a.ID = id
	if err := s.st.UpdateAnnouncement(a); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordChange(r, user, AuditPolicyChange, "announcement", strconv.FormatInt(id, 10),
		map[string]any{"op": "update", "title": a.Title, "level": a.Level, "scope": a.Scope})
	if in.Grants != nil && !sameStrings(before, grants) {
		if err := s.st.SetAnnouncementGrants(id, grants); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Written after the change lands, with both sides, like version_api's grant trail: an
		// audience line that records an intent rather than an outcome is worse than none.
		s.recordChange(r, user, AuditGrantChange, "announcement", strconv.FormatInt(id, 10),
			map[string]any{"before": before, "after": grants})
	}
	writeJSON(w, okJSON)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// apiAnnouncementToggle serves the two inline switches in the list, and only them. A whole-row PUT
// would work — it is what LinksPage does — but a link row carries nothing but decoration, and one
// of these rows carries an audience. Re-sending it to flip a switch is a disclosure change wearing
// the costume of a convenience.
func (s *Server) apiAnnouncementToggle(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	if s.st.GetAnnouncement(id) == nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	var in struct{ Enabled, Popup *bool }
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	detail := map[string]any{"op": "toggle"}
	for _, f := range []struct {
		name string
		val  *bool
	}{{"enabled", in.Enabled}, {"popup", in.Popup}} {
		if f.val == nil {
			continue
		}
		if err := s.st.SetAnnouncementFlag(id, f.name, *f.val); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		detail[f.name] = *f.val
	}
	s.recordChange(r, user, AuditPolicyChange, "announcement", strconv.FormatInt(id, 10), detail)
	writeJSON(w, okJSON)
}

func (s *Server) apiAnnouncementDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	cur := s.st.GetAnnouncement(id) // read first: the audit line wants the title and audience it had
	if cur == nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	grants := s.st.AllAnnouncementGrants()[id]
	if err := s.st.DeleteAnnouncement(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordChange(r, user, AuditPolicyChange, "announcement", strconv.FormatInt(id, 10),
		map[string]any{"op": "delete", "title": cur.Title, "audience": cur.Audience, "grants": grants})
	writeJSON(w, okJSON)
}

// apiAnnouncementReorder replaces the whole order in one call. Two things it does that
// apiLinkLayout does not, both learned from that handler: it reports its errors (that one discards
// readJSON's error and every store error and answers ok regardless, and the page swallows it), and
// it refuses an id set that is not exactly the current one.
//
// That refusal covers two failures with one check. Concurrently, two admins dragging at once can
// no longer half-overwrite each other's order. Individually, a page whose GET failed can no longer
// send "replace the entire order" from a list it never managed to render — the v0.4.35 lesson
// (a failed load must not become a destructive save), one row multiplied by N.
func (s *Server) apiAnnouncementReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	current := map[int64]bool{}
	for _, a := range s.st.Announcements() {
		current[a.ID] = true
	}
	seen := map[int64]bool{}
	for _, id := range in.IDs {
		if !current[id] || seen[id] {
			jsonErrorCode(w, http.StatusConflict, "announcement_reorder_stale", "公告列表已变化，请刷新后重试")
			return
		}
		seen[id] = true
	}
	if len(seen) != len(current) {
		jsonErrorCode(w, http.StatusConflict, "announcement_reorder_stale", "公告列表已变化，请刷新后重试")
		return
	}
	if err := s.st.ReorderAnnouncements(in.IDs); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Deliberately not audited: order is presentation, links reordering is not audited either, and
	// a log line per drag is noise that buries the changes an operator actually searches for.
	writeJSON(w, okJSON)
}
