package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/version"
)

// pdf.html is the only server-side template kept (for PDF export); all pages are rendered by the React SPA (web/dist).
//
//go:embed templates/pdf.html
var tplFS embed.FS

const cookieName = "rp_session"

type Server struct {
	cfg   *config.Config
	st    *Store
	old   *OldClient
	names *Names
	pdf   *template.Template
}

// statusRecorder records the response status code for use in request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// logMiddleware logs every request: method, status, latency, and path (static assets are excluded to avoid noise).
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/assets/") || strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/favicon.svg" || p == "/favicon.ico" {
			next.ServeHTTP(w, r) // SPA static assets / health checks / API (which have their own concise logs) are skipped here to avoid noise
			return
		}
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		path, _ := url.QueryUnescape(r.URL.RequestURI())
		log.Printf("%-4s %3d %7s  %s", r.Method, sw.status, time.Since(start).Round(time.Millisecond).String(), path)
	})
}

// RunServer loads config, opens the store, bootstraps first-run state, wires the
// HTTP routes, and serves until it errors. The CLI (cmd/report-portal) calls this.
func RunServer(cfgPath string) {
	cfg, err := config.EnsureConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(config.DirOf(cfg.DBPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if st.CountUsers() == 0 { // first run with no accounts → generate a random admin and print it to the terminal
		pw := randPassword(14)
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err := st.UpsertUser(User{Username: "admin", PasswordHash: string(h), Role: "admin"}); err != nil {
			log.Fatalf("create initial admin: %v", err)
		}
		bar := strings.Repeat("=", 52)
		log.Printf("\n%s\n  first run: created admin account\n    username: admin\n    password: %s\n  log in and change the password in Users soon.\n%s", bar, pw, bar)
	}
	s := &Server{cfg: cfg, st: st, old: NewOldClient("", "", "")}
	s.names = LoadNames(config.DirOf(cfg.DBPath), st)
	s.names.ensureFull() // if the full list is missing, do a best-effort background fetch once
	s.parseTemplates()

	// Old-portal credentials / sync interval are all stored in the database and managed in the web "System Settings" (config is no longer involved).
	s.old.SetCreds(st.GetSetting("old_base", ""), st.GetSetting("old_user", ""), st.GetSetting("old_pass", ""))

	if st.CountTokens() == 0 { // on first run, create a default API token (managed on the System Settings page: multiple tokens / notes / expiry / scope)
		st.CreateToken(randToken(), "default", "all", "")
		log.Printf("default API token created (see Settings page)")
	}

	if len(st.TypeConfigs()) == 0 { // on first run, seed our real report types so the Types page isn't empty
		n := seedDefaultTypes(st)
		log.Printf("seeded %d default report types", n)
	}

	go s.syncLoop() // sync old metadata in the background

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"version":%q,"commit":%q,"new":%d,"old":%d}`, version.Version, version.Commit, st.CountNew(), st.CountOld())
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { // public: shown on the login/about page
		writeJSON(w, map[string]any{"version": version.Version, "commit": version.Commit, "buildDate": version.BuildDate})
	})

	// ---- Dify machine API: Bearer token auth (kept unchanged; the workflow depends on it) ----
	mux.HandleFunc("POST /api/reports", s.ingestReport)        // ingest
	mux.HandleFunc("GET /api/reports", s.apiQueryReports)      // query/search historical reports
	mux.HandleFunc("GET /api/reports/manifest", s.apiManifest) // probe the manifest
	mux.HandleFunc("GET /api/report", s.apiGetReport)          // fetch a single report body (by uid)
	mux.HandleFunc("GET /api/runs", s.apiRuns)                 // report-group view
	mux.HandleFunc("GET /api/symbols", s.apiSymbols)           // stock list / autocomplete (also used by the omnibox)
	mux.HandleFunc("GET /api/tracking", s.apiTracking)         // structured hypotheses / tracking items

	// ---- Browser (React SPA) API: signed-cookie session auth ----
	mux.HandleFunc("GET /api/me", s.apiMe)
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.HandleFunc("POST /api/logout", s.apiLogout)
	mux.HandleFunc("GET /api/home", s.requireUserJSON(s.apiHome))
	mux.HandleFunc("GET /api/research", s.requireUserJSON(s.apiResearch)) // 深度研究: symbol-less topic reports
	mux.HandleFunc("GET /api/stock/{symbol}", s.requireUserJSON(s.apiStock))
	mux.HandleFunc("GET /api/run/{key}", s.requireUserJSON(s.apiRun))
	mux.HandleFunc("GET /api/repbody", s.requireUserJSON(s.apiRepBody))

	// ---- Admin API: session + admin ----
	mux.HandleFunc("GET /api/admin/links", s.requireAdminJSON(s.apiAdminLinks))
	mux.HandleFunc("POST /api/admin/links", s.requireAdminJSON(s.apiLinkAdd))
	mux.HandleFunc("PUT /api/admin/links/{id}", s.requireAdminJSON(s.apiLinkEdit))
	mux.HandleFunc("DELETE /api/admin/links/{id}", s.requireAdminJSON(s.apiLinkDelete))
	mux.HandleFunc("POST /api/admin/links/reorder", s.requireAdminJSON(s.apiLinkReorder))
	mux.HandleFunc("GET /api/admin/types", s.requireAdminJSON(s.apiAdminTypes))
	mux.HandleFunc("POST /api/admin/types/save", s.requireAdminJSON(s.apiTypesSave))
	mux.HandleFunc("POST /api/admin/types/add", s.requireAdminJSON(s.apiTypesAdd))
	mux.HandleFunc("POST /api/admin/types/reorder", s.requireAdminJSON(s.apiTypesReorder))
	mux.HandleFunc("DELETE /api/admin/types/{name}", s.requireAdminJSON(s.apiTypesDelete))
	mux.HandleFunc("GET /api/admin/users", s.requireAdminJSON(s.apiAdminUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdminJSON(s.apiUserAdd))
	mux.HandleFunc("PUT /api/admin/users/{name}", s.requireAdminJSON(s.apiUserSave))
	mux.HandleFunc("DELETE /api/admin/users/{name}", s.requireAdminJSON(s.apiUserDelete))
	mux.HandleFunc("GET /api/admin/settings", s.requireAdminJSON(s.apiAdminSettings))
	mux.HandleFunc("POST /api/admin/settings", s.requireAdminJSON(s.apiSettingsSave))
	mux.HandleFunc("POST /api/admin/settings/sync", s.requireAdminJSON(s.apiSettingsSync))
	mux.HandleFunc("GET /api/admin/tokens", s.requireAdminJSON(s.apiAdminTokens))
	mux.HandleFunc("POST /api/admin/tokens", s.requireAdminJSON(s.apiTokenAdd))
	mux.HandleFunc("DELETE /api/admin/tokens/{id}", s.requireAdminJSON(s.apiTokenDelete))

	// ---- Downloads: MD / PDF (cookie session) ----
	mux.HandleFunc("GET /report/{rid}/md", s.requireUser(s.reportMD))
	mux.HandleFunc("GET /report/{rid}/pdf", s.requireUser(s.reportPDF))

	// ---- SPA: hand all other paths to React (deep links fall back to index.html) ----
	mux.HandleFunc("GET /", s.spaHandler())

	log.Printf("report-portal %s | listen %s | db %s | reports new:%d old:%d", version.String(), cfg.Listen, cfg.DBDriver, st.CountNew(), st.CountOld())
	if err := http.ListenAndServe(cfg.Listen, logMiddleware(mux)); err != nil {
		log.Fatal(err)
	}
}

// AddUser creates or updates an account from the CLI (lockout fallback).
func AddUser(cfgPath, name, pw string, admin bool) error {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return err
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
	role := "user"
	if admin {
		role = "admin"
	}
	return st.UpsertUser(User{Username: name, PasswordHash: string(h), Role: role})
}

// FetchNames fetches the full A-share name list to <data-dir>/names.json and
// returns the count and the written path.
func FetchNames(cfgPath string) (int, string, error) {
	dir := "data"
	if c, err := config.EnsureConfig(cfgPath); err == nil {
		dir = config.DirOf(c.DBPath)
	}
	n, err := FetchNamesToFile(dir)
	return n, dir + "/names.json", err
}

// randPassword generates a random password (excluding easily confused characters 0/O/1/l/I).
func randPassword(n int) string {
	const cs = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = cs[i%len(cs)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = cs[int(b[i])%len(cs)]
	}
	return string(b)
}

// randToken generates an ingest API token (48 hex digits).
func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Templates ----------

// parseTemplates parses only the PDF export template (pages are handled by the React SPA).
func (s *Server) parseTemplates() {
	funcs := template.FuncMap{
		"safe": func(s string) template.HTML { return template.HTML(s) },
		"trunc10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
	s.pdf = template.Must(template.New("pdf.html").Funcs(funcs).ParseFS(tplFS, "templates/pdf.html"))
}

// ---------- Session / auth ----------

func (s *Server) sign(user string) string {
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	msg := fmt.Sprintf("%s|%d", user, exp)
	sig := s.hmac(msg)
	return base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." + sig
}

func (s *Server) hmac(msg string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) verify(cookie string) string {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	msg := string(raw)
	if !hmac.Equal([]byte(s.hmac(msg)), []byte(parts[1])) {
		return ""
	}
	i := strings.LastIndex(msg, "|")
	if i < 0 {
		return ""
	}
	exp, err := strconv.ParseInt(msg[i+1:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	return msg[:i]
}

func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return s.verify(c.Value)
}

func (s *Server) isAdmin(user string) bool {
	u := s.st.GetUser(user)
	return u != nil && can(u.Role, PermManage)
}

type handler func(http.ResponseWriter, *http.Request, string)

func (s *Server) requireUser(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, u)
	}
}

// ---------- Login ----------

// ---------- List ----------

func (s *Server) filtersFrom(r *http.Request) (Filters, string, int, int) {
	q := r.URL.Query()
	f := Filters{
		Q: strings.TrimSpace(q.Get("q")), Scope: q.Get("scope"), Symbol: q.Get("symbol"),
		RType: q.Get("rtype"), DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"),
		Sort: q.Get("sort"),
	}
	src := q.Get("src")
	if src == "" {
		src = "all"
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size != 15 && size != 30 && size != 50 {
		size = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	return f, src, size, page
}

// ---------- run detail ----------

func (s *Server) runMembers(key string) []Rep {
	var members []Rep
	if !strings.Contains(key, "|") {
		if rep := s.loadRep(key); rep != nil {
			members = []Rep{*rep}
		}
	} else {
		parts := strings.SplitN(key, "|", 2)
		symbol, date := parts[0], parts[1]
		nn, _ := s.st.SearchNew(Filters{DateFrom: date, DateTo: date, Sort: "date_asc"})
		for _, m := range nn {
			if m.Symbol == symbol {
				members = append(members, m)
			}
		}
		oo, _ := s.st.SearchOldMeta(Filters{Symbol: symbol})
		for _, m := range oo {
			if m.Symbol == symbol && m.Date == date {
				members = append(members, m)
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].Time < members[j].Time })
	}
	for i := range members {
		members[i].Label = label(members[i])
	}
	return members
}

// orderAndDefault sorts members and picks the default page (the type marked "汇总") according to the type config (which admins can edit).
// Fallback when unconfigured: detect summary by keyword → otherwise the last item. Tab labels can be renamed via config.
// defaultTypeOrd: the built-in default tab order for unconfigured types (conclusion first, the rest following the analysis flow).
// Dragging in "Type Management" writes to type_config.ord, which takes precedence over this.
var defaultTypeOrd = map[string]int{
	"投资决策建议": 0, "综合深度研究": 0,
	"事件监测": 10, "投资机会": 10, "研报分析": 10,
	"舆情分析": 20, "重组基本面分析": 20,
	"行业分析": 30, "重组分析": 30,
	"财务分析": 40, "资本运作分析": 40,
	"估值分析":   50,
	"股权分析":   60,
	"管理能力分析": 70,
	"调研纪要":   80,
}

// defaultSeedTypes is the set of report types pre-registered on a fresh DB so the
// Type Management page ships with our real categories instead of being empty.
// Admins can rename/reassign category/reorder/add in the UI afterward.
//
// These are the actual categories our Dify workflow modules emit (dept-1 single-
// stock analysis → 投资决策/深度研究; the 3-x 重组 series → 重组决策; DeepResearch →
// 深度研究), cross-checked against the categories present in ingested data.
var defaultSeedTypes = []struct {
	Name    string
	Kind    string
	Ord     int
	Summary bool
}{
	// 投资决策 (dept-1 single-stock analysis; 投资决策建议 is the decision summary)
	{"汇总", "投资决策", 0, true},
	{"投资决策建议", "投资决策", 10, true},
	{"研报分析", "投资决策", 20, false},
	{"行业分析", "投资决策", 30, false},
	{"估值分析", "投资决策", 40, false},
	{"财务分析", "投资决策", 50, false},
	{"股权分析", "投资决策", 60, false},
	{"投资机会", "投资决策", 70, false},
	// 深度研究 (DeepResearch-DS + management analysis)
	{"综合深度研究", "深度研究", 0, true},
	{"管理能力分析", "深度研究", 10, false},
	{"调研纪要", "深度研究", 20, false},
	// 事件监测 (sentiment / signal monitoring)
	{"舆情分析", "事件监测", 0, false},
	{"事件监测", "事件监测", 10, false},
	// 重组决策 (the 3-x 重组 series; 重组分析 is the 综合决策 summary)
	{"重组分析", "重组决策", 0, true},
	{"重组基本面分析", "重组决策", 10, false},
	{"重组交易分析", "重组决策", 20, false},
	{"资本运作分析", "重组决策", 30, false},
	// 其他 (uncategorized / thematic)
	{"未分类", "其他", 0, false},
}

// seedDefaultTypes registers the default report types (first run only) and returns the count.
func seedDefaultTypes(st *Store) int {
	for _, t := range defaultSeedTypes {
		st.UpsertTypeConfig(t.Name, t.Kind, "", t.Ord, t.Summary)
	}
	return len(defaultSeedTypes)
}

func (s *Server) orderAndDefault(members []Rep) ([]Rep, string) {
	cfg := s.st.TypeConfigs()
	ord := func(r Rep) int {
		if c, ok := cfg[r.RType]; ok {
			return c.Ord
		}
		if o, ok := defaultTypeOrd[r.RType]; ok {
			return o
		}
		return 1000
	}
	sum := func(r Rep) bool {
		if c, ok := cfg[r.RType]; ok && c.IsSummary {
			return true
		}
		return isSummary(r)
	}
	out := make([]Rep, len(members))
	copy(out, members)
	for i := range out {
		if c, ok := cfg[out[i].RType]; ok && c.Label != "" {
			out[i].Label = c.Label
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := sum(out[i]), sum(out[j]); si != sj {
			return si // summary / comprehensive / decision come first
		}
		if oi, oj := ord(out[i]), ord(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Time < out[j].Time
	})
	// append an index to same-named tabs so multiple "重组交易分析" entries can be told apart
	seen := map[string]int{}
	for i := range out {
		seen[out[i].Label]++
		if n := seen[out[i].Label]; n > 1 {
			out[i].Label = out[i].Label + " " + strconv.Itoa(n)
		}
	}
	def, bestOrd := "", 1<<30
	for _, m := range out {
		if c, ok := cfg[m.RType]; ok && c.IsSummary && c.Ord < bestOrd {
			bestOrd, def = c.Ord, m.RID
		}
	}
	if def == "" {
		for _, m := range out {
			if isSummary(m) {
				def = m.RID
				break
			}
		}
	}
	if def == "" && len(out) > 0 {
		def = out[len(out)-1].RID
	}
	return out, def
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func containsStr(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

// repKind returns a report's top-level category: new reports use the Kind field, otherwise it is inferred from the type.
func repKind(r Rep) string {
	if r.Kind != "" {
		return r.Kind
	}
	return runKind([]string{r.RType})
}

// tokenOK validates the Bearer token in the request; need = the required scope (ingest|query), and a token with scope=all passes everything.
func (s *Server) tokenOK(r *http.Request, need string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return s.st.TokenValid(got, need)
}

// ingestReport is the ingest endpoint for new reports (called by the Dify workflow's HTTP node). Bearer token auth.
func (s *Server) ingestReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		UID      string `json:"uid"`
		RunID    string `json:"run_id"`
		Symbol   string `json:"symbol"`
		Name     string `json:"name"` // optional: as-of company name; snapshotted so backdoor-listing/rename doesn't relabel old reports
		Date     string `json:"date"`
		Kind     string `json:"kind"`
		Subtype  string `json:"subtype"`
		RType    string `json:"rtype"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Time     string `json:"time"`
		BodyMD   string `json:"body_md"`
		BodyHTML string `json:"body_html"`
		Tracking []struct {
			IType       string `json:"itype"`
			Content     string `json:"content"`
			Status      string `json:"status"`
			ReviewPoint string `json:"review_point"`
		} `json:"tracking"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Symbol == "" || in.Date == "" {
		http.Error(w, "symbol and date required", http.StatusBadRequest)
		return
	}
	rtype := firstNonEmpty(in.Subtype, in.RType)
	// Top-level category: explicit from Dify > already registered in the registry > runKind fallback; and auto-register this subtype → category.
	kind := in.Kind
	if kind == "" {
		kind = s.st.TypeKind(rtype)
	}
	if kind == "" {
		kind = runKind([]string{rtype})
	}
	s.st.RegisterType(rtype, kind)
	s.names.EnsureOne(in.Symbol) // if this code has no name yet, do a background best-effort fetch from Tencent/Sina
	// Identity key = symbol|date|category|subtype: the same type coming in again on the same day overwrites/updates it (run_id is just a batch label and does not participate in identity).
	uid := firstNonEmpty(in.UID, in.Symbol+"|"+in.Date+"|"+kind+"|"+rtype)
	html := in.BodyHTML
	if html == "" && in.BodyMD != "" {
		html = mdToHTML(in.BodyMD)
	}
	// Snapshot the as-of name: Dify-provided > current stocks name at ingest time.
	// The report is immutable history, so a later rename/backdoor-listing won't relabel it.
	name := firstNonEmpty(in.Name, s.names.Get(in.Symbol))
	rep := Rep{
		UID: uid, RunID: in.RunID, Symbol: in.Symbol, Name: name, Date: in.Date, Kind: kind,
		RType: rtype, Title: in.Title, Source: in.Source, Time: firstNonEmpty(in.Time, in.Date),
		MD: in.BodyMD, HTML: html,
	}
	if err := s.st.UpsertReport(rep); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(in.Tracking) > 0 { // only update when tracking items are present (overwrite the old ones to match the latest body)
		items := make([]TrackingItem, 0, len(in.Tracking))
		for _, t := range in.Tracking {
			items = append(items, TrackingItem{IType: t.IType, Content: t.Content, Status: t.Status, ReviewPoint: t.ReviewPoint})
		}
		s.st.SetTracking(uid, in.Symbol, items)
	}
	log.Printf("ingest %s %s", in.Symbol, in.Date)
	writeJSON(w, map[string]any{"ok": true, "uid": uid})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// apiQueryReports queries/searches historical reports (used by Dify to re-check hypotheses / tracking items). Bearer token auth, scope query.
// GET /api/reports?symbol=300750&q=关键字&kind=投资决策&subtype=汇总&since=&until=&limit=20&with_body=1
// At least one of symbol and q must be given; when symbol is empty, search the whole database by keyword.
func (s *Server) apiQueryReports(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	kw := strings.TrimSpace(q.Get("q"))
	if symbol == "" && kw == "" {
		http.Error(w, "symbol or q required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	withBody := q.Get("with_body") == "1" || q.Get("with_body") == "true"
	since, until := q.Get("since"), q.Get("until")
	if d := strings.TrimSpace(q.Get("date")); d != "" { // date (an exact day; "today" is an alias)
		if d == "today" {
			d = time.Now().Format("2006-01-02")
		}
		since, until = d, d
	}
	reps, err := s.st.QueryReports(symbol, kw, q.Get("kind"), firstNonEmpty(q.Get("subtype"), q.Get("rtype")),
		since, until, limit, withBody)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"symbol": symbol, "q": kw, "has": len(reps) > 0,
		"count": len(reps), "reports": s.repsJSON(reps, withBody)})
	log.Printf("query symbol=%s -> %d reports", symbol, len(reps))
}

// repsJSON converts []Rep into Dify-friendly JSON (includes the company name; includes body_md when withBody is set).
func (s *Server) repsJSON(reps []Rep, withBody bool) []map[string]any {
	out := make([]map[string]any, 0, len(reps))
	for _, r := range reps {
		m := map[string]any{"uid": r.UID, "run_id": r.RunID, "symbol": r.Symbol, "name": s.names.Get(r.Symbol),
			"date": r.Date, "kind": r.Kind, "subtype": r.RType, "title": r.Title, "source": r.Source}
		if withBody {
			m["body_md"] = r.MD
		}
		out = append(out, m)
	}
	return out
}

// apiManifest lists "what reports exist" for a symbol: total count, each date (with category), and all categories/subtypes. Lets Dify probe before fetching.
// GET /api/reports/manifest?symbol=300750
func (s *Server) apiManifest(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	m := s.st.Manifest(symbol)
	m["name"] = s.names.Get(symbol)
	writeJSON(w, m)
	log.Printf("manifest %s -> %v reports", symbol, m["total"])
}

// apiGetReport fetches a single report body. GET /api/report?uid=... (or rid=n123). Bearer token auth, scope query.
func (s *Server) apiGetReport(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	var rep *Rep
	if uid := strings.TrimSpace(q.Get("uid")); uid != "" {
		rep = s.st.GetByUID(uid)
	} else if rid := strings.TrimSpace(q.Get("rid")); rid != "" {
		rep = s.loadRep(rid)
	}
	if rep == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"uid": rep.UID, "run_id": rep.RunID, "symbol": rep.Symbol, "date": rep.Date,
		"kind": rep.Kind, "subtype": rep.RType, "title": rep.Title, "source": rep.Source,
		"body_md": rep.MD, "body_html": rep.HTML})
}

// apiRuns is the report-group view (one generation run = same symbol + date + category). GET /api/runs?symbol=300750&date= (optional)
func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	runs := s.st.ListRuns(symbol, strings.TrimSpace(r.URL.Query().Get("date")))
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		out = append(out, map[string]any{"symbol": r.Symbol, "date": r.Date, "kind": r.Kind,
			"run_id": r.RunID, "subtypes": r.Subtypes, "count": r.Count})
	}
	writeJSON(w, map[string]any{"symbol": symbol, "name": s.names.Get(symbol), "has": len(out) > 0, "count": len(out), "runs": out})
}

// apiSymbols lists stocks that have reports / autocomplete. GET /api/symbols?q=300&limit=50
func (s *Server) apiSymbols(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list := s.st.ListSymbols(strings.TrimSpace(q.Get("q")), limit)
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		name := si.Name // name from the DB (stocks); fall back to the in-memory map if absent
		if name == "" {
			name = s.names.Get(si.Symbol)
		}
		out = append(out, map[string]any{"symbol": si.Symbol, "name": name, "count": si.Count, "latest": si.Latest})
	}
	writeJSON(w, map[string]any{"count": len(out), "symbols": out})
}

// apiTracking returns structured hypotheses / tracking items (for re-run review). GET /api/tracking?symbol=300750&status=pending&limit=100
func (s *Server) apiTracking(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items := s.st.QueryTracking(symbol, strings.TrimSpace(q.Get("status")), limit)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"report_uid": it.ReportUID, "itype": it.IType, "content": it.Content,
			"status": it.Status, "review_point": it.ReviewPoint, "created_at": it.Created})
	}
	writeJSON(w, map[string]any{"symbol": symbol, "has": len(out) > 0, "count": len(out), "items": out})
}

func repInList(reps []Rep, rid string) bool {
	for _, r := range reps {
		if r.RID == rid {
			return true
		}
	}
	return false
}

// loadRep fetches a report including its body by rid.
func (s *Server) loadRep(rid string) *Rep {
	if strings.HasPrefix(rid, "n") {
		id, err := strconv.ParseInt(rid[1:], 10, 64)
		if err != nil {
			return nil
		}
		rep, _ := s.st.GetNew(id)
		return rep
	}
	if strings.HasPrefix(rid, "o") {
		id, err := strconv.ParseInt(rid[1:], 10, 64)
		if err != nil {
			return nil
		}
		d, err := s.old.Detail(id)
		if err != nil {
			return nil
		}
		return &Rep{
			RID: rid, Src: "old", Title: d.Title, Symbol: d.StockCode, RType: d.Category,
			Date: d.ReportDate, Source: d.Author, Time: d.Time, HTML: d.ContentHTML, MD: d.Content,
		}
	}
	return nil
}

// ---------- Export ----------

func (s *Server) reportMD(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r.PathValue("rid"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	fn := safeFile(rep.Title, rid(r)) + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write([]byte(rep.MD))
}

func (s *Server) reportPDF(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r.PathValue("rid"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	var buf strings.Builder
	if err := s.pdf.ExecuteTemplate(&buf, "pdf.html", rep); err != nil {
		http.Error(w, "render", 500)
		return
	}
	pdf, err := htmlToPDF(buf.String())
	if err == ErrNoWkhtmltopdf {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(503)
		fmt.Fprint(w, `<div style="font-family:sans-serif;max-width:520px;margin:12vh auto;text-align:center;color:#334">`+
			`<h2 style="color:#0c447c">PDF 暂不可用</h2>`+
			`<p>本机未安装 <code>wkhtmltopdf</code>，无法在本地生成 PDF。</p>`+
			`<p><b>Docker 部署已内置</b>，线上正常。想本地用可装：<br><code>brew install --cask wkhtmltopdf</code></p>`+
			`<p>也可先用 <b>⬇ MD</b> 导出。</p>`+
			`<p><a href="#" onclick="window.close();return false;">关闭此页</a> · <a href="/">返回首页</a></p></div>`)
		return
	}
	if err != nil {
		http.Error(w, "PDF 生成失败: "+err.Error(), 500)
		return
	}
	fn := safeFile(rep.Title, rid(r)) + ".pdf"
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write(pdf)
}

func rid(r *http.Request) string { return r.PathValue("rid") }

func safeFile(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return title
}

// ---------- Entry-button management ----------

// ---------- Report-type management ----------

var kindOrder = []string{"重组决策", "投资决策", "深度研究", "技术分析", "事件监测", "其他"}

// ---------- Account management ----------

// ---------- Background sync ----------

// ---------- System settings (old-portal credentials / sync interval, stored in the DB) ----------

func (s *Server) syncLoop() {
	do := func() {
		if s.st.GetSetting("old_base", "") == "" {
			return // old portal not configured, skip sync
		}
		n, err := s.old.SyncAllMeta(s.st)
		if err != nil {
			log.Printf("old-meta sync error (synced %d): %v", n, err)
			return
		}
		log.Printf("old-meta sync done: %d", s.st.CountOld())
	}
	do()
	for {
		min := 0
		fmt.Sscanf(s.st.GetSetting("sync_min", "0"), "%d", &min)
		if min <= 0 {
			return // 0 = sync only once at startup
		}
		time.Sleep(time.Duration(min) * time.Minute)
		do()
	}
}

func uniqSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
