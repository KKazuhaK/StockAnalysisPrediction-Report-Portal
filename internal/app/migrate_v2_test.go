package app

import (
	"database/sql"
	"testing"
)

// seedV1Store builds a raw in-memory SQLite database in the pre-v2 ("v1") shape:
// the six 1:1 side tables still exist, user_group_members is present, links has the
// dead `collapsed` column, and reports uses the `rowid` surrogate-key column name.
// It deliberately does NOT call init() — the point is to hand init() a genuine old DB
// and prove migrateV1toV2 upgrades it. See docs/adr/0013-v2-schema-consolidation.md.
func seedV1Store(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1) // share the one in-memory connection (matches OpenStore)
	t.Cleanup(func() { _ = db.Close() })

	v1DDL := []string{
		`CREATE TABLE reports(rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT UNIQUE, title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT,
			kind TEXT, run_id TEXT, source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT)`,
		`CREATE TABLE links(id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, url TEXT,
			icon TEXT DEFAULT '', new_tab INTEGER DEFAULT 1, ord INTEGER DEFAULT 0,
			collapsed INTEGER DEFAULT 0, group_id INTEGER DEFAULT 0)`,
		`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE users(username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user')`,
		`CREATE TABLE user_profiles(username TEXT PRIMARY KEY, display_name TEXT, email TEXT,
			active INTEGER DEFAULT 1, last_login TEXT)`,
		`CREATE TABLE user_groups(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE,
			description TEXT, created_at TEXT, weight INTEGER, urgent_unlimited INTEGER,
			is_default INTEGER DEFAULT 0, allow_urgent INTEGER, max_queued INTEGER, run_window TEXT)`,
		`CREATE TABLE user_group_members(group_id BIGINT, username TEXT, PRIMARY KEY(group_id, username))`,
		`CREATE TABLE user_primary_group(username TEXT PRIMARY KEY, group_id BIGINT)`,
		`CREATE TABLE group_priority(group_id BIGINT PRIMARY KEY, priority TEXT)`,
		`CREATE TABLE batch_targets(id INTEGER PRIMARY KEY AUTOINCREMENT, plugin_slug TEXT,
			name TEXT, config TEXT, created_at TEXT)`,
		`CREATE TABLE target_order(target_id BIGINT PRIMARY KEY, ord INTEGER)`,
		`CREATE TABLE batch_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT, target_id BIGINT, status TEXT,
			concurrency INTEGER DEFAULT 1, max_retries INTEGER DEFAULT 0, total INTEGER DEFAULT 0,
			succeeded INTEGER DEFAULT 0, partial INTEGER DEFAULT 0, failed INTEGER DEFAULT 0,
			created_by TEXT, created_at TEXT, started_at TEXT, finished_at TEXT)`,
		`CREATE TABLE job_queue(job_id BIGINT PRIMARY KEY, priority TEXT DEFAULT 'normal')`,
		`CREATE TABLE job_schedule(job_id BIGINT PRIMARY KEY, run_at TEXT)`,
	}
	for _, ddl := range v1DDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("v1 DDL: %v\n%s", err, ddl)
		}
	}

	seed := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO reports(uid,title,symbol,name,rtype,rdate,kind) VALUES('u1','T','000001','Ping An','x','2026-01-01','k')`, nil},
		{`INSERT INTO users(username,password_hash,role) VALUES('alice','h','admin')`, nil},
		{`INSERT INTO user_profiles(username,display_name,email,active,last_login) VALUES('alice','Alice','a@x.io',0,'2026-01-02 03:04:05')`, nil},
		{`INSERT INTO user_groups(id,name,is_default,weight) VALUES(7,'ops',0,50)`, nil},
		{`INSERT INTO user_primary_group(username,group_id) VALUES('alice',7)`, nil},
		{`INSERT INTO user_group_members(group_id,username) VALUES(7,'alice')`, nil},
		{`INSERT INTO group_priority(group_id,priority) VALUES(7,'urgent')`, nil},
		{`INSERT INTO batch_targets(id,plugin_slug,name) VALUES(3,'dify','Target A')`, nil},
		{`INSERT INTO target_order(target_id,ord) VALUES(3,42)`, nil},
		{`INSERT INTO batch_jobs(id,target_id,status,created_by,created_at) VALUES(9,3,'queued','alice','2026-01-03 00:00:00')`, nil},
		{`INSERT INTO job_queue(job_id,priority) VALUES(9,'urgent')`, nil},
		{`INSERT INTO job_schedule(job_id,run_at) VALUES(9,'2026-02-01 08:00:00')`, nil},
	}
	for _, s := range seed {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("v1 seed: %v\n%s", err, s.q)
		}
	}
	return &Store{db: db, driver: "sqlite"}
}

// tableGone reports whether a table no longer exists (SQLite sqlite_master lookup).
func tableGone(t *testing.T, st *Store, name string) bool {
	t.Helper()
	var n int
	if err := st.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master(%s): %v", name, err)
	}
	return n == 0
}

// TestMigrateV1toV2 runs init() against a genuine v1 database and asserts every fold,
// drop, and rename from ADR 0013 happened with data preserved.
func TestMigrateV1toV2(t *testing.T) {
	st := seedV1Store(t)
	if err := st.init(); err != nil {
		t.Fatalf("init() (should upgrade v1->v2): %v", err)
	}

	// --- user_profiles + user_primary_group folded into users ---
	u := st.GetUser("alice")
	if u == nil {
		t.Fatal("GetUser(alice) = nil after migration")
	}
	if u.DisplayName != "Alice" || u.Email != "a@x.io" || u.Active {
		t.Errorf("profile not folded: DisplayName=%q Email=%q Active=%v, want Alice/a@x.io/false", u.DisplayName, u.Email, u.Active)
	}
	if u.LastLogin != "2026-01-02 03:04:05" {
		t.Errorf("last_login not folded: %q", u.LastLogin)
	}
	var gid sql.NullInt64
	if err := st.queryRow(`SELECT group_id FROM users WHERE username='alice'`).Scan(&gid); err != nil {
		t.Fatalf("users.group_id: %v", err)
	}
	if !gid.Valid || gid.Int64 != 7 {
		t.Errorf("primary group not folded: users.group_id = %v, want 7", gid)
	}

	// --- group_priority folded into user_groups ---
	var prio string
	if err := st.queryRow(`SELECT priority FROM user_groups WHERE id=7`).Scan(&prio); err != nil {
		t.Fatalf("user_groups.priority: %v", err)
	}
	if prio != "urgent" {
		t.Errorf("group priority not folded: %q, want urgent", prio)
	}

	// --- target_order folded into batch_targets ---
	var ord int
	if err := st.queryRow(`SELECT ord FROM batch_targets WHERE id=3`).Scan(&ord); err != nil {
		t.Fatalf("batch_targets.ord: %v", err)
	}
	if ord != 42 {
		t.Errorf("target order not folded: %d, want 42", ord)
	}

	// --- job_queue + job_schedule folded into batch_jobs ---
	var jprio, runAt string
	if err := st.queryRow(`SELECT priority, run_at FROM batch_jobs WHERE id=9`).Scan(&jprio, &runAt); err != nil {
		t.Fatalf("batch_jobs.priority/run_at: %v", err)
	}
	if jprio != "urgent" || runAt != "2026-02-01 08:00:00" {
		t.Errorf("job priority/schedule not folded: priority=%q run_at=%q", jprio, runAt)
	}

	// --- dead tables/columns dropped ---
	for _, tbl := range []string{"user_profiles", "user_primary_group", "user_group_members", "group_priority", "target_order", "job_queue", "job_schedule"} {
		if !tableGone(t, st, tbl) {
			t.Errorf("table %q should have been dropped", tbl)
		}
	}
	if err := st.queryRow(`SELECT collapsed FROM links LIMIT 1`).Scan(new(int)); err == nil {
		t.Error("links.collapsed should have been dropped")
	}

	// --- reports.rowid renamed to id; rid wire format n<id> preserved ---
	var id int64
	if err := st.queryRow(`SELECT id FROM reports WHERE uid='u1'`).Scan(&id); err != nil {
		t.Fatalf("reports.id after rename: %v", err)
	}
	rep, err := st.GetNew(id)
	if err != nil || rep == nil {
		t.Fatalf("GetNew(%d): %v", id, err)
	}
	if rep.RID != "n1" {
		t.Errorf("rid wire format changed: %q, want n1", rep.RID)
	}

	// --- schema stamped v2 (idempotent: a second init() is a no-op) ---
	if v := st.schemaVersion(); v < 2 {
		t.Errorf("schema_version = %d, want >= 2", v)
	}
	if err := st.init(); err != nil {
		t.Fatalf("second init() (idempotent): %v", err)
	}
}

// TestFreshStoreIsV2 proves a brand-new DB opens straight at v2 (the migration is a
// guarded no-op on a fresh base schema — no side tables, no rowid column to rename).
func TestFreshStoreIsV2(t *testing.T) {
	st := newTestStore(t)
	if v := st.schemaVersion(); v < 2 {
		t.Errorf("fresh store schema_version = %d, want >= 2", v)
	}
	for _, tbl := range []string{"user_profiles", "job_queue", "job_schedule", "target_order", "group_priority", "user_primary_group", "user_group_members"} {
		if !tableGone(t, st, tbl) {
			t.Errorf("fresh v2 store should not have side table %q", tbl)
		}
	}
}
