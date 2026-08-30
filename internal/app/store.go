package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver, registered as "pgx"
	_ "modernc.org/sqlite"             // sqlite driver (pure Go), registered as "sqlite"
)

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

// newSessionEpoch is the starting session revision for a newly created account: the current time in
// microseconds, so no two account instances sharing a username can start at the same value. Not
// secret and not a nonce — it only has to differ from whatever the previous holder's cookies carry,
// and it stays well inside int64 for the next several thousand years.
func newSessionEpoch() int64 { return time.Now().UnixMicro() }

// boolInt maps a bool to the 0/1 integer stored in SQLite/Postgres.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Rep is the unified representation of a report (used for lists/grouping/reading).
type Rep struct {
	ID            int64 // the report's only identifier: reports.id, spoken by every API
	Title, Symbol string
	Name          string // company name snapshotted at ingest (backdoor-listing / rename safe)
	RType, Date   string
	Kind, RunID   string // Kind: category (重组决策/投资决策…, used by new reports); RunID: one generation group
	Source, Time  string
	HTML, MD      string // body (only filled when reading)
	Label         string // short tab label within a run
	Version       string // which written form this is (ADR 0024); part of the report's identity
}

// Link is an entry button.
type Link struct {
	ID         int64
	Label, URL string
	Icon       string // icon name chosen in the admin UI (empty = default link glyph)
	NewTab     bool   // open in a new browser tab (default true)
	GroupID    int64  // the link group it belongs to, or 0 = ungrouped (top-level, shown inline)
	Ord        int
	Visible    bool // shown on the home page (default true); hidden entries stay editable in admin
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
	Visible   bool // shown on the home page (default true); hidden groups stay editable in admin
}

type Store struct {
	db       *sql.DB
	driver   string     // "sqlite" | "postgres"
	ticketMu sync.Mutex // serializes SpendTicket's read-refill-decrement so a concurrent double-spend can't over-draw a user's urgent quota (ADR 0005)
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

// init opens the database, enforces the current release-line baseline, lays down the complete
// base schema, reconciles pure-additive columns, then guarantees the fallback group.
//
// The three schema steps are ordered, and the order is the whole point: tables, THEN columns,
// THEN indexes. An index may cover a column introduced after the table (idx_track_id over
// tracking_items.report_id), and CREATE TABLE IF NOT EXISTS is a no-op on a database that
// already has the table — so on every upgrade that column arrives from ensureColumns, not from
// the CREATE TABLE. Building indexes last means baseSchemaStmts can declare EVERY index next to
// its table without anyone having to know which release each column landed in.
func (s *Store) init() error {
	fresh, err := s.requireSchemaBaseline()
	if err != nil {
		return err
	}
	if err := s.createBaseTables(); err != nil {
		return err
	}
	// Additive columns need no versioned migration — they are auto-reconciled here (guarded, so a
	// no-op once present). A major-boundary release never carries old data-move/drop steps.
	if err := s.ensureColumns(); err != nil {
		return err
	}
	// Ordered, and the order is the whole safety argument (ADR 0024). The identity index gained a
	// `version` component, so every existing row must carry a value BEFORE the unique index is
	// rebuilt: NULLs compare distinct in a unique index on both drivers, so building it first would
	// admit exactly the duplicate rows the index exists to forbid.
	if err := s.reconcileReportVersions(); err != nil {
		return err
	}
	if err := s.createBaseIndexes(); err != nil {
		return err
	}
	// Release-line adoption steps, all of them, in one file that gets deleted at the next major
	// boundary. Runs after the schema exists (a step reads and writes real tables) and before
	// anything serves from it. See upgrade_v04.go.
	if err := s.upgradeV04(); err != nil {
		return err
	}
	if fresh {
		if err := s.setSchemaVersion(schemaBaseline); err != nil {
			return err
		}
	}
	s.EnsureDefaultGroup() // group model B: guarantee the fallback group exists
	return nil
}

// reportIdentExpr is the column tuple that identifies a report: stock code + civil date +
// subtype + title + version. It is written ONCE and shared by the unique index and UpsertReport's
// ON CONFLICT target — those two must match exactly for conflict inference to resolve, so
// they must never be edited apart.
//
// version (ADR 0024) joins the tuple because two written forms of one analysis legitimately share
// every other component, title included: without it, publishing the external version would resolve
// to the internal row and overwrite the analysis it was derived from. Adding it is safe for existing
// data in a way that REMOVING a component never is — every pre-version row resolves to the same
// default version, so the five-column tuple is unique exactly where the four-column one was, and the
// index rebuild can neither merge two reports nor fork one (the v0.3.0 failure).
//
// title is load-bearing, not decoration: rtype is a coarse registry label ("股权分析",
// "估值分析") and one code+date+subtype legitimately carries several different reports that
// only their titles tell apart. Keying without it merges them and keeps only the last.
// A thematic report has no code (symbol ”), and is likewise told apart by its title, so
// this one tuple covers both without a code-or-title fallback expression.
const reportIdentExpr = `symbol, rdate, rtype, title, version`

const reportIdentIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_reports_ident ON reports(` + reportIdentExpr + `)`

// baseSchemaStmts returns the full current-generation schema as CREATE statements — the single
// source of truth for the DB shape. createBaseSchema execs it on a fresh database; ensureColumns
// (migrate.go) reads the SAME statements to auto-add any column an older database lacks, so a new
// additive column is declared here ONCE and picked up everywhere without a hand-written migration.
// Per the squash contract (CLAUDE.md hard rules), this base schema equals the fully-migrated final
// state of the previous release line: the six former 1:1 side tables are folded into parent
// columns, the dead user_group_members table and links.collapsed column are gone, and `id` is the
// reports table's one and only identifier — the former synthetic `uid` column is retired and every
// API speaks the numeric id. See docs/adr/0013-v2-schema-consolidation.md.
func (s *Store) baseSchemaStmts() []string {
	pk := s.pkAuto()
	return []string{
		// owner_group (ADR 0022): the OU that generated this report, stamped once at ingest
		// (first-writer-wins). NULL = internal/legacy/unattributed. NOT part of report identity
		// (idx_reports_ident below), so two OUs requesting the same symbol|date|subtype|title still
		// share one upserted row. Additive nullable column; picked up on existing DBs by ensureColumns.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS reports(
			id %s,
			title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT,
			kind TEXT, run_id TEXT, owner_group BIGINT,
			source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT,
			version TEXT NOT NULL DEFAULT '')`, pk),
		// These two carry every lookup by code or date. Single-column idx_reports_sym(symbol) and
		// idx_reports_date(rdate) used to sit beside them and are gone: a B-tree already serves its
		// leftmost prefix, so both were answered by a wider index anyway. Measured on 30k rows,
		// Postgres never once chose idx_reports_sym — symbol lookups go to idx_reports_ident — and
		// dropping idx_reports_date moved rdate lookups onto idx_reports_date_time for +0.9% cost.
		// They only ever charged write amplification on every ingest. Don't reintroduce them.
		`CREATE INDEX IF NOT EXISTS idx_reports_symbol_date_time ON reports(symbol,rdate,sent_at)`,
		`CREATE INDEX IF NOT EXISTS idx_reports_date_time ON reports(rdate,sent_at)`,
		// Report dedup identity, enforced by the DB rather than a derived string column: the
		// stock code — or the title when a thematic report has no code — plus the civil date and
		// the subtype. Re-ingesting the same identity overwrites the row (see UpsertReport's
		// matching ON CONFLICT target). kind is deliberately NOT part of identity: re-categorizing
		// a subtype in the registry must never fork a report into two rows. run_id is only a
		// batch label and likewise stays out.
		reportIdentIndex,
		// There is deliberately NO index on owner_group. It served the ADR 0022 read filter, which
		// ADR 0024 replaced: reads now resolve through version grants and report_viewers, and
		// owner_group survives only as attribution written by a by-id UPDATE. An index nothing reads
		// is pure write amplification on every ingest — measured at 13% — and this table has already
		// lost two indexes for that reason. Don't reintroduce it without a query that needs it.
		// Entry buttons. group_id: the link group it belongs to (0 = ungrouped/top-level, shown inline).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS links(
			id %s, label TEXT, url TEXT, icon TEXT DEFAULT '', new_tab INTEGER DEFAULT 1,
			ord INTEGER DEFAULT 0, group_id INTEGER DEFAULT 0, visible INTEGER DEFAULT 1)`, pk),
		// Named, foldable groups of entry buttons on the home page (mode: row/expand/popover/modal).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS link_groups(
			id %s, name TEXT DEFAULT '', mode TEXT DEFAULT 'row', show_label INTEGER DEFAULT 1, icon TEXT DEFAULT '', ord INTEGER DEFAULT 0, visible INTEGER DEFAULT 1)`, pk),
		`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT)`,
		// Site announcements (ADR 0025). One row = one announcement; this table replaces the five
		// announcement_* keys in meta, which upgrade_v04.go folds into the first row exactly once.
		//
		// level is notice|success|warning|error; '' is legal and reads as notice, because the meta key
		// it descends from could be empty. ord is a sort key ONLY — identity is always id, which is why
		// dragging a row can never re-fire a popup somebody already dismissed.
		// scope: home = the home page alone (today's behaviour, and the fallback for any unrecognized
		// value); app = every page behind the login. audience: all = every logged-in account; grant =
		// only the principals listed in announcement_grants, and an empty grant list shows the
		// announcement to NOBODY — default-deny is written out, never inferred from an absent filter.
		// starts_at/ends_at are RFC3339 UTC instants ('' = unbounded on that side), compared in UTC and
		// rendered in the panel timezone; a civil-time string here would shift meaning with the setting.
		// scope/audience/starts_at/ends_at are declared from the first release that has this table even
		// though the release line that ships it only writes the defaults: a column costs nothing here and
		// arrives on existing databases through ensureColumns anyway.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS announcements(
			id %s, level TEXT DEFAULT 'notice', title TEXT DEFAULT '', content TEXT DEFAULT '',
			ord INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, popup INTEGER DEFAULT 0,
			dismissible INTEGER DEFAULT 0, scope TEXT DEFAULT 'home', audience TEXT DEFAULT 'all',
			starts_at TEXT DEFAULT '', ends_at TEXT DEFAULT '',
			created_at TEXT DEFAULT '', created_by TEXT DEFAULT '', updated_at TEXT DEFAULT '')`, pk),
		// announcement_grants (ADR 0025): who an audience='grant' announcement goes to, in the principal
		// encoding version_grants and report_viewers already use — an OU is "g:<id>", one account is
		// "u:<name>". It resolves DIFFERENTLY from version_grants though, and the difference is the whole
		// point: a grant is a right, so the nearest ancestor OU with rows wins and a child never inherits
		// the root's; an announcement is a broadcast, so a reader matches the UNION of every OU on their
		// chain and a notice sent to a parent OU reaches the whole subtree. See announcementPrincipals.
		// Deliberately no index: the whole table is tens of rows and the read path loads it into a map.
		// If it ever grows past that, the covering shape is (principal, announcement_id) — but reports
		// has already lost two indexes to write amplification, so don't add one before a query needs it.
		`CREATE TABLE IF NOT EXISTS announcement_grants(
			announcement_id BIGINT, principal TEXT, PRIMARY KEY(announcement_id, principal))`,
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
		// expires_at (ADR 0022): account validity cutoff as a panel-tz civil date "YYYY-MM-DD" (NULL =
		// never), orthogonal to active — valid THROUGH that whole day; see Server.accountExpired. Additive.
		// users.restricted (ADR 0024): scopes THIS account's reads regardless of its OU, so a portal
		// with no OU tree can still have external users. ORs with the OU's own restricted flag.
		//
		// Authentication columns (ADR 0023). source/source_ref record WHERE an account came from
		// (local | jit | scim) so a future sync only ever owns its own rows; external_id is the IdP's
		// immutable object id and the SSO<->SCIM join key — it must be recorded during the SSO era
		// because it cannot be reconstructed later. The SCIM-only columns (uid, login_name,
		// deactivated_at, deleted_at, last_sync_at) are deliberately deferred to the SCIM change:
		// ensureColumns adds columns for free here, so reserving them early buys nothing.
		// totp_* / recovery_codes back TOTP 2FA; the secret is sealed (never plaintext) and the
		// recovery codes are stored HASHED and single-use, because they are password-equivalents.
		`CREATE TABLE IF NOT EXISTS users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user',
			display_name TEXT, email TEXT, active INTEGER DEFAULT 1, last_login TEXT, last_seen TEXT, group_id BIGINT,
			session_rev BIGINT DEFAULT 0, expires_at TEXT,
			external_id TEXT, source TEXT DEFAULT 'local', source_ref TEXT DEFAULT '',
			created_at TEXT, updated_at TEXT,
			totp_secret_enc TEXT, totp_enabled INTEGER DEFAULT 0, totp_confirmed_at TEXT,
			recovery_codes TEXT, restricted INTEGER DEFAULT 0,
			sso_provider TEXT DEFAULT '', sso_issuer TEXT DEFAULT '', sso_subject TEXT DEFAULT '',
			sso_slug TEXT DEFAULT '', sso_nameid_format TEXT DEFAULT '', sso_attrs TEXT DEFAULT '',
			sso_linked_at TEXT)`,
		// One row per IdP. Row-shaped (not a meta blob) so multiple providers are later a UI change
		// with no schema movement; v1 manages one saml row and one oidc row. Secrets (SP private key,
		// OIDC client secret) are sealed under the keyring DEK (in `meta`) and never returned by any API.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS sso_providers(
			id %s, kind TEXT, slug TEXT, name TEXT,
			enabled INTEGER DEFAULT 0, provisioning TEXT DEFAULT 'off',
			default_group BIGINT, default_role TEXT DEFAULT 'user',
			default_expiry_days INTEGER, allow_admin_role INTEGER DEFAULT 0,
			idp_metadata_url TEXT, idp_metadata_xml TEXT, idp_metadata_fetched_at TEXT,
			idp_metadata_error TEXT, idp_entity_id TEXT, idp_cert_pem TEXT,
			allow_idp_initiated INTEGER DEFAULT 0, clock_skew_sec INTEGER DEFAULT 60,
			sp_cert_pem TEXT, sp_cert_not_after TEXT, sp_key_enc TEXT,
			sp_cert_prev_pem TEXT, sp_key_prev_enc TEXT,
			issuer TEXT, client_id TEXT, client_secret_enc TEXT,
			scopes TEXT DEFAULT 'openid profile email', discovery_json TEXT, discovery_fetched_at TEXT,
			attr_upn TEXT, attr_email TEXT, attr_display TEXT, attr_groups TEXT, attr_external_id TEXT,
			session_hours INTEGER, created_at TEXT, updated_at TEXT, icon TEXT, link_by TEXT)`, pk),
		// Short-lived single-use state for EVERY interactive auth ceremony: the SAML AuthnRequest id,
		// the OIDC nonce + PKCE verifier, the 2FA pending-login step and the WebAuthn challenge. They
		// are one problem (single-use, restart-safe, cross-instance), so they get one table, one
		// sweeper, and one consumption rule: a conditional DELETE requiring RowsAffected()==1, which
		// no cookie can provide and which two concurrent callbacks cannot both win.
		`CREATE TABLE IF NOT EXISTS auth_requests(
			token TEXT PRIMARY KEY, provider_id BIGINT, kind TEXT,
			req_id TEXT DEFAULT '', nonce TEXT DEFAULT '', verifier TEXT DEFAULT '',
			username TEXT DEFAULT '', target TEXT DEFAULT '', purpose TEXT DEFAULT '',
			created_at BIGINT, expires_at BIGINT)`,
		// SAML assertion replay cache (a Web SSO profile MUST). Keyed on the HASH of entity id +
		// assertion id so one IdP cannot pre-poison another's ID space; a DB table rather than an
		// in-memory map because production runs several instances against shared Postgres and a
		// restart must not reopen the replay window.
		`CREATE TABLE IF NOT EXISTS sso_assertion_seen(
			seen_key TEXT PRIMARY KEY, expires_at BIGINT)`,
		// Passkeys. sign_count is stored because a counter that goes BACKWARDS is the one thing
		// WebAuthn's counter exists to detect (a cloned authenticator).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS webauthn_credentials(
			id %s, credential_id TEXT, username TEXT, label TEXT DEFAULT '',
			credential TEXT, sign_count BIGINT DEFAULT 0,
			created_at TEXT, last_used_at TEXT)`, pk),
		// API tokens (multiple, with note/scope/validity period/last used). scope: all|ingest|query.
		// Existing plaintext token values stay untouched and remain valid. New writes leave token
		// NULL and authenticate through token_hash; token_prefix is safe display metadata.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS api_tokens(
			id %s, token TEXT UNIQUE, token_hash TEXT, token_prefix TEXT,
			name TEXT, scope TEXT DEFAULT 'all',
			created_at TEXT, expires_at TEXT, last_used_at TEXT)`, pk),
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash) WHERE token_hash IS NOT NULL`,
		// ADR 0023 indexes. Uniqueness on users is a PARTIAL index rather than a column constraint
		// because SQLite cannot ALTER TABLE ADD COLUMN ... UNIQUE — the same shape as the token-hash
		// index above. The `<> ''` clause keeps pre-existing rows (which reconcile to NULL/'') from
		// colliding with each other.
		// One account holds at most one external identity, and no two accounts hold the same one
		// (ADR 0023, revised). The key is (issuer, subject) and never email — subject is unique only
		// within an issuer, so keying on the provider slug alone would let an admin repointing a
		// provider at a different IdP match a stranger's subject onto an existing account. Partial,
		// because every local account carries the empty subject.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_sso_identity ON users(sso_issuer, sso_subject)
			WHERE sso_subject IS NOT NULL AND sso_subject <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_external_id ON users(source_ref, external_id) WHERE external_id IS NOT NULL AND external_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_providers_slug ON sso_providers(slug)`,
		// The two TTL tables are swept by the existing cleanupLoop (ADR 0017), which scans by expiry.
		`CREATE INDEX IF NOT EXISTS idx_auth_requests_exp ON auth_requests(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sso_assertion_seen_exp ON sso_assertion_seen(expires_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_webauthn_cred_id ON webauthn_credentials(credential_id)`,
		`CREATE INDEX IF NOT EXISTS idx_webauthn_user ON webauthn_credentials(username)`,
		// Structured "assumption/tracking items" for re-run review (common across report types). itype: assumption|tracking.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS tracking_items(
			id %s, report_id BIGINT, symbol TEXT, itype TEXT, content TEXT,
			status TEXT DEFAULT 'pending', review_point TEXT, created_at TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_track_sym ON tracking_items(symbol, status)`,
		`CREATE INDEX IF NOT EXISTS idx_track_id ON tracking_items(report_id)`,
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
		// surfaces: comma-separated allow-list of the places a target may be offered in
		// ("run", "batch", "recurring", "chat"); '' means every surface, so pre-existing
		// targets keep behaving exactly as before. This is portal POLICY and deliberately
		// not part of `config`, which holds the Dify connection (base_url/api_key) — mixing
		// the two would leave no answer to "where does the next setting go?". Existing
		// databases pick the column up via ensureColumns; no migration step.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS batch_targets(
			id %s, plugin_slug TEXT, name TEXT, config TEXT, created_at TEXT, ord INTEGER,
			surfaces TEXT DEFAULT '')`, pk),
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
		// reconcile-not-retry money invariant (ADR 0015). They co-exist and are independent:
		// a workflow/chatflow run has run_id (+task_id); a pure agent/basic chat has only
		// conversation_id (+task_id). conversation_id/task_id are pure-additive (nullable, no
		// backfill), so existing databases pick them up via ensureColumns — no migration step.
		// dify_started_at is stamped the instant the Dify stream opens (2xx), BEFORE any id is
		// emitted: it is the persisted "this run reached Dify and started" signal, so a crash in
		// the tiny window before the first id can still tell a started run (→ untracked, never
		// re-run) from one that never reached Dify (→ safe to re-run). Also pure-additive.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS batch_items(
			id %s, job_id BIGINT, row_index INTEGER, inputs TEXT, status TEXT DEFAULT 'queued',
			attempts INTEGER DEFAULT 0, run_id TEXT, conversation_id TEXT, task_id TEXT,
			dify_started_at TEXT DEFAULT '', error TEXT, started_at TEXT, finished_at TEXT)`, pk),
		// Preset low-peak scheduling windows (docs/adr/0014-idle-lane-and-preset-windows.md): an
		// admin-managed, ordered list a user picks from to schedule a run into a recurring window —
		// structurally like type_config/links (a table with ord), not a meta blob. intervals is a
		// JSON array [{start,stop}] of sub-windows (the union — e.g. 09:00–12:00 and 14:00–18:00);
		// each anchor is {weekday?,month?,day?,time:"HH:mm"} and the used fields depend on freq
		// (daily|weekly|monthly|yearly). on_overrun (continue|next|cancel) decides what happens when
		// a whole period's sub-windows are all missed. invert (0/1, default 0) flips the polarity:
		// a normal preset runs a job INSIDE the intervals, an inverted one runs it OUTSIDE them (the
		// intervals become "do not run" / peak hours). invert is a plain additive column, so existing
		// databases pick it up via ensureColumns — no migration step. The job snapshots the rule, so
		// this row is never referenced by a job (no FK); id is a plain surrogate for CRUD/reorder/pick.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS run_presets(
			id %s, label TEXT, freq TEXT, intervals TEXT,
			on_overrun TEXT DEFAULT 'next', enabled INTEGER DEFAULT 1, invert INTEGER DEFAULT 0, ord INTEGER DEFAULT 0)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_batch_items_job ON batch_items(job_id, status)`,
		// The batch console polls JobsFirstInputs (first row of the page's jobs) every 3s;
		// without this the WHERE row_index=0 lookup is a full scan of batch_items — the
		// fastest-growing table — and gets slow on a large job history.
		`CREATE INDEX IF NOT EXISTS idx_batch_items_row0 ON batch_items(row_index, job_id)`,
		// The scheduler + queue console filter batch_jobs by status on every 3s/12s poll
		// (QueuedJobs / RunningJobCount / SchedulableJobs / QueuedPresetJobs) and the storage cleanup
		// filters status + finished_at; a composite (status, finished_at) serves both — the status-only
		// filters use the leftmost prefix, the cleanup predicate uses both columns. Without it these
		// full-scan batch_jobs on the single SQLite connection. RecentJobActivity range-scans created_at.
		`CREATE INDEX IF NOT EXISTS idx_batch_jobs_status ON batch_jobs(status, finished_at)`,
		`CREATE INDEX IF NOT EXISTS idx_batch_jobs_created ON batch_jobs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_batch_jobs_run_at ON batch_jobs(run_at) WHERE run_at <> ''`,
		// Run-queue priority (ADR 0004) and one-shot schedule run_at (ADR 0007) are now the
		// batch_jobs.priority / run_at columns above (folded from the former job_queue /
		// job_schedule side tables). The partial run_at index is part of the v0.3 base schema.
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
		// run_quota_period is the window daily_run_quota is measured over: day | week | month |
		// total. NULL/"" means day, which is what every row written before the column meant, so an
		// existing deployment keeps its behaviour with no backfill. It is declared with no SQL
		// comment beside it on purpose — ensureColumns parses column names out of this very string
		// and would try to ALTER TABLE ADD COLUMN "--".
		//
		// parent_id/restricted/daily_run_quota (ADR 0022) promote the group into the OU tree:
		// parent_id (NULL = root) builds the tenant hierarchy; restricted flags an external OU
		// (inherited down the tree); daily_run_quota is the R2 per-day run cap (NULL = inherit,
		// 0 = unlimited). Additive nullable/defaulted columns; picked up on existing DBs by ensureColumns.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS user_groups(
			id %s, name TEXT UNIQUE, description TEXT, created_at TEXT, weight INTEGER,
			urgent_unlimited INTEGER, is_default INTEGER DEFAULT 0,
			allow_urgent INTEGER, max_queued INTEGER, run_window TEXT, priority TEXT,
			parent_id BIGINT, restricted INTEGER DEFAULT 0, daily_run_quota INTEGER,
			run_quota_period TEXT)`, pk),
		// report_versions (ADR 0024): the registry of written forms a report can be published in.
		// `name` is a stable id stored on report rows; `label` is what users see, so renaming the
		// display name can never orphan a report. `visibility` decides WHOSE reports of this version a
		// reader sees once granted it (owner|group|all) — see the Visibility constants.
		`CREATE TABLE IF NOT EXISTS report_versions(
			name TEXT PRIMARY KEY, label TEXT DEFAULT '', ord INTEGER DEFAULT 0,
			visibility TEXT DEFAULT 'owner')`,
		// version_grants (ADR 0024): who may READ a version, default-deny. The principal is an OU
		// ("g:<id>") or a single account ("u:<name>") in one column, so a portal with no OU tree
		// configured is a first-class case rather than a workaround — and so the read path has ONE
		// shape to get right instead of two. Additive side table with a composite key, like
		// group_targets.
		`CREATE TABLE IF NOT EXISTS version_grants(
			version TEXT, principal TEXT, PRIMARY KEY(version, principal))`,
		// report_viewers (ADR 0024): who asked for a report. This is the single ownership mechanism
		// the READ path consults — the security-critical filter must not have two spellings — with
		// the same principal encoding as version_grants. rdate is carried on the row on purpose: the
		// list page orders by it, and a covering (principal, rdate, report_id) key turns that sort
		// into an index walk instead of a temp B-tree (measured 0.014ms vs 0.235ms at 200k reports).
		// It is safe to denormalize because rdate is part of report identity and never changes after
		// ingest.
		`CREATE TABLE IF NOT EXISTS report_viewers(
			principal TEXT, rdate TEXT, report_id BIGINT, PRIMARY KEY(principal, rdate, report_id))`,
		// (report_id, principal), not report_id alone: the read path asks "is this principal on this
		// report's list", and the second column makes that a COVERING index lookup with no table
		// visit. Same storage, strictly better shape.
		`CREATE INDEX IF NOT EXISTS idx_report_viewers_report ON report_viewers(report_id, principal)`,
		// group_targets (ADR 0022): a restricted OU's default-deny allow-list of which batch_targets it
		// may run and on which surfaces. A row = "this OU MAY run this target"; surfaces is the OU's
		// subset of run|batch|recurring|chat ('' = inherit the target's own batch_targets.surfaces).
		// Resolved up the OU tree (nearest ancestor with rows wins); unrestricted OUs ignore it. Additive
		// side table with a composite key (no surrogate id, like app_files); created by createBaseTables.
		`CREATE TABLE IF NOT EXISTS group_targets(
			group_id BIGINT, target_id BIGINT, surfaces TEXT DEFAULT '',
			PRIMARY KEY(group_id, target_id))`,
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
		// Storage-cleanup audit log (docs/adr/0017-storage-cleanup.md): one row per real cleanup
		// pass (scheduled or manual "clean now"; previews are not recorded), so an admin has a
		// durable, browsable trail of what a destructive auto-delete removed and when. Trimmed to a
		// ring buffer (InsertCleanupRun) so the audit table can't itself grow unbounded — idempotency
		// of the scheduler lives in the meta key cleanup_last_run_period, never derived from this table.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS cleanup_runs(
			id %s, ran_at TEXT, trigger TEXT, dry_run INTEGER DEFAULT 0, ok INTEGER DEFAULT 1, error TEXT DEFAULT '',
			batch_deleted INTEGER DEFAULT 0, tokens_deleted INTEGER DEFAULT 0, reports_deleted INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0, audit_deleted INTEGER DEFAULT 0)`, pk),
		// The audit log: who did what to which object, and when.
		//
		// One table for two audiences. "Who read this report" is the question a client asks and the
		// only one the portal can answer with evidence rather than assurance; "who changed this
		// grant" is what an operator asks when something is visible that should not be. Same shape,
		// so one table and one query rather than a UNION over two.
		//
		// actor_ou is stamped at WRITE time. People move between OUs, so resolving it later from
		// users.group_id would answer "which OU are they in now" — not what an audit line means.
		// It is also the seam for per-OU audit visibility later: a WHERE clause, not a migration.
		//
		// target_id is TEXT because targets are not all numeric — a version is named, a setting is
		// keyed. detail is free JSON: the columns are what must be queryable, the rest is context.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS audit_log(
			id %s, at TEXT, actor TEXT DEFAULT '', actor_ou BIGINT DEFAULT 0,
			action TEXT, target_type TEXT DEFAULT '', target_id TEXT DEFAULT '',
			detail TEXT DEFAULT '', ip TEXT DEFAULT '')`, pk),
		// The three questions the log is read with: the time feed, this object's history, and what
		// one person did. Each is a leading-column match, so each is a range scan rather than a scan.
		`CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_log(target_type, target_id, at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor, at DESC)`,
		// action leads, because the console's most-used filter is one action ordered by time. Without
		// it every page of a filtered view scans the table, and report.read alone is 70% of the rows.
		`CREATE INDEX IF NOT EXISTS idx_audit_action_at ON audit_log(action, at DESC)`,
		// Recurring tasks (scheduled tasks; docs/adr/0018-recurring-tasks.md): a saved job template + a
		// daily/weekly/monthly cadence a background loop fires into the run queue, indefinitely, until
		// disabled. rows is the JSON job template (the exact shape CreateBatchJob takes: 1 row = a
		// single run, N = a batch). priority is '' (normal — resolves to the creator's group base at
		// fire time) or 'idle'; never 'urgent' (a recurring urgent run would drain the scarce urgent-run
		// tickets every occurrence). freq/at_time/weekday/monthday reuse the storage-cleanup cadence engine
		// (cadence.go), valued in the panel timezone. last_fired is the YYYY-MM-DD period-stamp that
		// guards against a restart/slow-fire double-fire (stamped BEFORE the job is created). target_id
		// is a live reference to batch_targets (the template tracks the current workflow), not a
		// snapshot — a missing target is logged-and-skipped at fire time.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS recurring_tasks(
			id %s, name TEXT, target_id BIGINT, rows TEXT DEFAULT '[]',
			concurrency INTEGER DEFAULT 1, priority TEXT DEFAULT '', max_retries INTEGER DEFAULT 0,
			freq TEXT, at_time TEXT, weekday INTEGER DEFAULT 1, monthday INTEGER DEFAULT 1,
			enabled INTEGER DEFAULT 1, created_by TEXT, created_at TEXT, last_fired TEXT DEFAULT '')`, pk),
		// recurring_runs: the fire→job audit chain (one row per firing), trimmed to a per-task ring
		// (InsertRecurringRun). The scheduler's idempotency is NOT derived from this table (it lives in
		// recurring_tasks.last_fired), so trimming can never cause a re-fire.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS recurring_runs(
			id %s, task_id BIGINT, job_id BIGINT, fired_at TEXT)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_recurring_runs_task ON recurring_runs(task_id, id)`,
	}
}

// isIndexDDL reports whether a base-schema statement creates an index rather than a table.
// It is what lets init apply the two kinds in separate passes with ensureColumns between them.
func isIndexDDL(stmt string) bool {
	u := strings.ToUpper(strings.TrimSpace(stmt))
	return strings.HasPrefix(u, "CREATE INDEX") || strings.HasPrefix(u, "CREATE UNIQUE INDEX")
}

// createBaseTables applies every CREATE TABLE in the base schema; createBaseIndexes applies every
// index, and must run after ensureColumns (see init). Both are CREATE ... IF NOT EXISTS, so an
// existing database is left untouched and a re-run is a no-op.
func (s *Store) createBaseTables() error  { return s.execBaseSchema(false) }
func (s *Store) createBaseIndexes() error { return s.execBaseSchema(true) }

func (s *Store) execBaseSchema(indexes bool) error {
	for _, st := range s.baseSchemaStmts() {
		if isIndexDDL(st) != indexes {
			continue
		}
		if _, err := s.exec(st); err != nil {
			if st == reportIdentIndex {
				// The only statement that can fail on a database whose rows are otherwise fine:
				// history predating this index can hold rows that collide under it. Hand over the
				// query rather than a theory of how they got there — a bare "UNIQUE constraint
				// failed" here reads as an unrelated startup crash.
				return fmt.Errorf("create base schema: %w\nSQL: %s\n\n"+
					"idx_reports_ident enforces one report per (symbol, rdate, rtype, title). This database holds "+
					"rows that collide under it, from before the index existed. Inspect them with:\n\n"+
					"  SELECT symbol, rdate, rtype, title, COUNT(*) FROM reports\n"+
					"  GROUP BY 1,2,3,4 HAVING COUNT(*) > 1;\n\n"+
					"Archive them (CREATE TABLE ... AS SELECT) and keep one row per identity — prefer a row with a\n"+
					"body, newest wins — before starting this version", err, st)
			}
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
	COALESCE(u.display_name,''),COALESCE(u.email,''),COALESCE(u.active,1),COALESCE(u.last_login,''),COALESCE(u.last_seen,''),
	COALESCE(u.session_rev,0),COALESCE(u.expires_at,''),
	COALESCE(u.source,'local'),COALESCE(u.source_ref,''),COALESCE(u.external_id,''),COALESCE(u.totp_enabled,0),
	COALESCE(u.restricted,0),COALESCE(u.created_at,'')`

func scanUser(scan func(...any) error) (User, error) {
	var u User
	var role, dn, email, last, seen, expires, source, sourceRef, externalID sql.NullString
	var active, sessionRev, totp, restricted sql.NullInt64
	var createdAt sql.NullString
	if err := scan(&u.Username, &u.PasswordHash, &role, &dn, &email, &active, &last, &seen, &sessionRev, &expires,
		&source, &sourceRef, &externalID, &totp, &restricted, &createdAt); err != nil {
		return User{}, err
	}
	u.Restricted = restricted.Int64 != 0
	u.Role, u.DisplayName, u.Email, u.LastLogin = role.String, dn.String, email.String, last.String
	u.LastSeen = seen.String
	u.Active = !active.Valid || active.Int64 != 0
	u.SessionRev = sessionRev.Int64
	u.ExpiresAt = expires.String
	// A NULL source (a row that predates ADR 0023) reads as "local": a pre-existing account must
	// never be mistaken for a federated one, which would lock its owner out of password login.
	u.Source = source.String
	if u.Source == "" {
		u.Source = "local"
	}
	u.SourceRef, u.ExternalID = sourceRef.String, externalID.String
	u.TOTPEnabled = totp.Int64 != 0
	u.CreatedAt = createdAt.String
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

// UsernameTaken reports whether any account holds this name ignoring case. Creation paths ask this
// instead of GetUser: new names are folded (normalizeUsername), but a database written before that
// may hold `Alice`, and an exact-match guard would happily create `alice` alongside it — two accounts
// on the one principal `u:alice`. Deliberately not on the login path, which stays an exact
// primary-key hit; this runs only when an account is created.
// It normalizes its own argument rather than trusting the caller to have done it: this is a guard,
// and a guard that is only correct when called correctly is not one.
//
// The comparison is done in Go, not with SQL LOWER(). They disagree on non-ASCII, and not in the
// same way on each driver: SQLite's LOWER() is ASCII-only, so it leaves 'ÉLODIE' alone while Go
// folds it to 'élodie' — the guard would pass and two accounts would share the principal
// `u:élodie`, which is precisely what it exists to prevent. Postgres' lower() is collation-aware
// and would have caught it, so the hole existed on one driver only. Folding here makes the guard
// agree with userPrincipal by construction, on both. The scan is the same cost as LOWER() was —
// neither can use the primary key — and this only runs when an account is created.
func (s *Store) UsernameTaken(name string) bool {
	want := normalizeUsername(name)
	if want == "" {
		return false
	}
	for _, existing := range s.allUsernames() {
		if normalizeUsername(existing) == want {
			return true
		}
	}
	return false
}

// allUsernames lists every account name verbatim, for the two case-folding checks that must compare
// the way Go does rather than the way the database does.
func (s *Store) allUsernames() []string {
	rows, err := s.query("SELECT username FROM users")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out = append(out, name)
		}
	}
	return out
}

// CaseVariantUsernames returns the folded names that more than one account already answers to.
// Folding new writes cannot retrofit a collision that predates it, and a pair that silently shares a
// read principal is exactly the failure this is meant to make impossible — so the server says so out
// loud at startup rather than leaving an admin to discover it through the reports.
func (s *Store) CaseVariantUsernames() []string {
	// Grouped in Go for the same reason UsernameTaken compares in Go: SQL LOWER() is ASCII-only on
	// SQLite, so a GROUP BY over it would not report the very non-ASCII pair this warning describes.
	seen := map[string]int{}
	for _, name := range s.allUsernames() {
		seen[normalizeUsername(name)]++
	}
	var out []string
	for name, n := range seen {
		if n > 1 {
			out = append(out, name)
		}
	}
	sort.Strings(out) // a stable order, so the startup warning reads the same on every boot
	return out
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
	// A new account starts at a session revision nobody has held before, rather than at zero.
	//
	// session_rev already invalidates old cookies when a password changes, but it could not survive
	// a DELETION: a recreated row is a fresh INSERT, so it restarted at zero and the previous
	// holder's still-valid cookie — usernames are re-registerable — authenticated them as the new
	// account, with its OU, its role and its reports. Seeding from the clock makes the revision
	// unique per account INSTANCE, so the old cookie's revision simply no longer matches.
	//
	// Only on INSERT. The conflict branch keeps its +1, so an ordinary password change is still a
	// single monotonic step and no live session is disturbed by a profile save. Rows written before
	// this keep revision 0 and their cookies keep working.
	//
	// created_at is stamped here too — it was declared and never written, and "when was this
	// account created" is worth answering in the admin UI.
	_, err := s.exec(`INSERT INTO users(username,password_hash,role,created_at,session_rev)
			VALUES(?,?,?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash,role=excluded.role,
			session_rev=COALESCE(users.session_rev,0)+1`,
		u.Username, u.PasswordHash, u.EffRole(), nowStr(), newSessionEpoch())
	return err
}

func (s *Store) SetUserPassword(name, hash string) error {
	_, err := s.exec("UPDATE users SET password_hash=?,session_rev=COALESCE(session_rev,0)+1 WHERE username=?", hash, name)
	return err
}

func (s *Store) SetUserRole(name, role string) error {
	_, err := s.exec("UPDATE users SET role=? WHERE username=?", role, name)
	return err
}

// DeleteUser removes an account and everything keyed to its name.
//
// The name is the thing to worry about. There are no foreign keys in this schema, so each of these
// deletes is the only thing standing between a table and an orphan row — and a username is
// re-registerable, so an orphan is not merely wasted space: the next person to take that address
// inherits whatever the last one left behind. That is why this sweeps credentials and pending
// ceremonies too, not just the grant tables.
//
// Profile attributes and the primary group are columns on users now (ADR 0013), so they vanish with
// the row. batch_jobs is deliberately NOT swept: it is the run history an operator audits, who ran
// what must outlive the person, and it carries no grant, so nothing is inherited through it.
func (s *Store) DeleteUser(name string) error {
	principal := userPrincipal(name)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // a no-op once Commit has run; the safety net if any step below returns

	run := func(q string, args ...any) error {
		_, err := tx.Exec(s.bind(q), args...)
		return err
	}
	// The users row goes FIRST, inside the transaction. Deleting it last meant every sweep ran
	// while the account was still fully valid for authentication, so a concurrent request could
	// re-create a row that had just been swept — TicketStatus INSERTs a priority_tickets row on a
	// plain GET — and the orphan would then be inherited by the next holder of the username. One
	// transaction also means a failure half-way leaves the account intact rather than partly
	// dismantled, and on Postgres it keeps all of this on a single pooled connection.
	steps := []struct {
		q   string
		arg any
	}{
		{"DELETE FROM users WHERE username=?", name},
		// batch_jobs stays — it is the run history an operator audits — but it must stop being LIVE
		// state. A job left queued is still dispatched to Dify, and billed, for an account that no
		// longer exists; and the run counters read this table, so an unfinished job would also
		// count against whoever takes the username next.
		{"UPDATE batch_jobs SET status='cancelled', finished_at='" + nowStr() +
			"' WHERE created_by=? AND status IN ('queued','running','cancelling')", name},
		{"DELETE FROM recurring_runs WHERE task_id IN (SELECT id FROM recurring_tasks WHERE created_by=?)", name},
		{"DELETE FROM recurring_tasks WHERE created_by=?", name},
		// A passkey is a credential: left behind, it would let the previous holder of the name sign
		// in as whoever registers it next, with no expiry to bound it.
		{"DELETE FROM webauthn_credentials WHERE username=?", name},
		// Pending TOTP / passkey / reset / email-verify ceremonies. Short-lived, but a live token
		// resolves by username and would act on the account that holds the name when it is redeemed.
		{"DELETE FROM auth_requests WHERE username=?", name},
		// The portal's index of the person's chat threads (Dify holds the messages themselves).
		{"DELETE FROM chat_conversations WHERE created_by=?", name},
		// The urgent-run allowance, which is a scarce resource allocated per person (ADR 0005).
		{"DELETE FROM priority_tickets WHERE username=?", name},
		// The read path consults report_viewers alone (ADR 0024), so a surviving `u:<name>` row
		// hands the new holder of the name every report the old one could read.
		{"DELETE FROM report_viewers WHERE principal=?", principal},
		{"DELETE FROM version_grants WHERE principal=?", principal},
	}
	for _, st := range steps {
		if err := run(st.q, st.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// newReportFilter builds the shared reports predicate used by the full search and
// the home-feed search. Keeping it in one place prevents their filter semantics
// from drifting.
func (s *Store) newReportFilter(f Filters, sc *ownerScope) (string, []any) {
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
	if frag, fargs := sc.where("r."); frag != "" {
		where = append(where, frag)
		args = append(args, fargs...)
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// SearchNew returns matching new reports (without body). sc scopes the result to a restricted
// viewer's own OU + same-day internal pool (nil = no restriction; see ownerScope).
func (s *Store) SearchNew(f Filters, sc *ownerScope) ([]Rep, error) {
	where, args := s.newReportFilter(f, sc)
	q := "SELECT r.id,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at,COALESCE(r.version,'') FROM reports r LEFT JOIN stocks s ON s.code = r.symbol"
	q += where
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

// SearchNewLatest returns the reports needed by the home feed: every thematic
// report plus every member on the latest matching date for each stock. It also
// returns the count before that collapse. Doing the history collapse in SQL avoids
// transferring and retaining every historical report merely to discard it in Go.
// SearchNewLatest returns the newest run per symbol for the home feed.
//
// It returns EVERY match, with no LIMIT: the caller groups the rows in Go (buildGroups →
// collapseLatestBySymbol) and only then pages. So the cost is linear in the total report count
// rather than in the page size, and the window function makes it a full scan by construction.
//
// Measured on SQLite, 4000 symbols, warm: 8.5k reports → 28ms, 50k → 155ms, 200k → 622ms. Left
// alone deliberately. Pushing latest-per-symbol and pagination into SQL means restructuring the
// grouping pipeline, and at the size this portal actually runs at the query is 28ms — the rewrite
// would trade a real risk against a cost nobody can perceive. Worth revisiting nearer 50k, which is
// where it starts to be felt.
func (s *Store) SearchNewLatest(f Filters, sc *ownerScope) ([]Rep, int, error) {
	where, args := s.newReportFilter(f, sc)
	q := `WITH filtered AS (
		SELECT r.id,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at,COALESCE(r.version,'') AS version,
			COUNT(*) OVER() AS filtered_total
		FROM reports r LEFT JOIN stocks s ON s.code = r.symbol` + where + `
	), ranked AS (
		SELECT filtered.*,
			CASE WHEN COALESCE(symbol,'')='' THEN 1
			ELSE DENSE_RANK() OVER (PARTITION BY symbol ORDER BY rdate DESC) END AS date_rank
		FROM filtered
	)
	SELECT id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,filtered_total
	FROM ranked WHERE date_rank=1 ORDER BY rdate DESC,sent_at DESC`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Rep
	var total int
	for rows.Next() {
		var id int64
		var title, sym, name, rt, rd, kind, runID, src, sent sql.NullString
		if err := rows.Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, Rep{
			ID: id, Title: title.String, Symbol: sym.String, Name: name.String,
			RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
			Source: src.String, Time: sent.String,
		})
	}
	return out, total, rows.Err()
}

// scanNewRow scans one new-report row (without body). Fixed column order:
// id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,version — every caller's SELECT must
// match it exactly, in that order.
func scanNewRow(rows *sql.Rows) Rep {
	var id int64
	var title, sym, name, rt, rd, kind, runID, src, sent, version sql.NullString
	rows.Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &version)
	return Rep{
		ID: id, Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, Version: version.String,
	}
}

// ApiToken is a single API token (multiple coexist, with note/scope/validity period).
type ApiToken struct {
	ID                                              int64
	Prefix, Name, Scope, Created, Expires, LastUsed string
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tokenPrefix(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8]
}

func (s *Store) CreateToken(token, name, scope, expires string) error {
	if scope == "" {
		scope = "all"
	}
	_, err := s.exec(`INSERT INTO api_tokens(token_hash,token_prefix,name,scope,created_at,expires_at) VALUES(?,?,?,?,?,?)`,
		tokenDigest(token), tokenPrefix(token), name, scope, nowStr(), expires)
	return err
}

func (s *Store) ListTokens() []ApiToken {
	rows, err := s.query(`SELECT id,COALESCE(NULLIF(token_prefix,''),SUBSTR(COALESCE(token,''),1,8)),name,scope,created_at,expires_at,last_used_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ApiToken
	for rows.Next() {
		var t ApiToken
		var name, scope, created, expires, last sql.NullString
		var prefix sql.NullString
		rows.Scan(&t.ID, &prefix, &name, &scope, &created, &expires, &last)
		t.Prefix, t.Name, t.Scope, t.Created, t.Expires, t.LastUsed = prefix.String, name.String, scope.String, created.String, expires.String, last.String
		out = append(out, t)
	}
	return out
}

func (s *Store) DeleteToken(id int64) error {
	_, err := s.exec("DELETE FROM api_tokens WHERE id=?", id)
	return err
}

const tokenLastUsedWriteInterval = time.Minute

// TokenValid validates a token: exists, not expired, scope matches (all or equal to need).
// Successful requests refresh last_used_at at most once per minute. This preserves useful
// activity data without turning every authenticated read into a database write.
func (s *Store) TokenValid(token, need string) bool {
	if token == "" {
		return false
	}
	var scope, expires, lastUsed sql.NullString
	digest := tokenDigest(token)
	err := s.queryRow("SELECT scope,expires_at,last_used_at FROM api_tokens WHERE token_hash=? OR token=?", digest, token).Scan(&scope, &expires, &lastUsed)
	if err != nil {
		return false
	}
	now := time.Now()
	nowText := now.Format("2006-01-02 15:04:05")
	if expires.String != "" && expires.String < nowText {
		return false // expired
	}
	if need != "" && scope.String != "all" && scope.String != need {
		return false // scope does not cover this operation
	}
	last, parseErr := time.ParseInLocation("2006-01-02 15:04:05", lastUsed.String, time.Local)
	if !lastUsed.Valid || lastUsed.String == "" || parseErr != nil || now.Sub(last) >= tokenLastUsedWriteInterval {
		_, _ = s.exec("UPDATE api_tokens SET last_used_at=? WHERE token_hash=? OR token=?", nowText, digest, token)
	}
	return true
}

// Manifest builds a "what reports exist" listing for a given symbol (so Dify can probe before fetching): total count, each date (with categories), and all categories/subtypes.
func (s *Store) Manifest(symbol string, sc *ownerScope) map[string]any {
	reps, _ := s.NewBySymbol(symbol, sc) // sc nil for a machine (Dify) probe; scoped for a restricted cookie caller
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
// ReportQuery is the filter for QueryReports (Dify /api/v1/reports search).
type ReportQuery struct {
	Symbol, Q, Kind, RType, Source, RunID, Since, Until string
	Limit, Offset                                       int
	WithBody                                            bool
}

// QueryReports searches new reports and returns the page plus the TOTAL match
// count (for pagination). Keyword q matches title, code, current name, or body.
func (s *Store) QueryReports(f ReportQuery, sc *ownerScope) ([]Rep, int, error) {
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
	if frag, fargs := sc.where("r."); frag != "" {
		where = append(where, frag)
		args = append(args, fargs...)
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
	sqlStr := fmt.Sprintf(`SELECT r.id,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at,r.body_md
		%s ORDER BY r.rdate DESC, r.sent_at DESC LIMIT %d OFFSET %d`, from, limit, offset)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var out []Rep
	for rows.Next() {
		var id int64
		var title, sym, name, rt, rd, kind, runID, src, sent, md sql.NullString
		rows.Scan(&id, &title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md)
		r := Rep{ID: id, Title: title.String,
			Symbol: sym.String, Name: name.String, RType: rt.String, Date: rd.String, Kind: kind.String,
			RunID: runID.String, Source: src.String, Time: sent.String}
		if f.WithBody {
			r.MD = md.String
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// TrackingItem is a structured assumption/tracking item. ReportID is the id of the
// parent report, exposed as report_id by both APIs.
type TrackingItem struct {
	ID                                                   int64
	ReportID                                             int64
	Symbol, IType, Content, Status, ReviewPoint, Created string
}

// SetTracking overwrites a report's tracking items (on re-run, clears then writes to stay consistent with the latest body).
func (s *Store) SetTracking(reportID int64, symbol string, items []TrackingItem) error {
	// Carry a human's verdict across the rewrite.
	//
	// Clearing and rewriting is right for the CONTENT — the newest body is the truth about which
	// assumptions a report makes — but status and review_point are the one part a PERSON puts
	// there, and a nightly re-run would otherwise reset everything anyone had reviewed back to
	// pending, which makes a review queue worthless.
	//
	// Keyed on the exact text, so a reworded assumption is a new one and starts pending. Silently
	// carrying "confirmed" onto a claim that has changed would be worse than losing the review.
	type verdict struct{ status, reviewPoint string }
	prior := map[string]verdict{}
	if rows, err := s.query(
		"SELECT itype,content,COALESCE(status,''),COALESCE(review_point,'') FROM tracking_items WHERE report_id=?",
		reportID); err == nil {
		for rows.Next() {
			var itype, content, status, rp string
			if rows.Scan(&itype, &content, &status, &rp) == nil && status != "" && status != trackingPending {
				prior[itype+"\x00"+content] = verdict{status, rp}
			}
		}
		rows.Close()
	}
	if _, err := s.exec("DELETE FROM tracking_items WHERE report_id=?", reportID); err != nil {
		return err
	}
	now := nowStr()
	for _, it := range items {
		status := it.Status
		if status == "" {
			status = trackingPending
		}
		if v, ok := prior[it.IType+"\x00"+it.Content]; ok {
			// The workflow re-sends its own default; the human's answer outranks it.
			status = v.status
			if v.reviewPoint != "" {
				it.ReviewPoint = v.reviewPoint
			}
		}
		if _, err := s.exec(`INSERT INTO tracking_items(report_id,symbol,itype,content,status,review_point,created_at)
			VALUES(?,?,?,?,?,?,?)`, reportID, symbol, it.IType, it.Content, status, it.ReviewPoint, now); err != nil {
			return err
		}
	}
	return nil
}

// QueryTracking queries a symbol's assumption/tracking items (optionally filtered by status, newest first by default).
func (s *Store) QueryTracking(symbol, status string, limit int, sc *ownerScope) []TrackingItem {
	where := []string{"t.symbol=?"}
	args := []any{symbol}
	if status != "" {
		where = append(where, "t.status=?")
		args = append(args, status)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// tracking_items has no owner_group: scope a restricted viewer by joining to the owning report,
	// so only tracking for a report the viewer may see comes back (unattributable items fail closed).
	join := ""
	if frag, fargs := sc.where("r."); frag != "" {
		join = " JOIN reports r ON r.id = t.report_id"
		where = append(where, frag)
		args = append(args, fargs...)
	}
	rows, err := s.query(fmt.Sprintf(`SELECT t.id,t.report_id,t.symbol,t.itype,t.content,t.status,t.review_point,t.created_at
		FROM tracking_items t%s
		WHERE %s ORDER BY t.created_at DESC, t.id DESC LIMIT %d`, join, strings.Join(where, " AND "), limit), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TrackingItem
	for rows.Next() {
		var t TrackingItem
		var reportID sql.NullInt64
		var sym, it, c, st, rp, cr sql.NullString
		rows.Scan(&t.ID, &reportID, &sym, &it, &c, &st, &rp, &cr)
		t.ReportID = reportID.Int64
		t.Symbol, t.IType, t.Content, t.Status, t.ReviewPoint, t.Created =
			sym.String, it.String, c.String, st.String, rp.String, cr.String
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

// ListSymbols lists stocks that have reports (q matches code or name, empty means all), ordered by report count descending.
func (s *Store) ListSymbols(q string, limit int, sc *ownerScope) []SymbolInfo {
	// Only real stocks (skip reports with no code — those aren't a symbol).
	where := "WHERE t.sym != ''"
	var args []any
	// The owner-scope predicate must go INSIDE the per-symbol aggregate, so a restricted viewer's
	// counts/latest recompute over only its visible reports and wholly-other-OU symbols drop out.
	innerWhere := ""
	if frag, fargs := sc.where(""); frag != "" {
		innerWhere = " WHERE " + frag
		args = append(args, fargs...) // inner placeholders precede the outer LIKE ones in the SQL
	}
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
			SELECT symbol AS sym, COUNT(*) AS cnt, MAX(rdate) AS latest FROM reports%s GROUP BY symbol
		) t LEFT JOIN stocks s ON s.code = t.sym
		%s
		GROUP BY t.sym, s.name ORDER BY c DESC, t.sym LIMIT %d`, innerWhere, where, limit), args...)
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
func (s *Store) ListRuns(symbol, date string, sc *ownerScope) []RunInfo {
	where := []string{"symbol=?"}
	args := []any{symbol}
	if date != "" {
		where = append(where, "rdate=?")
		args = append(args, date)
	}
	if frag, fargs := sc.where(""); frag != "" {
		where = append(where, frag)
		args = append(args, fargs...)
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
func (s *Store) NewBySymbol(symbol string, sc *ownerScope) ([]Rep, error) {
	q := `SELECT id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,COALESCE(version,'') FROM reports WHERE symbol=?`
	args := []any{symbol}
	if frag, fargs := sc.where(""); frag != "" {
		q += " AND " + frag
		args = append(args, fargs...)
	}
	q += ` ORDER BY rdate DESC, sent_at ASC`
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

// GetNew fetches a report (with body) by id. sc scopes the fetch: for a restricted viewer an
// out-of-scope id returns (nil, nil), so every by-id read path fails closed at the SQL layer and its
// existing "nil → 404" handling keeps another OU's report unreachable by id enumeration (ADR 0022 R1).
func (s *Store) GetNew(rowid int64, sc *ownerScope) (*Rep, error) {
	var title, sym, name, rt, rd, kind, runID, src, sent, md, html, version sql.NullString
	q := "SELECT title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html,version FROM reports WHERE id=?"
	args := []any{rowid}
	if frag, fargs := sc.where(""); frag != "" {
		q += " AND " + frag
		args = append(args, fargs...)
	}
	err := s.queryRow(q, args...).
		Scan(&title, &sym, &name, &rt, &rd, &kind, &runID, &src, &sent, &md, &html, &version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Rep{
		ID: rowid, Title: title.String, Symbol: sym.String, Name: name.String,
		RType: rt.String, Date: rd.String, Kind: kind.String, RunID: runID.String,
		Source: src.String, Time: sent.String, MD: md.String, HTML: html.String,
		Version: version.String,
	}, nil
}

// reportIdentWhere matches one report by the identity reportIdentExpr indexes, for the
// existence probe UpsertReport needs to tell an insert from an overwrite. It is the same
// tuple spelled as a predicate; keep it in step with reportIdentExpr.
const reportIdentWhere = `symbol=? AND rdate=? AND rtype=? AND title=? AND version=?`

// UpsertReport inserts a report, or overwrites the existing row that shares its identity
// (see reportIdentExpr: code + date + subtype + title). Returns the id of the row actually
// written — callers key tracking items, webhook payloads and API responses off it — and
// created=true when a new row was inserted, false when an existing one was overwritten.
func (s *Store) UpsertReport(r Rep) (int64, bool, error) {
	// Probe first: ON CONFLICT alone cannot tell us which branch it took, and the portable
	// alternatives (Postgres' xmax trick) do not exist on SQLite. Both statements run against
	// idx_reports_ident, so this costs one extra index seek per ingest.
	var prevID int64
	version := s.resolveVersion(r.Version)
	err := s.queryRow("SELECT id FROM reports WHERE "+reportIdentWhere,
		r.Symbol, r.Date, r.RType, r.Title, version).Scan(&prevID)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}
	var id int64
	// RETURNING id yields the written row on both the insert and the update branch, on
	// SQLite and Postgres alike, so one statement serves both drivers.
	if err := s.queryRow(`
		INSERT INTO reports(title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html,version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(`+reportIdentExpr+`) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,name=excluded.name,
		  rtype=excluded.rtype,rdate=excluded.rdate,kind=excluded.kind,run_id=excluded.run_id,
		  source=excluded.source,sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html
		RETURNING id`,
		r.Title, r.Symbol, r.Name, r.RType, r.Date, r.Kind, r.RunID, r.Source, r.Time, r.MD, r.HTML,
		version).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, prevID == 0, nil
}

// DeleteReport removes a report and its tracking items by id (one tx). Returns
// the number of report rows deleted (0 = no match; safe to retry).
func (s *Store) DeleteReport(id int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(s.bind("DELETE FROM reports WHERE id=?"), id)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if _, err := tx.Exec(s.bind("DELETE FROM tracking_items WHERE report_id=?"), id); err != nil {
		tx.Rollback()
		return 0, err
	}
	// The viewer list is what the read path consults, so a row pointing at a report that no longer
	// exists is not merely waste — it is an access grant with nothing on the other end (ADR 0024).
	if _, err := tx.Exec(s.bind("DELETE FROM report_viewers WHERE report_id=?"), id); err != nil {
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

// NewTypes lists the distinct subtypes present across reports (home subtype filter). sc scopes it so
// a restricted viewer's filter never reveals another OU's subtypes.
func (s *Store) NewTypes(sc *ownerScope) []string {
	q := "SELECT DISTINCT rtype FROM reports WHERE rtype<>''"
	frag, args := sc.where("")
	if frag != "" {
		q += " AND " + frag
	}
	return s.distinct(q+" ORDER BY rtype", args...)
}

// ReportKinds returns the distinct 大类 (top-level categories) present across reports — used to
// populate the home 大类 filter. sc scopes it (see NewTypes).
func (s *Store) ReportKinds(sc *ownerScope) []string {
	q := "SELECT DISTINCT kind FROM reports WHERE kind<>''"
	frag, args := sc.where("")
	if frag != "" {
		q += " AND " + frag
	}
	return s.distinct(q+" ORDER BY kind", args...)
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
	rows, err := s.query("SELECT id,label,url,icon,new_tab,ord,COALESCE(group_id,0),COALESCE(visible,1) FROM links ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var icon sql.NullString
		var newTab, visible sql.NullInt64
		rows.Scan(&l.ID, &l.Label, &l.URL, &icon, &newTab, &l.Ord, &l.GroupID, &visible)
		l.Icon = icon.String
		l.NewTab = !newTab.Valid || newTab.Int64 != 0    // default: open in new tab
		l.Visible = !visible.Valid || visible.Int64 != 0 // default: shown
		out = append(out, l)
	}
	return out
}

func (s *Store) AddLink(label, url, icon string, newTab bool, groupID int64, ord int) error {
	_, err := s.exec("INSERT INTO links(label,url,icon,new_tab,ord,group_id) VALUES(?,?,?,?,?,?)", label, url, icon, boolInt(newTab), ord, groupID)
	return err
}

// UpdateLinkFields changes the label/URL/icon/newTab/visible, preserving position + group (both
// are handled by the layout drag, not the edit form).
func (s *Store) UpdateLinkFields(id int64, label, url, icon string, newTab, visible bool) error {
	_, err := s.exec("UPDATE links SET label=?,url=?,icon=?,new_tab=?,visible=? WHERE id=?", label, url, icon, boolInt(newTab), boolInt(visible), id)
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
	rows, err := s.query("SELECT id,COALESCE(name,''),COALESCE(mode,'row'),COALESCE(show_label,1),COALESCE(icon,''),ord,COALESCE(visible,1) FROM link_groups ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LinkGroup
	for rows.Next() {
		var g LinkGroup
		var showLabel, visible sql.NullInt64
		rows.Scan(&g.ID, &g.Name, &g.Mode, &showLabel, &g.Icon, &g.Ord, &visible)
		g.ShowLabel = !showLabel.Valid || showLabel.Int64 != 0
		g.Visible = !visible.Valid || visible.Int64 != 0
		out = append(out, g)
	}
	return out
}

func (s *Store) AddLinkGroup(name, mode string, showLabel bool, icon string, ord int) (int64, error) {
	return s.insertID("INSERT INTO link_groups(name,mode,show_label,icon,ord) VALUES(?,?,?,?,?)", name, mode, boolInt(showLabel), icon, ord)
}

func (s *Store) UpdateLinkGroup(id int64, name, mode string, showLabel bool, icon string, visible bool) error {
	_, err := s.exec("UPDATE link_groups SET name=?,mode=?,show_label=?,icon=?,visible=? WHERE id=?", name, mode, boolInt(showLabel), icon, boolInt(visible), id)
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

func (s *Store) distinct(q string, args ...any) []string {
	rows, err := s.query(q, args...)
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
