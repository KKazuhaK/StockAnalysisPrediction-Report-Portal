package main

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres 驱动，注册为 "pgx"
	_ "modernc.org/sqlite"             // sqlite 驱动(纯 Go)，注册为 "sqlite"
)

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

// Rep 是新旧统一的报告表示（列表/分组/阅读都用它）。
type Rep struct {
	RID, Src      string // RID: "n<rowid>" 新 / "o<id>" 旧
	UID           string // 新报告的稳定外部 id（upsert 用）
	Title, Symbol string
	RType, Date   string
	Kind, RunID   string // Kind: 大类(并购重组/投资决策…，新报告用)；RunID: 一次生成分组
	Source, Time  string
	HTML, MD      string // 正文（阅读时才填）
	Label         string // run 内 tab 短标签
}

// Link 入口按钮。
type Link struct {
	ID         int64
	Label, URL string
	Ord        int
}

type Store struct {
	db     *sql.DB
	driver string // "sqlite" | "postgres"
}

// OpenStore 按驱动打开数据库。driver: "sqlite"(默认) 或 "postgres"；
// source: sqlite=文件路径，postgres=DSN(postgres://user:pass@host/db?sslmode=disable)。
func OpenStore(driver, source string) (*Store, error) {
	if driver == "" {
		driver = "sqlite"
	}
	sqlDriver := "sqlite"
	if driver == "postgres" {
		sqlDriver = "pgx"
	}
	db, err := sql.Open(sqlDriver, source)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1) // SQLite：单写者，避免锁竞争
	} else {
		db.SetMaxOpenConns(10)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("连接数据库(%s)失败: %w", driver, err)
	}
	s := &Store{db: db, driver: driver}
	return s, s.init()
}

// bind 把 ? 占位符按驱动改写（postgres 用 $1,$2…）。
func (s *Store) bind(q string) string {
	if s.driver != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteString("$")
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (s *Store) exec(q string, args ...any) (sql.Result, error) { return s.db.Exec(s.bind(q), args...) }
func (s *Store) query(q string, args ...any) (*sql.Rows, error) { return s.db.Query(s.bind(q), args...) }
func (s *Store) queryRow(q string, args ...any) *sql.Row        { return s.db.QueryRow(s.bind(q), args...) }

// pkAuto 自增主键定义（两种数据库方言不同）。
func (s *Store) pkAuto() string {
	if s.driver == "postgres" {
		return "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY"
	}
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

func (s *Store) init() error {
	pk := s.pkAuto()
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS reports(
			rowid %s,
			uid TEXT UNIQUE, title TEXT, symbol TEXT, rtype TEXT, rdate TEXT,
			kind TEXT, run_id TEXT,
			source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_reports_date ON reports(rdate)`,
		`CREATE INDEX IF NOT EXISTS idx_reports_sym  ON reports(symbol)`,
		`CREATE TABLE IF NOT EXISTS old_meta(
			id BIGINT PRIMARY KEY, title TEXT, category TEXT, author TEXT,
			time TEXT, report_date TEXT, stock_code TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_old_date ON old_meta(report_date)`,
		`CREATE INDEX IF NOT EXISTS idx_old_sym  ON old_meta(stock_code)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS links(
			id %s, label TEXT, url TEXT, ord INTEGER DEFAULT 0)`, pk),
		`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT)`,
		// 报告类型注册表：小文档(name,唯一) → 显式大类(kind) + 显示名/排序/默认页。
		// 入库自动登记、后台可改；替代 runKind 猜测（runKind 仅作新类型的兜底默认）。
		`CREATE TABLE IF NOT EXISTS type_config(
			name TEXT PRIMARY KEY, kind TEXT, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT)`,
		// 登录账号（config.yaml 仅首启种子，之后网页管理）。role 可扩展更多角色。
		`CREATE TABLE IF NOT EXISTS users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user')`,
		// API 令牌（多枚，带备注/作用域/有效期/最近使用）。scope: all|ingest|query。
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS api_tokens(
			id %s, token TEXT UNIQUE, name TEXT, scope TEXT DEFAULT 'all',
			created_at TEXT, expires_at TEXT, last_used_at TEXT)`, pk),
		// 结构化“假设/跟踪项”，供重跑复核（跨报告类型通用）。itype: assumption|tracking。
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS tracking_items(
			id %s, report_uid TEXT, symbol TEXT, itype TEXT, content TEXT,
			status TEXT DEFAULT 'pending', review_point TEXT, created_at TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_track_sym ON tracking_items(symbol, status)`,
		`CREATE INDEX IF NOT EXISTS idx_track_uid ON tracking_items(report_uid)`,
		// 股票代码→名字（入库后可按名字搜索；来源 eastmoney，启动/fetchnames 时同步）。
		`CREATE TABLE IF NOT EXISTS stocks(code TEXT PRIMARY KEY, name TEXT, updated_at TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_stocks_name ON stocks(name)`,
	}
	for _, st := range stmts {
		if _, err := s.exec(st); err != nil {
			return fmt.Errorf("建表失败: %w\nSQL: %s", err, st)
		}
	}
	return nil
}

// ---------- 账号 ----------

func (s *Store) Users() []User {
	rows, err := s.query("SELECT username,password_hash,role FROM users ORDER BY role, username")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var role sql.NullString
		rows.Scan(&u.Username, &u.PasswordHash, &role)
		u.Role = role.String
		out = append(out, u)
	}
	return out
}

func (s *Store) GetUser(name string) *User {
	var u User
	var role sql.NullString
	err := s.queryRow("SELECT username,password_hash,role FROM users WHERE username=?", name).
		Scan(&u.Username, &u.PasswordHash, &role)
	if err != nil {
		return nil
	}
	u.Role = role.String
	return &u
}

func (s *Store) UpsertUser(u User) error {
	_, err := s.exec(`INSERT INTO users(username,password_hash,role) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash,role=excluded.role`,
		u.Username, u.PasswordHash, u.EffRole())
	return err
}

func (s *Store) SetUserPassword(name, hash string) error {
	_, err := s.exec("UPDATE users SET password_hash=? WHERE username=?", hash, name)
	return err
}

func (s *Store) SetUserRole(name, role string) error {
	_, err := s.exec("UPDATE users SET role=? WHERE username=?", role, name)
	return err
}

func (s *Store) DeleteUser(name string) error {
	_, err := s.exec("DELETE FROM users WHERE username=?", name)
	return err
}

func (s *Store) CountUsers() (n int) {
	s.queryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return
}

func (s *Store) CountAdmins() (n int) {
	s.queryRow("SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&n)
	return
}

// ---------- 系统设置（存 meta 表，网页可改） ----------

func (s *Store) GetSetting(k, def string) string {
	var v sql.NullString
	if err := s.queryRow("SELECT v FROM meta WHERE k=?", k).Scan(&v); err == nil && v.Valid {
		return v.String
	}
	return def
}

func (s *Store) SetSetting(k, v string) error {
	_, err := s.exec("INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, v)
	return err
}

// ---------- 报告类型配置（管理员可改） ----------

type TypeConfig struct {
	Name      string // 小文档名（唯一）
	Kind      string // 所属大类（显式登记）
	Ord       int
	IsSummary bool
	Label     string
}

func (s *Store) TypeConfigs() map[string]TypeConfig {
	m := map[string]TypeConfig{}
	rows, err := s.query("SELECT name,kind,ord,is_summary,label FROM type_config")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var t TypeConfig
		var isum int
		var kind, label sql.NullString
		rows.Scan(&t.Name, &kind, &t.Ord, &isum, &label)
		t.Kind, t.IsSummary, t.Label = kind.String, isum == 1, label.String
		m[t.Name] = t
	}
	return m
}

// TypeKind 查小文档所属大类（注册表里没有则空，调用方用 runKind 兜底）。
func (s *Store) TypeKind(name string) string {
	var kind sql.NullString
	s.queryRow("SELECT kind FROM type_config WHERE name=?", name).Scan(&kind)
	return kind.String
}

// RegisterType 入库时自动登记新小文档（已存在则不动，保留后台设置）。
func (s *Store) RegisterType(name, kind string) {
	s.exec(`INSERT INTO type_config(name,kind,ord,is_summary,label) VALUES(?,?,0,0,'')
		ON CONFLICT(name) DO UPDATE SET kind=CASE WHEN type_config.kind='' OR type_config.kind IS NULL
			THEN excluded.kind ELSE type_config.kind END`, name, kind)
}

func (s *Store) UpsertTypeConfig(name, kind, label string, ord int, isSummary bool) error {
	is := 0
	if isSummary {
		is = 1
	}
	_, err := s.exec(`INSERT INTO type_config(name,kind,ord,is_summary,label) VALUES(?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET kind=excluded.kind,ord=excluded.ord,is_summary=excluded.is_summary,label=excluded.label`,
		name, kind, ord, is, label)
	return err
}

// SetReportsKind 把某小文档的大类变更传播到已入库的报告（保持快照与注册表一致）。
func (s *Store) SetReportsKind(name, kind string) error {
	_, err := s.exec("UPDATE reports SET kind=? WHERE rtype=?", kind, name)
	return err
}

// SetTypeOrder 只更新排序位（拖拽落库），保留 kind/is_summary/label；未配置的类型自动建行。
func (s *Store) SetTypeOrder(name string, ord int) error {
	_, err := s.exec(`INSERT INTO type_config(name,kind,ord,is_summary,label) VALUES(?,'',?,0,'')
		ON CONFLICT(name) DO UPDATE SET ord=excluded.ord`, name, ord)
	return err
}

// DeleteTypeConfig 删除类型配置。若该类型仍有报告，则只是回到"未配置"(数据里还会出现)；
// 若是手动预注册、无对应报告，删完就彻底消失。
func (s *Store) DeleteTypeConfig(name string) error {
	_, err := s.exec("DELETE FROM type_config WHERE name=?", name)
	return err
}

// DiscoveredTypes 数据里出现过的所有类型（新+旧）并入已配置的。
func (s *Store) DiscoveredTypes() []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range s.distinct("SELECT DISTINCT rtype FROM reports WHERE rtype<>''") {
		add(v)
	}
	for _, v := range s.distinct("SELECT DISTINCT category FROM old_meta WHERE category<>''") {
		add(v)
	}
	for k := range s.TypeConfigs() {
		add(k)
	}
	return out
}

// Filters 列表/分组的筛选条件。
type Filters struct {
	Q, Scope, Symbol, RType string
	DateFrom, DateTo, Sort  string
}

func dir(sort string) string {
	if sort == "date_asc" {
		return "ASC"
	}
	return "DESC"
}

// ---------- 新报告 ----------

// SearchNew 返回匹配的新报告（不含正文）。
func (s *Store) SearchNew(f Filters) ([]Rep, error) {
	var where []string
	var args []any
	if f.Q != "" {
		if f.Scope == "fulltext" {
			where = append(where, "(title LIKE ? OR body_md LIKE ?)")
			args = append(args, "%"+f.Q+"%", "%"+f.Q+"%")
		} else {
			where = append(where, "title LIKE ?")
			args = append(args, "%"+f.Q+"%")
		}
	}
	if f.Symbol != "" {
		where = append(where, "symbol LIKE ?")
		args = append(args, "%"+f.Symbol+"%")
	}
	if f.RType != "" {
		where = append(where, "rtype = ?")
		args = append(args, f.RType)
	}
	if f.DateFrom != "" {
		where = append(where, "rdate >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "rdate <= ?")
		args = append(args, f.DateTo)
	}
	q := "SELECT rowid,title,symbol,rtype,rdate,kind,run_id,source,sent_at FROM reports"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY rdate %s, sent_at %s", dir(f.Sort), dir(f.Sort))
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		out = append(out, scanNewRow(rows))
	}
	return out, rows.Err()
}

// scanNewRow 扫一行新报告（不含正文）。列序固定：rowid,title,symbol,rtype,rdate,kind,run_id,source,sent_at。
func scanNewRow(rows *sql.Rows) Rep {
	var id int64
	var title, sym, rt, rd, kind, runID, src, sent sql.NullString
	rows.Scan(&id, &title, &sym, &rt, &rd, &kind, &runID, &src, &sent)
	return Rep{
		RID: fmt.Sprintf("n%d", id), Src: "new", Title: title.String, Symbol: sym.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String,
	}
}

// ApiToken 一枚 API 令牌（多枚共存，带备注/作用域/有效期）。
type ApiToken struct {
	ID                                           int64
	Token, Name, Scope, Created, Expires, LastUsed string
}

func (s *Store) CreateToken(token, name, scope, expires string) error {
	if scope == "" {
		scope = "all"
	}
	_, err := s.exec(`INSERT INTO api_tokens(token,name,scope,created_at,expires_at) VALUES(?,?,?,?,?)`,
		token, name, scope, nowStr(), expires)
	return err
}

func (s *Store) ListTokens() []ApiToken {
	rows, err := s.query(`SELECT id,token,name,scope,created_at,expires_at,last_used_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ApiToken
	for rows.Next() {
		var t ApiToken
		var name, scope, created, expires, last sql.NullString
		rows.Scan(&t.ID, &t.Token, &name, &scope, &created, &expires, &last)
		t.Name, t.Scope, t.Created, t.Expires, t.LastUsed = name.String, scope.String, created.String, expires.String, last.String
		out = append(out, t)
	}
	return out
}

func (s *Store) DeleteToken(id int64) error {
	_, err := s.exec("DELETE FROM api_tokens WHERE id=?", id)
	return err
}

func (s *Store) CountTokens() (n int) {
	s.queryRow("SELECT COUNT(*) FROM api_tokens").Scan(&n)
	return
}

// TokenValid 校验令牌：存在、未过期、作用域匹配（all 或等于 need）。通过则刷新 last_used。
func (s *Store) TokenValid(token, need string) bool {
	if token == "" {
		return false
	}
	var scope, expires sql.NullString
	err := s.queryRow("SELECT scope,expires_at FROM api_tokens WHERE token=?", token).Scan(&scope, &expires)
	if err != nil {
		return false
	}
	if expires.String != "" && expires.String < nowStr() {
		return false // 已过期
	}
	if need != "" && scope.String != "all" && scope.String != need {
		return false // 作用域不含该操作
	}
	s.exec("UPDATE api_tokens SET last_used_at=? WHERE token=?", nowStr(), token)
	return true
}

// Manifest 某标的“有哪些报告”的清单（供 Dify 先探再取）：总数、各日期(含大类)、全部大类/小文档。
func (s *Store) Manifest(symbol string) map[string]any {
	reps, _ := s.NewBySymbol(symbol)
	type dateInfo struct {
		Date  string   `json:"date"`
		Count int      `json:"count"`
		Kinds []string `json:"kinds"`
	}
	var dates []dateInfo
	dseen := map[string]int{}
	kindSet, subSet := map[string]bool{}, map[string]bool{}
	kseenByDate := map[string]map[string]bool{}
	for _, r := range reps {
		k := r.Kind
		if k == "" {
			k = runKind([]string{r.RType})
		}
		kindSet[k] = true
		if r.RType != "" {
			subSet[r.RType] = true
		}
		i, ok := dseen[r.Date]
		if !ok {
			i = len(dates)
			dseen[r.Date] = i
			dates = append(dates, dateInfo{Date: r.Date})
			kseenByDate[r.Date] = map[string]bool{}
		}
		dates[i].Count++
		if !kseenByDate[r.Date][k] {
			kseenByDate[r.Date][k] = true
			dates[i].Kinds = append(dates[i].Kinds, k)
		}
	}
	return map[string]any{
		"symbol": symbol, "total": len(reps),
		"dates": dates, "kinds": keysOf(kindSet), "subtypes": keysOf(subSet),
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// QueryReports 供 Dify 按代码/关键字/大类/小文档/日期区间查历史新报告（日期倒序）。symbol 可空（全库搜索）。withBody 带 body_md。
func (s *Store) QueryReports(symbol, q, kind, rtype, since, until string, limit int, withBody bool) ([]Rep, error) {
	var where []string
	var args []any
	if symbol != "" {
		where = append(where, "symbol=?")
		args = append(args, symbol)
	}
	if q != "" {
		where = append(where, "(title LIKE ? OR body_md LIKE ?)")
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if kind != "" {
		where = append(where, "kind=?")
		args = append(args, kind)
	}
	if rtype != "" {
		where = append(where, "rtype=?")
		args = append(args, rtype)
	}
	if since != "" {
		where = append(where, "rdate>=?")
		args = append(args, since)
	}
	if until != "" {
		where = append(where, "rdate<=?")
		args = append(args, until)
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	whereClause := "1=1"
	if len(where) > 0 {
		whereClause = strings.Join(where, " AND ")
	}
	sqlStr := fmt.Sprintf(`SELECT rowid,uid,title,symbol,rtype,rdate,kind,run_id,source,sent_at,body_md
		FROM reports WHERE %s ORDER BY rdate DESC, sent_at DESC LIMIT %d`, whereClause, limit)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		var id int64
		var uid, title, sym, rt, rd, kind, runID, src, sent, md sql.NullString
		rows.Scan(&id, &uid, &title, &sym, &rt, &rd, &kind, &runID, &src, &sent, &md)
		r := Rep{RID: fmt.Sprintf("n%d", id), Src: "new", UID: uid.String, Title: title.String,
			Symbol: sym.String, RType: rt.String, Date: rd.String, Kind: kind.String,
			RunID: runID.String, Source: src.String, Time: sent.String}
		if withBody {
			r.MD = md.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetByUID 按 uid 取单篇新报告（含正文）。
func (s *Store) GetByUID(uid string) *Rep {
	var id int64
	var title, sym, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(`SELECT rowid,title,symbol,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html
		FROM reports WHERE uid=?`, uid).Scan(&id, &title, &sym, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err != nil {
		return nil
	}
	return &Rep{RID: fmt.Sprintf("n%d", id), Src: "new", UID: uid, Title: title.String, Symbol: sym.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String}
}

// TrackingItem 结构化的假设/跟踪项。
type TrackingItem struct {
	ID                                           int64
	ReportUID, Symbol, IType, Content, Status, ReviewPoint, Created string
}

// SetTracking 覆盖某报告的跟踪项（重跑时先清后写，保持与最新正文一致）。
func (s *Store) SetTracking(reportUID, symbol string, items []TrackingItem) error {
	if _, err := s.exec("DELETE FROM tracking_items WHERE report_uid=?", reportUID); err != nil {
		return err
	}
	now := nowStr()
	for _, it := range items {
		status := it.Status
		if status == "" {
			status = "pending"
		}
		if _, err := s.exec(`INSERT INTO tracking_items(report_uid,symbol,itype,content,status,review_point,created_at)
			VALUES(?,?,?,?,?,?,?)`, reportUID, symbol, it.IType, it.Content, status, it.ReviewPoint, now); err != nil {
			return err
		}
	}
	return nil
}

// QueryTracking 查某标的的假设/跟踪项（可按状态筛，默认按新到旧）。
func (s *Store) QueryTracking(symbol, status string, limit int) []TrackingItem {
	where := []string{"symbol=?"}
	args := []any{symbol}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.query(fmt.Sprintf(`SELECT id,report_uid,symbol,itype,content,status,review_point,created_at
		FROM tracking_items WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`, strings.Join(where, " AND "), limit), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TrackingItem
	for rows.Next() {
		var t TrackingItem
		var uid, sym, it, c, st, rp, cr sql.NullString
		rows.Scan(&t.ID, &uid, &sym, &it, &c, &st, &rp, &cr)
		t.ReportUID, t.Symbol, t.IType, t.Content, t.Status, t.ReviewPoint, t.Created =
			uid.String, sym.String, it.String, c.String, st.String, rp.String, cr.String
		out = append(out, t)
	}
	return out
}

// SymbolInfo 有报告的股票概览。
type SymbolInfo struct {
	Symbol, Name, Latest string
	Count                int
}

// SyncStocks 批量 upsert 股票代码→名字（供按名字搜索；来源 eastmoney）。
func (s *Store) SyncStocks(m map[string]string) {
	if len(m) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(s.bind("INSERT INTO stocks(code,name,updated_at) VALUES(?,?,?) " +
		"ON CONFLICT(code) DO UPDATE SET name=excluded.name,updated_at=excluded.updated_at"))
	if err != nil {
		tx.Rollback()
		return
	}
	now := nowStr()
	for code, name := range m {
		stmt.Exec(code, name, now)
	}
	stmt.Close()
	tx.Commit()
}

// StockName 查单只股票名字（DB 里没有则空）。
func (s *Store) StockName(code string) string {
	var name sql.NullString
	s.queryRow("SELECT name FROM stocks WHERE code=?", code).Scan(&name)
	return name.String
}

// ListSymbols 列出有报告的股票（q 匹配代码或名字，为空则全部），按报告数倒序。
func (s *Store) ListSymbols(q string, limit int) []SymbolInfo {
	var where string
	var args []any
	if q != "" {
		where = "WHERE r.symbol LIKE ? OR s.name LIKE ?"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.query(fmt.Sprintf(`SELECT r.symbol, s.name, COUNT(*), MAX(r.rdate)
		FROM reports r LEFT JOIN stocks s ON s.code = r.symbol %s
		GROUP BY r.symbol ORDER BY COUNT(*) DESC, r.symbol LIMIT %d`, where, limit), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SymbolInfo
	for rows.Next() {
		var si SymbolInfo
		var name, latest sql.NullString
		rows.Scan(&si.Symbol, &name, &si.Count, &latest)
		si.Name, si.Latest = name.String, latest.String
		out = append(out, si)
	}
	return out
}

// RunInfo 报告组（一次生成 = 同 symbol+date+kind）概览。
type RunInfo struct {
	Symbol, Date, Kind, RunID string
	Subtypes                  []string
	Count                     int
}

// ListRuns 列出某标的的报告组（可选某日），按日期倒序。
func (s *Store) ListRuns(symbol, date string) []RunInfo {
	where := []string{"symbol=?"}
	args := []any{symbol}
	if date != "" {
		where = append(where, "rdate=?")
		args = append(args, date)
	}
	rows, err := s.query(fmt.Sprintf(`SELECT symbol,rdate,kind,MAX(run_id),
		GROUP_CONCAT(DISTINCT rtype), COUNT(*) FROM reports WHERE %s
		GROUP BY symbol,rdate,kind ORDER BY rdate DESC, kind`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RunInfo
	for rows.Next() {
		var ri RunInfo
		var kind, runID, subs sql.NullString
		rows.Scan(&ri.Symbol, &ri.Date, &kind, &runID, &subs, &ri.Count)
		ri.Kind, ri.RunID = kind.String, runID.String
		if subs.String != "" {
			ri.Subtypes = strings.Split(subs.String, ",")
		}
		out = append(out, ri)
	}
	return out
}

// NewBySymbol 取某标的的全部新报告（不含正文，按日期倒序），供个股 timeline 详情用。
func (s *Store) NewBySymbol(symbol string) ([]Rep, error) {
	rows, err := s.query(`SELECT rowid,title,symbol,rtype,rdate,kind,run_id,source,sent_at
		FROM reports WHERE symbol=? ORDER BY rdate DESC, sent_at ASC`, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		out = append(out, scanNewRow(rows))
	}
	return out, rows.Err()
}

func (s *Store) GetNew(rowid int64) (*Rep, error) {
	var title, sym, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(
		"SELECT title,symbol,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html FROM reports WHERE rowid=?", rowid).
		Scan(&title, &sym, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Rep{
		RID: fmt.Sprintf("n%d", rowid), Src: "new", Title: title.String, Symbol: sym.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String,
	}, nil
}

func (s *Store) UpsertReport(r Rep) error {
	_, err := s.exec(`
		INSERT INTO reports(uid,title,symbol,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,
		  rtype=excluded.rtype,rdate=excluded.rdate,kind=excluded.kind,run_id=excluded.run_id,
		  source=excluded.source,sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		r.UID, r.Title, r.Symbol, r.RType, r.Date, r.Kind, r.RunID, r.Source, r.Time, r.MD, r.HTML)
	return err
}

func (s *Store) CountNew() (n int) {
	s.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	return
}

func (s *Store) NewTypes() []string {
	return s.distinct("SELECT DISTINCT rtype FROM reports WHERE rtype<>'' ORDER BY rtype")
}

// ---------- 旧报告元数据本地索引 ----------

// OldRaw 旧 API /api/reports 的原始记录。
type OldRaw struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Category   string `json:"category"`
	Author     string `json:"author"`
	Time       string `json:"time"`
	ReportDate string `json:"reportDate"`
	StockCode  string `json:"stockCode"`
}

func (s *Store) UpsertOldMeta(rows []OldRaw) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(s.bind(`INSERT INTO old_meta(id,title,category,author,time,report_date,stock_code)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,category=excluded.category,
		author=excluded.author,time=excluded.time,report_date=excluded.report_date,stock_code=excluded.stock_code`))
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(r.ID, r.Title, r.Category, r.Author, r.Time, r.ReportDate, r.StockCode); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CountOld() (n int) {
	s.queryRow("SELECT COUNT(*) FROM old_meta").Scan(&n)
	return
}

func (s *Store) OldCategories() []string {
	return s.distinct("SELECT DISTINCT category FROM old_meta WHERE category<>'' ORDER BY category")
}

// SearchOldMeta 返回匹配的旧报告（归一为 Rep，正文留空）。
func (s *Store) SearchOldMeta(f Filters) ([]Rep, error) {
	var where []string
	var args []any
	if f.Q != "" {
		where = append(where, "title LIKE ?")
		args = append(args, "%"+f.Q+"%")
	}
	if f.Symbol != "" {
		where = append(where, "stock_code LIKE ?")
		args = append(args, "%"+f.Symbol+"%")
	}
	if f.RType != "" {
		where = append(where, "category = ?")
		args = append(args, f.RType)
	}
	if f.DateFrom != "" {
		where = append(where, "report_date >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "report_date <= ?")
		args = append(args, f.DateTo)
	}
	q := "SELECT id,title,category,author,time,report_date,stock_code FROM old_meta"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY report_date %s, time %s", dir(f.Sort), dir(f.Sort))
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		var id int64
		var title, cat, auth, tm, rd, sc sql.NullString
		if err := rows.Scan(&id, &title, &cat, &auth, &tm, &rd, &sc); err != nil {
			return nil, err
		}
		out = append(out, Rep{
			RID: fmt.Sprintf("o%d", id), Src: "old", Title: title.String, Symbol: sc.String,
			RType: cat.String, Date: rd.String, Source: auth.String, Time: tm.String,
		})
	}
	return out, rows.Err()
}

// ---------- 入口按钮 ----------

func (s *Store) Links() []Link {
	rows, err := s.query("SELECT id,label,url,ord FROM links ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		rows.Scan(&l.ID, &l.Label, &l.URL, &l.Ord)
		out = append(out, l)
	}
	return out
}

func (s *Store) AddLink(label, url string, ord int) error {
	_, err := s.exec("INSERT INTO links(label,url,ord) VALUES(?,?,?)", label, url, ord)
	return err
}
func (s *Store) UpdateLink(id int64, label, url string, ord int) error {
	_, err := s.exec("UPDATE links SET label=?,url=?,ord=? WHERE id=?", label, url, ord, id)
	return err
}

// UpdateLinkFields 只改名称/URL，保留排序位（排序由拖拽管）。
func (s *Store) UpdateLinkFields(id int64, label, url string) error {
	_, err := s.exec("UPDATE links SET label=?,url=? WHERE id=?", label, url, id)
	return err
}

// SetLinkOrder 拖拽落库排序位。
func (s *Store) SetLinkOrder(id int64, ord int) error {
	_, err := s.exec("UPDATE links SET ord=? WHERE id=?", ord, id)
	return err
}
func (s *Store) DeleteLink(id int64) error {
	_, err := s.exec("DELETE FROM links WHERE id=?", id)
	return err
}

func (s *Store) distinct(q string) []string {
	rows, err := s.query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		out = append(out, v)
	}
	return out
}
