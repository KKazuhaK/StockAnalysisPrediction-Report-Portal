package app

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver, registered as "pgx"
	_ "modernc.org/sqlite"             // sqlite driver (pure Go), registered as "sqlite"
)

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

// boolInt maps a bool to the 0/1 integer stored in SQLite/Postgres.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Rep is the unified representation for both new and old reports (used for lists/grouping/reading).
type Rep struct {
	RID, Src      string // RID: "n<rowid>" new / "o<id>" old
	UID           string // stable external id of a new report (used for upsert)
	Title, Symbol string
	Name          string // company name snapshotted at ingest (backdoor-listing / rename safe)
	RType, Date   string
	Kind, RunID   string // Kind: category (重组决策/投资决策…, used by new reports); RunID: one generation group
	Source, Time  string
	HTML, MD      string // body (only filled when reading)
	Label         string // short tab label within a run
}

// Link is an entry button.
type Link struct {
	ID         int64
	Label, URL string
	Icon       string // icon name chosen in the admin UI (empty = default link glyph)
	NewTab     bool   // open in a new browser tab (default true)
	Ord        int
}

type Store struct {
	db     *sql.DB
	driver string // "sqlite" | "postgres"
}

// OpenStore opens the database using the given driver. driver: "sqlite" (default) or "postgres";
// source: sqlite=file path, postgres=DSN(postgres://user:pass@host/db?sslmode=disable).
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
		db.SetMaxOpenConns(1) // SQLite: single writer, avoids lock contention
	} else {
		db.SetMaxOpenConns(10)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("连接数据库(%s)失败: %w", driver, err)
	}
	s := &Store{db: db, driver: driver}
	return s, s.init()
}

// bind rewrites ? placeholders according to the driver (postgres uses $1,$2…).
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
func (s *Store) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.bind(q), args...)
}
func (s *Store) queryRow(q string, args ...any) *sql.Row { return s.db.QueryRow(s.bind(q), args...) }

// pkAuto returns the auto-increment primary key definition (differs between the two SQL dialects).
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
			uid TEXT UNIQUE, title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT,
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
			id %s, label TEXT, url TEXT, icon TEXT DEFAULT '', new_tab INTEGER DEFAULT 1, ord INTEGER DEFAULT 0)`, pk),
		`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT)`,
		// Report type registry: subtype (name, unique) → explicit category (kind) + display name/order/default page.
		// Auto-registered on ingest, editable in the admin backend; replaces runKind guessing (runKind only serves as the fallback default for new types).
		`CREATE TABLE IF NOT EXISTS type_config(
			name TEXT PRIMARY KEY, kind TEXT, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT)`,
		// Login accounts (config.yaml only seeds on first startup, managed via the web UI afterwards). role can be extended with more roles.
		`CREATE TABLE IF NOT EXISTS users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user')`,
		// API tokens (multiple, with note/scope/validity period/last used). scope: all|ingest|query.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS api_tokens(
			id %s, token TEXT UNIQUE, name TEXT, scope TEXT DEFAULT 'all',
			created_at TEXT, expires_at TEXT, last_used_at TEXT)`, pk),
		// Structured "assumption/tracking items" for re-run review (common across report types). itype: assumption|tracking.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS tracking_items(
			id %s, report_uid TEXT, symbol TEXT, itype TEXT, content TEXT,
			status TEXT DEFAULT 'pending', review_point TEXT, created_at TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_track_sym ON tracking_items(symbol, status)`,
		`CREATE INDEX IF NOT EXISTS idx_track_uid ON tracking_items(report_uid)`,
		// Stock code → name (enables searching by name after ingest; sourced from eastmoney, synced on startup/fetchnames).
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

// ---------- Accounts ----------

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

// ---------- System settings (stored in the meta table, editable via the web UI) ----------

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

// ---------- Report type configuration (editable by admins) ----------

type TypeConfig struct {
	Name      string // subtype name (unique)
	Kind      string // owning category (explicitly registered)
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

// TypeKind looks up the category a subtype belongs to (empty if not in the registry; callers fall back to runKind).
func (s *Store) TypeKind(name string) string {
	var kind sql.NullString
	s.queryRow("SELECT kind FROM type_config WHERE name=?", name).Scan(&kind)
	return kind.String
}

// RegisterType auto-registers a new subtype on ingest (left untouched if it already exists, preserving admin settings).
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

// SetReportsKind propagates a subtype's category change to already-ingested reports (keeping the snapshot consistent with the registry).
func (s *Store) SetReportsKind(name, kind string) error {
	_, err := s.exec("UPDATE reports SET kind=? WHERE rtype=?", kind, name)
	return err
}

// SetTypeOrder updates only the sort position (persisted on drag), preserving kind/is_summary/label; unconfigured types get a row created automatically.
func (s *Store) SetTypeOrder(name string, ord int) error {
	_, err := s.exec(`INSERT INTO type_config(name,kind,ord,is_summary,label) VALUES(?,'',?,0,'')
		ON CONFLICT(name) DO UPDATE SET ord=excluded.ord`, name, ord)
	return err
}

// DeleteTypeConfig deletes a type configuration. If the type still has reports, it just reverts to "unconfigured" (still appears in the data);
// if it was manually pre-registered with no matching reports, it disappears entirely after deletion.
func (s *Store) DeleteTypeConfig(name string) error {
	_, err := s.exec("DELETE FROM type_config WHERE name=?", name)
	return err
}

// DiscoveredTypes returns all types that have appeared in the data (new + old) merged with the configured ones.
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

// Filters holds the filter conditions for lists/grouping.
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

// ---------- New reports ----------

// SearchNew returns matching new reports (without body).
func (s *Store) SearchNew(f Filters) ([]Rep, error) {
	var where []string
	var args []any
	if f.Q != "" {
		// Match title, code, the as-of snapshot name, or the current name (via
		// the stocks join); full-text scope also scans the body.
		like := "%" + f.Q + "%"
		if f.Scope == "fulltext" {
			where = append(where, "(r.title LIKE ? OR r.symbol LIKE ? OR r.name LIKE ? OR s.name LIKE ? OR r.body_md LIKE ?)")
			args = append(args, like, like, like, like, like)
		} else {
			where = append(where, "(r.title LIKE ? OR r.symbol LIKE ? OR r.name LIKE ? OR s.name LIKE ?)")
			args = append(args, like, like, like, like)
		}
	}
	if f.Symbol != "" {
		where = append(where, "r.symbol LIKE ?")
		args = append(args, "%"+f.Symbol+"%")
	}
	if f.RType != "" {
		where = append(where, "r.rtype = ?")
		args = append(args, f.RType)
	}
	if f.DateFrom != "" {
		where = append(where, "r.rdate >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "r.rdate <= ?")
		args = append(args, f.DateTo)
	}
	q := "SELECT r.rowid,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at FROM reports r LEFT JOIN stocks s ON s.code = r.symbol"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY r.rdate %s, r.sent_at %s", dir(f.Sort), dir(f.Sort))
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

// scanNewRow scans one new-report row (without body). Fixed column order: rowid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at.
func scanNewRow(rows *sql.Rows) Rep {
	var id int64
	var title, sym, name, rt, rd, kind, runID, src, sent sql.NullString
	rows.Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent)
	return Rep{
		RID: fmt.Sprintf("n%d", id), Src: "new", Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String,
	}
}

// ApiToken is a single API token (multiple coexist, with note/scope/validity period).
type ApiToken struct {
	ID                                             int64
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

// TokenValid validates a token: exists, not expired, scope matches (all or equal to need). Refreshes last_used on success.
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
		return false // expired
	}
	if need != "" && scope.String != "all" && scope.String != need {
		return false // scope does not cover this operation
	}
	s.exec("UPDATE api_tokens SET last_used_at=? WHERE token=?", nowStr(), token)
	return true
}

// Manifest builds a "what reports exist" listing for a given symbol (so Dify can probe before fetching): total count, each date (with categories), and all categories/subtypes.
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

// QueryReports lets Dify query historical new reports by code/keyword/category/subtype/date range (date descending). symbol may be empty (searches the whole database). withBody includes body_md.
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
	sqlStr := fmt.Sprintf(`SELECT rowid,uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md
		FROM reports WHERE %s ORDER BY rdate DESC, sent_at DESC LIMIT %d`, whereClause, limit)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		var id int64
		var uid, title, sym, name, rt, rd, kind, runID, src, sent, md sql.NullString
		rows.Scan(&id, &uid, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md)
		r := Rep{RID: fmt.Sprintf("n%d", id), Src: "new", UID: uid.String, Title: title.String,
			Symbol: sym.String, Name: name.String, RType: rt.String, Date: rd.String, Kind: kind.String,
			RunID: runID.String, Source: src.String, Time: sent.String}
		if withBody {
			r.MD = md.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetByUID fetches a single new report by uid (with body).
func (s *Store) GetByUID(uid string) *Rep {
	var id int64
	var title, sym, name, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(`SELECT rowid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html
		FROM reports WHERE uid=?`, uid).Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err != nil {
		return nil
	}
	return &Rep{RID: fmt.Sprintf("n%d", id), Src: "new", UID: uid, Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String}
}

// TrackingItem is a structured assumption/tracking item.
type TrackingItem struct {
	ID                                                              int64
	ReportUID, Symbol, IType, Content, Status, ReviewPoint, Created string
}

// SetTracking overwrites a report's tracking items (on re-run, clears then writes to stay consistent with the latest body).
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

// QueryTracking queries a symbol's assumption/tracking items (optionally filtered by status, newest first by default).
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

// SymbolInfo is an overview of a stock that has reports.
type SymbolInfo struct {
	Symbol, Name, Latest string
	Count                int
}

// SyncStocks batch-upserts stock code → name (enables searching by name; sourced from eastmoney).
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

// AllStockNames reads all code → name entries from the stocks table (merged into the in-memory map at startup, so fetched fallback names survive restarts).
func (s *Store) AllStockNames() map[string]string {
	m := map[string]string{}
	rows, err := s.query("SELECT code,name FROM stocks WHERE name IS NOT NULL AND name!=''")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var c, n sql.NullString
		rows.Scan(&c, &n)
		if c.String != "" {
			m[c.String] = n.String
		}
	}
	return m
}

// StockName looks up a single stock's name (empty if not in the DB).
func (s *Store) StockName(code string) string {
	var name sql.NullString
	s.queryRow("SELECT name FROM stocks WHERE code=?", code).Scan(&name)
	return name.String
}

// ListSymbols lists stocks that have reports (q matches code or name, empty means all), ordered by report count descending.
func (s *Store) ListSymbols(q string, limit int) []SymbolInfo {
	// Only real stocks (skip reports with no code — those aren't a symbol).
	where := "WHERE t.sym != ''"
	var args []any
	if q != "" {
		// Match the stock code OR its current name (from the stocks table), so a
		// name fragment or a code fragment both work — even for legacy reports,
		// whose titles carry only the code.
		where += " AND (t.sym LIKE ? OR s.name LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// Aggregate report counts per symbol across both the new and legacy tables,
	// then resolve the display name from the stocks table.
	rows, err := s.query(fmt.Sprintf(`SELECT t.sym, s.name, SUM(t.cnt) AS c, MAX(t.latest) AS latest
		FROM (
			SELECT symbol AS sym, COUNT(*) AS cnt, MAX(rdate) AS latest FROM reports GROUP BY symbol
			UNION ALL
			SELECT stock_code AS sym, COUNT(*) AS cnt, MAX(report_date) AS latest FROM old_meta GROUP BY stock_code
		) t LEFT JOIN stocks s ON s.code = t.sym
		%s
		GROUP BY t.sym, s.name ORDER BY c DESC, t.sym LIMIT %d`, where, limit), args...)
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

// RunInfo is an overview of a report group (one generation = same symbol+date+kind).
type RunInfo struct {
	Symbol, Date, Kind, RunID string
	Subtypes                  []string
	Count                     int
}

// ListRuns lists a symbol's report groups (optionally for a specific day), ordered by date descending.
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

// NewBySymbol fetches all new reports for a symbol (without body, date descending), for the per-stock timeline detail view.
func (s *Store) NewBySymbol(symbol string) ([]Rep, error) {
	rows, err := s.query(`SELECT rowid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at
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
	var title, sym, name, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(
		"SELECT title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html FROM reports WHERE rowid=?", rowid).
		Scan(&title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Rep{
		RID: fmt.Sprintf("n%d", rowid), Src: "new", Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String,
	}, nil
}

func (s *Store) UpsertReport(r Rep) error {
	_, err := s.exec(`
		INSERT INTO reports(uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,name=excluded.name,
		  rtype=excluded.rtype,rdate=excluded.rdate,kind=excluded.kind,run_id=excluded.run_id,
		  source=excluded.source,sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		r.UID, r.Title, r.Symbol, r.Name, r.RType, r.Date, r.Kind, r.RunID, r.Source, r.Time, r.MD, r.HTML)
	return err
}

func (s *Store) CountNew() (n int) {
	s.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	return
}

func (s *Store) NewTypes() []string {
	return s.distinct("SELECT DISTINCT rtype FROM reports WHERE rtype<>'' ORDER BY rtype")
}

// ---------- Local index of old-report metadata ----------

// OldRaw is a raw record from the old API /api/reports.
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

// SearchOldMeta returns matching old reports (normalized to Rep, body left empty).
func (s *Store) SearchOldMeta(f Filters) ([]Rep, error) {
	var where []string
	var args []any
	if f.Q != "" {
		// Match the report title, the stock code, or the stock's current name —
		// legacy titles carry only the code, so name search needs the join.
		where = append(where, "(o.title LIKE ? OR o.stock_code LIKE ? OR s.name LIKE ?)")
		args = append(args, "%"+f.Q+"%", "%"+f.Q+"%", "%"+f.Q+"%")
	}
	if f.Symbol != "" {
		where = append(where, "o.stock_code LIKE ?")
		args = append(args, "%"+f.Symbol+"%")
	}
	if f.RType != "" {
		where = append(where, "o.category = ?")
		args = append(args, f.RType)
	}
	if f.DateFrom != "" {
		where = append(where, "o.report_date >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "o.report_date <= ?")
		args = append(args, f.DateTo)
	}
	q := "SELECT o.id,o.title,o.category,o.author,o.time,o.report_date,o.stock_code FROM old_meta o LEFT JOIN stocks s ON s.code = o.stock_code"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY o.report_date %s, o.time %s", dir(f.Sort), dir(f.Sort))
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

// ResearchReports lists the symbol-less (topic / free-form Q&A) reports — deep
// research not tied to a fixed ticker — newest first, with optional title search
// and pagination. Returns the page and the total match count.
// (Legacy reports live in old_meta; new symbol-less reports can be folded in here later.)
func (s *Store) ResearchReports(q string, limit, offset int) ([]Rep, int) {
	where := "(stock_code IS NULL OR stock_code='')"
	var args []any
	if q != "" {
		where += " AND title LIKE ?"
		args = append(args, "%"+q+"%")
	}
	var total int
	s.queryRow("SELECT COUNT(*) FROM old_meta WHERE "+where, args...).Scan(&total)
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.query("SELECT id,title,category,author,time,report_date,stock_code FROM old_meta WHERE "+
		where+" ORDER BY report_date DESC, time DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, total
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		var id int64
		var title, cat, auth, tm, rd, sc sql.NullString
		rows.Scan(&id, &title, &cat, &auth, &tm, &rd, &sc)
		out = append(out, Rep{
			RID: fmt.Sprintf("o%d", id), Src: "old", Title: title.String, Symbol: sc.String,
			RType: cat.String, Date: rd.String, Source: auth.String, Time: tm.String,
		})
	}
	return out, total
}

// ---------- Entry buttons ----------

func (s *Store) Links() []Link {
	rows, err := s.query("SELECT id,label,url,icon,new_tab,ord FROM links ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var icon sql.NullString
		var newTab sql.NullInt64
		rows.Scan(&l.ID, &l.Label, &l.URL, &icon, &newTab, &l.Ord)
		l.Icon = icon.String
		l.NewTab = !newTab.Valid || newTab.Int64 != 0 // default: open in new tab
		out = append(out, l)
	}
	return out
}

func (s *Store) AddLink(label, url, icon string, newTab bool, ord int) error {
	_, err := s.exec("INSERT INTO links(label,url,icon,new_tab,ord) VALUES(?,?,?,?,?)", label, url, icon, boolInt(newTab), ord)
	return err
}

// UpdateLinkFields changes the label/URL/icon/newTab, preserving the sort position (ordering is handled by drag).
func (s *Store) UpdateLinkFields(id int64, label, url, icon string, newTab bool) error {
	_, err := s.exec("UPDATE links SET label=?,url=?,icon=?,new_tab=? WHERE id=?", label, url, icon, boolInt(newTab), id)
	return err
}

// SetLinkOrder persists the sort position on drag.
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
