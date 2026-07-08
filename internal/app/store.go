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
	RID, Src      string // RID: "n<id>" new / "o<id>" old
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
	GroupID    int64  // the link group it belongs to, or 0 = ungrouped (top-level, shown inline)
	Ord        int
}

// LinkGroup is a named, foldable group of home-page entry buttons (replacing the old single
// "More"). Mode decides how it renders: "row" (its own always-visible row) | "expand"
// (inline reveal) | "popover" (floating) | "modal" (dialog). ShowLabel toggles whether the
// group name is shown (mainly for row mode; the folding modes always label their trigger).
type LinkGroup struct {
	ID        int64
	Name      string
	Mode      string
	ShowLabel bool
	Icon      string
	Ord       int
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
		return nil, fmt.Errorf("connect database (%s): %w", driver, err)
	}
	s := &Store{db: db, driver: driver}
	return s, s.init()
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

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

// insertID runs an INSERT and returns the new row's id, portable across sqlite
// (LastInsertId) and postgres (RETURNING id). The query must not already end with
// RETURNING; the id column must be named "id".
func (s *Store) insertID(q string, args ...any) (int64, error) {
	if s.driver == "postgres" {
		var id int64
		err := s.queryRow(q+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := s.exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
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

// blobType returns the column type for opaque binary content (SQLite BLOB vs Postgres BYTEA).
func (s *Store) blobType() string {
	if s.driver == "postgres" {
		return "BYTEA"
	}
	return "BLOB"
}

// likeOp returns the substring-match operator. Postgres LIKE is case-sensitive
// while SQLite's is not, so on Postgres we use ILIKE to keep name/keyword search
// case-insensitive (matches the SQLite behaviour users rely on).
func (s *Store) likeOp() string {
	if s.driver == "postgres" {
		return "ILIKE"
	}
	return "LIKE"
}

// groupConcatDistinct returns a driver-specific aggregate joining the distinct
// values of col with commas. SQLite has GROUP_CONCAT; Postgres has no such
// function and uses STRING_AGG instead.
func (s *Store) groupConcatDistinct(col string) string {
	if s.driver == "postgres" {
		return fmt.Sprintf("STRING_AGG(DISTINCT %s, ',' ORDER BY %s)", col, col)
	}
	return fmt.Sprintf("GROUP_CONCAT(DISTINCT %s)", col)
}

// init opens/upgrades the database: it lays down the current-generation base schema, runs any
// pending migration to bring an older database up to it, reconciles pure-additive columns, then
// guarantees the fallback group.
func (s *Store) init() error {
	if err := s.createBaseSchema(); err != nil {
		return err
	}
	if err := s.migrate(); err != nil {
		return err
	}
	// Additive columns need no versioned migration — they are auto-reconciled here (guarded, so a
	// no-op once present). Only data moves/drops go through migrate().
	if err := s.ensureColumns(); err != nil {
		return err
	}
	s.EnsureDefaultGroup() // group model B: guarantee the fallback group exists
	return nil
}

// baseSchemaStmts returns the full current-generation schema as CREATE statements — the single
// source of truth for the DB shape. createBaseSchema execs it on a fresh database; ensureColumns
// (migrate.go) reads the SAME statements to auto-add any column an older database lacks, so a new
// additive column is declared here ONCE and picked up everywhere without a hand-written migration.
// Per the squash contract (CLAUDE.md hard rules), this base schema equals the fully-migrated final
// state of the previous release line: the six former 1:1 side tables are folded into parent
// columns, the dead user_group_members table and links.collapsed column are gone, and the reports
// surrogate key is `id`. See docs/adr/0013-v2-schema-consolidation.md.
func (s *Store) baseSchemaStmts() []string {
	pk := s.pkAuto()
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS reports(
			id %s,
			uid TEXT UNIQUE, title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT,
			kind TEXT, run_id TEXT,
			source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_reports_date ON reports(rdate)`,
		`CREATE INDEX IF NOT EXISTS idx_reports_sym  ON reports(symbol)`,
		// Entry buttons. group_id: the link group it belongs to (0 = ungrouped/top-level, shown inline).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS links(
			id %s, label TEXT, url TEXT, icon TEXT DEFAULT '', new_tab INTEGER DEFAULT 1,
			ord INTEGER DEFAULT 0, group_id INTEGER DEFAULT 0)`, pk),
		// Named, foldable groups of entry buttons on the home page (mode: row/expand/popover/modal).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS link_groups(
			id %s, name TEXT DEFAULT '', mode TEXT DEFAULT 'row', show_label INTEGER DEFAULT 1, icon TEXT DEFAULT '', ord INTEGER DEFAULT 0)`, pk),
		`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT)`,
		// Report type registry: subtype (name, unique) → explicit category (kind) + display name/order/default page.
		// Auto-registered on ingest, editable in the admin backend; replaces runKind guessing (runKind only serves as the fallback default for new types).
		`CREATE TABLE IF NOT EXISTS type_config(
			name TEXT PRIMARY KEY, kind TEXT, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT)`,
		// Admin-configurable antd Tag preset color per top-level kind (大类), replacing a
		// previously-hardcoded frontend map. Kinds with no row here fall back to "default" client-side.
		`CREATE TABLE IF NOT EXISTS kind_config(kind TEXT PRIMARY KEY, color TEXT)`,
		// Login accounts (config.yaml only seeds the first admin on first startup; managed via the
		// web UI afterwards). role can be extended with more roles. Extended profile attributes
		// (display_name/email/active/last_login) and the single primary group_id (NULL = the Default
		// group) are columns here, folded from the former user_profiles + user_primary_group side
		// tables (docs/adr/0013-v2-schema-consolidation.md). active defaults to 1 (enabled).
		`CREATE TABLE IF NOT EXISTS users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user',
			display_name TEXT, email TEXT, active INTEGER DEFAULT 1, last_login TEXT, group_id BIGINT)`,
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
		// Batch-run feature (see docs/adr/0001-batch-run-engine.md). Plugins are
		// declarative manifests; a target is a configured instance; a job fans a
		// target over many input rows with per-row state persisted for resume.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS plugins(
			id %s, slug TEXT UNIQUE, name TEXT, version TEXT, spec TEXT,
			enabled INTEGER DEFAULT 1, source TEXT DEFAULT 'imported', imported_at TEXT)`, pk),
		// batch_targets. ord: admin drag-to-sort display position (folded from the former
		// target_order side table; NULL = unordered, sorts after ordered ones, newest-first).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS batch_targets(
			id %s, plugin_slug TEXT, name TEXT, config TEXT, created_at TEXT, ord INTEGER)`, pk),
		// batch_jobs. priority (run-queue level, folded from the former job_queue side table;
		// default 'normal') and run_at (one-shot scheduled start, folded from job_schedule;
		// default '' = run ASAP) — see docs/adr/0013-v2-schema-consolidation.md. run_preset (default
		// '' = none) is a JSON snapshot of a chosen preset low-peak window (rule + on_overrun +
		// the occurrence end) so a run stays in its window and rolls/continues/cancels if it closes
		// before starting — see docs/adr/0014-idle-lane-and-preset-windows.md; picked up on existing
		// databases by ensureColumns (no migration step).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS batch_jobs(
			id %s, target_id BIGINT, status TEXT, concurrency INTEGER DEFAULT 1, max_retries INTEGER DEFAULT 0,
			total INTEGER DEFAULT 0, succeeded INTEGER DEFAULT 0, partial INTEGER DEFAULT 0, failed INTEGER DEFAULT 0,
			created_by TEXT, created_at TEXT, started_at TEXT, finished_at TEXT,
			priority TEXT DEFAULT 'normal', run_at TEXT DEFAULT '', run_preset TEXT DEFAULT '')`, pk),
		// run_id / conversation_id / task_id are the Dify handles for a run, persisted the
		// instant they stream in (not just at finish) so a crash/restart mid-run can reconcile
		// the true outcome instead of re-running it — the restart-durable half of the
		// reconcile-not-retry money invariant (ADR 0006). They co-exist and are independent:
		// a workflow/chatflow run has run_id (+task_id); a pure agent/basic chat has only
		// conversation_id (+task_id). conversation_id/task_id are pure-additive (nullable, no
		// backfill), so existing databases pick them up via ensureColumns — no migration step.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS batch_items(
			id %s, job_id BIGINT, row_index INTEGER, inputs TEXT, status TEXT DEFAULT 'queued',
			attempts INTEGER DEFAULT 0, run_id TEXT, conversation_id TEXT, task_id TEXT,
			error TEXT, started_at TEXT, finished_at TEXT)`, pk),
		// Preset low-peak scheduling windows (docs/adr/0014-idle-lane-and-preset-windows.md): an
		// admin-managed, ordered list a user picks from to schedule a run into a recurring window —
		// structurally like type_config/links (a table with ord), not a meta blob. start_spec/
		// stop_spec are JSON anchors ({weekday?,month?,day?,time:"HH:mm"}); the used fields depend
		// on freq (daily|weekly|monthly|yearly). on_overrun (continue|next|cancel) decides what
		// happens if the window closes before the run started. The job snapshots the rule, so this
		// row is never referenced by a job (no FK); id is a plain surrogate for CRUD/reorder/pick.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS run_presets(
			id %s, label TEXT, freq TEXT, start_spec TEXT, stop_spec TEXT,
			on_overrun TEXT DEFAULT 'next', enabled INTEGER DEFAULT 1, ord INTEGER DEFAULT 0)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_batch_items_job ON batch_items(job_id, status)`,
		// The batch console polls AllJobsFirstInputs (first row of every job) every 2s;
		// without this the WHERE row_index=0 lookup is a full scan of batch_items — the
		// fastest-growing table — and gets slow on a large job history.
		`CREATE INDEX IF NOT EXISTS idx_batch_items_row0 ON batch_items(row_index, job_id)`,
		// Run-queue priority (ADR 0004) and one-shot schedule run_at (ADR 0007) are now the
		// batch_jobs.priority / run_at columns above (folded from the former job_queue /
		// job_schedule side tables). The partial index over run_at is created by migrateV1toV2,
		// after the column is guaranteed to exist on an upgraded database.
		// Outbound event webhooks (extension point; see docs/adr/0002-extension-architecture.md).
		// events is a comma-separated subscription list; last_* columns give the admin delivery visibility.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS webhooks(
			id %s, url TEXT, events TEXT, secret TEXT, active INTEGER DEFAULT 1,
			created_at TEXT, last_status INTEGER DEFAULT 0, last_error TEXT, last_delivered_at TEXT)`, pk),
		// Downloadable iframe apps (see docs/adr/0003-downloadable-apps.md). An app is
		// a manifest (id/name/icon/version/entry/scopes) plus its self-contained
		// frontend files, both stored here so install needs no writable filesystem.
		// The host renders each app in a sandboxed iframe; it reaches /api/v1 only
		// through a scoped token over a postMessage bridge.
		`CREATE TABLE IF NOT EXISTS apps(
			id TEXT PRIMARY KEY, name TEXT, icon TEXT, version TEXT, entry TEXT,
			scopes TEXT, created_at TEXT)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS app_files(
			app_id TEXT, path TEXT, ctype TEXT, content %s, PRIMARY KEY(app_id, path))`, s.blobType()),
		// user_groups (organizational groups; docs/adr/0010-group-model.md). A user's single
		// primary group is users.group_id (NULL = the Default group) — there is no many-to-many
		// membership table. priority: the group's default run priority (folded from the former
		// group_priority side table; NULL = inherit the system default). weight / urgent_unlimited
		// are NULL-able on purpose: on a non-default group NULL means "inherit from the Default
		// group", a concrete value means "override"; the Default group (is_default=1) holds
		// concrete baselines. allow_urgent / max_queued / run_window are per-group governance:
		// NULL on a non-default group means "inherit the Default group"; the Default group's NULL
		// means the permissive baseline (urgent allowed, no queue cap, any hour).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS user_groups(
			id %s, name TEXT UNIQUE, description TEXT, created_at TEXT, weight INTEGER,
			urgent_unlimited INTEGER, is_default INTEGER DEFAULT 0,
			allow_urgent INTEGER, max_queued INTEGER, run_window TEXT, priority TEXT)`, pk),
		// Priority "次票": a per-user quota of 加急 runs, allocated by group weight and
		// refilled each period. State is lazy (no cron): a period rollover is detected
		// from period_start on access. See docs/adr/0005-priority-tickets.md.
		`CREATE TABLE IF NOT EXISTS priority_tickets(
			username TEXT PRIMARY KEY, remaining INTEGER DEFAULT 0, period_start TEXT)`,
		// Interactive chat/assistant conversations (docs/adr/0012-interactive-chat.md): a
		// THIN index only. Dify owns the messages (keyed by conv_id + user) and the whole
		// context/memory; this table just lets the portal list a user's conversations per
		// target and reopen them. No message content is stored here. conv_id is empty until
		// Dify assigns one on the first reply.
		// starred: pinned to the top of the conversation list.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chat_conversations(
			id %s, target_id BIGINT, conv_id TEXT DEFAULT '', created_by TEXT,
			title TEXT DEFAULT '', created_at TEXT, updated_at TEXT, starred INTEGER DEFAULT 0)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_chat_conv_user ON chat_conversations(created_by, target_id, updated_at)`,
	}
}

// createBaseSchema lays down the current-generation schema on a fresh database (CREATE ... IF NOT
// EXISTS, so existing tables are left untouched — migrate() folds older shapes up and
// ensureColumns tops up any missing additive column).
func (s *Store) createBaseSchema() error {
	for _, st := range s.baseSchemaStmts() {
		if _, err := s.exec(st); err != nil {
			return fmt.Errorf("create base schema: %w\nSQL: %s", err, st)
		}
	}
	return nil
}

// ---------- Accounts ----------

// userCols is the shared SELECT list for a user row. The COALESCEs keep the prior semantics
// now that the profile attributes are nullable columns on users (folded from user_profiles,
// ADR 0013): a NULL display_name/email/last_login reads as ” and a NULL active reads as 1.
const userCols = `u.username,u.password_hash,u.role,
	COALESCE(u.display_name,''),COALESCE(u.email,''),COALESCE(u.active,1),COALESCE(u.last_login,'')`

func scanUser(scan func(...any) error) (User, error) {
	var u User
	var role, dn, email, last sql.NullString
	var active sql.NullInt64
	if err := scan(&u.Username, &u.PasswordHash, &role, &dn, &email, &active, &last); err != nil {
		return User{}, err
	}
	u.Role, u.DisplayName, u.Email, u.LastLogin = role.String, dn.String, email.String, last.String
	u.Active = !active.Valid || active.Int64 != 0
	return u, nil
}

func (s *Store) Users() []User {
	rows, err := s.query("SELECT " + userCols + " FROM users u ORDER BY u.role, u.username")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			continue
		}
		out = append(out, u)
	}
	return out
}

func (s *Store) GetUser(name string) *User {
	u, err := scanUser(s.queryRow("SELECT "+userCols+" FROM users u WHERE u.username=?", name).Scan)
	if err != nil {
		return nil
	}
	return &u
}

// UserByEmail finds a user by their (case-insensitive, non-empty) profile email, for
// the "forgot password" lookup. Returns nil if none or the email is blank.
func (s *Store) UserByEmail(email string) *User {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	u, err := scanUser(s.queryRow("SELECT "+userCols+" FROM users u WHERE u.email IS NOT NULL AND u.email<>'' AND LOWER(u.email)=LOWER(?)", email).Scan)
	if err != nil {
		return nil
	}
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
	// Profile attributes and the primary group are columns on users now (ADR 0013), so they
	// vanish with the row — no side-table cleanup needed.
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

// ClearTypeConfigs removes every type configuration row, returning the page to
// its first-run state before defaults are re-seeded. Report data is untouched;
// a type that still has reports reappears as an unconfigured (discovered) entry.
func (s *Store) ClearTypeConfigs() error {
	_, err := s.exec("DELETE FROM type_config")
	return err
}

// KindColors returns the admin-configured antd Tag preset color for each top-level
// kind (大类), keyed by kind name. A kind absent from the map has no configured
// color; callers fall back to "default".
func (s *Store) KindColors() map[string]string {
	m := map[string]string{}
	rows, err := s.query("SELECT kind,color FROM kind_config")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var kind, color sql.NullString
		rows.Scan(&kind, &color)
		m[kind.String] = color.String
	}
	return m
}

// SetKindColor upserts the Tag color for one kind.
func (s *Store) SetKindColor(kind, color string) error {
	_, err := s.exec(`INSERT INTO kind_config(kind,color) VALUES(?,?)
		ON CONFLICT(kind) DO UPDATE SET color=excluded.color`, kind, color)
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
	for k := range s.TypeConfigs() {
		add(k)
	}
	return out
}

// Filters holds the filter conditions for lists/grouping.
type Filters struct {
	Q, Scope, Symbol, RType string
	Kind                    string // 大类 (top-level category) filter, matched against reports.kind
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
	op := s.likeOp()
	if f.Q != "" {
		// Match title, code, the as-of snapshot name, or the current name (via
		// the stocks join); full-text scope also scans the body.
		like := "%" + f.Q + "%"
		if f.Scope == "fulltext" {
			where = append(where, fmt.Sprintf("(r.title %[1]s ? OR r.symbol %[1]s ? OR r.name %[1]s ? OR s.name %[1]s ? OR r.body_md %[1]s ?)", op))
			args = append(args, like, like, like, like, like)
		} else {
			where = append(where, fmt.Sprintf("(r.title %[1]s ? OR r.symbol %[1]s ? OR r.name %[1]s ? OR s.name %[1]s ?)", op))
			args = append(args, like, like, like, like)
		}
	}
	if f.Symbol != "" {
		where = append(where, "r.symbol "+op+" ?")
		args = append(args, "%"+f.Symbol+"%")
	}
	if f.RType != "" {
		where = append(where, "r.rtype = ?")
		args = append(args, f.RType)
	}
	if f.Kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, f.Kind)
	}
	if f.DateFrom != "" {
		where = append(where, "r.rdate >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "r.rdate <= ?")
		args = append(args, f.DateTo)
	}
	q := "SELECT r.id,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at FROM reports r LEFT JOIN stocks s ON s.code = r.symbol"
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

// scanNewRow scans one new-report row (without body). Fixed column order: id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at.
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
		} else {
			k = foldKind(k)
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
	sort.SliceStable(dates, func(i, j int) bool { return dates[i].Date > dates[j].Date }) // newest first
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
// ReportQuery is the filter for QueryReports (Dify /api/reports search).
type ReportQuery struct {
	Symbol, Q, Kind, RType, Source, RunID, Since, Until string
	Limit, Offset                                       int
	WithBody                                            bool
}

// QueryReports searches new reports and returns the page plus the TOTAL match
// count (for pagination). Keyword q matches title, code, current name, or body.
func (s *Store) QueryReports(f ReportQuery) ([]Rep, int, error) {
	var where []string
	var args []any
	if f.Symbol != "" {
		where = append(where, "r.symbol=?")
		args = append(args, f.Symbol)
	}
	if f.Q != "" {
		like := "%" + f.Q + "%"
		where = append(where, fmt.Sprintf("(r.title %[1]s ? OR r.symbol %[1]s ? OR s.name %[1]s ? OR r.body_md %[1]s ?)", s.likeOp()))
		args = append(args, like, like, like, like)
	}
	if f.Kind != "" {
		where = append(where, "r.kind=?")
		args = append(args, f.Kind)
	}
	if f.RType != "" {
		where = append(where, "r.rtype=?")
		args = append(args, f.RType)
	}
	if f.Source != "" {
		where = append(where, "r.source=?")
		args = append(args, f.Source)
	}
	if f.RunID != "" {
		where = append(where, "r.run_id=?")
		args = append(args, f.RunID)
	}
	if f.Since != "" {
		where = append(where, "r.rdate>=?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where = append(where, "r.rdate<=?")
		args = append(args, f.Until)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	whereClause := "1=1"
	if len(where) > 0 {
		whereClause = strings.Join(where, " AND ")
	}
	from := "FROM reports r LEFT JOIN stocks s ON s.code = r.symbol WHERE " + whereClause
	var total int
	s.queryRow("SELECT COUNT(*) "+from, args...).Scan(&total)
	sqlStr := fmt.Sprintf(`SELECT r.id,r.uid,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at,r.body_md
		%s ORDER BY r.rdate DESC, r.sent_at DESC LIMIT %d OFFSET %d`, from, limit, offset)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, total, err
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
		if f.WithBody {
			r.MD = md.String
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetByUID fetches a single new report by uid (with body).
func (s *Store) GetByUID(uid string) *Rep {
	var id int64
	var title, sym, name, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(`SELECT id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html
		FROM reports WHERE uid=?`, uid).Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err != nil {
		return nil
	}
	return &Rep{RID: fmt.Sprintf("n%d", id), Src: "new", UID: uid, Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String}
}

// TrackingItem is a structured assumption/tracking item. ReportUID is the internal
// composite key of the parent report; ReportRID is that report's numeric id (rid),
// exposed by the v1 API.
type TrackingItem struct {
	ID                                                                         int64
	ReportUID, ReportRID, Symbol, IType, Content, Status, ReviewPoint, Created string
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
	where := []string{"t.symbol=?"}
	args := []any{symbol}
	if status != "" {
		where = append(where, "t.status=?")
		args = append(args, status)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Join the parent report to expose its numeric id (rid). reports.uid is
	// UNIQUE-indexed, so this is one index seek per row (the result is LIMIT-capped).
	rows, err := s.query(fmt.Sprintf(`SELECT t.id,t.report_uid,r.id,t.symbol,t.itype,t.content,t.status,t.review_point,t.created_at
		FROM tracking_items t LEFT JOIN reports r ON r.uid=t.report_uid
		WHERE %s ORDER BY t.created_at DESC, t.id DESC LIMIT %d`, strings.Join(where, " AND "), limit), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TrackingItem
	for rows.Next() {
		var t TrackingItem
		var uid, sym, it, c, st, rp, cr sql.NullString
		var rowid sql.NullInt64
		rows.Scan(&t.ID, &uid, &rowid, &sym, &it, &c, &st, &rp, &cr)
		t.ReportUID, t.Symbol, t.IType, t.Content, t.Status, t.ReviewPoint, t.Created =
			uid.String, sym.String, it.String, c.String, st.String, rp.String, cr.String
		if rowid.Valid {
			t.ReportRID = fmt.Sprintf("n%d", rowid.Int64)
		}
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
		stmt.Exec(code, cleanName(name), now)
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
		where += fmt.Sprintf(" AND (t.sym %[1]s ? OR s.name %[1]s ?)", s.likeOp())
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// Aggregate report counts per symbol from the unified reports table (legacy
	// reports were migrated in), then resolve the display name from stocks.
	rows, err := s.query(fmt.Sprintf(`SELECT t.sym, s.name, SUM(t.cnt) AS c, MAX(t.latest) AS latest
		FROM (
			SELECT symbol AS sym, COUNT(*) AS cnt, MAX(rdate) AS latest FROM reports GROUP BY symbol
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
		%s, COUNT(*) FROM reports WHERE %s
		GROUP BY symbol,rdate,kind ORDER BY rdate DESC, kind`, s.groupConcatDistinct("rtype"), strings.Join(where, " AND ")), args...)
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
	rows, err := s.query(`SELECT id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at
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
	var uid, title, sym, name, rt, rd, kind, runID, src, sent, md, html sql.NullString
	err := s.queryRow(
		"SELECT uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html FROM reports WHERE id=?", rowid).
		Scan(&uid, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md, &html)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Rep{
		RID: fmt.Sprintf("n%d", rowid), Src: "new", UID: uid.String, Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String,
	}, nil
}

// UpsertReport inserts or overwrites a report by uid. Returns created=true when a
// new row was inserted, false when an existing row was overwritten.
func (s *Store) UpsertReport(r Rep) (bool, error) {
	var x int
	created := s.queryRow("SELECT 1 FROM reports WHERE uid=?", r.UID).Scan(&x) == sql.ErrNoRows
	_, err := s.exec(`
		INSERT INTO reports(uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,name=excluded.name,
		  rtype=excluded.rtype,rdate=excluded.rdate,kind=excluded.kind,run_id=excluded.run_id,
		  source=excluded.source,sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		r.UID, r.Title, r.Symbol, r.Name, r.RType, r.Date, r.Kind, r.RunID, r.Source, r.Time, r.MD, r.HTML)
	return created, err
}

// DeleteReport removes a report and its tracking items by uid (one tx). Returns
// the number of report rows deleted (0 = no match; safe to retry).
func (s *Store) DeleteReport(uid string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(s.bind("DELETE FROM reports WHERE uid=?"), uid)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if _, err := tx.Exec(s.bind("DELETE FROM tracking_items WHERE report_uid=?"), uid); err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// UpdateTrackingStatus updates a single tracking item's status and/or review_point
// by id (the hypothesis re-check loop). Empty fields are left unchanged. Returns
// ok=false when no row matched the id.
func (s *Store) UpdateTrackingStatus(id int64, status, reviewPoint string) (bool, error) {
	var sets []string
	var args []any
	if status != "" {
		sets = append(sets, "status=?")
		args = append(args, status)
	}
	if reviewPoint != "" {
		sets = append(sets, "review_point=?")
		args = append(args, reviewPoint)
	}
	if len(sets) == 0 {
		return false, nil
	}
	args = append(args, id)
	res, err := s.exec("UPDATE tracking_items SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) CountNew() (n int) {
	s.queryRow("SELECT COUNT(*) FROM reports").Scan(&n)
	return
}

// RecomputeKinds re-derives every report's top-level kind from its subtype using
// the current rules — the admin-editable 类型管理 mapping (TypeKind) first, then
// runKind, then folded into the canonical buckets — and updates rows that changed
// (returns how many). This is the "重新分类" action: re-apply the subtype→大类 table
// to all stored reports.
func (s *Store) RecomputeKinds() (int, error) {
	// Load the subtype→大类 map once up front; querying it inside the open rows
	// loop would deadlock the single-connection SQLite pool.
	cfg := s.TypeConfigs()
	rows, err := s.query("SELECT id, rtype, kind FROM reports")
	if err != nil {
		return 0, err
	}
	type upd struct {
		rowid int64
		kind  string
	}
	var ups []upd
	for rows.Next() {
		var rowid int64
		var rtype, kind sql.NullString
		if err := rows.Scan(&rowid, &rtype, &kind); err != nil {
			rows.Close()
			return 0, err
		}
		nk := ""
		if c, ok := cfg[rtype.String]; ok {
			nk = c.Kind
		}
		if nk == "" {
			nk = runKind([]string{rtype.String})
		}
		nk = foldKind(nk)
		if nk != kind.String {
			ups = append(ups, upd{rowid, nk})
		}
	}
	rows.Close()
	for _, u := range ups {
		if _, err := s.exec("UPDATE reports SET kind=? WHERE id=?", u.kind, u.rowid); err != nil {
			return 0, err
		}
	}
	return len(ups), nil
}

func (s *Store) NewTypes() []string {
	return s.distinct("SELECT DISTINCT rtype FROM reports WHERE rtype<>'' ORDER BY rtype")
}

// ReportKinds returns the distinct 大类 (top-level categories) present across
// reports — used to populate the home 大类 filter.
func (s *Store) ReportKinds() []string {
	return s.distinct("SELECT DISTINCT kind FROM reports WHERE kind<>'' ORDER BY kind")
}

// FreezeReportNames snapshots the current stocks-cache name onto each report that has no
// frozen name yet (legacy imports / pre-snapshot ingests). Afterwards a report's displayed
// name comes solely from its own row, so a later rename never rewrites earlier reports.
// Idempotent; leaves already-named rows and unknown symbols untouched. Returns rows frozen.
func (s *Store) FreezeReportNames() (int64, error) {
	res, err := s.exec("UPDATE reports SET name = (SELECT s.name FROM stocks s WHERE s.code = reports.symbol) " +
		"WHERE (name IS NULL OR name = '') " +
		"AND EXISTS (SELECT 1 FROM stocks s WHERE s.code = reports.symbol AND s.name IS NOT NULL AND s.name <> '')")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- Entry buttons ----------

func (s *Store) Links() []Link {
	rows, err := s.query("SELECT id,label,url,icon,new_tab,ord,COALESCE(group_id,0) FROM links ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var icon sql.NullString
		var newTab sql.NullInt64
		rows.Scan(&l.ID, &l.Label, &l.URL, &icon, &newTab, &l.Ord, &l.GroupID)
		l.Icon = icon.String
		l.NewTab = !newTab.Valid || newTab.Int64 != 0 // default: open in new tab
		out = append(out, l)
	}
	return out
}

func (s *Store) AddLink(label, url, icon string, newTab bool, groupID int64, ord int) error {
	_, err := s.exec("INSERT INTO links(label,url,icon,new_tab,ord,group_id) VALUES(?,?,?,?,?,?)", label, url, icon, boolInt(newTab), ord, groupID)
	return err
}

// UpdateLinkFields changes the label/URL/icon/newTab, preserving position + group (both are
// handled by the layout drag, not the edit form).
func (s *Store) UpdateLinkFields(id int64, label, url, icon string, newTab bool) error {
	_, err := s.exec("UPDATE links SET label=?,url=?,icon=?,new_tab=? WHERE id=?", label, url, icon, boolInt(newTab), id)
	return err
}

// SetLinkGroupAndOrder persists a link's group membership + sort position on drag.
func (s *Store) SetLinkGroupAndOrder(id, groupID int64, ord int) error {
	_, err := s.exec("UPDATE links SET group_id=?,ord=? WHERE id=?", groupID, ord, id)
	return err
}
func (s *Store) DeleteLink(id int64) error {
	_, err := s.exec("DELETE FROM links WHERE id=?", id)
	return err
}

// ---------- Entry-button groups ----------

func (s *Store) LinkGroups() []LinkGroup {
	rows, err := s.query("SELECT id,COALESCE(name,''),COALESCE(mode,'row'),COALESCE(show_label,1),COALESCE(icon,''),ord FROM link_groups ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LinkGroup
	for rows.Next() {
		var g LinkGroup
		var showLabel sql.NullInt64
		rows.Scan(&g.ID, &g.Name, &g.Mode, &showLabel, &g.Icon, &g.Ord)
		g.ShowLabel = !showLabel.Valid || showLabel.Int64 != 0
		out = append(out, g)
	}
	return out
}

func (s *Store) AddLinkGroup(name, mode string, showLabel bool, icon string, ord int) (int64, error) {
	return s.insertID("INSERT INTO link_groups(name,mode,show_label,icon,ord) VALUES(?,?,?,?,?)", name, mode, boolInt(showLabel), icon, ord)
}

func (s *Store) UpdateLinkGroup(id int64, name, mode string, showLabel bool, icon string) error {
	_, err := s.exec("UPDATE link_groups SET name=?,mode=?,show_label=?,icon=? WHERE id=?", name, mode, boolInt(showLabel), icon, id)
	return err
}

// SetLinkGroupOrder persists a group's sort position on drag.
func (s *Store) SetLinkGroupOrder(id int64, ord int) error {
	_, err := s.exec("UPDATE link_groups SET ord=? WHERE id=?", ord, id)
	return err
}

// DeleteLinkGroup removes a group and returns its links to the top level (ungrouped).
func (s *Store) DeleteLinkGroup(id int64) error {
	s.exec("UPDATE links SET group_id=0 WHERE group_id=?", id)
	_, err := s.exec("DELETE FROM link_groups WHERE id=?", id)
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
