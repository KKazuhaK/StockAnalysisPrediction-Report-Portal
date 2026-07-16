package app

// apiv1.go — the clean, versioned Dify machine API (/api/v1). Differences from the
// legacy bare /api/* paths (kept for compat):
//   - errors are a JSON envelope {ok:false, error:{code,message}} (never plain text)
//   - collections use a uniform {ok:true, count, items:[...]} shape (+ total/offset/limit)
//   - report identity is portal-derived & deterministic (symbol-or-title + date + rtype,
//     enforced by a unique index); the client never supplies an id, and the server-inferred
//     kind is NOT part of identity
//   - date is validated (YYYY-MM-DD); the as-of name snapshot is honored on every path

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validReportDate accepts only a zero-padded, real calendar date (YYYY-MM-DD).
func validReportDate(s string) bool {
	if !reISODate.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// v1ReportByPathID resolves a v1 report {id} path value to its stored report.
// Returns nil if the value is not a positive integer or there is no such report.
func (s *Server) v1ReportByPathID(id string) *Rep {
	rowid, err := strconv.ParseInt(id, 10, 64)
	if err != nil || rowid <= 0 {
		return nil
	}
	rep, _ := s.st.GetNew(rowid)
	return rep
}

// ingestInstant is the real time-of-day stamped onto a report's sent_at. It is a
// UTC RFC3339 instant, server-stamped by default so same-day reports always order
// correctly; a client-supplied `time` is honored only when it parses as a full
// RFC3339 instant (e.g. the utc from GET /api/v1/now), never the old date-only
// fallback. The instant is stored/returned in UTC and localized for display
// client-side; it never enters the report identity (symbol-or-title + date + subtype).
func ingestInstant(clientTime string) string {
	if t := strings.TrimSpace(clientTime); t != "" {
		if _, err := time.Parse(time.RFC3339, t); err == nil {
			return t
		}
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// panelLocation resolves the configured panel timezone (meta['timezone']), falling
// back to the process/system zone when unset or unparseable. This is the business
// timezone: date-only values (report date, date=today, /now's date) resolve here,
// while instant timestamps travel as UTC and are localized for display client-side.
func (s *Server) panelLocation() *time.Location {
	name := s.st.GetSetting("timezone", "")
	if name == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.Local
}

// GET /api/v1/now — the portal's authoritative clock. Returns a UTC instant plus the
// civil date/datetime in the panel timezone, so producers (e.g. Dify) anchor "today"
// to the portal instead of their own (possibly UTC) sandbox clock. scope query.
func (s *Server) v1Now(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	loc := s.panelLocation()
	now := time.Now()
	writeJSON(w, map[string]any{
		"ok":       true,
		"utc":      now.UTC().Format(time.RFC3339),
		"date":     now.In(loc).Format("2006-01-02"),
		"datetime": now.In(loc).Format("2006-01-02 15:04:05"),
		"tz":       loc.String(),
	})
}

// v1err writes a JSON error envelope with the given HTTP status and machine code.
func v1err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
}

// v1RepJSON shapes a report for v1 responses. name prefers the stored as-of snapshot,
// falling back to the current name only when no snapshot was recorded.
func (s *Server) v1RepJSON(r Rep, withBody bool) map[string]any {
	// "id" is the report's numeric row id — the one identifier every API speaks.
	m := map[string]any{
		"id": r.ID, "run_id": r.RunID, "symbol": r.Symbol,
		"name": firstNonEmpty(r.Name, s.names.Get(r.Symbol)),
		"date": r.Date, "time": r.Time, "kind": r.Kind, "subtype": r.RType, "title": r.Title, "source": r.Source,
	}
	if withBody {
		m["body_md"] = r.MD
	}
	return m
}

// POST /api/v1/reports — ingest (portal-derived identity, validated). scope ingest.
func (s *Server) v1Ingest(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid ingest token")
		return
	}
	var in struct {
		RunID    string `json:"run_id"`
		Symbol   string `json:"symbol"`
		Name     string `json:"name"`
		Date     string `json:"date"`
		Kind     string `json:"kind"`
		Subtype  string `json:"subtype"`
		RType    string `json:"rtype"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Time     string `json:"time"`
		BodyMD   string `json:"body_md"`
		BodyHTML string `json:"body_html"`
		Tracking []struct {
			IType       string `json:"itype"`
			Content     string `json:"content"`
			Status      string `json:"status"`
			ReviewPoint string `json:"review_point"`
		} `json:"tracking"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		v1err(w, http.StatusBadRequest, "bad_json", "request body is not valid JSON")
		return
	}
	in.Symbol = strings.TrimSpace(in.Symbol)
	in.Title = strings.TrimSpace(in.Title)
	if in.Symbol == "" && in.Title == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "symbol or title is required")
		return
	}
	if !validReportDate(in.Date) {
		v1err(w, http.StatusBadRequest, "invalid_param", "date must be a valid YYYY-MM-DD")
		return
	}
	rtype := strings.TrimSpace(firstNonEmpty(in.Subtype, in.RType))
	if rtype == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "subtype (or rtype) is required")
		return
	}
	kind := in.Kind
	if kind == "" {
		kind = s.st.TypeKind(rtype)
	}
	if kind == "" {
		kind = runKind([]string{rtype})
	}
	s.st.RegisterType(rtype, kind)
	// Freeze the as-of name onto this report row: an explicit payload name wins,
	// otherwise resolve the current live name (rename-safe; earlier reports keep theirs).
	name := cleanName(in.Name)
	if name == "" {
		name = s.names.Resolve(in.Symbol)
	}
	// Identity is resolved by the store's unique index (code-or-title + date + subtype),
	// so a re-ingest overwrites in place and hands back the same id.
	id, created, err := s.st.UpsertReport(Rep{
		RunID: in.RunID, Symbol: in.Symbol, Name: name, Date: in.Date, Kind: kind,
		RType: rtype, Title: in.Title, Source: in.Source, Time: ingestInstant(in.Time),
		MD: in.BodyMD, HTML: htmlToStore(in.BodyMD, in.BodyHTML),
	})
	if err != nil {
		log.Printf("v1 ingest db error: %v", err)
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	if len(in.Tracking) > 0 {
		items := make([]TrackingItem, 0, len(in.Tracking))
		for _, t := range in.Tracking {
			items = append(items, TrackingItem{IType: t.IType, Content: t.Content, Status: t.Status, ReviewPoint: t.ReviewPoint})
		}
		s.st.SetTracking(id, in.Symbol, items)
	}
	log.Printf("v1 ingest %s %s id=%d created=%v", in.Symbol, in.Date, id, created)
	s.fireEvent(EventReportIngested, map[string]any{
		"id": id, "symbol": in.Symbol, "name": name, "date": in.Date,
		"rtype": rtype, "kind": kind, "title": in.Title, "source": in.Source, "created": created,
	})
	writeJSON(w, map[string]any{"ok": true, "id": id, "created": created})
}

// GET /api/v1/reports — search. scope query.
func (s *Server) v1QueryReports(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	kw := strings.TrimSpace(q.Get("q"))
	runID := strings.TrimSpace(q.Get("run_id"))
	subtype := firstNonEmpty(strings.TrimSpace(q.Get("subtype")), strings.TrimSpace(q.Get("rtype")))
	// A query must be scoped by at least one selector so it can't scan the whole store.
	// symbol/q/run_id or any category/time filter all qualify — dedup tools legitimately
	// query by subtype alone for symbol-less reports (e.g. 行业分析 has no stock code), so
	// requiring specifically symbol/q/run_id wrongly 400s those (missing_param).
	scoped := symbol != "" || kw != "" || runID != "" || subtype != "" ||
		strings.TrimSpace(q.Get("kind")) != "" || strings.TrimSpace(q.Get("source")) != "" ||
		strings.TrimSpace(q.Get("date")) != "" || strings.TrimSpace(q.Get("since")) != "" ||
		strings.TrimSpace(q.Get("until")) != ""
	if !scoped {
		v1err(w, http.StatusBadRequest, "missing_param", "at least one filter is required (symbol, q, run_id, subtype, kind, source, or date)")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	withBody := q.Get("with_body") == "1" || q.Get("with_body") == "true"
	since, until := q.Get("since"), q.Get("until")
	if d := strings.TrimSpace(q.Get("date")); d != "" {
		if d == "today" {
			d = time.Now().In(s.panelLocation()).Format("2006-01-02")
		}
		since, until = d, d
	}
	reps, total, err := s.st.QueryReports(ReportQuery{
		Symbol: symbol, Q: kw, Kind: q.Get("kind"), RType: subtype,
		Source: strings.TrimSpace(q.Get("source")), RunID: runID, Since: since, Until: until,
		Limit: limit, Offset: offset, WithBody: withBody,
	})
	if err != nil {
		log.Printf("v1 query db error: %v", err)
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	items := make([]map[string]any, 0, len(reps))
	for _, rp := range reps {
		items = append(items, s.v1RepJSON(rp, withBody))
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(items), "total": total, "offset": offset, "limit": limit, "items": items})
}

// GET /api/v1/reports/{id} — single report with body. scope query.
func (s *Server) v1GetReport(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	rep := s.v1ReportByPathID(r.PathValue("id"))
	if rep == nil {
		v1err(w, http.StatusNotFound, "not_found", "no report with that id")
		return
	}
	m := s.v1RepJSON(*rep, true)
	m["ok"] = true
	m["body_html"] = htmlOf(*rep)
	writeJSON(w, m)
}

// DELETE /api/v1/reports/{id} — retract a report (cascades tracking). scope ingest.
func (s *Server) v1DeleteReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid ingest token")
		return
	}
	// The tracking cascade keys on report_id, so the id deletes both directly — no
	// lookup needed. A non-numeric id simply matches nothing (deleted=0, idempotent).
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, map[string]any{"ok": true, "deleted": 0})
		return
	}
	n, err := s.st.DeleteReport(id)
	if err != nil {
		log.Printf("v1 delete db error: %v", err)
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "deleted": n})
}

// GET /api/v1/reports/manifest?symbol= — probe. scope query.
func (s *Server) v1Manifest(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "symbol is required")
		return
	}
	m := s.st.Manifest(symbol)
	m["ok"] = true
	m["name"] = s.names.Get(symbol)
	writeJSON(w, m)
}

// GET /api/v1/runs?symbol=&date= — report-group view. scope query.
func (s *Server) v1Runs(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "symbol is required")
		return
	}
	runs := s.st.ListRuns(symbol, strings.TrimSpace(r.URL.Query().Get("date")))
	items := make([]map[string]any, 0, len(runs))
	for _, rn := range runs {
		items = append(items, map[string]any{"symbol": rn.Symbol, "date": rn.Date, "kind": rn.Kind,
			"run_id": rn.RunID, "subtypes": rn.Subtypes, "count": rn.Count})
	}
	writeJSON(w, map[string]any{"ok": true, "symbol": symbol, "name": s.names.Get(symbol),
		"count": len(items), "has": len(items) > 0, "items": items})
}

// GET /api/v1/symbols?q=&limit= — stocks with reports. scope query.
func (s *Server) v1Symbols(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list := s.st.ListSymbols(strings.TrimSpace(q.Get("q")), limit)
	items := make([]map[string]any, 0, len(list))
	for _, si := range list {
		name := si.Name
		if name == "" {
			name = s.names.Get(si.Symbol)
		}
		items = append(items, map[string]any{"symbol": si.Symbol, "name": name, "count": si.Count, "latest": si.Latest})
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(items), "has": len(items) > 0, "items": items})
}

// GET /api/v1/tracking?symbol=&status=&limit= — assumption/tracking items. scope query.
func (s *Server) v1Tracking(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid query credentials")
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	if symbol == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "symbol is required")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items := s.st.QueryTracking(symbol, strings.TrimSpace(q.Get("status")), limit)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"id": it.ID, "report_id": it.ReportID, "itype": it.IType,
			"content": it.Content, "status": it.Status, "review_point": it.ReviewPoint, "created_at": it.Created})
	}
	writeJSON(w, map[string]any{"ok": true, "symbol": symbol, "count": len(out), "has": len(out) > 0, "items": out})
}

// PATCH /api/v1/tracking/{id} — update one item's status/review_point. scope ingest.
func (s *Server) v1TrackingUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		v1err(w, http.StatusUnauthorized, "unauthorized", "missing or invalid ingest token")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		v1err(w, http.StatusBadRequest, "invalid_param", "id must be an integer")
		return
	}
	var in struct {
		Status      string `json:"status"`
		ReviewPoint string `json:"review_point"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
	if strings.TrimSpace(in.Status) == "" && strings.TrimSpace(in.ReviewPoint) == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "status or review_point is required")
		return
	}
	ok, err := s.st.UpdateTrackingStatus(id, strings.TrimSpace(in.Status), strings.TrimSpace(in.ReviewPoint))
	if err != nil {
		log.Printf("v1 tracking update db error: %v", err)
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	if !ok {
		v1err(w, http.StatusNotFound, "not_found", "no tracking item with that id")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "status": in.Status})
}
