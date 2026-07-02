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
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var okJSON = map[string]any{"ok": true}

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
		u := s.currentUser(r)
		if u == "" {
			jsonError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) requireAdminJSON(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
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

// canQuery allows two kinds of query auth: a logged-in browser session, or a Bearer token with the query scope (Dify).
func (s *Server) canQuery(r *http.Request) bool {
	if s.currentUser(r) != "" {
		return true
	}
	return s.tokenOK(r, "query")
}

// ---------- Authentication ----------

// apiMe returns the current login state. Not logged in → 401, so the frontend switches to the login page.
func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == "" {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, map[string]any{"user": u, "admin": s.isAdmin(u)})
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
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sign(u.Username), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	log.Printf("login %s", u.Username)
	writeJSON(w, map[string]any{"user": u.Username, "admin": s.isAdmin(u.Username)})
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
	if src == "all" || src == "old" {
		oo, _ := s.st.SearchOldMeta(f)
		oldTotal = len(oo)
		reps = append(reps, oo...)
	}
	groups := buildGroups(reps, s.names.Get)
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
	types := uniqSorted(append(s.st.NewTypes(), s.st.OldCategories()...))
	writeJSON(w, map[string]any{
		"groups":   groupsJSON(groups[lo:hi]),
		"newTotal": newTotal, "oldTotal": oldTotal, "totalRuns": totalRuns,
		"page": page, "pages": pages, "size": size,
		"types": types, "links": linksJSON(s.st.Links()),
	})
}

// apiResearch lists symbol-less deep-research / topic reports (free-form Q&A not
// tied to a ticker), searchable by title and paginated.
func (s *Server) apiResearch(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("size"))
	if size != 15 && size != 30 && size != 50 {
		size = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	reps, total := s.st.ResearchReports(strings.TrimSpace(q.Get("q")), size, (page-1)*size)
	items := make([]map[string]any, 0, len(reps))
	for _, rep := range reps {
		items = append(items, map[string]any{
			"rid": rep.RID, "title": rep.Title, "rtype": rep.RType, "date": rep.Date, "source": rep.Source,
		})
	}
	pages := int(math.Max(1, math.Ceil(float64(total)/float64(size))))
	writeJSON(w, map[string]any{"items": items, "total": total, "page": page, "pages": pages, "size": size})
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
			"kind": g.Kind, "kinds": g.Kinds, "src": g.Src, "n": g.N, "members": members,
		})
	}
	return out
}

// ---------- Stock detail: timeline → category tab → document tab → body (reuses stockView logic) ----------

func (s *Server) apiStock(w http.ResponseWriter, r *http.Request, user string) {
	symbol := r.PathValue("symbol")
	all, _ := s.st.NewBySymbol(symbol)
	// Also fold in legacy reports for this symbol so the per-stock timeline covers
	// the full history (most data lives in old_meta), not just new reports.
	old, _ := s.st.SearchOldMeta(Filters{Symbol: symbol})
	all = append(all, old...)
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
		"date": rep.Date, "kind": repKind(*rep), "rtype": rep.RType, "source": rep.Source,
		"md": rep.MD, "html": rep.HTML,
	}
}

// ---------- Admin: entry buttons ----------

func linksJSON(ls []Link) []map[string]any {
	out := make([]map[string]any, 0, len(ls))
	for _, l := range ls {
		out = append(out, map[string]any{"id": l.ID, "label": l.Label, "url": l.URL, "icon": l.Icon, "newTab": l.NewTab, "ord": l.Ord})
	}
	return out
}

func (s *Server) apiAdminLinks(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"links": linksJSON(s.st.Links())})
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
	s.st.AddLink(strings.TrimSpace(in.Label), strings.TrimSpace(in.URL), strings.TrimSpace(in.Icon), in.NewTab == nil || *in.NewTab, ord)
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

func (s *Server) apiLinkReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	readJSON(r, &in)
	for i, id := range in.IDs {
		s.st.SetLinkOrder(id, i)
	}
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
	writeJSON(w, map[string]any{"groups": groups, "kinds": kinds})
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

// ---------- Admin: accounts ----------

func (s *Server) apiAdminUsers(w http.ResponseWriter, r *http.Request, user string) {
	us := s.st.Users()
	out := make([]map[string]any, 0, len(us))
	for _, u := range us {
		out = append(out, map[string]any{"username": u.Username, "role": u.EffRole()})
	}
	roles := make([]map[string]any, 0, len(roleRegistry))
	for _, ro := range roleRegistry {
		roles = append(roles, map[string]any{"code": ro.Code, "name": ro.Name})
	}
	writeJSON(w, map[string]any{"users": out, "me": user, "roles": roles})
}

func (s *Server) apiUserAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct{ Username, Password, Role string }
	readJSON(r, &in)
	name := strings.TrimSpace(in.Username)
	if name == "" || in.Password == "" {
		jsonError(w, http.StatusBadRequest, "username and password required")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	s.st.UpsertUser(User{Username: name, PasswordHash: string(h), Role: validRole(in.Role)})
	writeJSON(w, okJSON)
}

func (s *Server) apiUserSave(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	if u == nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	var in struct{ Role, Password string }
	readJSON(r, &in)
	newRole := validRole(in.Role)
	if newRole != "admin" && u.IsAdmin() && s.st.CountAdmins() <= 1 { // don't allow demoting the last admin
		newRole = "admin"
	}
	s.st.SetUserRole(name, newRole)
	if pw := strings.TrimSpace(in.Password); pw != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		s.st.SetUserPassword(name, string(h))
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

// ---------- Admin: system settings (legacy portal credentials / sync interval) ----------

func (s *Server) apiAdminSettings(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{
		"oldBase":  s.st.GetSetting("old_base", ""),
		"oldUser":  s.st.GetSetting("old_user", ""),
		"hasPass":  s.st.GetSetting("old_pass", "") != "",
		"syncMin":  s.st.GetSetting("sync_min", "0"),
		"newCount": s.st.CountNew(),
		"oldCount": s.st.CountOld(),
	})
}

func (s *Server) apiSettingsSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct{ OldBase, OldUser, OldPass, SyncMin string }
	readJSON(r, &in)
	s.st.SetSetting("old_base", strings.TrimSpace(in.OldBase))
	s.st.SetSetting("old_user", strings.TrimSpace(in.OldUser))
	if in.OldPass != "" { // empty = don't change the password
		s.st.SetSetting("old_pass", in.OldPass)
	}
	s.st.SetSetting("sync_min", strings.TrimSpace(in.SyncMin))
	s.old.SetCreds(s.st.GetSetting("old_base", ""), s.st.GetSetting("old_user", ""), s.st.GetSetting("old_pass", ""))
	writeJSON(w, okJSON)
}

func (s *Server) apiSettingsSync(w http.ResponseWriter, r *http.Request, user string) {
	go func() {
		n, err := s.old.SyncAllMeta(s.st)
		log.Printf("manual old-meta sync: %d, err=%v", n, err)
	}()
	writeJSON(w, okJSON)
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
