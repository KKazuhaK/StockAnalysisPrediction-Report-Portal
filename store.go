package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres 驱动，注册为 "pgx"
	_ "modernc.org/sqlite"             // sqlite 驱动(纯 Go)，注册为 "sqlite"
)

// Rep 是新旧统一的报告表示（列表/分组/阅读都用它）。
type Rep struct {
	RID, Src      string // RID: "n<rowid>" 新 / "o<id>" 旧
	UID           string // 新报告的稳定外部 id（upsert 用）
	Title, Symbol string
	RType, Date   string
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
		// 报告类型显示配置（管理员网页可改：tab 顺序/默认页/改名），为工作流变更留后路。
		`CREATE TABLE IF NOT EXISTS type_config(
			name TEXT PRIMARY KEY, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT)`,
		// 登录账号（config.yaml 仅首启种子，之后网页管理）。role 可扩展更多角色。
		`CREATE TABLE IF NOT EXISTS users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user')`,
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
	Name      string
	Ord       int
	IsSummary bool
	Label     string
}

func (s *Store) TypeConfigs() map[string]TypeConfig {
	m := map[string]TypeConfig{}
	rows, err := s.query("SELECT name,ord,is_summary,label FROM type_config")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var t TypeConfig
		var isum int
		var label sql.NullString
		rows.Scan(&t.Name, &t.Ord, &isum, &label)
		t.IsSummary = isum == 1
		t.Label = label.String
		m[t.Name] = t
	}
	return m
}

func (s *Store) UpsertTypeConfig(name string, ord int, isSummary bool, label string) error {
	is := 0
	if isSummary {
		is = 1
	}
	_, err := s.exec(`INSERT INTO type_config(name,ord,is_summary,label) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET ord=excluded.ord,is_summary=excluded.is_summary,label=excluded.label`,
		name, ord, is, label)
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
	q := "SELECT rowid,title,symbol,rtype,rdate,source,sent_at FROM reports"
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
		var id int64
		var title, sym, rt, rd, src, sent sql.NullString
		if err := rows.Scan(&id, &title, &sym, &rt, &rd, &src, &sent); err != nil {
			return nil, err
		}
		out = append(out, Rep{
			RID: fmt.Sprintf("n%d", id), Src: "new", Title: title.String, Symbol: sym.String,
			RType: rt.String, Date: rd.String, Source: src.String, Time: sent.String,
		})
	}
	return out, rows.Err()
}

func (s *Store) GetNew(rowid int64) (*Rep, error) {
	var title, sym, rt, rd, src, sent, md, html sql.NullString
	err := s.queryRow(
		"SELECT title,symbol,rtype,rdate,source,sent_at,body_md,body_html FROM reports WHERE rowid=?", rowid).
		Scan(&title, &sym, &rt, &rd, &src, &sent, &md, &html)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Rep{
		RID: fmt.Sprintf("n%d", rowid), Src: "new", Title: title.String, Symbol: sym.String,
		RType: rt.String, Date: rd.String, Source: src.String, Time: sent.String,
		MD: md.String, HTML: html.String,
	}, nil
}

func (s *Store) UpsertReport(r Rep) error {
	_, err := s.exec(`
		INSERT INTO reports(uid,title,symbol,rtype,rdate,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,
		  rtype=excluded.rtype,rdate=excluded.rdate,source=excluded.source,
		  sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		r.UID, r.Title, r.Symbol, r.RType, r.Date, r.Source, r.Time, r.MD, r.HTML)
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
