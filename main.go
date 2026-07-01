package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
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

func main() {
	// 子命令：report-portal hashpw <password> —— 生成 bcrypt 哈希贴进 config.yaml
	if len(os.Args) > 1 && os.Args[1] == "hashpw" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: report-portal hashpw <password>")
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
		if c, err := LoadConfig(cfgP); err == nil {
			dir = dirOf(c.DBPath)
		}
		n, err := FetchNamesToFile(dir)
		if err != nil {
			log.Fatalf("抓取失败: %v", err)
		}
		fmt.Printf("已写 %s/names.json: %d 条\n", dir, n)
		return
	}

	// 子命令：report-portal adduser <用户名> <密码> [admin] —— 兜底防锁死
	if len(os.Args) > 1 && os.Args[1] == "adduser" {
		if len(os.Args) < 4 {
			log.Fatal("用法: report-portal adduser <用户名> <密码> [admin]")
		}
		cfgP := os.Getenv("RP_CONFIG")
		if cfgP == "" {
			cfgP = "config.yaml"
		}
		c, err := LoadConfig(cfgP)
		if err != nil {
			log.Fatalf("配置: %v", err)
		}
		os.MkdirAll(dirOf(c.DBPath), 0o755)
		st, err := OpenStore(c.DBPath)
		if err != nil {
			log.Fatal(err)
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(os.Args[3]), 12)
		admin := len(os.Args) > 4 && os.Args[4] == "admin"
		if err := st.UpsertUser(User{Username: os.Args[2], PasswordHash: string(h), IsAdmin: admin}); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("已写入账号 %s (admin=%v)\n", os.Args[2], admin)
		return
	}

	cfgPath := os.Getenv("RP_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("配置读取失败 %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(dirOf(cfg.DBPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("数据库: %v", err)
	}
	if st.CountUsers() == 0 { // 首启：从 config 种入初始账号
		for _, u := range cfg.Users {
			if u.Username != "" && u.PasswordHash != "" {
				st.UpsertUser(u)
			}
		}
		log.Printf("已从 config 种入 %d 个账号", st.CountUsers())
	}
	s := &Server{cfg: cfg, st: st, old: NewOldClient(cfg.OldPortal.BaseURL, cfg.OldPortal.Username, cfg.OldPortal.Password)}
	s.names = LoadNames(dirOf(cfg.DBPath))
	s.names.ensureFull() // 无全量表时后台 best-effort 抓一次
	s.parseTemplates()

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
	mux.HandleFunc("GET /{$}", s.requireUser(s.index))
	mux.HandleFunc("GET /run/{key}", s.requireUser(s.runView))
	mux.HandleFunc("GET /report/{rid}/md", s.requireUser(s.reportMD))
	mux.HandleFunc("GET /report/{rid}/pdf", s.requireUser(s.reportPDF))
	mux.HandleFunc("GET /manage/links", s.requireAdmin(s.manageLinks))
	mux.HandleFunc("POST /manage/links/add", s.requireAdmin(s.linkAdd))
	mux.HandleFunc("POST /manage/links/{id}/edit", s.requireAdmin(s.linkEdit))
	mux.HandleFunc("POST /manage/links/{id}/delete", s.requireAdmin(s.linkDelete))
	mux.HandleFunc("GET /manage/types", s.requireAdmin(s.manageTypes))
	mux.HandleFunc("POST /manage/types", s.requireAdmin(s.manageTypesSave))
	mux.HandleFunc("GET /manage/users", s.requireAdmin(s.manageUsers))
	mux.HandleFunc("POST /manage/users/add", s.requireAdmin(s.userAdd))
	mux.HandleFunc("POST /manage/users/{name}/save", s.requireAdmin(s.userSave))
	mux.HandleFunc("POST /manage/users/{name}/delete", s.requireAdmin(s.userDelete))

	log.Printf("研报门户 %s 启动于 %s (新:%d 旧:%d)", version, cfg.Listen, st.CountNew(), st.CountOld())
	if err := http.ListenAndServe(cfg.Listen, mux); err != nil {
		log.Fatal(err)
	}
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
		"trunc10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
	s.pages = map[string]*template.Template{}
	for _, name := range []string{"login", "index", "run", "manage_links", "manage_types", "manage_users"} {
		s.pages[name] = template.Must(template.New("base.html").Funcs(funcs).
			ParseFS(tplFS, "templates/base.html", "templates/"+name+".html"))
	}
	s.pdf = template.Must(template.New("pdf.html").Funcs(funcs).ParseFS(tplFS, "templates/pdf.html"))
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
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
	return u != nil && u.IsAdmin
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
	s.render(w, "login", map[string]any{"Err": ""})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := s.st.GetUser(r.FormValue("username"))
	pw := r.FormValue("password")
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) != nil {
		w.WriteHeader(401)
		s.render(w, "login", map[string]any{"Err": "用户名或密码错误"})
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
	s.render(w, "index", map[string]any{
		"User": user, "Admin": s.isAdmin(user),
		"Groups": pageGroups, "NewTotal": newTotal, "OldTotal": oldTotal, "TotalRuns": totalRuns,
		"Types": types, "Links": s.st.Links(),
		"Page": page, "Pages": pages, "PageSizes": pageSizes,
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
func (s *Server) orderAndDefault(members []Rep) ([]Rep, string) {
	cfg := s.st.TypeConfigs()
	ord := func(r Rep) int {
		if c, ok := cfg[r.RType]; ok {
			return c.Ord
		}
		return 1000
	}
	out := make([]Rep, len(members))
	copy(out, members)
	for i := range out {
		if c, ok := cfg[out[i].RType]; ok && c.Label != "" {
			out[i].Label = c.Label
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if oi, oj := ord(out[i]), ord(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Time < out[j].Time
	})
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
	s.render(w, "run", map[string]any{
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
	s.render(w, "manage_links", map[string]any{"User": user, "Admin": true, "Links": s.st.Links()})
}
func (s *Server) linkAdd(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	ord, _ := strconv.Atoi(r.FormValue("ord"))
	s.st.AddLink(strings.TrimSpace(r.FormValue("label")), strings.TrimSpace(r.FormValue("url")), ord)
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}
func (s *Server) linkEdit(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ord, _ := strconv.Atoi(r.FormValue("ord"))
	s.st.UpdateLink(id, strings.TrimSpace(r.FormValue("label")), strings.TrimSpace(r.FormValue("url")), ord)
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}
func (s *Server) linkDelete(w http.ResponseWriter, r *http.Request, user string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.st.DeleteLink(id)
	http.Redirect(w, r, "/manage/links", http.StatusSeeOther)
}

// ---------- 报告类型管理 ----------

type typeRow struct {
	Name      string
	Ord       int
	IsSummary bool
	Label     string
}

func (s *Server) manageTypes(w http.ResponseWriter, r *http.Request, user string) {
	cfg := s.st.TypeConfigs()
	var rows []typeRow
	for _, name := range s.st.DiscoveredTypes() {
		c := cfg[name]
		rows = append(rows, typeRow{Name: name, Ord: c.Ord, IsSummary: c.IsSummary, Label: c.Label})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Ord != rows[j].Ord {
			return rows[i].Ord < rows[j].Ord
		}
		return rows[i].Name < rows[j].Name
	})
	s.render(w, "manage_types", map[string]any{"User": user, "Admin": true, "Rows": rows})
}

func (s *Server) manageTypesSave(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	names := r.Form["name"]
	ords := r.Form["ord"]
	labels := r.Form["label"]
	sum := map[string]bool{}
	for _, v := range r.Form["summary"] {
		sum[v] = true
	}
	for i, name := range names {
		ord := 0
		if i < len(ords) {
			ord, _ = strconv.Atoi(ords[i])
		}
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		s.st.UpsertTypeConfig(name, ord, sum[name], label)
	}
	http.Redirect(w, r, "/manage/types", http.StatusSeeOther)
}

// ---------- 账号管理 ----------

func (s *Server) manageUsers(w http.ResponseWriter, r *http.Request, user string) {
	s.render(w, "manage_users", map[string]any{
		"User": user, "Admin": true, "Users": s.st.Users(), "Me": user})
}

func (s *Server) userAdd(w http.ResponseWriter, r *http.Request, user string) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("username"))
	pw := r.FormValue("password")
	if name != "" && pw != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		s.st.UpsertUser(User{Username: name, PasswordHash: string(h), IsAdmin: r.FormValue("is_admin") != ""})
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
	wantAdmin := r.FormValue("is_admin") != ""
	if u.IsAdmin && !wantAdmin && s.st.CountAdmins() <= 1 { // 不许降级最后一个管理员
		wantAdmin = true
	}
	s.st.SetUserAdmin(name, wantAdmin)
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
	if u != nil && name != user && !(u.IsAdmin && s.st.CountAdmins() <= 1) {
		s.st.DeleteUser(name)
	}
	http.Redirect(w, r, "/manage/users", http.StatusSeeOther)
}

// ---------- 后台同步 ----------

func (s *Server) syncLoop() {
	do := func() {
		n, err := s.old.SyncAllMeta(s.st)
		if err != nil {
			log.Printf("旧元数据同步出错(已同步%d): %v", n, err)
			return
		}
		log.Printf("旧元数据同步完成: %d 条", s.st.CountOld())
	}
	do()
	if s.cfg.SyncMin > 0 {
		t := time.NewTicker(time.Duration(s.cfg.SyncMin) * time.Minute)
		for range t.C {
			do()
		}
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
