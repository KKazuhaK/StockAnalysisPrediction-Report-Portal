package main

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
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

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite：单写者，避免锁竞争
	s := &Store{db: db}
	return s, s.init()
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS reports(
		rowid INTEGER PRIMARY KEY AUTOINCREMENT,
		uid TEXT UNIQUE, title TEXT, symbol TEXT, rtype TEXT, rdate TEXT,
		source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_reports_date ON reports(rdate);
	CREATE INDEX IF NOT EXISTS idx_reports_sym  ON reports(symbol);
	CREATE TABLE IF NOT EXISTS old_meta(
		id INTEGER PRIMARY KEY, title TEXT, category TEXT, author TEXT,
		time TEXT, report_date TEXT, stock_code TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_old_date ON old_meta(report_date);
	CREATE INDEX IF NOT EXISTS idx_old_sym  ON old_meta(stock_code);
	CREATE TABLE IF NOT EXISTS links(
		id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, url TEXT, ord INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);
	-- 报告类型显示配置（管理员网页可改：tab 顺序 + 哪个是"汇总/默认页" + 改名）。
	-- 为工作流变更留后路：新类型自动出现，管理员排序/指定默认，无需改代码。
	CREATE TABLE IF NOT EXISTS type_config(
		name TEXT PRIMARY KEY, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT
	);
	-- 登录账号（config.yaml 仅首启种子，之后网页管理）。
	CREATE TABLE IF NOT EXISTS users(
		username TEXT PRIMARY KEY, password_hash TEXT, is_admin INTEGER DEFAULT 0
	);
	`)
	return err
}

// ---------- 账号 ----------

func (s *Store) Users() []User {
	rows, err := s.db.Query("SELECT username,password_hash,is_admin FROM users ORDER BY is_admin DESC, username")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var adm int
		rows.Scan(&u.Username, &u.PasswordHash, &adm)
		u.IsAdmin = adm == 1
		out = append(out, u)
	}
	return out
}

func (s *Store) GetUser(name string) *User {
	var u User
	var adm int
	err := s.db.QueryRow("SELECT username,password_hash,is_admin FROM users WHERE username=?", name).
		Scan(&u.Username, &u.PasswordHash, &adm)
	if err != nil {
		return nil
	}
	u.IsAdmin = adm == 1
	return &u
}

func (s *Store) UpsertUser(u User) error {
	adm := 0
	if u.IsAdmin {
		adm = 1
	}
	_, err := s.db.Exec(`INSERT INTO users(username,password_hash,is_admin) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash,is_admin=excluded.is_admin`,
		u.Username, u.PasswordHash, adm)
	return err
}

func (s *Store) SetUserPassword(name, hash string) error {
	_, err := s.db.Exec("UPDATE users SET password_hash=? WHERE username=?", hash, name)
	return err
}

func (s *Store) SetUserAdmin(name string, admin bool) error {
	adm := 0
	if admin {
		adm = 1
	}
	_, err := s.db.Exec("UPDATE users SET is_admin=? WHERE username=?", adm, name)
	return err
}

func (s *Store) DeleteUser(name string) error {
	_, err := s.db.Exec("DELETE FROM users WHERE username=?", name)
	return err
}

func (s *Store) CountUsers() (n int) {
	s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return
}

func (s *Store) CountAdmins() (n int) {
	s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin=1").Scan(&n)
	return
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
	rows, err := s.db.Query("SELECT name,ord,is_summary,label FROM type_config")
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
	_, err := s.db.Exec(`INSERT INTO type_config(name,ord,is_summary,label) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET ord=excluded.ord,is_summary=excluded.is_summary,label=excluded.label`,
		name, ord, is, label)
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
	rows, err := s.db.Query(q, args...)
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
	err := s.db.QueryRow(
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
	_, err := s.db.Exec(`
		INSERT INTO reports(uid,title,symbol,rtype,rdate,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,
		  rtype=excluded.rtype,rdate=excluded.rdate,source=excluded.source,
		  sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		r.UID, r.Title, r.Symbol, r.RType, r.Date, r.Source, r.Time, r.MD, r.HTML)
	return err
}

func (s *Store) CountNew() (n int) {
	s.db.QueryRow("SELECT COUNT(*) FROM reports").Scan(&n)
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
	st, err := tx.Prepare(`INSERT INTO old_meta(id,title,category,author,time,report_date,stock_code)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,category=excluded.category,
		author=excluded.author,time=excluded.time,report_date=excluded.report_date,stock_code=excluded.stock_code`)
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
	s.db.QueryRow("SELECT COUNT(*) FROM old_meta").Scan(&n)
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
	rows, err := s.db.Query(q, args...)
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
	rows, err := s.db.Query("SELECT id,label,url,ord FROM links ORDER BY ord,id")
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
	_, err := s.db.Exec("INSERT INTO links(label,url,ord) VALUES(?,?,?)", label, url, ord)
	return err
}
func (s *Store) UpdateLink(id int64, label, url string, ord int) error {
	_, err := s.db.Exec("UPDATE links SET label=?,url=?,ord=? WHERE id=?", label, url, ord, id)
	return err
}
func (s *Store) DeleteLink(id int64) error {
	_, err := s.db.Exec("DELETE FROM links WHERE id=?", id)
	return err
}

func (s *Store) distinct(q string) []string {
	rows, err := s.db.Query(q)
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
