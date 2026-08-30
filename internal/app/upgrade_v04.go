package app

import (
	"strings"
	"time"
)

// Adoption steps for the v0.4 release line — the code that folds an OLDER shape into the current
// one. It is deliberately one file, and the file name carries its own expiry date.
//
// The project's contract for this (docs/adr/0013-v2-schema-consolidation.md, and the note at the
// top of migrate.go) is that versioned data-move steps never survive a major boundary: at v0.5 the
// complete v0.4 result becomes the base schema, this file is deleted whole, its call in Store.init
// goes with it, and requireSchemaBaseline starts refusing databases that never ran a v0.4 release
// — a refusal with a message beats a silent, half-adopted upgrade. So: nothing else may live here,
// and nothing here may be called from anywhere but init. Adding a step means adding a function
// below plus one line in init; retiring the line means `git rm` and two deletions.
//
// Each step is idempotent, guarded by its own marker key in meta rather than by "does this look
// empty" — an empty table is a legitimate steady state (the operator deleted every row) and must
// never be repopulated by the next restart.

// upgradeV04 runs every v0.4-line adoption step, in order. Called from Store.init after the schema
// exists and before anything reads it.
func (s *Store) upgradeV04() error {
	return s.importLegacyAnnouncement()
}

// importLegacyAnnouncement folds the single announcement that lived in five meta keys
// (announcement_enabled/popup/level/title/content) into the first row of the announcements table.
//
// Field mapping is chosen so upgrade day looks like no change at all: scope='home' and
// audience='all' reproduce today's behaviour exactly, dismissible=0 keeps the banner
// non-closable, and level/enabled/popup go through the same normalizers the old read path used,
// so an empty level still reads as notice and "on"/"yes"/"1" still read as true.
//
// It also writes announcement_enabled=false. The five keys stay on disk — old_base/old_user/
// old_pass set the precedent that a retired setting is left inert rather than deleted — but a
// rollback to the previous binary must not resurrect a nine-day-old incident banner that nobody
// can take down from the new admin page. Silence is a defensible rollback state; a stale warning
// is not. The release notes say so: rollback is clean, not lossless.
func (s *Store) importLegacyAnnouncement() error {
	// Every read happens BEFORE Begin. On SQLite the pool is capped at one connection
	// (SetMaxOpenConns(1)), so a GetSetting issued while this function holds a transaction would
	// wait for a connection the transaction itself is holding — a hang at boot, not an error.
	var (
		title   = strings.TrimSpace(s.GetSetting("announcement_title", ""))
		content = strings.TrimSpace(s.GetSetting("announcement_content", ""))
		level   = normalizeAnnouncementLevel(s.GetSetting("announcement_level", "notice"))
		enabled = settingBool(s.GetSetting("announcement_enabled", ""), false)
		popup   = settingBool(s.GetSetting("announcement_popup", ""), false)
	)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Inserting the marker IS the guard — there is deliberately no "have we already done this?"
	// read in front of it. A read plus a write is two gates that can disagree (a marker row holding
	// an empty string satisfies one and not the other), and it is not atomic: on a rolling restart
	// against one shared Postgres, two instances both read "not yet" and both import. One INSERT
	// with a primary key settles both problems — the loser blocks until the winner commits, then
	// sees 0 rows affected and stops.
	res, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES('announcements_imported','1')
		ON CONFLICT(k) DO NOTHING`))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil // another instance imported it while we were starting
	}
	if title != "" || content != "" {
		now := time.Now().UTC().Format(announcementStamp)
		if _, err := tx.Exec(s.bind(`INSERT INTO announcements(
			level,title,content,ord,enabled,popup,dismissible,scope,audience,
			starts_at,ends_at,created_at,created_by,updated_at)
			VALUES(?,?,?,0,?,?,0,'home','all','','',?,'',?)`),
			level, title, content, boolInt(enabled), boolInt(popup), now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES('announcement_enabled','false')
			ON CONFLICT(k) DO UPDATE SET v=excluded.v`)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
