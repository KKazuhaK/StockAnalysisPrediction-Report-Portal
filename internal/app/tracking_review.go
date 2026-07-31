package app

import (
	"database/sql"
	"fmt"
	"strings"
)

// The review queue: tracking items across every symbol, not one at a time.
//
// Every ingest may attach what a report ASSUMED and when that should be checked (`tracking` on
// POST /api/v1/reports). The data has been accumulating since the v1 API shipped and nothing has
// ever read it back except the machine API, so the assumptions were being recorded and never
// revisited — which is the difference between research and opinion.
//
// QueryTracking answers "this symbol's items" for the stock page. This is the other question: what
// is outstanding, across the book.

// TrackingRow is one item plus the report context needed to act on it. Joined in the query rather
// than fetched per row: a list of assumptions with no company, date or report title beside them is
// not reviewable, and one query per row would make the page quadratic.
type TrackingRow struct {
	TrackingItem
	Name        string // company name as of the report
	ReportTitle string
	ReportDate  string
	ReportKind  string
	ReportType  string
}

// TrackingFilter narrows the queue. Every field is optional; the zero value is "everything".
type TrackingFilter struct {
	Symbol string
	Status string
	IType  string
	Q      string // substring of the item's content or its review point
	Limit  int
	Offset int
}

// trackingScopeJoin applies the viewer's read scope. tracking_items carries no owner of its own, so
// visibility is inherited from the report it belongs to — the same rule as every other read path
// (ADR 0024). An item whose report is gone is therefore invisible to a scoped viewer, which is the
// safe direction.
func trackingScopeJoin(sc *ownerScope) (join string, where []string, args []any) {
	if frag, fargs := sc.where("r."); frag != "" {
		return " JOIN reports r ON r.id = t.report_id", []string{frag}, fargs
	}
	return " LEFT JOIN reports r ON r.id = t.report_id", nil, nil
}

// ListTracking returns one page of the queue plus the total matching it, so the UI can page without
// a second round trip.
func (s *Store) ListTracking(f TrackingFilter, sc *ownerScope) ([]TrackingRow, int) {
	join, where, args := trackingScopeJoin(sc)
	add := func(cond string, v ...any) {
		where = append(where, cond)
		args = append(args, v...)
	}
	if f.Symbol != "" {
		add("t.symbol=?", f.Symbol)
	}
	if f.Status != "" {
		add("t.status=?", f.Status)
	}
	if f.IType != "" {
		add("t.itype=?", f.IType)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add("(t.content "+s.likeOp()+" ? OR t.review_point "+s.likeOp()+" ?)", "%"+q+"%", "%"+q+"%")
	}
	cond := "1=1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}

	var total int
	s.queryRow("SELECT COUNT(*) FROM tracking_items t"+join+" WHERE "+cond, args...).Scan(&total)

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	// Newest first. review_point is free text from the workflow — it may be a date, a quarter, or a
	// sentence — so it cannot be ordered as a due date without lying about what it contains.
	rows, err := s.query(fmt.Sprintf(`SELECT t.id,t.report_id,t.symbol,t.itype,t.content,t.status,
			t.review_point,t.created_at,
			COALESCE(r.name,''),COALESCE(r.title,''),COALESCE(r.rdate,''),COALESCE(r.kind,''),COALESCE(r.rtype,'')
		FROM tracking_items t%s WHERE %s
		ORDER BY t.created_at DESC, t.id DESC LIMIT %d OFFSET %d`, join, cond, limit, offset), args...)
	if err != nil {
		return nil, total
	}
	defer rows.Close()
	out := make([]TrackingRow, 0, limit)
	for rows.Next() {
		var t TrackingRow
		var reportID sql.NullInt64
		var sym, it, c, st, rp, cr, name, title, date, kind, rtype sql.NullString
		if rows.Scan(&t.ID, &reportID, &sym, &it, &c, &st, &rp, &cr, &name, &title, &date, &kind, &rtype) != nil {
			continue
		}
		t.ReportID = reportID.Int64
		t.Symbol, t.IType, t.Content, t.Status = sym.String, it.String, c.String, st.String
		t.ReviewPoint, t.Created = rp.String, cr.String
		t.Name, t.ReportTitle, t.ReportDate = name.String, title.String, date.String
		t.ReportKind, t.ReportType = kind.String, rtype.String
		out = append(out, t)
	}
	return out, total
}

// TrackingStatusCounts is status → count for the queue's tabs, scoped the same way the list is —
// otherwise a badge advertises items the viewer cannot open.
func (s *Store) TrackingStatusCounts(sc *ownerScope) map[string]int {
	join, where, args := trackingScopeJoin(sc)
	cond := "1=1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	out := map[string]int{}
	rows, err := s.query("SELECT COALESCE(t.status,''),COUNT(*) FROM tracking_items t"+join+
		" WHERE "+cond+" GROUP BY COALESCE(t.status,'')", args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if rows.Scan(&status, &n) == nil {
			if status == "" {
				status = trackingPending // an item stored before status had a default
			}
			out[status] += n
		}
	}
	return out
}

// TrackingVocabulary reports the itype and status values actually present, scoped. The ingest
// contract lets a workflow send any string for either, so the filters have to be built from the
// data rather than from a list the portal made up — a hardcoded set would silently hide whatever
// the pipeline started emitting last week.
func (s *Store) TrackingVocabulary(sc *ownerScope) (itypes []string, statuses []string) {
	join, where, args := trackingScopeJoin(sc)
	cond := "1=1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	for _, col := range []string{"t.itype", "t.status"} {
		var vals []string
		rows, err := s.query("SELECT DISTINCT COALESCE("+col+",'') FROM tracking_items t"+join+
			" WHERE "+cond+" ORDER BY 1", args...)
		if err == nil {
			for rows.Next() {
				var v string
				if rows.Scan(&v) == nil && v != "" {
					vals = append(vals, v)
				}
			}
			rows.Close()
		}
		if col == "t.itype" {
			itypes = vals
		} else {
			statuses = vals
		}
	}
	return itypes, statuses
}

// The one status the portal itself assigns — SetTracking defaults to it when a workflow omits one.
// Everything else in the vocabulary comes from the pipeline, so the portal must not enumerate it.
const trackingPending = "pending"
