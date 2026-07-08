package app

import (
	"fmt"
	"log"
)

// migrateV1toV2 is the one-shot v0.1 -> v0.2 schema upgrade (schema generation 1 -> 2, ADR 0013):
// it folds the six 1:1 side tables into parent columns, drops the dead user_group_members table
// and links.collapsed column, and renames reports.rowid -> id. Every step is individually guarded
// (guarded ADD COLUMN, table/column-existence checks, DROP ... IF EXISTS, portable
// correlated-subquery backfills valid on both SQLite and Postgres), so it is idempotent and a
// no-op on a database already at the target shape — including a fresh base-schema database. The
// catch-up ADD COLUMNs let ANY v0.1.x database upgrade straight to v0.2 without first stepping
// through the last v0.1 release.
//
// This whole FILE is disposable. At the next major boundary (v0.3.0), per the squash contract:
// delete this file, move the idx_batch_jobs_run_at index (below) into createBaseSchema in
// store.go, and point migrate() (migrate.go) at the next step. Nothing else in the main program
// depends on it.
func (s *Store) migrateV1toV2() error {
	log.Printf("migrateV1toV2: consolidating schema — fold 6 side tables, drop dead, rename reports.id")
	// Kept columns a database behind the last v0.1 release may still lack, plus the new folded
	// columns. Guarded, so each is a no-op where the column already exists.
	addCols := []string{
		// catch-up: columns already in the base schema of the last v0.1 line
		`ALTER TABLE links ADD COLUMN group_id INTEGER DEFAULT 0`,
		`ALTER TABLE chat_conversations ADD COLUMN starred INTEGER DEFAULT 0`,
		`ALTER TABLE user_groups ADD COLUMN urgent_unlimited INTEGER DEFAULT 0`,
		`ALTER TABLE user_groups ADD COLUMN is_default INTEGER DEFAULT 0`,
		`ALTER TABLE user_groups ADD COLUMN allow_urgent INTEGER`,
		`ALTER TABLE user_groups ADD COLUMN max_queued INTEGER`,
		`ALTER TABLE user_groups ADD COLUMN run_window TEXT`,
		// folded columns: their source side tables are copied then dropped below
		`ALTER TABLE users ADD COLUMN display_name TEXT`,
		`ALTER TABLE users ADD COLUMN email TEXT`,
		`ALTER TABLE users ADD COLUMN active INTEGER DEFAULT 1`,
		`ALTER TABLE users ADD COLUMN last_login TEXT`,
		`ALTER TABLE users ADD COLUMN group_id BIGINT`,
		`ALTER TABLE user_groups ADD COLUMN priority TEXT`,
		`ALTER TABLE batch_targets ADD COLUMN ord INTEGER`,
		`ALTER TABLE batch_jobs ADD COLUMN priority TEXT DEFAULT 'normal'`,
		`ALTER TABLE batch_jobs ADD COLUMN run_at TEXT DEFAULT ''`,
	}
	for _, ddl := range addCols {
		if _, err := s.exec(ddl); err != nil && !duplicateColumnErr(err) {
			return fmt.Errorf("add column [%s]: %w", ddl, err)
		}
	}

	// Backfill each folded column from its side table, then drop the side table. Scoped
	// WHERE ... IN (SELECT ...) so a parent row without a side-table entry keeps the column
	// default (a user with no profile stays active=1 / empty name; an ungrouped user stays
	// group_id=NULL = the Default group; a job with no queue/schedule row stays 'normal' / '').
	folds := []struct{ src, update string }{
		{"user_profiles", `UPDATE users SET
			display_name=(SELECT p.display_name FROM user_profiles p WHERE p.username=users.username),
			email=(SELECT p.email FROM user_profiles p WHERE p.username=users.username),
			active=COALESCE((SELECT p.active FROM user_profiles p WHERE p.username=users.username),1),
			last_login=(SELECT p.last_login FROM user_profiles p WHERE p.username=users.username)
			WHERE username IN (SELECT username FROM user_profiles)`},
		{"user_primary_group", `UPDATE users SET
			group_id=(SELECT g.group_id FROM user_primary_group g WHERE g.username=users.username)
			WHERE username IN (SELECT username FROM user_primary_group)`},
		{"group_priority", `UPDATE user_groups SET
			priority=(SELECT gp.priority FROM group_priority gp WHERE gp.group_id=user_groups.id)
			WHERE id IN (SELECT group_id FROM group_priority)`},
		{"target_order", `UPDATE batch_targets SET
			ord=(SELECT o.ord FROM target_order o WHERE o.target_id=batch_targets.id)
			WHERE id IN (SELECT target_id FROM target_order)`},
		{"job_queue", `UPDATE batch_jobs SET
			priority=COALESCE((SELECT q.priority FROM job_queue q WHERE q.job_id=batch_jobs.id),'normal')
			WHERE id IN (SELECT job_id FROM job_queue)`},
		{"job_schedule", `UPDATE batch_jobs SET
			run_at=COALESCE((SELECT sc.run_at FROM job_schedule sc WHERE sc.job_id=batch_jobs.id),'')
			WHERE id IN (SELECT job_id FROM job_schedule)`},
	}
	for _, f := range folds {
		if !s.tableExists(f.src) {
			continue
		}
		res, err := s.exec(f.update)
		if err != nil {
			return fmt.Errorf("backfill from %s: %w", f.src, err)
		}
		n, _ := res.RowsAffected()
		if _, err := s.exec("DROP TABLE IF EXISTS " + f.src); err != nil {
			return fmt.Errorf("drop %s: %w", f.src, err)
		}
		log.Printf("migrateV1toV2: folded %s into its parent column (%d rows), dropped side table", f.src, n)
	}

	// Dead many-to-many membership table. The single primary group (users.group_id) is
	// authoritative (ADR 0010); before dropping, defensively reconcile one membership into any
	// still-ungrouped user (prod holds 0 rows here).
	if s.tableExists("user_group_members") {
		if _, err := s.exec(`UPDATE users SET
			group_id=(SELECT m.group_id FROM user_group_members m WHERE m.username=users.username LIMIT 1)
			WHERE group_id IS NULL AND username IN (SELECT username FROM user_group_members)`); err != nil {
			return fmt.Errorf("reconcile user_group_members: %w", err)
		}
		if _, err := s.exec(`DROP TABLE IF EXISTS user_group_members`); err != nil {
			return fmt.Errorf("drop user_group_members: %w", err)
		}
		log.Printf("migrateV1toV2: dropped dead table user_group_members")
	}

	// Dead links.collapsed column (superseded by link_groups; no reader).
	if s.columnExists("links", "collapsed") {
		if _, err := s.exec(`ALTER TABLE links DROP COLUMN collapsed`); err != nil {
			return fmt.Errorf("drop links.collapsed: %w", err)
		}
		log.Printf("migrateV1toV2: dropped dead column links.collapsed")
	}

	// Rename the reports surrogate key rowid -> id (every table's PK is now `id`). Guarded so a
	// database already at the target shape skips it. On SQLite a missed `rowid` reference silently
	// falls back to the implicit rowid alias and keeps working; on Postgres it fails hard — so the
	// TEST_POSTGRES_DSN path is the real guard against a missed rename elsewhere in the code.
	if s.columnExists("reports", "rowid") && !s.columnExists("reports", "id") {
		if _, err := s.exec(`ALTER TABLE reports RENAME COLUMN rowid TO id`); err != nil {
			return fmt.Errorf("rename reports.rowid -> id: %w", err)
		}
		log.Printf("migrateV1toV2: renamed reports.rowid -> id")
	}

	// The run_at partial index lives here (not in createBaseSchema) because on an upgrading
	// database batch_jobs.run_at does not exist until the fold above adds it. At the next squash
	// (v0.3.0) this moves into createBaseSchema alongside the other batch_jobs indexes.
	if _, err := s.exec(`CREATE INDEX IF NOT EXISTS idx_batch_jobs_run_at ON batch_jobs(run_at) WHERE run_at <> ''`); err != nil {
		return fmt.Errorf("create idx_batch_jobs_run_at: %w", err)
	}
	return nil
}
