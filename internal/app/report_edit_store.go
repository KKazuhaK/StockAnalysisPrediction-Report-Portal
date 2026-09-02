package app

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Storage for hand-written reports (ADR 0026).
//
// There is no table here. A hand-written report is an ordinary row of `reports` carrying the manual
// version, which is what makes the whole feature small: the read path, the version switcher, the
// diff, the export, the tracking items and the storage cleanup all already work on it and none of
// them needed a line of change. What this file adds is the two things the ingest path gets from a
// run and a hand-written report has no run to get them from — an identity that cannot be taken by a
// workflow, and a viewer list somebody chooses on purpose.

// ErrReportExists is returned when a hand-written report would take an identity that is already
// occupied. It carries the occupant's id, because the only useful thing to tell the author is
// "that one already exists, here it is" — and the answer to a collision is almost always to go and
// edit the existing report rather than to invent a different title for this one.
type ErrReportExists struct{ ID int64 }

func (e ErrReportExists) Error() string { return "a report with this identity already exists" }

// ErrReportStale is returned when a save was computed against a version of the report that is no
// longer the current one. Reports have no revision counter, so `sent_at` serves as the token — see
// manualInstant for why that is sound only because hand-written saves write it at nanosecond
// precision.
var ErrReportStale = errors.New("the report changed since it was loaded")

// manualInstant stamps a hand-written save. RFC3339Nano rather than the RFC3339 that ingest uses
// (ingestInstant), because this value is also the concurrency token: at second precision two
// editors saving within the same second would each compute a token equal to the other's, and the
// second save would be accepted as if it had seen the first. It is still an RFC3339 timestamp, so
// every reader of sent_at — the list ordering, the version switcher's staleness column, the storage
// cleanup's cutoff — takes it unchanged.
func manualInstant() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// CreateManualReport writes a new hand-written report and returns its id.
//
// The insert is the collision check. A probe-then-insert would be two statements with a gap between
// them, and the gap is exactly where two authors publishing the same title on the same day land; DO
// NOTHING makes the unique identity index the arbiter, and the follow-up SELECT only runs on the
// losing branch, to name the occupant. This also avoids reading a driver's error string to find out
// what happened, which is the other way to write this and is not portable between SQLite and
// Postgres.
func (s *Store) CreateManualReport(r Rep) (int64, error) {
	r.Version = manualVersionName
	r.Time = manualInstant()
	var id int64
	err := s.queryRow(`
		INSERT INTO reports(title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html,version,author)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(`+reportIdentExpr+`) DO NOTHING
		RETURNING id`,
		r.Title, r.Symbol, r.Name, r.RType, r.Date, r.Kind, "", r.Source, r.Time, r.MD, "",
		r.Version, r.Author).Scan(&id)
	if err == sql.ErrNoRows {
		var prev int64
		s.queryRow("SELECT id FROM reports WHERE "+reportIdentWhere,
			r.Symbol, r.Date, r.RType, r.Title, r.Version).Scan(&prev)
		return 0, ErrReportExists{ID: prev}
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateManualReport rewrites one hand-written report in place, by id, keeping what it used to say.
//
// By id, and NOT through UpsertReport, because the editable fields include the identity ones: an
// author who corrects the title of their own report would otherwise have it written to the new
// identity as a second row, leaving the original behind as an orphan that still appears in every
// list. Updating by id moves the row instead of copying it.
//
// expect is the sent_at the editor loaded. A mismatch means somebody else saved in between, and the
// update touches nothing rather than overwriting words the author never saw.
//
// keep bounds how many superseded versions this report retains; 0 means unlimited, which is the
// shipped state. It is passed in rather than read here because reading a setting through the pool
// while this transaction is open deadlocks on SQLite — see the rollback comment below, which is the
// same hazard from the other direction.
func (s *Store) UpdateManualReport(id int64, r Rep, expect string, keep int) (string, error) {
	now := manualInstant()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	// The pre-image, read on the transaction and under the SAME three-part predicate the UPDATE
	// uses. Two things follow from that. It has to be read BEFORE the UPDATE, because the UPDATE is
	// what destroys it — a snapshot taken from the handler's earlier read would be a value nothing
	// guarantees is still current. And no rows here means exactly the staleness the UPDATE would
	// have reported, so the save is refused before anything is written at all.
	var prev Rep
	if err := tx.QueryRow(s.bind(`SELECT COALESCE(title,''), COALESCE(symbol,''), COALESCE(name,''),
		COALESCE(rtype,''), COALESCE(rdate,''), COALESCE(kind,''), COALESCE(source,''),
		COALESCE(body_md,''), COALESCE(sent_at,''), COALESCE(author,'')
		FROM reports WHERE id=? AND version=? AND sent_at=?`), id, manualVersionName, expect).
		Scan(&prev.Title, &prev.Symbol, &prev.Name, &prev.RType, &prev.Date, &prev.Kind,
			&prev.Source, &prev.MD, &prev.Time, &prev.Author); err != nil {
		tx.Rollback()
		if err == sql.ErrNoRows {
			return "", ErrReportStale
		}
		return "", err
	}
	res, err := tx.Exec(s.bind(`UPDATE reports SET title=?, symbol=?, name=?, rtype=?, rdate=?, kind=?,
		source=?, body_md=?, body_html='', sent_at=?, author=?
		WHERE id=? AND version=? AND sent_at=?`),
		r.Title, r.Symbol, r.Name, r.RType, r.Date, r.Kind, r.Source, r.MD, now, r.Author,
		id, manualVersionName, expect)
	if err != nil {
		// Rolled back BEFORE the probe, not by a deferred rollback afterwards: on SQLite the pool is
		// one connection wide, so a read issued while this transaction is still open waits for the
		// connection the transaction is holding, and the two wait on each other forever.
		tx.Rollback()
		// A unique-index violation lands here: the author renamed this report onto an identity
		// another one already holds. Reported as the same collision the create path reports, so the
		// editor has one case to handle rather than two spellings of the same problem — and found by
		// asking the database who holds it, rather than by reading a driver's error text, which
		// SQLite and Postgres word differently.
		if prev, taken := s.reportIdentHolder(r, id); taken {
			return "", ErrReportExists{ID: prev}
		}
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		return "", ErrReportStale
	}
	if err := s.snapshot(tx, id, prev, r, keep); err != nil {
		tx.Rollback()
		return "", err
	}
	// The viewer list is keyed by rdate as well as report id (report_viewers is denormalized on it so
	// the list page's sort is an index walk), so moving a report to another date has to move its
	// audience with it, in the same transaction. Otherwise a failure here leaves the report readable
	// by nobody — silently, because a viewer row whose date no longer matches simply never joins.
	if _, err := tx.Exec(s.bind("UPDATE report_viewers SET rdate=? WHERE report_id=?"), r.Date, id); err != nil {
		tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return now, nil
}

// snapshot files the superseded version of a report, on the transaction that superseded it.
//
// In the transaction and not beside it, so the two cannot come apart: a save that fails leaves no
// history, and a save that succeeds cannot fail to be recorded. It runs after the staleness check,
// which is what makes a refused save leave the log untouched.
//
// A save that changed nothing writes nothing. An author who presses save twice, or corrects the
// audience without touching a word, would otherwise bury the version they actually want under
// identical copies of the one they are looking at — the same argument the cleanup console makes for
// not recording a pass that deleted nothing.
func (s *Store) snapshot(tx *sql.Tx, id int64, prev, next Rep, keep int) error {
	if !reportContentDiffers(prev, next) {
		return nil
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO report_revisions
		(report_id, saved_at, author, title, symbol, name, rtype, rdate, kind, source, body_md)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`),
		id, prev.Time, prev.Author, prev.Title, prev.Symbol, prev.Name, prev.RType, prev.Date,
		prev.Kind, prev.Source, prev.MD); err != nil {
		return err
	}
	if keep <= 0 {
		return nil // unlimited, the shipped state
	}
	// The ring trim the recurring-run log already uses. Not `DELETE ... LIMIT`, which is a syntax
	// error on this SQLite build and does not exist in Postgres at all; and report_id is named twice
	// because bind() numbers placeholders positionally, so a repeated parameter is passed twice.
	_, err := tx.Exec(s.bind(`DELETE FROM report_revisions WHERE report_id=? AND id NOT IN
		(SELECT id FROM report_revisions WHERE report_id=? ORDER BY id DESC LIMIT ?)`), id, id, keep)
	return err
}

// reportContentDiffers reports whether a save changed anything a revision would record. The audience
// is deliberately not part of it: who a report is for is not what it said, it is stored in another
// table, and a revision that cannot restore it should not claim to have captured it.
func reportContentDiffers(a, b Rep) bool {
	return a.MD != b.MD || a.Title != b.Title || a.Symbol != b.Symbol || a.Name != b.Name ||
		a.RType != b.RType || a.Date != b.Date || a.Source != b.Source
}

// reportIdentHolder answers "who already holds the identity this report is trying to take", other
// than the report itself. Used to turn a driver's unique-violation error — whose text differs
// between SQLite and Postgres — into the same collision the create path reports.
func (s *Store) reportIdentHolder(r Rep, self int64) (int64, bool) {
	var id int64
	err := s.queryRow("SELECT id FROM reports WHERE "+reportIdentWhere+" AND id<>?",
		r.Symbol, r.Date, r.RType, r.Title, manualVersionName, self).Scan(&id)
	return id, err == nil
}

// ManualSiblingOf finds the hand-written form of a report, if one has been written.
//
// Matched on (symbol, rdate, rtype) and NOT on title, exactly as VersionsOfReport groups the version
// switcher, and for the same reason: a hand-written report is allowed to be retitled, and matching
// on the title would make the editor offer to create a second one every time somebody had. Newest
// wins where several exist, which is the same tie-break the switcher applies.
func (s *Store) ManualSiblingOf(rep Rep) (int64, bool) {
	var id int64
	err := s.queryRow(`SELECT id FROM reports WHERE symbol=? AND rdate=? AND rtype=? AND version=?
		ORDER BY id DESC`, rep.Symbol, rep.Date, rep.RType, manualVersionName).Scan(&id)
	return id, err == nil
}

// ReportViewers lists the principals a report is addressed to, for the editor to load into its
// audience picker. Same encoding as version_grants and announcement grants: "g:<id>" or "u:<name>".
func (s *Store) ReportViewers(reportID int64) []string {
	rows, err := s.query("SELECT principal FROM report_viewers WHERE report_id=? ORDER BY principal",
		reportID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// SetReportViewers replaces one report's whole audience in a transaction.
//
// Replaces rather than adds, unlike AddReportViewer — which is additive because a second person
// asking for the same generated report JOINS its readers. A hand-written report's audience is a
// decision somebody made in a form, so removing a recipient there has to actually remove them; an
// additive save would make the picker able to widen an audience but never to narrow one.
func (s *Store) SetReportViewers(reportID int64, rdate string, principals []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM report_viewers WHERE report_id=?"), reportID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range principals {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := tx.Exec(s.bind(
			"INSERT INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?) ON CONFLICT(principal,rdate,report_id) DO NOTHING"),
			p, rdate, reportID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IsManualReport reports whether an id names a hand-written report. The editor's edit and delete
// paths both ask this before writing: everything else in the reports table is the record of a run,
// and a person editing one of those in place is the thing ADR 0026 exists to prevent.
func (s *Store) IsManualReport(id int64) bool {
	var v string
	err := s.queryRow("SELECT COALESCE(version,'') FROM reports WHERE id=?", id).Scan(&v)
	return err == nil && v == manualVersionName
}
