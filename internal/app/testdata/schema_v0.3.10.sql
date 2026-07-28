-- The schema a v0.3.10 database actually has, dumped from a database created by the
-- v0.3.10 binary itself (git worktree of the tag, `adduser` to run its init()).
--
-- It is committed as text rather than as a .db file so it is reviewable and diffable, and
-- it is the REAL shape rather than a hand-written approximation — the whole value of the
-- upgrade test is that nobody guessed what production looks like. Production runs v0.3.10;
-- v0.4.0 was never deployed, so this is the one upgrade path that has to work.

CREATE TABLE api_tokens(
			id INTEGER PRIMARY KEY AUTOINCREMENT, token TEXT UNIQUE, token_hash TEXT, token_prefix TEXT,
			name TEXT, scope TEXT DEFAULT 'all',
			created_at TEXT, expires_at TEXT, last_used_at TEXT);
CREATE TABLE app_files(
			app_id TEXT, path TEXT, ctype TEXT, content BLOB, PRIMARY KEY(app_id, path));
CREATE TABLE apps(
			id TEXT PRIMARY KEY, name TEXT, icon TEXT, version TEXT, entry TEXT,
			scopes TEXT, created_at TEXT);
CREATE TABLE batch_items(
			id INTEGER PRIMARY KEY AUTOINCREMENT, job_id BIGINT, row_index INTEGER, inputs TEXT, status TEXT DEFAULT 'queued',
			attempts INTEGER DEFAULT 0, run_id TEXT, conversation_id TEXT, task_id TEXT,
			dify_started_at TEXT DEFAULT '', error TEXT, started_at TEXT, finished_at TEXT);
CREATE TABLE batch_jobs(
			id INTEGER PRIMARY KEY AUTOINCREMENT, target_id BIGINT, status TEXT, concurrency INTEGER DEFAULT 1, max_retries INTEGER DEFAULT 0,
			total INTEGER DEFAULT 0, succeeded INTEGER DEFAULT 0, partial INTEGER DEFAULT 0, failed INTEGER DEFAULT 0,
			created_by TEXT, created_at TEXT, started_at TEXT, finished_at TEXT,
			priority TEXT DEFAULT 'normal', run_at TEXT DEFAULT '', run_preset TEXT DEFAULT '');
CREATE TABLE batch_targets(
			id INTEGER PRIMARY KEY AUTOINCREMENT, plugin_slug TEXT, name TEXT, config TEXT, created_at TEXT, ord INTEGER,
			surfaces TEXT DEFAULT '');
CREATE TABLE chat_conversations(
			id INTEGER PRIMARY KEY AUTOINCREMENT, target_id BIGINT, conv_id TEXT DEFAULT '', created_by TEXT,
			title TEXT DEFAULT '', created_at TEXT, updated_at TEXT, starred INTEGER DEFAULT 0);
CREATE TABLE cleanup_runs(
			id INTEGER PRIMARY KEY AUTOINCREMENT, ran_at TEXT, trigger TEXT, dry_run INTEGER DEFAULT 0, ok INTEGER DEFAULT 1, error TEXT DEFAULT '',
			batch_deleted INTEGER DEFAULT 0, tokens_deleted INTEGER DEFAULT 0, reports_deleted INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0);
CREATE TABLE kind_config(kind TEXT PRIMARY KEY, color TEXT);
CREATE TABLE link_groups(
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT DEFAULT '', mode TEXT DEFAULT 'row', show_label INTEGER DEFAULT 1, icon TEXT DEFAULT '', ord INTEGER DEFAULT 0, visible INTEGER DEFAULT 1);
CREATE TABLE links(
			id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, url TEXT, icon TEXT DEFAULT '', new_tab INTEGER DEFAULT 1,
			ord INTEGER DEFAULT 0, group_id INTEGER DEFAULT 0, visible INTEGER DEFAULT 1);
CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE plugins(
			id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT UNIQUE, name TEXT, version TEXT, spec TEXT,
			enabled INTEGER DEFAULT 1, source TEXT DEFAULT 'imported', imported_at TEXT);
CREATE TABLE priority_tickets(
			username TEXT PRIMARY KEY, remaining INTEGER DEFAULT 0, period_start TEXT);
CREATE TABLE recurring_runs(
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id BIGINT, job_id BIGINT, fired_at TEXT);
CREATE TABLE recurring_tasks(
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, target_id BIGINT, rows TEXT DEFAULT '[]',
			concurrency INTEGER DEFAULT 1, priority TEXT DEFAULT '', max_retries INTEGER DEFAULT 0,
			freq TEXT, at_time TEXT, weekday INTEGER DEFAULT 1, monthday INTEGER DEFAULT 1,
			enabled INTEGER DEFAULT 1, created_by TEXT, created_at TEXT, last_fired TEXT DEFAULT '');
CREATE TABLE reports(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT, symbol TEXT, name TEXT, rtype TEXT, rdate TEXT,
			kind TEXT, run_id TEXT,
			source TEXT, sent_at TEXT, body_md TEXT, body_html TEXT);
CREATE TABLE run_presets(
			id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, freq TEXT, intervals TEXT,
			on_overrun TEXT DEFAULT 'next', enabled INTEGER DEFAULT 1, invert INTEGER DEFAULT 0, ord INTEGER DEFAULT 0);
CREATE TABLE stocks(code TEXT PRIMARY KEY, name TEXT, updated_at TEXT);
CREATE TABLE tracking_items(
			id INTEGER PRIMARY KEY AUTOINCREMENT, report_id BIGINT, symbol TEXT, itype TEXT, content TEXT,
			status TEXT DEFAULT 'pending', review_point TEXT, created_at TEXT);
CREATE TABLE type_config(
			name TEXT PRIMARY KEY, kind TEXT, ord INTEGER DEFAULT 0, is_summary INTEGER DEFAULT 0, label TEXT);
CREATE TABLE user_groups(
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE, description TEXT, created_at TEXT, weight INTEGER,
			urgent_unlimited INTEGER, is_default INTEGER DEFAULT 0,
			allow_urgent INTEGER, max_queued INTEGER, run_window TEXT, priority TEXT);
CREATE TABLE users(
			username TEXT PRIMARY KEY, password_hash TEXT, role TEXT DEFAULT 'user',
			display_name TEXT, email TEXT, active INTEGER DEFAULT 1, last_login TEXT, group_id BIGINT,
			session_rev BIGINT DEFAULT 0);
CREATE TABLE webhooks(
			id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT, events TEXT, secret TEXT, active INTEGER DEFAULT 1,
			created_at TEXT, last_status INTEGER DEFAULT 0, last_error TEXT, last_delivered_at TEXT);
CREATE UNIQUE INDEX idx_api_tokens_hash ON api_tokens(token_hash) WHERE token_hash IS NOT NULL;
CREATE INDEX idx_batch_items_job ON batch_items(job_id, status);
CREATE INDEX idx_batch_items_row0 ON batch_items(row_index, job_id);
CREATE INDEX idx_batch_jobs_created ON batch_jobs(created_at);
CREATE INDEX idx_batch_jobs_run_at ON batch_jobs(run_at) WHERE run_at <> '';
CREATE INDEX idx_batch_jobs_status ON batch_jobs(status, finished_at);
CREATE INDEX idx_chat_conv_user ON chat_conversations(created_by, target_id, updated_at);
CREATE INDEX idx_recurring_runs_task ON recurring_runs(task_id, id);
CREATE INDEX idx_reports_date_time ON reports(rdate,sent_at);
CREATE UNIQUE INDEX idx_reports_ident ON reports(symbol, rdate, rtype, title);
CREATE INDEX idx_reports_symbol_date_time ON reports(symbol,rdate,sent_at);
CREATE INDEX idx_stocks_name ON stocks(name);
CREATE INDEX idx_track_id ON tracking_items(report_id);
CREATE INDEX idx_track_sym ON tracking_items(symbol, status);
