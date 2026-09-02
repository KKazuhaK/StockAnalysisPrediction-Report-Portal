package app

import "time"

// The edit history of a hand-written report (ADR 0026).
//
// A revision is one SUPERSEDED version. It is written by Store.snapshot, on the same transaction
// that superseded it, so there is exactly one place history is created and it cannot come apart
// from the save that caused it. What lives here is the read side, the retention side, and the
// restore — which is not a fourth way to write a report but a save like any other, computed from
// what a revision holds.

// Revision is one prior state of a report: what it said, who wrote that, and when.
type Revision struct {
	ID      int64  `json:"id"`
	SavedAt string `json:"savedAt"` // when the content in this row was written, not when it was replaced
	Author  string `json:"author"`
	Title   string `json:"title"`
	Symbol  string `json:"symbol"`
	Name    string `json:"name"`
	RType   string `json:"subtype"`
	Date    string `json:"date"`
	Kind    string `json:"-"`
	Source  string `json:"source"`
	MD      string `json:"body_md,omitempty"` // omitted from the list; only the by-id read carries it
	// Bytes is the body's length, so the list can say how big a version is without shipping it.
	Bytes int `json:"bytes"`
}

const revisionCols = `id, COALESCE(saved_at,''), COALESCE(author,''), COALESCE(title,''),
	COALESCE(symbol,''), COALESCE(name,''), COALESCE(rtype,''), COALESCE(rdate,''),
	COALESCE(kind,''), COALESCE(source,''), COALESCE(body_md,'')`

func scanRevision(sc interface{ Scan(...any) error }) (Revision, error) {
	var v Revision
	err := sc.Scan(&v.ID, &v.SavedAt, &v.Author, &v.Title, &v.Symbol, &v.Name, &v.RType, &v.Date,
		&v.Kind, &v.Source, &v.MD)
	v.Bytes = len(v.MD)
	return v, err
}

// Revisions lists one report's history, newest first, WITHOUT the bodies.
//
// Without them because a list of twenty versions of a long report is twenty full documents, and the
// list is drawn to be scanned rather than read. The body arrives from Revision(), one at a time,
// when somebody opens one.
func (s *Store) Revisions(reportID int64) []Revision {
	rows, err := s.query(`SELECT `+revisionCols+` FROM report_revisions
		WHERE report_id=? ORDER BY id DESC`, reportID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		v, err := scanRevision(rows)
		if err != nil {
			continue
		}
		v.MD = "" // the list is an index, not twenty documents
		out = append(out, v)
	}
	return out
}

// Revision reads one prior state in full, scoped to its report. The report id is part of the lookup
// rather than trusted from the path alone: without it, a revision id from another report would be
// served to whoever could read this one.
func (s *Store) Revision(reportID, revID int64) (*Revision, bool) {
	v, err := scanRevision(s.queryRow(`SELECT `+revisionCols+` FROM report_revisions
		WHERE id=? AND report_id=?`, revID, reportID))
	if err != nil {
		return nil, false
	}
	return &v, true
}

// CountRevisionsBefore and DeleteRevisionsBefore share one predicate so the storage console's
// preview and its delete can never disagree about what a pass would remove.
//
// The blank-guard is not the fail-closed dance the reports target needs: every saved_at is written
// by manualInstant, so it is homogeneous UTC RFC3339Nano and a lexical compare is exact. A blank one
// could only come from a hand-edited row, and skipping it errs toward keeping history.
const revisionsRetentionWhere = `saved_at <> '' AND saved_at < ?`

func (s *Store) CountRevisionsBefore(cutoff time.Time) (int64, error) {
	var n int64
	err := s.queryRow(`SELECT COUNT(*) FROM report_revisions WHERE `+revisionsRetentionWhere,
		cutoff.UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n, err
}

func (s *Store) DeleteRevisionsBefore(cutoff time.Time) (int64, error) {
	res, err := s.exec(`DELETE FROM report_revisions WHERE `+revisionsRetentionWhere,
		cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// countRevisions is the tests' and the editor's "how many versions does this report have".
func (s *Store) countRevisions(reportID int64) int {
	var n int
	s.queryRow("SELECT COUNT(*) FROM report_revisions WHERE report_id=?", reportID).Scan(&n)
	return n
}
