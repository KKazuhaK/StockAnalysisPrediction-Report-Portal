package app

import (
	"errors"
	"net/http"
	"strconv"
)

// The edit history's HTTP surface (ADR 0026).
//
// Three endpoints and no fourth way to write a report. Restore is a save: it loads a prior state and
// hands it to UpdateManualReport, so it snapshots the version it replaces, refuses on a stale token,
// translates an identity collision, moves the audience with the date and is bounded by the same cap
// — all of it inherited rather than reimplemented. That is the same argument the editor makes for
// having no "fork" verb.
//
// Authorization is the parent report's, not the revision's — a revision carries no viewer rows and
// must not, because a second read path into the same content is exactly what ADR 0024 refuses. That
// leaves one gap the parent cannot cover, and it is closed by refusing the whole surface to any
// caller whose reads are scoped at all: see revisionParent.

// revisionParent resolves the report a revision request is about, refusing anything the caller may
// not read, anything a workflow produced, and anyone whose reads are scoped.
//
// That last refusal is the one worth explaining, because it is broader than it looks and it is
// deliberate. A revision is authorized through its report, and a report's audience is MUTABLE: the
// editor rewrites report_viewers on every save. A revision is not — it is a frozen copy of what the
// report used to say. So a reader added to the audience today would inherit read access to every
// version written while the report was addressed to somebody else.
//
// That is not a theoretical ordering. "Take the internal detail out, then add the client" is the
// natural way an author widens an audience, and it is exactly the sequence that leaves the
// pre-sanitisation text one request away for the reader who was just added — in full from the body
// endpoint, and as removed lines in the diff without even opening it. Before this feature that text
// was gone; the history is what makes it reachable, so the history is what has to refuse.
//
// The narrow fix would be to snapshot each version's audience and authorize a revision against the
// audience in force when it was written. That is the right answer the day somebody actually runs a
// scoped editor; nobody does, and inventing a second audience table to serve nobody is how a read
// path acquires the two ways of being right ADR 0024 exists to prevent. So: the edit history is an
// internal surface, and it says so by failing closed.
func (s *Server) revisionParent(w http.ResponseWriter, r *http.Request, user string) *Rep {
	if s.isRestricted(user) {
		jsonErrorCode(w, http.StatusForbidden, "revision_history_internal_only",
			"编辑历史仅对内部账号开放")
		return nil
	}
	rep, _ := s.st.GetNew(pathID(r, "id"), s.viewerScope(user))
	if rep == nil {
		jsonError(w, http.StatusNotFound, "report not found")
		return nil
	}
	if rep.Version != s.st.ManualVersion() {
		// Not 404: the report exists and the caller may read it. It simply has no history, because
		// nothing has ever edited it — a workflow's report is the record of one run.
		jsonErrorCode(w, http.StatusConflict, "report_not_manual",
			"这是工作流生成的报告，没有编辑历史")
		return nil
	}
	return rep
}

// apiReportRevisions lists one report's history, newest first. Bodies are not included — see
// Store.Revisions.
func (s *Server) apiReportRevisions(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.revisionParent(w, r, user)
	if rep == nil {
		return
	}
	writeJSON(w, map[string]any{
		"revisions": s.st.Revisions(rep.ID),
		// The current state, so the list can show what the newest revision was replaced BY without a
		// second request. It is not a revision row and is deliberately not shaped like one: it is
		// the report.
		"current": map[string]any{
			"savedAt": rep.Time, "author": rep.Author, "title": rep.Title,
			"bytes": len(rep.MD),
		},
		"keep": s.reportRevisionsKeep(),
	})
}

// apiReportRevision serves one prior state in full, with a section diff against what the report says
// now.
//
// The diff is the answer to the question a history is opened to ask, and the body is the answer to
// the one asked just before restoring: an author about to overwrite the current text wants to read
// the old one as a document first. Both come back together because they are two views of one thing
// already loaded, and a second round trip to switch between them would be a spinner over a value the
// client already had.
func (s *Server) apiReportRevision(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.revisionParent(w, r, user)
	if rep == nil {
		return
	}
	revID, _ := strconv.ParseInt(r.PathValue("rev"), 10, 64)
	rev, ok := s.st.Revision(rep.ID, revID)
	if !ok {
		jsonErrorCode(w, http.StatusNotFound, "revision_not_found", "该历史版本不存在")
		return
	}
	// A whole prior body is about to be served, and diffLines keeps unchanged lines as context, so
	// this is a read of the document rather than of a delta — recorded for the same reason the
	// comparison endpoint records both of its sides.
	s.recordReportRead(r, user, rep)
	sections := diffMarkdown(rev.MD, rep.MD)
	changed := 0
	for _, sec := range sections {
		if sec.Status != "same" {
			changed++
		}
	}
	writeJSON(w, map[string]any{
		"revision": rev, "sections": sections, "changed": changed,
		"current": map[string]any{"savedAt": rep.Time, "author": rep.Author, "title": rep.Title},
	})
}

// apiReportRevisionRestore puts a prior state back.
//
// It goes through UpdateManualReport, which means the restore itself is snapshotted: the version it
// replaced becomes the newest entry in the history, so restoring is undoable by restoring again.
// Nothing is lost by pressing the button, which is the only way a restore button is safe to press.
func (s *Server) apiReportRevisionRestore(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.revisionParent(w, r, user)
	if rep == nil {
		return
	}
	revID, _ := strconv.ParseInt(r.PathValue("rev"), 10, 64)
	rev, ok := s.st.Revision(rep.ID, revID)
	if !ok {
		jsonErrorCode(w, http.StatusNotFound, "revision_not_found", "该历史版本不存在")
		return
	}
	var in struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// The caller's token, not the report's current one. Restoring is a write like any other, and a
	// restore computed from a history list drawn before somebody else saved must be refused rather
	// than silently winning — the whole reason the token exists.
	next := Rep{
		Symbol: rev.Symbol, Name: rev.Name, Date: rev.Date, RType: rev.RType, Kind: rev.Kind,
		Title: rev.Title, Source: rev.Source, MD: rev.MD,
		// The person restoring, not the person who originally wrote it. The report now says what it
		// says because this account put it back, and the revision row keeps the original authorship
		// of the text either way.
		Author: user,
	}
	// The subtype travelled with the revision and may since have been deleted from the registry;
	// re-registering it is what the editor's own save does, for the same reason.
	if next.Kind == "" {
		next.Kind = s.st.TypeKind(next.RType)
	}
	if next.Kind == "" {
		next.Kind = runKind([]string{next.RType})
	}
	s.st.RegisterType(next.RType, next.Kind)
	written, err := s.st.UpdateManualReport(rep.ID, next, in.UpdatedAt, s.reportRevisionsKeep())
	if err != nil {
		var exists ErrReportExists
		switch {
		case errors.As(err, &exists):
			// Restoring an old title can collide exactly as renaming can — another hand-written
			// report has taken the identity this one used to hold.
			reportExists(w, exists.ID)
		case errors.Is(err, ErrReportStale):
			jsonErrorCode(w, http.StatusConflict, "report_stale", "报告已被他人修改，请刷新后重试")
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.recordChange(r, user, AuditReportRestore, "report", strconv.FormatInt(rep.ID, 10), map[string]any{
		"revision": rev.ID, "saved_at": rev.SavedAt, "author": rev.Author, "title": rev.Title,
	})
	writeJSON(w, map[string]any{"ok": true, "id": rep.ID, "updated_at": written})
}
