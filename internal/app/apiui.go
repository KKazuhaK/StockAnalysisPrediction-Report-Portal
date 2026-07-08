package app

// apiui.go —— JSON API layer consumed by the browser (React SPA). Kept separate from Dify's
// Bearer-token API (main.go): everything here uses signed-cookie session auth
// (requireUserJSON / requireAdminJSON). Domain logic (grouping / timeline ordering / type
// registry) is still reused on the Go side; React only renders, avoiding duplicate
// implementations and drift.

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var okJSON = map[string]any{"ok": true}

const (
	maxSiteTitleRunes           = 80
	maxFooterTextRunes          = 1000
	maxAnnouncementTitleRunes   = 160
	maxAnnouncementContentRunes = 2000
	maxSiteLogoBytes            = 1024 * 1024
)

// jsonError writes a uniform JSON error response.
func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// readJSON parses the request JSON body (capped at 1MB).
func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

// ---------- Session auth middleware (JSON variant: 401/403 return JSON instead of redirecting) ----------

func (s *Server) requireUserJSON(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentActiveUser(r)
		if u == "" {
			jsonError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) requireAdminJSON(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentActiveUser(r)
		if u == "" {
			jsonError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !s.isAdmin(u) {
			jsonError(w, http.StatusForbidden, "forbidden")
			return
		}
		h(w, r, u)
	}
}

// requirePermJSON wraps a handler so only a logged-in user whose role holds perm
// may reach it. Generalises requireAdminJSON (which is requirePermJSON(PermManage)).
func (s *Server) requirePermJSON(perm string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentActiveUser(r)
		if u == "" {
			jsonError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !s.hasPerm(u, perm) {
			jsonError(w, http.StatusForbidden, "forbidden")
			return
		}
		h(w, r, u)
	}
}

// canQuery allows two kinds of query auth: a logged-in browser session, or a Bearer token with the query scope (Dify).
func (s *Server) canQuery(r *http.Request) bool {
	if s.currentActiveUser(r) != "" {
		return true
	}
	return s.tokenOK(r, "query")
}

// ---------- Public site chrome ----------

// normalizeHomeMoreStyle validates how the home-page "More" button reveals folded quick
// links; anything unrecognized falls back to inline expand.
func normalizeHomeMoreStyle(v string) string {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "modal", "popover":
		return strings.TrimSpace(strings.ToLower(v))
	default:
		return "expand"
	}
}

func (s *Server) siteSettingsJSON() map[string]any {
	return map[string]any{
		"siteTitle":           s.st.GetSetting("site_title", ""),
		"siteLogoUrl":         s.st.GetSetting("site_logo_url", ""),
		"homeMoreStyle":       normalizeHomeMoreStyle(s.st.GetSetting("home_more_style", "")),
		"footerText":          s.st.GetSetting("footer_text", ""),
		"footerShowInfo":      settingBool(s.st.GetSetting("footer_show_info", ""), true),
		"footerShowVersion":   settingBool(s.st.GetSetting("footer_show_version", ""), true),
		"pwaEnabled":          settingBool(s.st.GetSetting("pwa_enabled", ""), true),
		"pwaIconUrl":          s.st.GetSetting("pwa_icon_url", ""),
		"announcementEnabled": settingBool(s.st.GetSetting("announcement_enabled", ""), false),
		"announcementPopup":   settingBool(s.st.GetSetting("announcement_popup", ""), false),
		"announcementLevel":   normalizeAnnouncementLevel(s.st.GetSetting("announcement_level", "notice")),
		"announcementTitle":   s.st.GetSetting("announcement_title", ""),
		"announcementContent": s.st.GetSetting("announcement_content", ""),
	}
}

// apiSite returns public brand settings used before login as well as in the app shell.
func (s *Server) apiSite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.siteSettingsJSON())
}

// ---------- Authentication ----------

// apiMe returns the current login state. Not logged in → 401, so the frontend switches to the login page.
func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	u := s.currentActiveUser(r)
	if u == "" {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, s.meJSON(u))
}

// meJSON is the shared "who am I" payload for /api/me and /api/login (email +
// mail_enabled drive the "email me when done" opt-in).
func (s *Server) meJSON(user string) map[string]any {
	role, name, email := "", user, ""
	if usr := s.st.GetUser(user); usr != nil {
		role, name, email = usr.EffRole(), usr.Name(), usr.Email
	}
	return map[string]any{
		"user": user, "name": name, "admin": s.isAdmin(user), "role": role, "perms": permsOf(role),
		"email": email, "mail_enabled": s.emailEnabled(),
	}
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	u := s.st.GetUser(strings.TrimSpace(in.Username))
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		jsonError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !u.Active {
		jsonError(w, http.StatusForbidden, "账号已停用")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sign(u.Username), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	s.st.TouchLastLogin(u.Username)
	log.Printf("login %s", u.Username)
	writeJSON(w, s.meJSON(u.Username))
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, okJSON)
}

// ---------- Home page: search + card list + pagination (reuses the SSR grouping/filtering logic) ----------

func (s *Server) apiHome(w http.ResponseWriter, r *http.Request, user string) {
	f, src, size, page := s.filtersFrom(r)
	var reps []Rep
	var newTotal, oldTotal int
	if src == "all" || src == "new" {
		nn, _ := s.st.SearchNew(f)
		newTotal = len(nn)
		reps = append(reps, nn...)
	}
	// oldTotal stays 0: legacy reports were migrated into the reports table and now
	// come back via SearchNew above (the live old-portal read path is gone).
	groups := buildGroups(reps, s.names.Get)
	// Browse/search feed shows one card per stock (its latest run); the full per-date
	// history stays on the stock detail page. Thematic (symbol-less) reports are unaffected.
	groups = collapseLatestBySymbol(groups)
	totalRuns := len(groups)
	pages := int(math.Max(1, math.Ceil(float64(totalRuns)/float64(size))))
	lo := (page - 1) * size
	hi := lo + size
	if lo > len(groups) {
		lo = len(groups)
	}
	if hi > len(groups) {
		hi = len(groups)
	}
	types := uniqSorted(s.st.NewTypes())
	// 大类 filter options: the categories actually present, in the canonical
	// pipeline order (kindOrder), with any non-standard kinds appended.
	present := map[string]bool{}
	for _, k := range s.st.ReportKinds() {
		present[k] = true
	}
	kinds := make([]string, 0)
	for _, k := range kindOrder {
		if present[k] {
			kinds = append(kinds, k)
			delete(present, k)
		}
	}
	for _, k := range s.st.ReportKinds() {
		if present[k] {
			kinds = append(kinds, k)
		}
	}
	writeJSON(w, map[string]any{
		"groups":   groupsJSON(groups[lo:hi]),
		"newTotal": newTotal, "oldTotal": oldTotal, "totalRuns": totalRuns,
		"page": page, "pages": pages, "size": size,
		"types": types, "kinds": kinds, "links": linksJSON(s.st.Links()), "linkGroups": linkGroupsJSON(s.st.LinkGroups()),
		"kindColors": s.st.KindColors(),
	})
}

func groupsJSON(gs []Group) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		members := make([]map[string]any, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, map[string]any{"rid": m.RID, "rtype": m.RType, "kind": repKind(m), "title": m.Title})
		}
		out = append(out, map[string]any{
			"key": g.Key, "symbol": g.Symbol, "name": g.Name, "curName": g.CurName, "title": g.Title, "date": g.Date,
			"time": g.Time, "kind": g.Kind, "kinds": g.Kinds, "src": g.Src, "n": g.N, "members": members,
		})
	}
	return out
}

// ---------- Stock detail: timeline → category tab → document tab → body (reuses stockView logic) ----------

func (s *Server) apiStock(w http.ResponseWriter, r *http.Request, user string) {
	symbol := r.PathValue("symbol")
	all, _ := s.st.NewBySymbol(symbol)
	if len(all) == 0 {
		jsonError(w, http.StatusNotFound, "该标的暂无报告")
		return
	}
	// Newest date first; keep a stable order within a date by ingest/report time.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date
		}
		return all[i].Time < all[j].Time
	})
	q := r.URL.Query()
	var order []string // rdate DESC → newest to oldest
	byDate := map[string][]Rep{}
	for _, m := range all {
		if _, ok := byDate[m.Date]; !ok {
			order = append(order, m.Date)
		}
		byDate[m.Date] = append(byDate[m.Date], m)
	}
	selDate := q.Get("date")
	if _, ok := byDate[selDate]; !ok {
		selDate = order[0]
	}
	dateReps := byDate[selDate]
	kindSet := map[string]bool{}
	for _, m := range dateReps {
		kindSet[repKind(m)] = true
	}
	var kinds []string
	for _, k := range kindOrder {
		if kindSet[k] {
			kinds = append(kinds, k)
			delete(kindSet, k)
		}
	}
	for k := range kindSet {
		kinds = append(kinds, k)
	}
	selKind := q.Get("kind")
	if !containsStr(kinds, selKind) {
		selKind = kinds[0]
	}
	var kindReps []Rep
	for _, m := range dateReps {
		if repKind(m) == selKind {
			kindReps = append(kindReps, m)
		}
	}
	for i := range kindReps {
		kindReps[i].Label = label(kindReps[i])
	}
	kindReps, defRID := s.orderAndDefault(kindReps)
	selRID := q.Get("r")
	if !repInList(kindReps, selRID) {
		selRID = defRID
	}
	rep := s.loadRep(selRID)
	timeline := make([]map[string]any, 0, len(order)) // newest to oldest
	for _, d := range order {
		timeline = append(timeline, map[string]any{"date": d, "n": len(byDate[d])})
	}
	subtabs := make([]map[string]any, 0, len(kindReps))
	for _, m := range kindReps {
		subtabs = append(subtabs, map[string]any{"rid": m.RID, "label": m.Label, "rtype": m.RType})
	}
	writeJSON(w, map[string]any{
		"symbol": symbol, "name": s.names.Get(symbol),
		"selDate": selDate, "selKind": selKind, "selRID": selRID,
		"timeline": timeline, "kinds": kinds, "subtabs": subtabs,
		"rep": repJSON(rep, s.names.Get),
	})
}

// apiRun reads a report group (legacy report card / single report): member tabs + selected body.
func (s *Server) apiRun(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	members := s.runMembers(key)
	if len(members) == 0 {
		jsonError(w, http.StatusNotFound, "未找到该 run")
		return
	}
	members, defRID := s.orderAndDefault(members)
	selRID := r.URL.Query().Get("r")
	if !repInList(members, selRID) {
		selRID = defRID
	}
	rep := s.loadRep(selRID)
	tabs := make([]map[string]any, 0, len(members))
	for _, m := range members {
		tabs = append(tabs, map[string]any{"rid": m.RID, "label": m.Label, "rtype": m.RType})
	}
	first := members[0]
	writeJSON(w, map[string]any{
		"key": key, "symbol": first.Symbol, "name": s.names.Get(first.Symbol), "date": first.Date,
		"selRID": selRID, "tabs": tabs, "rep": repJSON(rep, s.names.Get),
	})
}

// apiRepBody fetches a single report body by rid (frontend lazy-loads on tab switch, avoiding a full-page refetch).
func (s *Server) apiRepBody(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(strings.TrimSpace(r.URL.Query().Get("rid")))
	if rep == nil {
		jsonError(w, http.StatusNotFound, "报告不存在")
		return
	}
	writeJSON(w, repJSON(rep, s.names.Get))
}

func repJSON(rep *Rep, nameOf func(string) string) map[string]any {
	if rep == nil {
		return nil
	}
	cur := ""
	if nameOf != nil {
		cur = nameOf(rep.Symbol)
	}
	asof := firstNonEmpty(rep.Name, cur) // as-of snapshot; fall back to current for pre-snapshot reports
	return map[string]any{
		"rid": rep.RID, "uid": rep.UID, "title": rep.Title, "symbol": rep.Symbol,
		"name": asof, "curName": cur, // name = as-of; curName = current (client shows both when they differ)
		"date": rep.Date, "time": rep.Time, "kind": repKind(*rep), "rtype": rep.RType, "source": rep.Source,
		"md": rep.MD, "html": rep.HTML,
	}
}

// ---------- Admin: entry buttons ----------

func linksJSON(ls []Link) []map[string]any {
	out := make([]map[string]any, 0, len(ls))
	for _, l := range ls {
		out = append(out, map[string]any{"id": l.ID, "label": l.Label, "url": l.URL, "icon": l.Icon, "newTab": l.NewTab, "groupId": l.GroupID, "ord": l.Ord})
	}
	return out
}

func linkGroupsJSON(gs []LinkGroup) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "mode": g.Mode, "showLabel": g.ShowLabel, "icon": g.Icon, "ord": g.Ord})
	}
	return out
}

func (s *Server) apiAdminLinks(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"links": linksJSON(s.st.Links()), "groups": linkGroupsJSON(s.st.LinkGroups())})
}

func (s *Server) apiLinkAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Label, URL, Icon string
		NewTab           *bool // pointer so an omitted field defaults to true (open in new tab)
	}
	readJSON(r, &in)
	ord := 0
	if ls := s.st.Links(); len(ls) > 0 {
		ord = ls[len(ls)-1].Ord + 1
	}
	// New links start ungrouped (top-level); grouping is done by dragging in the layout.
	s.st.AddLink(strings.TrimSpace(in.Label), strings.TrimSpace(in.URL), strings.TrimSpace(in.Icon), in.NewTab == nil || *in.NewTab, 0, ord)
	writeJSON(w, okJSON)
}

func (s *Server) apiLinkEdit(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Label, URL, Icon string
		NewTab           *bool
	}
	readJSON(r, &in)
	s.st.UpdateLinkFields(pathID(r, "id"), strings.TrimSpace(in.Label), strings.TrimSpace(in.URL), strings.TrimSpace(in.Icon), in.NewTab == nil || *in.NewTab)
	writeJSON(w, okJSON)
}

func (s *Server) apiLinkDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteLink(pathID(r, "id"))
	writeJSON(w, okJSON)
}

// apiLinkLayout persists the whole entry-button layout in one shot: the ordered mix of group
// headers and links (from the admin's single drag list). Walked once — each group gets its
// order; each link is assigned to the most recent group above it (0 = top-level) + an order.
func (s *Server) apiLinkLayout(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Items []struct {
			Kind string `json:"kind"` // "group" | "link"
			ID   int64  `json:"id"`
		} `json:"items"`
	}
	readJSON(r, &in)
	groupOrd, linkOrd := 0, 0
	var current int64
	for _, it := range in.Items {
		if it.Kind == "group" {
			s.st.SetLinkGroupOrder(it.ID, groupOrd)
			groupOrd++
			current = it.ID
		} else {
			s.st.SetLinkGroupAndOrder(it.ID, current, linkOrd)
			linkOrd++
		}
	}
	writeJSON(w, okJSON)
}

func normalizeLinkGroupMode(m string) string {
	switch m {
	case "row", "expand", "popover", "modal":
		return m
	default:
		return "row"
	}
}

func (s *Server) apiLinkGroupAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name, Mode, Icon string
		ShowLabel        *bool
	}
	readJSON(r, &in)
	ord := 0
	if gs := s.st.LinkGroups(); len(gs) > 0 {
		ord = gs[len(gs)-1].Ord + 1
	}
	id, err := s.st.AddLinkGroup(strings.TrimSpace(in.Name), normalizeLinkGroupMode(in.Mode), in.ShowLabel == nil || *in.ShowLabel, strings.TrimSpace(in.Icon), ord)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

func (s *Server) apiLinkGroupEdit(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name, Mode, Icon string
		ShowLabel        *bool
	}
	readJSON(r, &in)
	s.st.UpdateLinkGroup(pathID(r, "id"), strings.TrimSpace(in.Name), normalizeLinkGroupMode(in.Mode), in.ShowLabel == nil || *in.ShowLabel, strings.TrimSpace(in.Icon))
	writeJSON(w, okJSON)
}

func (s *Server) apiLinkGroupDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteLinkGroup(pathID(r, "id"))
	writeJSON(w, okJSON)
}

// ---------- Admin: report types (category dropdown + drag-and-drop + add/remove, reuses manageTypes grouping) ----------

func (s *Server) apiAdminTypes(w http.ResponseWriter, r *http.Request, user string) {
	cfg := s.st.TypeConfigs()
	// Group each type by its ACTUAL category so custom (user-added) categories get
	// their own group. Order: preset categories (that have rows), then custom
	// categories (sorted), then the "其他" catch-all last.
	presets := []string{"重组决策", "投资决策", "深度研究", "技术分析", "事件监测"}
	presetSet := map[string]bool{}
	for _, k := range presets {
		presetSet[k] = true
	}
	byKind := map[string][]map[string]any{}
	for _, name := range s.st.DiscoveredTypes() {
		c := cfg[name]
		k := c.Kind
		if k == "" {
			k = runKind([]string{name})
		}
		if k == "" {
			k = "其他"
		}
		byKind[k] = append(byKind[k], map[string]any{
			"name": name, "kind": k, "ord": c.Ord, "isSummary": c.IsSummary, "label": c.Label})
	}
	var custom []string
	for k := range byKind {
		if !presetSet[k] && k != "其他" {
			custom = append(custom, k)
		}
	}
	sort.Strings(custom)

	var order []string
	for _, k := range presets {
		if len(byKind[k]) > 0 {
			order = append(order, k)
		}
	}
	order = append(order, custom...)
	if len(byKind["其他"]) > 0 {
		order = append(order, "其他")
	}

	var groups []map[string]any
	for _, k := range order {
		rows := byKind[k]
		sort.SliceStable(rows, func(i, j int) bool {
			oi, oj := rows[i]["ord"].(int), rows[j]["ord"].(int)
			if oi != oj {
				return oi < oj
			}
			return rows[i]["name"].(string) < rows[j]["name"].(string)
		})
		groups = append(groups, map[string]any{"kind": k, "rows": rows})
	}
	// Dropdown suggestions: preset categories + existing custom ones (users can
	// also type a brand-new category via the AutoComplete on the client).
	kinds := append([]string{}, presets...)
	kinds = append(kinds, custom...)
	writeJSON(w, map[string]any{"groups": groups, "kinds": kinds, "colors": s.st.KindColors()})
}

// apiKindColorSave upserts the antd Tag color for one kind (大类), used by the
// color picker next to each kind-group header on the Types Management page.
func (s *Server) apiKindColorSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Kind  string
		Color string
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		jsonError(w, http.StatusBadRequest, "kind required")
		return
	}
	s.st.SetKindColor(kind, strings.TrimSpace(in.Color))
	writeJSON(w, okJSON)
}

func (s *Server) apiTypesSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Rows []struct {
			Name    string
			Label   string
			Kind    string
			Summary bool
		} `json:"rows"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	cfg := s.st.TypeConfigs() // keep existing sort positions (ord is only changed by drag-and-drop)
	for _, row := range in.Rows {
		kind := strings.TrimSpace(row.Kind)
		s.st.UpsertTypeConfig(row.Name, kind, strings.TrimSpace(row.Label), cfg[row.Name].Ord, row.Summary)
		if kind != "" && kind != cfg[row.Name].Kind {
			s.st.SetReportsKind(row.Name, kind) // propagate the category change to already-stored reports
		}
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiTypesAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name    string
		Kind    string
		Label   string
		Summary bool
	}
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "name required")
		return
	}
	ord := 1
	for _, c := range s.st.TypeConfigs() {
		if c.Ord >= ord {
			ord = c.Ord + 1
		}
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = runKind([]string{name})
	}
	s.st.UpsertTypeConfig(name, kind, strings.TrimSpace(in.Label), ord, in.Summary)
	writeJSON(w, okJSON)
}

func (s *Server) apiTypesReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Names []string `json:"names"`
	}
	readJSON(r, &in)
	for i, n := range in.Names {
		s.st.SetTypeOrder(n, i)
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiTypesDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteTypeConfig(r.PathValue("name"))
	writeJSON(w, okJSON)
}

// apiTypesRestoreDefaults wipes the type configuration and re-seeds the shipped
// first-run defaults — the "恢复默认" button. The page returns to exactly the set
// the program generates on first run; admin-added custom types are removed.
// Report data is untouched: a type that still has reports reappears as an
// unconfigured (discovered) entry. Returns how many defaults were seeded.
func (s *Server) apiTypesRestoreDefaults(w http.ResponseWriter, r *http.Request, user string) {
	s.st.ClearTypeConfigs()
	n := seedDefaultTypes(s.st)
	seedDefaultKindColors(s.st) // also restore the shipped kind → color mapping
	writeJSON(w, map[string]any{"ok": true, "restored": n})
}

// ---------- Admin: accounts ----------

// userJSON is the enriched account row the admin UI renders. primaryGroup is 0 when
// the user has no assigned group (they inherit the Default group).
func userJSON(u User, primaryGroup int64) map[string]any {
	return map[string]any{
		"username": u.Username, "role": u.EffRole(), "display_name": u.DisplayName,
		"email": u.Email, "active": u.Active, "last_login": u.LastLogin, "primary_group": primaryGroup,
	}
}

func (s *Server) apiAdminUsers(w http.ResponseWriter, r *http.Request, user string) {
	us := s.st.Users()
	primary := s.st.AllPrimaryGroups()
	out := make([]map[string]any, 0, len(us))
	for _, u := range us {
		out = append(out, userJSON(u, primary[u.Username]))
	}
	roles := make([]map[string]any, 0, len(roleRegistry))
	for _, ro := range roleRegistry {
		roles = append(roles, map[string]any{"code": ro.Code, "name": ro.Name})
	}
	writeJSON(w, map[string]any{"users": out, "me": user, "roles": roles, "groups": userGroupsJSON(s.st.ListUserGroups())})
}

func (s *Server) apiUserAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		Role         string `json:"role"`
		DisplayName  string `json:"display_name"`
		Email        string `json:"email"`
		PrimaryGroup int64  `json:"primary_group"`
	}
	readJSON(r, &in)
	name := strings.TrimSpace(in.Username)
	if name == "" || in.Password == "" {
		jsonError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if s.st.GetUser(name) != nil {
		jsonError(w, http.StatusBadRequest, "username already exists")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	s.st.UpsertUser(User{Username: name, PasswordHash: string(h), Role: validRole(in.Role)})
	s.st.SetUserProfile(name, strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.Email))
	s.st.SetPrimaryGroup(name, in.PrimaryGroup)
	writeJSON(w, okJSON)
}

// apiUserSave is a partial update: only the fields present in the body are applied,
// so the password-reset modal and the full edit form can share one endpoint.
func (s *Server) apiUserSave(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	if u == nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Role         *string `json:"role"`
		Password     string  `json:"password"`
		DisplayName  *string `json:"display_name"`
		Email        *string `json:"email"`
		Active       *bool   `json:"active"`
		PrimaryGroup *int64  `json:"primary_group"`
	}
	readJSON(r, &in)
	if in.Role != nil {
		newRole := validRole(*in.Role)
		if newRole != "admin" && u.IsAdmin() && s.st.CountAdmins() <= 1 { // never demote the last admin
			newRole = "admin"
		}
		s.st.SetUserRole(name, newRole)
	}
	if pw := strings.TrimSpace(in.Password); pw != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		s.st.SetUserPassword(name, string(h))
	}
	if in.DisplayName != nil || in.Email != nil {
		dn, em := u.DisplayName, u.Email
		if in.DisplayName != nil {
			dn = strings.TrimSpace(*in.DisplayName)
		}
		if in.Email != nil {
			em = strings.TrimSpace(*in.Email)
		}
		s.st.SetUserProfile(name, dn, em)
	}
	if in.Active != nil {
		active := *in.Active
		if !active && (name == user || (u.IsAdmin() && s.st.CountAdmins() <= 1)) {
			active = true // can't disable yourself or the last admin
		}
		s.st.SetUserActive(name, active)
	}
	if in.PrimaryGroup != nil {
		s.st.SetPrimaryGroup(name, *in.PrimaryGroup)
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiUserDelete(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	if u != nil && name != user && !(u.IsAdmin() && s.st.CountAdmins() <= 1) {
		s.st.DeleteUser(name)
	}
	writeJSON(w, okJSON)
}

// ---------- Admin: system settings ----------
// old_base/old_user/old_pass persist as inert settings after the legacy importer was
// removed (the old portal is fully retired); they have no remaining consumer.

func (s *Server) apiAdminSettings(w http.ResponseWriter, r *http.Request, user string) {
	out := map[string]any{
		"oldBase":  s.st.GetSetting("old_base", ""),
		"oldUser":  s.st.GetSetting("old_user", ""),
		"hasPass":  s.st.GetSetting("old_pass", "") != "",
		"timezone": s.st.GetSetting("timezone", ""), // "" = follow system zone
		"newCount": s.st.CountNew(),
	}
	for k, v := range s.siteSettingsJSON() {
		out[k] = v
	}
	writeJSON(w, out)
}

// apiTypesRecompute re-applies the subtype→大类 (类型管理) mapping to every stored
// report — the "重新分类" button. Returns how many reports changed kind.
func (s *Server) apiTypesRecompute(w http.ResponseWriter, r *http.Request, user string) {
	n, err := s.st.RecomputeKinds()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "重新分类失败")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "updated": n})
}

func (s *Server) apiSettingsSave(w http.ResponseWriter, r *http.Request, user string) {
	// All pointers: a nil field was omitted from the request → leave that setting
	// untouched, so a timezone-only save can't wipe the legacy creds and vice-versa.
	var in struct {
		OldBase, OldUser, OldPass, Timezone, SiteTitle, SiteLogoUrl, FooterText, PwaIconUrl   *string
		AnnouncementLevel, AnnouncementTitle, AnnouncementContent, HomeMoreStyle              *string
		FooterShowInfo, FooterShowVersion, PwaEnabled, AnnouncementEnabled, AnnouncementPopup *bool
	}
	readJSON(r, &in)
	// Validate before writing anything so a bad field can't half-apply.
	if in.Timezone != nil {
		if tz := strings.TrimSpace(*in.Timezone); tz != "" {
			if _, err := time.LoadLocation(tz); err != nil {
				jsonError(w, http.StatusBadRequest, "无效的时区")
				return
			}
		}
	}
	if in.SiteTitle != nil && len([]rune(strings.TrimSpace(*in.SiteTitle))) > maxSiteTitleRunes {
		jsonError(w, http.StatusBadRequest, "站点标题过长")
		return
	}
	if in.SiteLogoUrl != nil && !validSiteLogoURL(strings.TrimSpace(*in.SiteLogoUrl)) {
		jsonError(w, http.StatusBadRequest, "无效的 Logo 地址")
		return
	}
	if in.FooterText != nil && len([]rune(strings.TrimSpace(*in.FooterText))) > maxFooterTextRunes {
		jsonError(w, http.StatusBadRequest, "底部信息过长")
		return
	}
	if in.AnnouncementLevel != nil && !validAnnouncementLevel(strings.TrimSpace(*in.AnnouncementLevel)) {
		jsonError(w, http.StatusBadRequest, "无效的公告级别")
		return
	}
	if in.AnnouncementTitle != nil && len([]rune(strings.TrimSpace(*in.AnnouncementTitle))) > maxAnnouncementTitleRunes {
		jsonError(w, http.StatusBadRequest, "公告标题过长")
		return
	}
	if in.AnnouncementContent != nil && len([]rune(strings.TrimSpace(*in.AnnouncementContent))) > maxAnnouncementContentRunes {
		jsonError(w, http.StatusBadRequest, "公告内容过长")
		return
	}
	if in.PwaIconUrl != nil && !validSiteLogoURL(strings.TrimSpace(*in.PwaIconUrl)) {
		jsonError(w, http.StatusBadRequest, "无效的安装图标地址")
		return
	}
	if in.OldBase != nil {
		s.st.SetSetting("old_base", strings.TrimSpace(*in.OldBase))
	}
	if in.OldUser != nil {
		s.st.SetSetting("old_user", strings.TrimSpace(*in.OldUser))
	}
	if in.OldPass != nil && *in.OldPass != "" { // empty = don't change the password
		s.st.SetSetting("old_pass", *in.OldPass)
	}
	if in.Timezone != nil { // "" clears → follow system zone
		s.st.SetSetting("timezone", strings.TrimSpace(*in.Timezone))
	}
	if in.SiteTitle != nil { // "" clears → localized default brand title
		s.st.SetSetting("site_title", strings.TrimSpace(*in.SiteTitle))
	}
	if in.SiteLogoUrl != nil { // "" clears → built-in SVG mark
		s.st.SetSetting("site_logo_url", strings.TrimSpace(*in.SiteLogoUrl))
	}
	if in.HomeMoreStyle != nil {
		s.st.SetSetting("home_more_style", normalizeHomeMoreStyle(*in.HomeMoreStyle))
	}
	if in.FooterText != nil { // "" clears → use the site title as footer text
		s.st.SetSetting("footer_text", strings.TrimSpace(*in.FooterText))
	}
	if in.FooterShowInfo != nil {
		s.st.SetSetting("footer_show_info", strconv.FormatBool(*in.FooterShowInfo))
	}
	if in.FooterShowVersion != nil {
		s.st.SetSetting("footer_show_version", strconv.FormatBool(*in.FooterShowVersion))
	}
	if in.PwaEnabled != nil {
		s.st.SetSetting("pwa_enabled", strconv.FormatBool(*in.PwaEnabled))
	}
	if in.PwaIconUrl != nil { // "" clears → follow site logo / built-in default logo
		s.st.SetSetting("pwa_icon_url", strings.TrimSpace(*in.PwaIconUrl))
	}
	if in.AnnouncementEnabled != nil {
		s.st.SetSetting("announcement_enabled", strconv.FormatBool(*in.AnnouncementEnabled))
	}
	if in.AnnouncementPopup != nil {
		s.st.SetSetting("announcement_popup", strconv.FormatBool(*in.AnnouncementPopup))
	}
	if in.AnnouncementLevel != nil {
		s.st.SetSetting("announcement_level", normalizeAnnouncementLevel(*in.AnnouncementLevel))
	}
	if in.AnnouncementTitle != nil {
		s.st.SetSetting("announcement_title", strings.TrimSpace(*in.AnnouncementTitle))
	}
	if in.AnnouncementContent != nil {
		s.st.SetSetting("announcement_content", strings.TrimSpace(*in.AnnouncementContent))
	}
	writeJSON(w, okJSON)
}

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

func settingBool(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func validSiteLogoURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > maxSiteLogoBytes {
		return false
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "data:image/") {
		meta, _, ok := strings.Cut(lower, ",")
		return ok && strings.HasSuffix(meta, ";base64")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return u.Host != ""
	case "":
		return strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, "\\")
	default:
		return false
	}
}

// ---------- Admin: multiple tokens (Dify API auth) ----------

func (s *Server) apiAdminTokens(w http.ResponseWriter, r *http.Request, user string) {
	ts := s.st.ListTokens()
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, map[string]any{"id": t.ID, "token": t.Token, "name": t.Name,
			"scope": t.Scope, "created": t.Created, "expires": t.Expires, "lastUsed": t.LastUsed})
	}
	writeJSON(w, map[string]any{"tokens": out})
}

func (s *Server) apiTokenAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct{ Name, Scope, Expires string }
	readJSON(r, &in)
	exp := strings.TrimSpace(in.Expires)
	if len(exp) == 10 { // date only → expires at 23:59:59 that day
		exp += " 23:59:59"
	}
	s.st.CreateToken(randToken(), strings.TrimSpace(in.Name), in.Scope, exp)
	writeJSON(w, okJSON)
}

func (s *Server) apiTokenDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteToken(pathID(r, "id"))
	writeJSON(w, okJSON)
}
