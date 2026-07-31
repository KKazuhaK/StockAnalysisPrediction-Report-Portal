package app

import (
	"net/http"
	"strconv"
	"strings"
)

// The browser-facing review API.
//
// /api/v1/tracking already exposes the same rows to machines, but it cannot be reused here: it
// authenticates with an ingest token, which by definition already has access to everything, so it
// does no viewer scoping at all. A person's session must be scoped, and the update in particular —
// otherwise a restricted client could review an assumption on a report they may not read, and the
// response would tell them the id exists.

// GET /api/tracking — the review queue for whoever is asking.
func (s *Server) apiTracking(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	sc := s.viewerScope(user)

	rows, total := s.st.ListTracking(TrackingFilter{
		Symbol: strings.TrimSpace(q.Get("symbol")),
		Status: strings.TrimSpace(q.Get("status")),
		IType:  strings.TrimSpace(q.Get("itype")),
		Q:      strings.TrimSpace(q.Get("q")),
		Sort:   strings.TrimSpace(q.Get("sort")),
		Limit:  limit,
		Offset: offset,
	}, sc)

	items := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		items = append(items, map[string]any{
			"id": it.ID, "symbol": it.Symbol, "name": it.Name,
			"itype": it.IType, "content": it.Content, "status": it.Status,
			"review_point": it.ReviewPoint, "created_at": it.Created,
			// Parsed out of the review point when it holds a date; "" otherwise, and the UI shows
			// the raw text in that case rather than pretending there is a deadline.
			"due":       it.Due,
			"report_id": it.ReportID, "report_title": it.ReportTitle,
			"report_date": it.ReportDate, "report_kind": it.ReportKind, "report_type": it.ReportType,
		})
	}
	itypes, statuses := s.st.TrackingVocabulary(sc)
	writeJSON(w, map[string]any{
		"items": items, "total": total,
		"counts": s.st.TrackingStatusCounts(sc),
		// The filter vocabularies come from the data, not from a list the portal invented: the
		// ingest contract lets a workflow send any string for either field.
		"itypes": itypes, "statuses": statuses,
	})
}

// PATCH /api/tracking/{id} — record what a human concluded about one assumption.
func (s *Server) apiTrackingUpdate(w http.ResponseWriter, r *http.Request, user string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Status      string `json:"status"`
		ReviewPoint string `json:"review_point"`
	}
	readJSON(r, &in)
	status, rp := strings.TrimSpace(in.Status), strings.TrimSpace(in.ReviewPoint)
	if status == "" && rp == "" {
		jsonError(w, http.StatusBadRequest, "status or review_point is required")
		return
	}
	// Scoped in the UPDATE itself rather than checked first, so there is no window between the
	// check and the write. An item the caller may not read is indistinguishable from one that does
	// not exist — 404 either way, because answering differently would confirm the id.
	ok, err := s.st.UpdateTrackingStatusScoped(id, status, rp, s.viewerScope(user))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		jsonErrorCode(w, http.StatusNotFound, "tracking_not_found", "没有这条跟踪项")
		return
	}
	writeJSON(w, okJSON)
}
