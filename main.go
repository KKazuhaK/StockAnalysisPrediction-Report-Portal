package main

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
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static/*
var staticFS embed.FS

const cookieName = "rp_session"

// 由 CI 通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

var pageSizes = []int{15, 30, 50}

type Server struct {
	cfg   *Config
	st    *Store
	old   *OldClient
	names *Names
	pages map[string]*template.Template
	pdf   *template.Template
}

// statusRecorder 记录响应状态码，供请求日志用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// logMiddleware 打印每个请求：方法 状态 耗时 路径（静态资源不刷屏）。
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/favicon.ico" {
			next.ServeHTTP(w, r) // 静态/健康检查/API（有各自的简洁日志）不在此刷屏
			return
		}
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		path, _ := url.QueryUnescape(r.URL.RequestURI())
		log.Printf("%-4s %3d %7s  %s", r.Method, sw.status, time.Since(start).Round(time.Millisecond).String(), path)
	})
}

func main() {
	// 子命令：report-portal hashpw <password> —— 生成 bcrypt 哈希贴进 config.yaml
	if len(os.Args) > 1 && os.Args[1] == "hashpw" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: report-portal hashpw <password>")
			os.Exit(1)
		}
		h, err := bcrypt.GenerateFromPassword([]byte(os.Args[2]), 12)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(h))
		return
	}

	// 子命令：report-portal fetchnames —— 抓全量 A 股名称写 data/names.json
	if len(os.Args) > 1 && os.Args[1] == "fetchnames" {
		cfgP := os.Getenv("RP_CONFIG")
		if cfgP == "" {
			cfgP = "config.yaml"
		}
		dir := "data"
		if c, err := EnsureConfig(cfgP); err == nil {
			dir = dirOf(c.DBPath)
		}
		n, err := FetchNamesToFile(dir)
		if err != nil {
			log.Fatalf("fetch failed: %v", err)
		}
		fmt.Printf("wrote %s/names.json: %d\n", dir, n)
		return
	}

	// 子命令：report-portal adduser <用户名> <密码> [admin] —— 兜底防锁死
	if len(os.Args) > 1 && os.Args[1] == "adduser" {
		if len(os.Args) < 4 {
			log.Fatal("usage: report-portal adduser <username> <password> [admin]")
		}
		cfgP := os.Getenv("RP_CONFIG")
		if cfgP == "" {
			cfgP = "config.yaml"
		}
		c, err := EnsureConfig(cfgP)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		os.MkdirAll(dirOf(c.DBPath), 0o755)
		st, err := OpenStore(c.DBDriver, c.dbSource())
		if err != nil {
			log.Fatal(err)
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(os.Args[3]), 12)
		role := "user"
		if len(os.Args) > 4 && os.Args[4] == "admin" {
			role = "admin"
		}
		if err := st.UpsertUser(User{Username: os.Args[2], PasswordHash: string(h), Role: role}); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user saved: %s (role=%s)\n", os.Args[2], role)
		return
	}

	cfgPath := os.Getenv("RP_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, err := EnsureConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(dirOf(cfg.DBPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBDriver, cfg.dbSource())
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if st.CountUsers() == 0 { // 首启无账号 → 生成随机管理员并打印到终端
		pw := randPassword(14)
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err := st.UpsertUser(User{Username: "admin", PasswordHash: string(h), Role: "admin"}); err != nil {
			log.Fatalf("create initial admin: %v", err)
		}
		bar := strings.Repeat("=", 52)
		log.Printf("\n%s\n  first run: created admin account\n    username: admin\n    password: %s\n  log in and change the password in Users soon.\n%s", bar, pw, bar)
	}
	s := &Server{cfg: cfg, st: st, old: NewOldClient("", "", "")}
	s.names = LoadNames(dirOf(cfg.DBPath))
	s.names.ensureFull() // 无全量表时后台 best-effort 抓一次
	s.parseTemplates()

	// 旧门户凭据 / 同步间隔全部存数据库、网页「系统设置」里管（config 不再涉及）。
	s.old.SetCreds(st.GetSetting("old_base", ""), st.GetSetting("old_user", ""), st.GetSetting("old_pass", ""))

	if st.CountTokens() == 0 { // 首启建一枚默认 API 令牌（系统设置页管理：多枚/备注/有效期/作用域）
		st.CreateToken(randToken(), "default", "all", "")
		log.Printf("default API token created (see Settings page)")
	}

	go s.syncLoop() // 后台同步旧元数据

	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"new":%d,"old":%d}`, st.CountNew(), st.CountOld())
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("GET /logout", s.logout)
	mux.HandleFunc("POST /api/reports", s.ingestReport)            // Dify 入库：Bearer 令牌鉴权
	mux.HandleFunc("GET /api/reports", s.apiQueryReports)         // Dify 查/搜历史报告
	mux.HandleFunc("GET /api/reports/manifest", s.apiManifest)   // Dify 探清单：某票有哪些报告
	mux.HandleFunc("GET /api/report", s.apiGetReport)            // Dify 取单篇正文(uid)
	mux.HandleFunc("GET /{$}", s.requireUser(s.index))
	mux.HandleFunc("GET /run/{key}", s.requireUser(s.runView))
	mux.HandleFunc("GET /stock/{symbol}", s.requireUser(s.stockView)) // 个股 timeline 详情（新报告）
	mux.HandleFunc("GET /report/{rid}/md", s.requireUser(s.reportMD))
	mux.HandleFunc("GET /report/{rid}/pdf", s.requireUser(s.reportPDF))
	mux.HandleFunc("GET /manage/links", s.requireAdmin(s.manageLinks))
	mux.HandleFunc("POST /manage/links/add", s.requireAdmin(s.linkAdd))
	mux.HandleFunc("POST /manage/links/{id}/edit", s.requireAdmin(s.linkEdit))
	mux.HandleFunc("POST /manage/links/{id}/delete", s.requireAdmin(s.linkDelete))
	mux.HandleFunc("POST /manage/links/reorder", s.requireAdmin(s.linkReorder))
	mux.HandleFunc("GET /manage/types", s.requireAdmin(s.manageTypes))
	mux.HandleFunc("POST /manage/types", s.requireAdmin(s.manageTypesSave))
	mux.HandleFunc("POST /manage/types/add", s.requireAdmin(s.manageTypesAdd))
	mux.HandleFunc("POST /manage/types/reorder", s.requireAdmin(s.manageTypesReorder))
	mux.HandleFunc("POST /manage/types/{name}/delete", s.requireAdmin(s.manageTypesDelete))
	mux.HandleFunc("GET /manage/users", s.requireAdmin(s.manageUsers))
	mux.HandleFunc("POST /manage/users/add", s.requireAdmin(s.userAdd))
	mux.HandleFunc("POST /manage/users/{name}/save", s.requireAdmin(s.userSave))
	mux.HandleFunc("POST /manage/users/{name}/delete", s.requireAdmin(s.userDelete))
	mux.HandleFunc("GET /manage/settings", s.requireAdmin(s.manageSettings))
	mux.HandleFunc("POST /manage/settings", s.requireAdmin(s.manageSettingsSave))
	mux.HandleFunc("POST /manage/settings/sync", s.requireAdmin(s.settingsSyncNow))
	mux.HandleFunc("POST /manage/tokens/add", s.requireAdmin(s.tokenCreate))
	mux.HandleFunc("POST /manage/tokens/{id}/delete", s.requireAdmin(s.tokenDelete))

	log.Printf("report-portal %s | listen %s | db %s | reports new:%d old:%d", version, cfg.Listen, cfg.DBDriver, st.CountNew(), st.CountOld())
	if err := http.ListenAndServe(cfg.Listen, logMiddleware(mux)); err != nil {
		log.Fatal(err)
	}
}

// randPassword 生成随机密码（去掉易混字符 0/O/1/l/I）。
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

// randToken 生成入库 API 令牌（48 位十六进制）。
func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// pageList 生成页码序列（-1 表示省略号）：首页、当前±2、末页。
func pageList(cur, total int) []int {
	if total <= 9 {
		out := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			out = append(out, i)
		}
		return out
	}
	keep := map[int]bool{1: true, total: true, cur: true}
	for d := 1; d <= 2; d++ {
		if cur-d >= 1 {
			keep[cur-d] = true
		}
		if cur+d <= total {
			keep[cur+d] = true
		}
	}
	var ks []int
	for k := range keep {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	var out []int
	prev := 0
	for _, k := range ks {
		if prev > 0 && k-prev > 1 {
			out = append(out, -1)
		}
		out = append(out, k)
		prev = k
	}
	return out
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

// ---------- 模板 ----------

func (s *Server) parseTemplates() {
	funcs := template.FuncMap{
		"urlq": url.QueryEscape,
		"join": strings.Join,
		"add":  func(a, b int) int { return a + b },
		"safe": func(s string) template.HTML { return template.HTML(s) },
		"icon": icon,
		"t":    T,
		"trunc10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
	s.pages = map[string]*template.Template{}
	for _, name := range []string{"login", "index", "run", "stock", "manage_links", "manage_types", "manage_users", "manage_settings"} {
		s.pages[name] = template.Must(template.New("base.html").Funcs(funcs).
			ParseFS(tplFS, "templates/base.html", "templates/"+name+".html"))
	}
	s.pdf = template.Must(template.New("pdf.html").Funcs(funcs).ParseFS(tplFS, "templates/pdf.html"))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Lang"] = langOf(r)      // 当前语言
	data["Langs"] = langs         // 可选语言（下拉）
	if err := s.pages[name].ExecuteTemplate(w, "base.html", data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error", 500)
	}
}

// ---------- 会话/鉴权 ----------

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

func (s *Server) requireAdmin(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !s.isAdmin(u) {
			http.Error(w, "需要管理员权限", 403)
			return
		}
		h(w, r, u)
	}
}

// ---------- 登录 ----------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", map[string]any{"Err": ""})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := s.st.GetUser(r.FormValue("username"))
	pw := r.FormValue("password")
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) != nil {
		w.WriteHeader(401)
		s.render(w, r, "login", map[string]any{"Err": "用户名或密码错误"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sign(u.Username), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------- 列表 ----------

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

func (s *Server) index(w http.ResponseWriter, r *http.Request, user string) {
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
	pageGroups := groups[lo:hi]

	types := append(s.st.NewTypes(), s.st.OldCategories()...)
	types = uniqSorted(types)

	// 分页链接在后端整条构建，作为 template.URL 传出（避免模板再次转义）
	base := url.Values{}
	for _, k := range []string{"symbol", "rtype", "date_from", "date_to", "q", "scope", "sort", "src", "size"} {
		if v := r.URL.Query().Get(k); v != "" {
			base.Set(k, v)
		}
	}
	mkURL := func(p int) template.URL {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		v.Set("page", strconv.Itoa(p))
		return template.URL("/?" + v.Encode())
	}
	// 页码列表（1 2 3 … 49 50 + 省略号），并配好各自 URL
	var pager []map[string]any
	for _, p := range pageList(page, pages) {
		if p < 0 {
			pager = append(pager, map[string]any{"Ellipsis": true})
		} else {
			pager = append(pager, map[string]any{"Num": p, "URL": mkURL(p), "Cur": p == page})
		}
	}
	s.render(w, r, "index", map[string]any{
		"User": user, "Admin": s.isAdmin(user),
		"Groups": pageGroups, "NewTotal": newTotal, "OldTotal": oldTotal, "TotalRuns": totalRuns,
		"Types": types, "Links": s.st.Links(),
		"Page": page, "Pages": pages, "PageSizes": pageSizes, "Pager": pager,
		"PrevURL": mkURL(page - 1), "NextURL": mkURL(page + 1), "ListURL": "/?" + r.URL.RawQuery,
		"F": map[string]any{
			"Q": f.Q, "Scope": f.Scope, "Symbol": f.Symbol, "RType": f.RType,
			"DateFrom": f.DateFrom, "DateTo": f.DateTo, "Sort": f.Sort, "Src": src, "Size": size,
		},
	})
}

// ---------- run 详情 ----------

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

// orderAndDefault 按类型配置(管理员可改)给成员排序、选默认页(标"汇总"的类型)。
// 未配置时回退：关键字判定汇总 → 否则最后一篇。tab 标签支持配置改名。
// defaultTypeOrd：未配置类型的内建默认 tab 顺序（结论优先，其余按分析流程）。
// 用户在「类型管理」拖拽会写入 type_config.ord，优先于此。
var defaultTypeOrd = map[string]int{
	"投资决策建议": 0, "综合深度研究": 0,
	"事件监测": 10, "投资机会": 10, "研报分析": 10,
	"舆情分析": 20, "重组基本面分析": 20,
	"行业分析": 30, "重组分析": 30,
	"财务分析": 40, "资本运作分析": 40,
	"估值分析": 50,
	"股权分析": 60,
	"管理能力分析": 70,
	"调研纪要": 80,
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
			return si // 汇总/综合/决策 排最前
		}
		if oi, oj := ord(out[i]), ord(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Time < out[j].Time
	})
	// 同名 tab 追加序号，避免多份"重组交易分析"难以区分
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

// repKind 报告的大类：新报告用 Kind 字段，否则按类型推断。
func repKind(r Rep) string {
	if r.Kind != "" {
		return r.Kind
	}
	return runKind([]string{r.RType})
}

// tokenOK 校验请求里的 Bearer 令牌；need = 所需作用域（ingest|query），令牌 scope=all 时全通过。
func (s *Server) tokenOK(r *http.Request, need string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return s.st.TokenValid(got, need)
}

// ingestReport 新报告入库接口（Dify 工作流 HTTP 节点调用）。Bearer 令牌鉴权。
func (s *Server) ingestReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		UID      string `json:"uid"`
		RunID    string `json:"run_id"`
		Symbol   string `json:"symbol"`
		Date     string `json:"date"`
		Kind     string `json:"kind"`
		Subtype  string `json:"subtype"`
		RType    string `json:"rtype"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Time     string `json:"time"`
		BodyMD   string `json:"body_md"`
		BodyHTML string `json:"body_html"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Symbol == "" || in.Date == "" {
		http.Error(w, "symbol 和 date 必填", http.StatusBadRequest)
		return
	}
	rtype := firstNonEmpty(in.Subtype, in.RType)
	kind := firstNonEmpty(in.Kind, runKind([]string{rtype}))
	uid := in.UID
	if uid == "" {
		uid = firstNonEmpty(in.RunID, in.Symbol+"|"+in.Date+"|"+kind) + "|" + rtype
	}
	html := in.BodyHTML
	if html == "" && in.BodyMD != "" {
		html = mdToHTML(in.BodyMD)
	}
	rep := Rep{
		UID: uid, RunID: in.RunID, Symbol: in.Symbol, Date: in.Date, Kind: kind,
		RType: rtype, Title: in.Title, Source: in.Source, Time: firstNonEmpty(in.Time, in.Date),
		MD: in.BodyMD, HTML: html,
	}
	if err := s.st.UpsertReport(rep); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("ingest %s %s %s/%s", in.Symbol, in.Date, kind, rtype)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"uid":%q}`, uid)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// apiQueryReports 查/搜历史报告（Dify 复核假设/跟踪事项用）。Bearer 令牌鉴权，作用域 query。
// GET /api/reports?symbol=300750&q=关键字&kind=投资决策&subtype=汇总&since=&until=&limit=20&with_body=1
// symbol 与 q 至少给一个；symbol 空时按关键字全库搜索。
func (s *Server) apiQueryReports(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "query") {
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
	reps, err := s.st.QueryReports(symbol, kw, q.Get("kind"), firstNonEmpty(q.Get("subtype"), q.Get("rtype")),
		q.Get("since"), q.Get("until"), limit, withBody)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"symbol": symbol, "q": kw, "count": len(reps), "reports": repsJSON(reps, withBody)})
	log.Printf("query symbol=%q q=%q -> %d reports", symbol, kw, len(reps))
}

// repsJSON 把 []Rep 转成对 Dify 友好的 JSON（含名字；withBody 时带 body_md）。
func repsJSON(reps []Rep, withBody bool) []map[string]any {
	out := make([]map[string]any, 0, len(reps))
	for _, r := range reps {
		m := map[string]any{"uid": r.UID, "run_id": r.RunID, "symbol": r.Symbol, "date": r.Date,
			"kind": r.Kind, "subtype": r.RType, "title": r.Title, "source": r.Source}
		if withBody {
			m["body_md"] = r.MD
		}
		out = append(out, m)
	}
	return out
}

// apiManifest 某标的“有哪些报告”的清单：总数、各日期(含大类)、全部大类/小文档。让 Dify 先探再取。
// GET /api/reports/manifest?symbol=300750
func (s *Server) apiManifest(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "query") {
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

// apiGetReport 取单篇正文。GET /api/report?uid=...（或 rid=n123）。Bearer 令牌鉴权，作用域 query。
func (s *Server) apiGetReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "query") {
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

// tokenCreate 生成一枚新令牌。tokenDelete 删除。
func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	exp := strings.TrimSpace(r.FormValue("expires"))
	if len(exp) == 10 { // 只给日期 → 当天 23:59:59 到期
		exp += " 23:59:59"
	}
	s.st.CreateToken(randToken(), strings.TrimSpace(r.FormValue("name")), r.FormValue("scope"), exp)
	http.Redirect(w, r, "/manage/settings#tokens", http.StatusSeeOther)
}
func (s *Server) tokenDelete(w http.ResponseWriter, r *http.Request, user string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.st.DeleteToken(id)
	http.Redirect(w, r, "/manage/settings#tokens", http.StatusSeeOther)
}

type tlNode struct {
	Date   string
	N      int
	Active bool
	URL    string
}
type tabItem struct {
	Text, RID string
	Active    bool
	URL       string
}

// stockView 个股 timeline 详情（新报告）：时间线选日期 → 大类 tab → 小文档 tab → 正文。
func (s *Server) stockView(w http.ResponseWriter, r *http.Request, user string) {
	symbol := r.PathValue("symbol")
	all, _ := s.st.NewBySymbol(symbol)
	if len(all) == 0 {
		http.Error(w, "该标的暂无新报告", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	// 按日期分组（all 已 rdate DESC → order 为新到旧）
	var order []string
	byDate := map[string][]Rep{}
	for _, m := range all {
		if _, ok := byDate[m.Date]; !ok {
			order = append(order, m.Date)
		}
		byDate[m.Date] = append(byDate[m.Date], m)
	}
	selDate := q.Get("date")
	if _, ok := byDate[selDate]; !ok {
		selDate = order[0] // 默认最新
	}
	dateReps := byDate[selDate]
	// 该日的大类（按 kindOrder 排）
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
	// 该大类的小文档：排序 + 去重标签 + 选默认
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
	if rep == nil {
		http.Error(w, "报告不存在", http.StatusNotFound)
		return
	}
	// URL 构造器
	mk := func(date, kind, rid string) template.URL {
		v := url.Values{}
		v.Set("date", date)
		if kind != "" {
			v.Set("kind", kind)
		}
		if rid != "" {
			v.Set("r", rid)
		}
		return template.URL("/stock/" + url.PathEscape(symbol) + "?" + v.Encode())
	}
	// 时间线：按时间正序（旧→新），最新在最右
	var timeline []tlNode
	for i := len(order) - 1; i >= 0; i-- {
		d := order[i]
		timeline = append(timeline, tlNode{Date: d, N: len(byDate[d]), Active: d == selDate, URL: string(mk(d, "", ""))})
	}
	var kindTabs []tabItem
	for _, k := range kinds {
		kindTabs = append(kindTabs, tabItem{Text: k, Active: k == selKind, URL: string(mk(selDate, k, ""))})
	}
	var subTabs []tabItem
	for _, m := range kindReps {
		subTabs = append(subTabs, tabItem{Text: m.Label, RID: m.RID, Active: m.RID == selRID, URL: string(mk(selDate, selKind, m.RID))})
	}
	s.render(w, r, "stock", map[string]any{
		"User": user, "Admin": s.isAdmin(user), "Symbol": symbol, "Name": s.names.Get(symbol),
		"SelDate": selDate, "SelKind": selKind, "Rep": rep,
		"Timeline": timeline, "KindTabs": kindTabs, "SubTabs": subTabs,
		"NKinds": len(kindTabs), "NSubs": len(subTabs),
	})
}

func repInList(reps []Rep, rid string) bool {
	for _, r := range reps {
		if r.RID == rid {
			return true
		}
	}
	return false
}

func (s *Server) runView(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	members := s.runMembers(key)
	if len(members) == 0 {
		http.Error(w, "未找到该 run", 404)
		return
	}
	var defRID string
	members, defRID = s.orderAndDefault(members)
	sel := r.URL.Query().Get("r")
	if sel == "" {
		sel = defRID
	}
	rep := s.loadRep(sel)
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	back := r.URL.Query().Get("back")
	if back == "" {
		back = "/"
	}
	var mtypes []string
	for _, m := range members {
		if m.RType != "" {
			mtypes = append(mtypes, m.RType)
		}
	}
	s.render(w, r, "run", map[string]any{
		"User": user, "Admin": s.isAdmin(user), "Key": key, "Back": back,
		"Members": members, "Sel": sel, "Rep": rep,
		"Symbol": members[0].Symbol, "Name": s.names.Get(members[0].Symbol),
		"Kind": runKind(mtypes), "Date": members[0].Date, "Source": members[0].Source,
	})
}

// loadRep 按 rid 取含正文的报告。
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

// ---------- 导出 ----------

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
			`<p>也可先用 <b>⬇ MD</b> 导出。<br><a href="javascript:history.back()">← 返回</a></p></div>`)
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

// ---------- 入口按钮管理 ----------

func (s *Server) manageLinks(w http.ResponseWriter, r *http.Request, user string) {
	s.render(w, r, "manage_links", map[string]any{"User": user, "Admin": true, "Links": s.st.Links()})
}
func (s *Server) linkAdd(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	// 新按钮排到末尾；顺序之后靠拖拽调。
	ord := 0
	if ls := s.st.Links(); len(ls) > 0 {
		ord = ls[len(ls)-1].Ord + 1
	}
	s.st.AddLink(strings.TrimSpace(r.FormValue("label")), strings.TrimSpace(r.FormValue("url")), ord)
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}
func (s *Server) linkEdit(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.st.UpdateLinkFields(id, strings.TrimSpace(r.FormValue("label")), strings.TrimSpace(r.FormValue("url")))
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}
func (s *Server) linkDelete(w http.ResponseWriter, r *http.Request, user string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.st.DeleteLink(id)
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}

// linkReorder 拖拽排序：按提交的 id 顺序写 ord=0,1,2…
func (s *Server) linkReorder(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	for i, v := range r.Form["id"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.st.SetLinkOrder(id, i)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 报告类型管理 ----------

type typeRow struct {
	Name      string
	Ord       int
	IsSummary bool
	Label     string
}

type typeGroup struct {
	Kind string
	Rows []typeRow
}

var kindOrder = []string{"并购重组", "投资决策", "深度研究", "技术分析", "事件监测", "其他"}

func (s *Server) manageTypes(w http.ResponseWriter, r *http.Request, user string) {
	cfg := s.st.TypeConfigs()
	known := map[string]bool{"并购重组": true, "投资决策": true, "深度研究": true, "技术分析": true, "事件监测": true}
	byKind := map[string][]typeRow{}
	for _, name := range s.st.DiscoveredTypes() {
		c := cfg[name]
		row := typeRow{Name: name, Ord: c.Ord, IsSummary: c.IsSummary, Label: c.Label}
		k := runKind([]string{name})
		if !known[k] {
			k = "其他"
		}
		byKind[k] = append(byKind[k], row)
	}
	var groups []typeGroup
	for _, k := range kindOrder {
		rows := byKind[k]
		if len(rows) == 0 {
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Ord != rows[j].Ord {
				return rows[i].Ord < rows[j].Ord
			}
			return rows[i].Name < rows[j].Name
		})
		groups = append(groups, typeGroup{Kind: k, Rows: rows})
	}
	s.render(w, r, "manage_types", map[string]any{"User": user, "Admin": true, "Groups": groups})
}

func (s *Server) manageTypesDelete(w http.ResponseWriter, r *http.Request, user string) {
	s.st.DeleteTypeConfig(r.PathValue("name"))
	http.Redirect(w, r, "/manage/types", http.StatusSeeOther)
}

func (s *Server) manageTypesSave(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	names := r.Form["name"]
	labels := r.Form["label"]
	sum := map[string]bool{}
	for _, v := range r.Form["summary"] {
		sum[v] = true
	}
	cfg := s.st.TypeConfigs() // 保留既有排序位（ord 只由拖拽改）
	for i, name := range names {
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		s.st.UpsertTypeConfig(name, cfg[name].Ord, sum[name], label)
	}
	http.Redirect(w, r, "/manage/types", http.StatusSeeOther)
}

// manageTypesAdd 手动预注册一个类型（工作流还没产出该类型时先配好默认/改名）。排到末尾，顺序靠拖拽。
func (s *Server) manageTypesAdd(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		ord := 1
		for _, c := range s.st.TypeConfigs() {
			if c.Ord >= ord {
				ord = c.Ord + 1
			}
		}
		s.st.UpsertTypeConfig(name, ord, r.FormValue("summary") != "", strings.TrimSpace(r.FormValue("label")))
	}
	http.Redirect(w, r, "/manage/types", http.StatusSeeOther)
}

// manageTypesReorder 拖拽排序：按提交的 name 顺序写 ord=0,1,2…（同一大类内）。
func (s *Server) manageTypesReorder(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	for i, name := range r.Form["name"] {
		s.st.SetTypeOrder(name, i)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 账号管理 ----------

func (s *Server) manageUsers(w http.ResponseWriter, r *http.Request, user string) {
	s.render(w, r, "manage_users", map[string]any{
		"User": user, "Admin": true, "Users": s.st.Users(), "Me": user, "Roles": roleRegistry})
}

func (s *Server) userAdd(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("username"))
	pw := r.FormValue("password")
	if name != "" && pw != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		s.st.UpsertUser(User{Username: name, PasswordHash: string(h), Role: validRole(r.FormValue("role"))})
	}
	http.Redirect(w, r, "/manage/users", http.StatusSeeOther)
}

func (s *Server) userSave(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	if u == nil {
		http.Redirect(w, r, "/manage/users", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	newRole := validRole(r.FormValue("role"))
	if newRole != "admin" && u.IsAdmin() && s.st.CountAdmins() <= 1 { // 不许降级最后一个管理员
		newRole = "admin"
	}
	s.st.SetUserRole(name, newRole)
	if pw := strings.TrimSpace(r.FormValue("password")); pw != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		s.st.SetUserPassword(name, string(h))
	}
	http.Redirect(w, r, "/manage/users", http.StatusSeeOther)
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	// 不许删自己、不许删最后一个管理员
	if u != nil && name != user && !(u.IsAdmin() && s.st.CountAdmins() <= 1) {
		s.st.DeleteUser(name)
	}
	http.Redirect(w, r, "/manage/users", http.StatusSeeOther)
}

// ---------- 后台同步 ----------

// ---------- 系统设置（旧门户凭据 / 同步间隔，存 DB） ----------

func (s *Server) manageSettings(w http.ResponseWriter, r *http.Request, user string) {
	s.render(w, r, "manage_settings", map[string]any{"User": user, "Admin": true,
		"OldBase":  s.st.GetSetting("old_base", ""),
		"OldUser":  s.st.GetSetting("old_user", ""),
		"HasPass":  s.st.GetSetting("old_pass", "") != "",
		"SyncMin":  s.st.GetSetting("sync_min", "0"),
		"Tokens":   s.st.ListTokens(),
		"NewCount": s.st.CountNew(),
		"OldCount": s.st.CountOld()})
}

func (s *Server) manageSettingsSave(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	s.st.SetSetting("old_base", strings.TrimSpace(r.FormValue("old_base")))
	s.st.SetSetting("old_user", strings.TrimSpace(r.FormValue("old_user")))
	if pw := r.FormValue("old_pass"); pw != "" { // 留空=不改密码
		s.st.SetSetting("old_pass", pw)
	}
	s.st.SetSetting("sync_min", strings.TrimSpace(r.FormValue("sync_min")))
	s.old.SetCreds(s.st.GetSetting("old_base", ""), s.st.GetSetting("old_user", ""), s.st.GetSetting("old_pass", ""))
	http.Redirect(w, r, "/manage/settings", http.StatusSeeOther)
}

func (s *Server) settingsSyncNow(w http.ResponseWriter, r *http.Request, user string) {
	go func() {
		n, err := s.old.SyncAllMeta(s.st)
		log.Printf("manual old-meta sync: %d, err=%v", n, err)
	}()
	http.Redirect(w, r, "/manage/settings", http.StatusSeeOther)
}

func (s *Server) syncLoop() {
	do := func() {
		if s.st.GetSetting("old_base", "") == "" {
			return // 未配置旧门户，跳过同步
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
			return // 0 = 只启动同步一次
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
