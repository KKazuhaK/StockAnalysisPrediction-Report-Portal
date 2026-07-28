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
