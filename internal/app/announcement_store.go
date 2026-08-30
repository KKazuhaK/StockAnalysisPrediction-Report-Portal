package app

import (
	"database/sql"
	"strings"
	"time"
)

// Persistence for site announcements (ADR 0025). The reader-facing filter lives in
// announcement_api.go: this file only stores and returns rows.
//
// Every write that touches more than one row goes through a transaction. Reordering used to be
// modelled on apiLinkLayout, which updates row by row and swallows the error — a half-renumbered
// table then sorts by ORDER BY ord,id into an interleaving nobody chose.

// announcementStamp is the format for created_at/updated_at. Nanoseconds, unlike the RFC3339 the
// rest of the store uses, because updated_at is not only shown — the editor sends it back as an
// optimistic-concurrency token. At second precision two saves inside the same second carry the
// same token, so the stale one is indistinguishable from the fresh one and wins by being second:
// exactly the lost audience edit the check exists to prevent.
const announcementStamp = time.RFC3339Nano

// Announcement is one row of the announcements table. Grants is populated only by the admin
// read path (AnnouncementsWithGrants); the reader-facing payload never carries it.
type Announcement struct {
	ID          int64
	Level       string
	Title       string
	Content     string
	Ord         int
	Enabled     bool
	Popup       bool
	Dismissible bool
	Scope       string
	Audience    string
	StartsAt    string
	EndsAt      string
	CreatedAt   string
	CreatedBy   string
	UpdatedAt   string
	Grants      []string
}

const announcementCols = `id,COALESCE(level,''),COALESCE(title,''),COALESCE(content,''),
	COALESCE(ord,0),COALESCE(enabled,1),COALESCE(popup,0),COALESCE(dismissible,0),
	COALESCE(scope,''),COALESCE(audience,''),COALESCE(starts_at,''),COALESCE(ends_at,''),
	COALESCE(created_at,''),COALESCE(created_by,''),COALESCE(updated_at,'')`

func scanAnnouncement(sc interface{ Scan(...any) error }) (Announcement, error) {
	var a Announcement
	var enabled, popup, dismissible sql.NullInt64
	err := sc.Scan(&a.ID, &a.Level, &a.Title, &a.Content, &a.Ord, &enabled, &popup, &dismissible,
		&a.Scope, &a.Audience, &a.StartsAt, &a.EndsAt, &a.CreatedAt, &a.CreatedBy, &a.UpdatedAt)
	// NULL reads as the permissive default for enabled and the restrictive one for the rest, which
	// is what a row written before ensureColumns added the column should mean.
	a.Enabled = !enabled.Valid || enabled.Int64 != 0
	a.Popup = popup.Valid && popup.Int64 != 0
	a.Dismissible = dismissible.Valid && dismissible.Int64 != 0
	a.Level = normalizeAnnouncementLevel(a.Level)
	a.Scope = normalizeAnnouncementScope(a.Scope)
	a.Audience = normalizeAnnouncementAudience(a.Audience)
	return a, err
}

// Announcements returns every row in display order. ord first, then id, so two rows that were
// never dragged apart still have a stable order rather than the driver's.
func (s *Store) Announcements() []Announcement {
	rows, err := s.query("SELECT " + announcementCols + " FROM announcements ORDER BY ord,id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GetAnnouncement reads one row, or nil when it is gone.
func (s *Store) GetAnnouncement(id int64) *Announcement {
	a, err := scanAnnouncement(s.queryRow("SELECT "+announcementCols+" FROM announcements WHERE id=?", id))
	if err != nil {
		return nil
	}
	return &a
}

// AllAnnouncementGrants loads the whole grant table into memory, keyed by announcement. Both call
// sites want every row anyway — the admin list renders all of them, and the reader path intersects
// them with the viewer's principals — so this is one query instead of one per announcement.
func (s *Store) AllAnnouncementGrants() map[int64][]string {
	out := map[int64][]string{}
	rows, err := s.query("SELECT announcement_id,principal FROM announcement_grants ORDER BY announcement_id,principal")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var principal string
		if rows.Scan(&id, &principal) == nil {
			out[id] = append(out[id], principal)
		}
	}
	return out
}

// AnnouncementsWithGrants is the admin list: every row with its principals attached.
func (s *Store) AnnouncementsWithGrants() []Announcement {
	list := s.Announcements()
	grants := s.AllAnnouncementGrants()
	for i := range list {
		list[i].Grants = grants[list[i].ID]
	}
	return list
}

func (s *Store) CountAnnouncements() int {
	var n int
	s.queryRow("SELECT COUNT(*) FROM announcements").Scan(&n)
	return n
}

// nextAnnouncementOrd appends after the last row. COALESCE(...,-1)+1 so the first row lands on 0,
// matching the dense 0..N-1 numbering ReorderAnnouncements rewrites.
func (s *Store) nextAnnouncementOrd() int {
	var ord int
	s.queryRow("SELECT COALESCE(MAX(ord),-1)+1 FROM announcements").Scan(&ord)
	return ord
}

// AddAnnouncement appends a row and returns its id. Grants are written separately, by the same
// handler, inside SetAnnouncementGrants.
func (s *Store) AddAnnouncement(a Announcement) (int64, error) {
	now := time.Now().UTC().Format(announcementStamp)
	return s.insertID(`INSERT INTO announcements(
		level,title,content,ord,enabled,popup,dismissible,scope,audience,
		starts_at,ends_at,created_at,created_by,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Level, a.Title, a.Content, s.nextAnnouncementOrd(),
		boolInt(a.Enabled), boolInt(a.Popup), boolInt(a.Dismissible), a.Scope, a.Audience,
		a.StartsAt, a.EndsAt, now, a.CreatedBy, now)
}

// UpdateAnnouncement replaces every editable field except ord, which only the drag owns.
func (s *Store) UpdateAnnouncement(a Announcement) error {
	_, err := s.exec(`UPDATE announcements SET level=?,title=?,content=?,enabled=?,popup=?,
		dismissible=?,scope=?,audience=?,starts_at=?,ends_at=?,updated_at=? WHERE id=?`,
		a.Level, a.Title, a.Content, boolInt(a.Enabled), boolInt(a.Popup), boolInt(a.Dismissible),
		a.Scope, a.Audience, a.StartsAt, a.EndsAt, time.Now().UTC().Format(announcementStamp), a.ID)
	return err
}

// SetAnnouncementFlag flips one boolean from the list row's inline switch. It is deliberately NOT
// a whole-row PUT: re-sending a row to toggle a switch would also write back the title, body and
// audience the admin's page loaded minutes ago, quietly reverting another admin's edit — and on a
// row that carries an audience, that turns a decorative toggle into a disclosure change.
// updated_at is left alone for the same reason the reader keys dismissal on content: flipping a
// switch is not an edit of what the announcement says.
func (s *Store) SetAnnouncementFlag(id int64, field string, on bool) error {
	switch field { // never interpolate a caller's string into DDL/DML
	case "enabled", "popup":
	default:
		return sql.ErrNoRows
	}
	_, err := s.exec("UPDATE announcements SET "+field+"=? WHERE id=?", boolInt(on), id)
	return err
}

// SetAnnouncementGrants replaces one announcement's principal list, mirroring SetVersionGrants.
func (s *Store) SetAnnouncementGrants(id int64, principals []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM announcement_grants WHERE announcement_id=?"), id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range principals {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := tx.Exec(s.bind("INSERT INTO announcement_grants(announcement_id,principal) VALUES(?,?)"),
			id, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAnnouncement drops the row and its grants together. Two statements in one transaction, so
// a crash between them cannot leave principals pointing at an announcement that no longer exists —
// ids are not reused by either driver, but an orphan row is still a row nothing will ever clean up.
func (s *Store) DeleteAnnouncement(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM announcement_grants WHERE announcement_id=?"), id); err != nil {
		return err
	}
	if _, err := tx.Exec(s.bind("DELETE FROM announcements WHERE id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

// ReorderAnnouncements writes the whole order at once: ord becomes the index in ids, dense from 0.
// One transaction, because a partial renumber is worse than a failed one — ORDER BY ord,id would
// then return an interleaving of the old and new orders that no admin ever asked for.
// The caller (apiAnnouncementReorder) has already checked that ids covers exactly the current set.
func (s *Store) ReorderAnnouncements(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(s.bind("UPDATE announcements SET ord=? WHERE id=?"), i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
