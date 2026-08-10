package app

import (
	"net/http"
)

// Admin and reader APIs for report versions (ADR 0024).
//
// The admin side is one page: register the written forms a report can be published in, decide for
// each whose reports a reader sees, and tick which OUs or accounts may read it. The reader side is
// one endpoint: which versions of the report I am looking at may I switch to.

// GET /api/admin/versions — the registry plus, for each version, its grants; and the principals an
// admin can grant to, so the page needs no second round-trip to render its pickers.
func (s *Server) apiAdminVersions(w http.ResponseWriter, r *http.Request, user string) {
	versions := make([]map[string]any, 0)
	for _, v := range s.st.Versions() {
		versions = append(versions, map[string]any{
			"name": v.Name, "label": v.Label, "ord": v.Ord,
			"visibility": string(v.Visibility),
			"grants":     s.st.VersionGrants(v.Name),
			// The default version is where every version-less ingest lands, so the UI must not offer
			// to delete it. Saying so here keeps that rule in one place rather than in two.
			"is_default": v.Name == s.st.DefaultVersion(),
			"reports":    s.st.CountReportsOfVersion(v.Name),
		})
	}
	groups := make([]map[string]any, 0)
	for _, g := range s.st.ListUserGroups() {
		groups = append(groups, map[string]any{
			"principal": groupPrincipal(g.ID), "name": g.Name, "restricted": g.RestrictedEffective,
		})
	}
	users := make([]map[string]any, 0)
	for _, u := range s.st.Users() {
		users = append(users, map[string]any{
			"principal": userPrincipal(u.Username), "name": u.Username,
			"restricted": u.Restricted, "display": u.Name(),
		})
	}
	writeJSON(w, map[string]any{"versions": versions, "groups": groups, "users": users})
}

// POST /api/admin/versions — register or update one version, grants included, so a save is one
// atomic-looking action to the admin rather than "saved the version but not who can see it".
func (s *Server) apiAdminVersionSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name       string   `json:"name"`
		Label      string   `json:"label"`
		Ord        int      `json:"ord"`
		Visibility string   `json:"visibility"`
		Grants     []string `json:"grants"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	name := normalizeVersion(in.Name)
	if name == "" {
		jsonError(w, http.StatusBadRequest, "a version needs a name")
		return
	}
	// Captured before the write, because the write is what destroys the answer. Reading a version
	// takes BOTH a grant and a visibility that admits you (ADR 0024), so the two together are the
	// read permission and the line has to carry both — recording only the grants meant flipping a
	// version from owner-only to everyone logged a row whose before and after were identical.
	beforeVis := versionVisibility(s.st.Versions(), name)
	beforeGrants := s.st.VersionGrants(name)
	if err := s.st.SaveVersion(ReportVersion{
		Name: name, Label: in.Label, Ord: in.Ord, Visibility: Visibility(in.Visibility),
	}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetVersionGrants(name, in.Grants); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Written AFTER the change lands, not before. An audit line asserting an access change that
	// then failed is worse than a missing one: it is evidence of something that never happened.
	// Recorded with both sides, because "who can read this now" is answerable from the current
	// state — "when did they gain it, and who gave it" is not.
	s.st.WriteAudit(AuditEntry{Actor: user, ActorOU: s.st.PrimaryGroupOf(user), Action: AuditGrantChange,
		TargetType: "version", TargetID: name,
		Detail: auditJSON(map[string]any{
			"before": beforeGrants, "after": in.Grants,
			"visibility_before": string(beforeVis), "visibility_after": in.Visibility,
		})})
	writeJSON(w, okJSON)
}

// DELETE /api/admin/versions/{name} — unregister a version. Reports carrying the name are left
// alone; see Store.DeleteVersion for why that is deliberate.
func (s *Server) apiAdminVersionDelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeleteVersion(r.PathValue("name")); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// GET /api/report/{id}/versions — the other written forms of the report being read, filtered to
// what this reader may open. It powers the reading page's version switcher.
//
// Only versions that were actually GENERATED appear. Each form comes from its own run, so a
// registered-but-never-run version is absent rather than an empty tab — "this report has no external
// edition yet" is a normal state, not an error.
func (s *Server) apiReportVersions(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r, user, pathID(r, "id"))
	if rep == nil {
		jsonError(w, http.StatusNotFound, "report not found")
		return
	}
	labels := map[string]ReportVersion{}
	for _, v := range s.st.Versions() {
		labels[v.Name] = v
	}
	out := make([]map[string]any, 0)
	for _, sib := range s.st.VersionsOfReport(*rep, s.viewerScope(user)) {
		v := labels[sib.Version]
		out = append(out, map[string]any{
			"id": sib.ID, "version": sib.Version,
			"label": firstNonEmpty(v.Label, sib.Version),
			"title": sib.Title,
			// The generation time of each form, so a reader can see when one is staler than another —
			// the versions come from separate runs and can legitimately disagree in age.
			"time":    sib.Time,
			"current": sib.ID == rep.ID,
		})
	}
	writeJSON(w, map[string]any{"versions": out, "current": rep.Version})
}

// CountReportsOfVersion is shown next to each registry row so an admin can see what deleting or
// re-scoping a version would affect before doing it.
func (s *Store) CountReportsOfVersion(name string) int {
	var n int
	s.queryRow("SELECT COUNT(*) FROM reports WHERE version=?", normalizeVersion(name)).Scan(&n)
	return n
}

// VersionsOfReport lists the written forms of one report that this viewer may read — the rows
// sharing its identity apart from the version.
//
// Grouped by (symbol, date, subtype) and NOT by title. Each version is produced by its own run of
// its own workflow, so two forms of one analysis will almost never carry a byte-identical title;
// requiring that would make the switcher silently fail to group exactly the reports it exists for.
// Where several reports share (symbol, date, subtype) in one version, the newest wins — the same
// rule FindSameDayReport already uses, and for the same reason: the difference is generator output
// nobody can predict.
func (s *Store) VersionsOfReport(rep Rep, sc *ownerScope) []Rep {
	q := `SELECT id, COALESCE(version,''), COALESCE(title,''), COALESCE(sent_at,'')
	      FROM reports WHERE symbol=? AND rdate=? AND rtype=?`
	args := []any{rep.Symbol, rep.Date, rep.RType}
	if frag, fargs := sc.where(""); frag != "" {
		q += " AND " + frag
		args = append(args, fargs...)
	}
	rows, err := s.query(q+" ORDER BY id DESC", args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []Rep
	for rows.Next() {
		var r Rep
		if rows.Scan(&r.ID, &r.Version, &r.Title, &r.Time) != nil || seen[r.Version] {
			continue
		}
		seen[r.Version] = true
		out = append(out, r)
	}
	// Registry order, so the switcher is stable across reports rather than ordered by whichever
	// version happened to be generated last.
	ord := map[string]int{}
	for i, v := range s.Versions() {
		ord[v.Name] = i
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && ord[out[j].Version] < ord[out[j-1].Version]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// versionVisibility is the stored visibility of one version, or "" when it does not exist yet — a
// creation, whose "before" is genuinely nothing rather than a default worth printing.
func versionVisibility(all []ReportVersion, name string) Visibility {
	for _, v := range all {
		if v.Name == name {
			return v.Visibility
		}
	}
	return ""
}
