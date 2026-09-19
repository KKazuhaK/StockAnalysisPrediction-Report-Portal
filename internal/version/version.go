// Package version exposes build metadata injected at link time via -ldflags:
//
//	-X <module>/internal/version.Version=<tag>
//	-X <module>/internal/version.Commit=<short-sha>
//	-X <module>/internal/version.BuildDate=<RFC3339>
//
// Useful for production triage, aligning Release archives with images, and the
// /api/version endpoint shown in the SPA.
package version

import "fmt"

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// String combines the fields into one line for the startup log and the
// `report-portal version` subcommand.
func String() string {
	return fmt.Sprintf("%s (%s, %s)", Display(), Commit, BuildDate)
}

// Display is the product version as a person reads it: the CalVer number without the leading "v" of
// the git tag that carries it — 2026.38.1, not v2026.38.1. A diagnostic version such as "dev", "ci"
// or a legacy v0.4.x tag is passed through unchanged, because there is no number to trim.
//
// Version itself stays the tag verbatim. It is the build's identity, not its label: the backup header
// records it, /api/version returns it, and the SPA compares it against the running build to notice a
// deploy or a rollback. Trimming it in those places would make two different builds of one number
// indistinguishable.
func Display() string {
	if len(Version) > 1 && Version[0] == 'v' && Version[1] >= '0' && Version[1] <= '9' {
		return Version[1:]
	}
	return Version
}
