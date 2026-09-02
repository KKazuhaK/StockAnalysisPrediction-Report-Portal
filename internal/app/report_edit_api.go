package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Hand-written reports (ADR 0026): the editor's surface.
//
// There is no "fork" verb here, and that absence is the design. Editing a machine-generated report
// and writing one from scratch are the same operation — compose a body, choose who it is for, save
// it into the manual version — so this file has one create and one update, and the editor seeds the
// create with a machine report's text when the author started from one. A separate fork endpoint
// would be a second write path into the same table, with its own way of getting the identity, the
// audience and the concurrency token wrong.
//
// What a machine report cannot do here is be modified. Every write below is constrained to the
// manual version, in the store (UpdateManualReport's `AND version=?`) rather than only in a handler
// check, so a report that records what a workflow said keeps saying it.

// maxManualBodyBytes bounds the Markdown itself; maxManualRequestBytes bounds the envelope carrying
// it, and has to be the larger of the two because JSON escaping inflates the body and the form has
// other fields. Both are generous on purpose: the ingest path accepts 16 MiB, and a report is not a
// smaller thing for having been typed rather than generated.
const (
	maxManualBodyBytes    = 4 << 20
	maxManualRequestBytes = 8 << 20
)

// reportEditInput is the editor form. Every field is required except symbol/name/source, so unlike
// announcementInput there is nothing to distinguish "absent" from "cleared" and plain values do.
type reportEditInput struct {
	Symbol   string   `json:"symbol"`
	Name     string   `json:"name"`
	Date     string   `json:"date"`
	Subtype  string   `json:"subtype"`
	Title    string   `json:"title"`
	Source   string   `json:"source"`
	BodyMD   string   `json:"body_md"`
	Audience string   `json:"audience"` // "all" | "grant"
	Viewers  []string `json:"viewers"`
	// UpdatedAt is the sent_at the editor loaded, echoed back on save. See manualInstant: reports
	// have no revision counter, so the write timestamp is the token.
	UpdatedAt string `json:"updated_at"`
}

// recipientPicker is the group and account list an audience picker is drawn from — shared with the
// announcements console so both editors offer the same principals under the same names, and so the
// account cap is one number rather than two that drift.
func (s *Server) recipientPicker(includeDefaultOU bool) (groups, users []map[string]any, truncated bool) {
	defaultOU := s.st.DefaultGroupID()
	groups = make([]map[string]any, 0)
	for _, g := range s.st.ListUserGroups() {
		if g.ID == defaultOU && !includeDefaultOU {
			continue
		}
		groups = append(groups, map[string]any{
			"principal": groupPrincipal(g.ID), "name": g.Name, "restricted": g.RestrictedEffective,
		})
	}
	all := s.st.Users()
	users = make([]map[string]any, 0, len(all))
	for _, u := range all {
		if len(users) >= maxPickerUsers {
			break
		}
		users = append(users, map[string]any{
			"principal": userPrincipal(u.Username), "name": u.Username,
			"restricted": u.Restricted, "display": u.Name(),
		})
	}
	return groups, users, len(all) > len(users)
}

// reportAudience reads a stored viewer list back as the editor's two-part choice. The Default OU
// principal is on every account's chain, so a report addressed to it reaches everyone — which is
// what "all" means, and storing it that way is why "everyone" needs no column of its own.
func (s *Server) reportAudience(viewers []string) (string, []string) {
	defaultOU := groupPrincipal(s.st.DefaultGroupID())
	for _, p := range viewers {
		if p == defaultOU {
			return "all", nil
		}
	}
	return "grant", viewers
}

// apiReportEditor serves the editor form: an empty one, or one seeded from an existing report.
//
// ?from=<id> is how "edit this" and "write a new one starting from this" are the same request. The
// response says which of the two will happen — `manual` for a report that is already hand-written
// (saving updates it in place) and `manualId` for a machine report that already HAS a hand-written
// form, so the editor sends the author to that one instead of silently creating a second.
func (s *Server) apiReportEditor(w http.ResponseWriter, r *http.Request, user string) {
	groups, users, truncated := s.recipientPicker(false)
	out := map[string]any{
		"subtypes": s.st.DiscoveredTypes(), "groups": groups, "users": users,
		"usersTruncated": truncated, "today": s.panelToday(),
		"audience": "grant", "viewers": []string{},
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		id, _ := strconv.ParseInt(raw, 10, 64)
		// Scoped read: an editor may only start from a report they could have read anyway. GetNew
		// rather than loadRep, because loadRep records a report.read against the user and opening an
		// editor is not somebody being served the report.
		rep, _ := s.st.GetNew(id, s.viewerScope(user))
		if rep == nil {
			jsonError(w, http.StatusNotFound, "report not found")
			return
		}
		manual := rep.Version == s.st.ManualVersion()
		out["from"] = rep.ID
		out["manual"] = manual
		out["symbol"], out["name"] = rep.Symbol, rep.Name
		out["date"], out["subtype"] = rep.Date, rep.RType
		out["title"], out["source"] = rep.Title, rep.Source
		out["body_md"] = rep.MD
		if manual {
			out["id"] = rep.ID
			out["updated_at"] = rep.Time
			aud, viewers := s.reportAudience(s.st.ReportViewers(rep.ID))
			out["audience"], out["viewers"] = aud, viewers
		} else if sib, ok := s.st.ManualSiblingOf(*rep); ok {
			out["manualId"] = sib
		}
	}
	writeJSON(w, out)
}

// validateReportEdit checks the form and derives what the store needs: the subtype's category, and
// the as-of company name. Mirrors v1Ingest's validation deliberately — a report written by hand and
// one pushed by a workflow are the same kind of thing and must not be admissible under different
// rules.
func (s *Server) validateReportEdit(w http.ResponseWriter, in reportEditInput) (Rep, []string, bool) {
	in.Symbol = strings.TrimSpace(in.Symbol)
	in.Title = strings.TrimSpace(in.Title)
	if in.Symbol == "" && in.Title == "" {
		jsonErrorCode(w, http.StatusBadRequest, "report_needs_subject", "请填写股票代码或标题")
		return Rep{}, nil, false
	}
	if !validReportDate(in.Date) {
		jsonErrorCode(w, http.StatusBadRequest, "report_bad_date", "日期格式应为 YYYY-MM-DD")
		return Rep{}, nil, false
	}
	subtype := strings.TrimSpace(in.Subtype)
	if subtype == "" {
		jsonErrorCode(w, http.StatusBadRequest, "report_needs_subtype", "请选择报告子类型")
		return Rep{}, nil, false
	}
	// A report with no body is not a draft, it is a blank page that shows up in everyone's list
	// looking like something went wrong. There is no draft state in this table to put one in.
	if strings.TrimSpace(in.BodyMD) == "" {
		jsonErrorCode(w, http.StatusBadRequest, "report_needs_body", "报告正文不能为空")
		return Rep{}, nil, false
	}
	if len(in.BodyMD) > maxManualBodyBytes {
		jsonErrorCode(w, http.StatusBadRequest, "report_body_too_long", "报告正文超出长度上限")
		return Rep{}, nil, false
	}
	kind := s.st.TypeKind(subtype)
	if kind == "" {
		kind = runKind([]string{subtype})
	}
	s.st.RegisterType(subtype, kind)
	name := cleanName(in.Name)
	if name == "" {
		name = s.names.Resolve(in.Symbol)
	}
	viewers, ok := s.reportViewersFromInput(w, in)
	if !ok {
		return Rep{}, nil, false
	}
	return Rep{
		Symbol: in.Symbol, Name: name, Date: in.Date, RType: subtype, Kind: kind,
		Title: in.Title, Source: strings.TrimSpace(in.Source), MD: in.BodyMD,
	}, viewers, true
}

// reportViewersFromInput turns the form's audience choice into the principal rows the read path
// consults. "all" is stored as the Default OU principal, which every account's chain ends with —
// the same encoding, no special case in the reader.
func (s *Server) reportViewersFromInput(w http.ResponseWriter, in reportEditInput) ([]string, bool) {
	if strings.ToLower(strings.TrimSpace(in.Audience)) == "all" {
		return []string{groupPrincipal(s.st.DefaultGroupID())}, true
	}
	// allowDefaultOU=false: an author who picks the Default OU from a list means "everyone" and
	// should be told to say so, for the same reason the announcements console refuses it.
	viewers, ok := s.normalizePrincipals(w, in.Viewers, false)
	if !ok {
		return nil, false
	}
	// A hand-written report addressed to nobody is readable by no restricted account at all, looks
	// published in every list, and reports no problem. Refused rather than saved and flagged: unlike
	// an announcement, whose audience can be emptied later by deleting a group, this state is only
	// reachable by choosing it here.
	if len(viewers) == 0 {
		jsonErrorCode(w, http.StatusBadRequest, "report_needs_audience", "请选择接收人，或改为「所有人」")
		return nil, false
	}
	return viewers, true
}

// apiReportCreate writes a new hand-written report.
func (s *Server) apiReportCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in reportEditInput
	if err := readJSONLimit(r, &in, maxManualRequestBytes); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	rep, viewers, ok := s.validateReportEdit(w, in)
	if !ok {
		return
	}
	id, err := s.st.CreateManualReport(rep)
	if err != nil {
		var exists ErrReportExists
		if errors.As(err, &exists) {
			// The id travels with the refusal so the editor can offer to open it. A collision here
			// almost always means the author is writing the report that already exists.
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"error": "report_exists", "message": "已存在同样的人工报告", "id": exists.ID})
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetReportViewers(id, rep.Date, viewers); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordChange(r, user, AuditReportCreate, "report", strconv.FormatInt(id, 10), map[string]any{
		"symbol": rep.Symbol, "date": rep.Date, "subtype": rep.RType, "title": rep.Title,
		"audience": strings.ToLower(strings.TrimSpace(in.Audience)), "viewers": len(viewers),
		"from": strings.TrimSpace(r.URL.Query().Get("from")),
	})
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// apiReportSave rewrites one hand-written report.
func (s *Server) apiReportSave(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	rep, _ := s.st.GetNew(id, s.viewerScope(user))
	if rep == nil {
		jsonError(w, http.StatusNotFound, "report not found")
		return
	}
	// Refused here as well as constrained in the UPDATE, because the two refusals answer different
	// questions: this one tells the editor what is wrong, the store's `AND version=?` guarantees it
	// even if some later handler forgets to ask.
	if rep.Version != s.st.ManualVersion() {
		jsonErrorCode(w, http.StatusConflict, "report_not_manual",
			"这是工作流生成的报告，不能直接修改；请另存为人工报告")
		return
	}
	var in reportEditInput
	if err := readJSONLimit(r, &in, maxManualRequestBytes); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	next, viewers, ok := s.validateReportEdit(w, in)
	if !ok {
		return
	}
	written, err := s.st.UpdateManualReport(id, next, in.UpdatedAt)
	if err != nil {
		var exists ErrReportExists
		switch {
		case errors.As(err, &exists):
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"error": "report_exists", "message": "已存在同样的人工报告", "id": exists.ID})
		case errors.Is(err, ErrReportStale):
			jsonErrorCode(w, http.StatusConflict, "report_stale", "报告已被他人修改，请刷新后重试")
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if err := s.st.SetReportViewers(id, next.Date, viewers); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordChange(r, user, AuditReportEdit, "report", strconv.FormatInt(id, 10), map[string]any{
		"symbol": next.Symbol, "date": next.Date, "subtype": next.RType, "title": next.Title,
		"audience": strings.ToLower(strings.TrimSpace(in.Audience)), "viewers": len(viewers),
	})
	writeJSON(w, map[string]any{"ok": true, "id": id, "updated_at": written})
}

// apiReportDelete removes a hand-written report. Machine-generated ones are refused: deleting the
// record of a run is a storage decision, and it has its own console and its own retention policy.
func (s *Server) apiReportDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	rep, _ := s.st.GetNew(id, s.viewerScope(user))
	if rep == nil {
		jsonError(w, http.StatusNotFound, "report not found")
		return
	}
	if rep.Version != s.st.ManualVersion() {
		jsonErrorCode(w, http.StatusConflict, "report_not_manual",
			"这是工作流生成的报告，不能在这里删除")
		return
	}
	if _, err := s.st.DeleteReport(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordChange(r, user, AuditReportDelete, "report", strconv.FormatInt(id, 10), map[string]any{
		"symbol": rep.Symbol, "date": rep.Date, "subtype": rep.RType, "title": rep.Title,
		"manual": true,
	})
	writeJSON(w, map[string]any{"ok": true})
}
