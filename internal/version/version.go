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
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, BuildDate)
}
