package app

import (
	"fmt"
	"strings"
)

// Report versions (ADR 0024).
//
// One analysis is published in several written forms: one carrying the scoring table, the weights
// and the prompt shape, another carrying only the conclusion. Each form is produced by its OWN run —
// running the internal workflow does not produce the external write-up and vice versa — so a report
// exists in whichever versions have actually been generated, and a missing version is normal.
//
// Version is a COLUMN on the report, not a second report type. Version and report type are
// orthogonal dimensions; encoding one inside the other costs (types × versions) registry entries and
// degrades precisely as versions multiply, which is the direction this is expected to grow.
//
// There is deliberately no built-in notion of "internal" or "external" anywhere in this file. A
// version is a name an admin registered; who may read it is configuration. The only distinguished
// row is the default, and it is distinguished purely mechanically: it is where a version-less
// ingest lands.

// defaultVersionName is the stable identifier of the seeded default version. It is an id, not a
// label — an admin renames what users SEE by setting Label, because report rows store the name and
// a renameable identity would orphan every report on the next rename.
const defaultVersionName = "default"

// manualVersionName is where every hand-written report lands (ADR 0026). It is an ordinary registry
// row — the read path, the grants and the version switcher know nothing special about it — with one
// mechanical distinction, the mirror of the default's: no ingest may write it. That is what makes it
// a place a person can put words without a workflow overwriting them on its next run.
const manualVersionName = "manual"

// Visibility decides WHOSE reports of a version a reader may see, once they have been granted the
// version at all. It is the answer to "can I see a report I did not ask for".
type Visibility string

const (
	// VisibilityOwner — only reports you personally asked for. The narrowest, and the default for a
	// newly created version: a forgotten setting must under-disclose.
	VisibilityOwner Visibility = "owner"
	// VisibilityGroup — anything anyone in your OU asked for. For a company client whose staff are
	// expected to share.
	VisibilityGroup Visibility = "group"
	// VisibilityAll — every report of this version, whoever asked. Turns the version into a
	// browsable library: a client onboarded today immediately sees the whole back catalogue.
	VisibilityAll Visibility = "all"
)

func validVisibility(v Visibility) Visibility {
	switch v {
	case VisibilityGroup, VisibilityAll:
		return v
	default:
		return VisibilityOwner // includes "" — an unset visibility is the narrowest, never the widest
	}
}

// ReportVersion is one row of the version registry.
type ReportVersion struct {
	Name       string // stable id, matched against the ingest payload's `version` and stored on rows
	Label      string // display name, admin-editable, "" = fall back to Name in the UI
	Ord        int
	Visibility Visibility
}

// ensureDefaultVersion seeds the registry so a version-less ingest always has somewhere to land.
// Idempotent — the same class of first-run seeding as EnsureDefaultGroup.
func (s *Store) ensureDefaultVersion() error {
	var n int
	s.queryRow("SELECT COUNT(*) FROM report_versions WHERE name=?", defaultVersionName).Scan(&n)
	if n > 0 {
		return nil
	}
	// The default version carries the reports that existed before versions did, i.e. everything
	// internal. VisibilityAll matches how those rows behave today for the people who can already
	// read them; who those people ARE is decided by the grants, which start empty.
	_, err := s.exec("INSERT INTO report_versions(name,label,ord,visibility) VALUES(?,?,?,?)",
		defaultVersionName, "", 0, string(VisibilityAll))
	return err
}

// ensureManualVersion seeds the version hand-written reports land in. Same first-run seeding as
// ensureDefaultVersion, and idempotent for the same reason: it must never overwrite a label or a
// visibility an admin has since changed.
//
// VisibilityGroup, not VisibilityAll, and that choice is the whole per-report audience mechanism.
// Under `all`, granting the version would hand a reader every hand-written report there is; under
// `group` the reader additionally has to appear on the report's own report_viewers list, which is
// exactly "who is this one for". So the two gates divide the question cleanly — the grant decides
// who may read hand-written reports at all, the viewer list decides which ones — using the table
// that already exists for it rather than a second parallel mechanism.
func (s *Store) ensureManualVersion() error {
	var n int
	s.queryRow("SELECT COUNT(*) FROM report_versions WHERE name=?", manualVersionName).Scan(&n)
	if n > 0 {
		return nil
	}
	// ord 50 puts it after the seeded default (0) and before an auto-registered workflow version
	// (100), so the switcher reads machine-first, hand-written-second without anyone ordering it.
	_, err := s.exec("INSERT INTO report_versions(name,label,ord,visibility) VALUES(?,?,?,?)",
		manualVersionName, "人工", 50, string(VisibilityGroup))
	return err
}

// ManualVersion is the version a hand-written report carries.
func (s *Store) ManualVersion() string { return manualVersionName }

// isManualVersion answers the one question the write paths ask of a version name. Callers must
// normalize nothing themselves; the trimming is here so an ingest payload of " manual " cannot slip
// past the refusal in v1Ingest by not matching the constant exactly.
func isManualVersion(name string) bool { return normalizeVersion(name) == manualVersionName }

// DefaultVersion is the version a report carries when its producer named none.
func (s *Store) DefaultVersion() string { return defaultVersionName }

// Versions lists the registry in display order.
func (s *Store) Versions() []ReportVersion {
	rows, err := s.query(`SELECT name, COALESCE(label,''), COALESCE(ord,0), COALESCE(visibility,'')
		FROM report_versions ORDER BY ord, name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ReportVersion
	for rows.Next() {
		var v ReportVersion
		var vis string
		if rows.Scan(&v.Name, &v.Label, &v.Ord, &vis) == nil {
			v.Visibility = validVisibility(Visibility(vis))
			out = append(out, v)
		}
	}
	return out
}

// Version reads one registry row. ok=false for a name nobody registered, which every caller must
// treat as "grants nothing" rather than as a permissive default.
func (s *Store) Version(name string) (ReportVersion, bool) {
	var v ReportVersion
	var vis string
	err := s.queryRow(`SELECT name, COALESCE(label,''), COALESCE(ord,0), COALESCE(visibility,'')
		FROM report_versions WHERE name=?`, normalizeVersion(name)).Scan(&v.Name, &v.Label, &v.Ord, &vis)
	if err != nil {
		return ReportVersion{}, false
	}
	v.Visibility = validVisibility(Visibility(vis))
	return v, true
}

// SaveVersion registers or updates one version. The name is the identity, so saving an existing
// name edits its label, order and visibility rather than creating a duplicate.
func (s *Store) SaveVersion(v ReportVersion) error {
	v.Name = normalizeVersion(v.Name)
	if v.Name == "" {
		return fmt.Errorf("a version needs a name")
	}
	_, err := s.exec(`INSERT INTO report_versions(name,label,ord,visibility) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET label=excluded.label, ord=excluded.ord, visibility=excluded.visibility`,
		v.Name, strings.TrimSpace(v.Label), v.Ord, string(validVisibility(v.Visibility)))
	return err
}

// DeleteVersion removes a version from the registry, refusing the default. Reports already carrying
// the name are left alone: deleting a registry row must not delete anyone's analysis. An
// unregistered version simply stops being grantable — unreadable rather than destroyed, and so
// recoverable by registering the name again.
func (s *Store) DeleteVersion(name string) error {
	name = normalizeVersion(name)
	if name == defaultVersionName {
		return fmt.Errorf("the default version cannot be deleted — every version-less report resolves to it")
	}
	// Same reason, from the other end: the hand-written version is where the editor writes, so
	// deleting it would leave the write path pointing at a name nobody has registered and every
	// hand-written report ungrantable — unreadable to exactly the readers it was addressed to.
	if name == manualVersionName {
		return fmt.Errorf("the manual version cannot be deleted — every hand-written report lands in it")
	}
	if _, err := s.exec("DELETE FROM version_grants WHERE version=?", name); err != nil {
		return err
	}
	_, err := s.exec("DELETE FROM report_versions WHERE name=?", name)
	return err
}

// normalizeVersion canonicalizes a version name from any source — an ingest payload, an admin form,
// a grant — so the same name written with stray whitespace cannot become a second, silently
// ungranted version.
func normalizeVersion(name string) string { return strings.TrimSpace(name) }

// resolveVersion maps an ingest payload's version onto a stored name. An empty version is the
// default; an unregistered name is registered on sight, mirroring how RegisterType already treats
// report subtypes. Refusing it instead would mean a workflow's output vanishes behind a 400 that
// nobody is watching — and an auto-registered version is granted to nobody, so appearing in the
// registry discloses nothing.
func (s *Store) resolveVersion(name string) string {
	name = normalizeVersion(name)
	if name == "" {
		return defaultVersionName
	}
	if _, ok := s.Version(name); !ok {
		s.exec(`INSERT INTO report_versions(name,label,ord,visibility) VALUES(?,?,?,?)
			ON CONFLICT(name) DO NOTHING`, name, "", 100, string(VisibilityOwner))
	}
	return name
}

// reconcileReportVersions prepares the reports table for the five-column identity index (ADR 0024).
// It runs between ensureColumns and createBaseIndexes, and the ordering is load-bearing:
//
//  1. seed the registry, so the default version exists to point rows at;
//  2. give every row a version — a NULL or empty one would compare DISTINCT inside the unique
//     index on both SQLite and Postgres, so the index would silently admit the duplicates it
//     exists to forbid;
//  3. drop the old four-column index, so the new definition can take its name.
//
// Every pre-version row resolves to the same default, so the five-column tuple is unique exactly
// where the four-column one was: the rebuild cannot merge two reports or fork one. That is the
// property that makes ADDING a component safe where removing one (v0.3.0, 626 reports merged) was
// not, and the tests assert it rather than leaving it as an argument.
func (s *Store) reconcileReportVersions() error {
	if err := s.ensureDefaultVersion(); err != nil {
		return err
	}
	// Seeded beside the default and before the early return below, so a portal that has already
	// rebuilt the identity index — i.e. every portal upgrading to this release — still gets it.
	if err := s.ensureManualVersion(); err != nil {
		return err
	}
	// The index already covering version means step 2 ran to completion in an earlier boot — the
	// index could not have been built otherwise. Returning here keeps startup off a full-table scan
	// of reports forever after: measured at 200k rows, the unguarded backfill cost ~700ms on EVERY
	// start, which is a migration quietly billing itself to every restart.
	if s.identIndexCoversVersion() {
		return nil
	}
	if _, err := s.exec(`UPDATE reports SET version=? WHERE version IS NULL OR version=''`,
		defaultVersionName); err != nil {
		return err
	}
	// The index is recreated from reportIdentIndex moments later, so dropping it here costs one
	// rebuild on the upgrade run and nothing on any run after it.
	if _, err := s.exec(`DROP INDEX IF EXISTS idx_reports_ident`); err != nil {
		return err
	}
	return nil
}

// identIndexCoversVersion reports whether the identity index already includes the version column,
// so a steady-state startup does not drop and rebuild a unique index over the whole reports table
// on every boot.
func (s *Store) identIndexCoversVersion() bool {
	var def string
	if s.driver == "postgres" {
		s.queryRow(`SELECT COALESCE(indexdef,'') FROM pg_indexes
			WHERE schemaname='public' AND indexname='idx_reports_ident'`).Scan(&def)
	} else {
		s.queryRow(`SELECT COALESCE(sql,'') FROM sqlite_master
			WHERE type='index' AND name='idx_reports_ident'`).Scan(&def)
	}
	return def != "" && strings.Contains(def, "version")
}

// ---------- who may read which version ----------

// A grant names a PRINCIPAL, and one column holds both kinds: an OU ("g:<id>") or a single account
// ("u:<name>"). Two tables — one for OUs, one for accounts — would mean the read path has two
// shapes, and the read path is the last place that should have two ways of being right. It also
// makes "no OU tree configured at all" a first-class case rather than a workaround: a lone external
// account is granted directly.
func groupPrincipal(id int64) string { return fmt.Sprintf("g:%d", id) }
func userPrincipal(name string) string {
	return "u:" + strings.ToLower(strings.TrimSpace(name))
}

// VersionGrants lists the principals granted one version.
func (s *Store) VersionGrants(version string) []string {
	rows, err := s.query("SELECT principal FROM version_grants WHERE version=? ORDER BY principal",
		normalizeVersion(version))
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

// SetVersionGrants replaces one version's whole grant list in a transaction, so a save is atomic and
// can never leave a version half-disclosed.
func (s *Store) SetVersionGrants(version string, principals []string) error {
	version = normalizeVersion(version)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM version_grants WHERE version=?"), version); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range principals {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := tx.Exec(s.bind("INSERT INTO version_grants(version,principal) VALUES(?,?)"),
			version, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GrantedVersions resolves which versions a user may read: their account's own grants if it has
// any, otherwise the NEAREST ancestor OU that has some. Empty means nothing — default-deny, so an
// account nobody configured discloses nothing.
//
// Nearest-wins rather than union, and the reason is this project's tree shape: external OUs hang off
// the Default OU, so a union would push whatever the root was granted down into every tenant, the
// internal version included. Configuring a nearer principal is how an admin narrows.
func (s *Store) GrantedVersions(username string) []string {
	if v := s.principalVersions(userPrincipal(username)); len(v) > 0 {
		return v
	}
	chain := s.groupChain(username) // root → leaf
	for i := len(chain) - 1; i >= 0; i-- {
		if v := s.principalVersions(groupPrincipal(chain[i])); len(v) > 0 {
			return v
		}
	}
	return nil
}

// principalVersions lists the versions one principal is granted directly, ordered by the registry so
// the reading page's version switcher is stable rather than reordering itself per report.
func (s *Store) principalVersions(principal string) []string {
	rows, err := s.query(`SELECT g.version FROM version_grants g
		LEFT JOIN report_versions v ON v.name = g.version
		WHERE g.principal=? ORDER BY COALESCE(v.ord,0), g.version`, principal)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// SetUserRestricted throws the scoping switch on one ACCOUNT. Until now "restricted" was purely an
// OU property, so a portal that never built an OU tree had no way to scope anybody — which is the
// setup an external user is most likely to arrive into first.
func (s *Store) SetUserRestricted(username string, restricted bool) error {
	v := 0
	if restricted {
		v = 1
	}
	_, err := s.exec("UPDATE users SET restricted=? WHERE username=?", v, username)
	return err
}

// isRestricted reports whether a user's reads are scoped at all. The account flag and the OU flag OR
// together, so an OU-restricted member cannot be un-restricted by leaving their own flag off.
// Admins are never scoped: someone has to be able to diagnose a tenancy problem.
func (s *Server) isRestricted(username string) bool {
	if username == "" || s.isAdmin(username) {
		return false
	}
	if u := s.st.GetUser(username); u != nil && u.Restricted {
		return true
	}
	return s.st.EffectiveGroupSettings(username).Restricted
}

// AddReportViewer records that someone asked for a report. It is what "only my own reports" is made
// of, and it is additive by design: when a same-day request is served from an existing report the
// requester JOINS its viewer list instead of triggering a second run, so strict per-person isolation
// costs no duplicate generation. Two people can each see "their" report and neither learns of the
// other.
//
// Both principals are recorded — the person and, when there is one, their OU — so a version whose
// visibility is later widened from owner to group needs no backfill.
func (s *Store) AddReportViewer(reportID int64, rdate, username string, ou int64) error {
	principals := []string{userPrincipal(username)}
	if ou != 0 {
		principals = append(principals, groupPrincipal(ou))
	}
	for _, p := range principals {
		if _, err := s.exec(`INSERT INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)
			ON CONFLICT(principal,rdate,report_id) DO NOTHING`, p, rdate, reportID); err != nil {
			return err
		}
	}
	return nil
}
