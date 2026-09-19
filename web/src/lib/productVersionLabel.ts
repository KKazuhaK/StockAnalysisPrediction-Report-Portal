// How the installed build's version is shown (ADR 0034).
//
// The release tag is v2026.38.1; the product version is 2026.38.1. The "v" belongs to git, not to the
// number a reader sees in the footer, and every other place a CalVer version appears — the release
// name, the release notes heading — spells it without one.
//
// The tag itself is deliberately NOT trimmed where it is used as an identity. /api/version returns it
// and useVersionCheck compares it against the running build to notice a deploy or a rollback; that
// comparison is an equality check over version, commit and build date, and trimming there would make
// two different builds of the same number look equal.
//
// A diagnostic version — "dev", "ci", "unknown" — passes through untouched: there is no number to
// trim, and inventing one would be a lie about what is running.
export function productVersionLabel(version: string): string {
  return /^v[0-9]/.test(version) ? version.slice(1) : version
}
