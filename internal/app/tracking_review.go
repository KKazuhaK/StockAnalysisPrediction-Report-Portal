package app

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
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
	// Due is a date parsed out of the free-text review point, "" when it holds none. Derived on
	// read rather than stored: the text is the workflow's, and a column would go stale the moment
	// a re-run reworded it.
	Due string
}

// TrackingFilter narrows the queue. Every field is optional; the zero value is "everything".
type TrackingFilter struct {
	Symbol string
	Status string
	IType  string
	Q      string // substring of the item's content or its review point
	// Sort is "" (newest first) or "due" — soonest due first, undated last. Applied in Go, since
	// the due date is parsed out of free text and no column holds it.
	Sort   string
	Limit  int
	Offset int
}

// trackingDueDate pulls a date out of a free-text review point, returning "" when there is nothing
// it can trust. The workflow writes whatever it likes there — "2026-10-31 三季报", "三季报发布后",
// a whole sentence — so this is opportunistic by design.
//
// Deliberately narrow: guessing wrong is worse than admitting there is no date, because a wrong
// guess sorts someone's day around a deadline that does not exist. A bare year is not a date, and
// an impossible one (month 13, February 30th) is rejected by round-tripping it through time.Parse
// rather than by range-checking the parts.
func trackingDueDate(reviewPoint string) string {
	for _, m := range dueDatePatterns {
		g := m.FindStringSubmatch(reviewPoint)
		if g == nil {
			continue
		}
		iso := fmt.Sprintf("%s-%02s-%02s", g[1], pad(g[2]), pad(g[3]))
		if t, err := time.Parse("2006-01-02", iso); err == nil && t.Format("2006-01-02") == iso {
			return iso
		}
	}
	return ""
}

func pad(v string) string {
	if len(v) == 1 {
		return "0" + v
	}
	return v
}

var dueDatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(\d{4})[-/](\d{1,2})[-/](\d{1,2})`),
	regexp.MustCompile(`(\d{4})年(\d{1,2})月(\d{1,2})日?`),
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
	// Newest first from SQL; a due-date order is layered on below.
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
		t.Due = trackingDueDate(t.ReviewPoint)
		out = append(out, t)
	}
	// Due-date order is applied here rather than in SQL, because the date lives inside free text and
	// no column holds it. That means it orders the PAGE, not the whole table — acceptable while the
	// queue is small, and the alternative is asking the workflow for a real date column, which is a
	// contract change rather than a query.
	if f.Sort == "due" {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i].Due, out[j].Due
			if (a == "") != (b == "") {
				return a != "" // an undated item never outranks one that is actually due
			}
			if a != b {
				return a < b // soonest first
			}
			return out[i].Created > out[j].Created
		})
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

// UpdateTrackingStatusScoped records a human's verdict on one item, but only if the caller may read
// the report it belongs to. The scope is part of the UPDATE rather than a check before it: a
// separate read would leave a window, and a scoped statement that matches nothing is exactly the
// "not found" the caller should be told.
func (s *Store) UpdateTrackingStatusScoped(id int64, status, reviewPoint string, sc *ownerScope) (bool, error) {
	sets, args := []string{}, []any{}
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
	q := "UPDATE tracking_items SET " + strings.Join(sets, ",") + " WHERE id=?"
	args = append(args, id)
	// UPDATE ... JOIN is not portable, so the scope rides in as a subquery on the report id, which
	// behaves the same on both drivers.
	if frag, fargs := sc.where("r."); frag != "" {
		q += " AND report_id IN (SELECT r.id FROM reports r WHERE " + frag + ")"
		args = append(args, fargs...)
	}
	res, err := s.exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ComparableReport is one candidate to diff a report against.
type ComparableReport struct {
	ID      int64  `json:"id"`
	Date    string `json:"date"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// ComparableReports lists the other editions of the SAME analysis — same symbol, same subtype —
// newest first, so the UI can default to the previous one and offer the rest.
//
// Deliberately narrow: a different analysis of the same company, or the same analysis of a
// different company, is not a sensible thing to diff against. The endpoint that performs the diff
// accepts any pair, so nothing here prevents a deliberate cross-comparison; this is only what the
// picker suggests.
func (s *Store) ComparableReports(id int64, limit int, sc *ownerScope) []ComparableReport {
	var symbol, rtype string
	if s.queryRow("SELECT symbol,rtype FROM reports WHERE id=?", id).Scan(&symbol, &rtype) != nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	where := []string{"r.symbol=?", "r.rtype=?", "r.id<>?"}
	args := []any{symbol, rtype, id}
	if frag, fargs := sc.where("r."); frag != "" {
		where = append(where, frag)
		args = append(args, fargs...)
	}
	rows, err := s.query(fmt.Sprintf(`SELECT r.id,r.rdate,r.title,COALESCE(r.version,'')
		FROM reports r WHERE %s ORDER BY r.rdate DESC, r.id DESC LIMIT %d`,
		strings.Join(where, " AND "), limit), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ComparableReport{}
	for rows.Next() {
		var c ComparableReport
		if rows.Scan(&c.ID, &c.Date, &c.Title, &c.Version) == nil {
			out = append(out, c)
		}
	}
	return out
}
