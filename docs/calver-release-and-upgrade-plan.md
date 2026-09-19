# CalVer cutover and database compatibility reset

Status: implementation handoff; accepted direction, implementation pending.
Prepared: 2026-09-19.
Reviewed checkout: `0211169e24f999649533a628507ec9be128716e5` (`v0.4.72`).

This document specifies the work for the next implementer. No application code, database,
Git tag, or published release is changed by this document. Recheck the target branch and actual
production versions before implementation.

## 1. Final requirements and precedence

The first CalVer release is an explicit database compatibility boundary. Remove all existing
historical upgrade code from the new release and establish a complete fresh-install schema.
An existing legacy database must not be silently accepted or partially upgraded by the new binary.

This decision supersedes the earlier proposal to make the initial numbering cutover compatible
with existing databases. The 12-month direct-upgrade policy starts with the new baseline; it does
not retroactively cover the legacy release lines.

| ID | Requirement |
| --- | --- |
| V1 | Product display uses `YYYY.W.R`; Git and fixed image tags use `vYYYY.W.R`. |
| V2 | New release numbers have no alpha, beta, rc, or other maturity suffix. |
| V3 | GitHub metadata owns draft/pre-release/full-release status. Latest identifies the recommended full release. |
| V4 | Changed published artifacts require a new number; promotion of identical artifacts does not. |
| V5 | Preserve historical tags, release assets, and Git history. |
| D1 | The first CalVer release establishes a new database compatibility baseline. |
| D2 | Remove all existing historical schema upgrades, data adoption, and legacy repair logic from the new runtime. |
| D3 | Fresh installation creates the complete current schema and required seed state directly. |
| D4 | Legacy, unknown, or newer incompatible databases are rejected before mutation. |
| D5 | Preserve data through an explicit, separately tested transition route; do not delete or reset a user's database automatically. |
| D6 | Following the reset, support direct upgrades from new-baseline full releases within at least the preceding 12 calendar months. |
| D7 | Future migration retirement is deliberate and documented, never triggered by changing calendar numbers. |
| D8 | Release notes state the boundary, supported database states, transition steps, and rollback requirements. |

Removing upgrade code is authorized implementation scope. It does not authorize production data
loss or an arbitrary new database layout. Concrete persisted baseline/ledger changes still require
owner confirmation under [AGENTS.md](../AGENTS.md), before writing those changes.

## 2. Scope and non-goals

Implement the release-numbering change, GitHub publication lifecycle, clean database baseline,
legacy-database rejection, and a documented deployment/transition procedure. This handoff itself
only changes documentation; actual implementation and publication happen later.

Do not renumber `/api/v1`, backup formats, report editions, workflow execution versions, installed
apps, or dependencies merely because the product adopts CalVer. Do not collapse domain side tables
or redesign report identity as incidental cleanup. Do not rewrite Git history.

Do not promise direct legacy upgrades, automatic downgrade, indefinite historical migration
support, or an automatic application updater. Do not retain the old migration functions behind
an unreachable condition just to call the runtime clean.

## 3. CalVer contract

`YYYY` is the ISO week-numbering year. `W` is the UTC ISO week in which the release series starts.
`R` starts at 1 and increases for every changed published artifact set, regardless of maturity.
Week and revision have no leading zeroes. Validate whether week 53 exists in the selected year.

Examples are illustrative and do not reserve a release number:

| Tag | GitHub status | Meaning |
| --- | --- | --- |
| `v2026.38.1` | Pre-release | First published artifact set in the series. |
| `v2026.38.2` | Pre-release | A changed artifact set. |
| `v2026.38.2` | Full release | Promotion of that same artifact set, without rebuilding. |
| `v2026.39.1` | Pre-release | New series. |
| `v2026.38.3` | Full release | Later maintenance publication for an older series. |

A series retains its original year/week across delayed publication or maintenance. Empty weeks
require no releases. Revision gaps are allowed; published numbers must not be recycled. Compare
CalVer tuples numerically, not lexically. Larger numbers do not imply greater stability.

New-release validation rejects suffixes, extra components, zero week/revision, invalid weeks, and
leading zeroes. Preserve readability of historical tags and nonrelease `dev`/`ci` diagnostics.
Retain commit and build-time identity. Explicitly handle legacy tags during channel selection.

## 4. Repository inventory

| File or area | Current behavior and required work |
| --- | --- |
| [.github/workflows/release.yml](../.github/workflows/release.yml) | Builds six binary targets and two image architectures; currently infers stability and image channels from a hyphen. Replace that inference. |
| [scripts/tag-release.sh](../scripts/tag-release.sh) | Annotated tags from release notes, broad `v*.*.*` validation, no push. Tighten validation and preserve separate push. |
| [scripts/check-release-build.sh](../scripts/check-release-build.sh) | Build identity checks to preserve. |
| [internal/version/version.go](../internal/version/version.go) | Injected product version, commit, and build date. |
| [internal/app/server.go](../internal/app/server.go) | Authenticated `/api/version` and startup diagnostics. |
| [web/src/lib/useVersionCheck.ts](../web/src/lib/useVersionCheck.ts) | Detects deployed build identity changes, including rollbacks; not a release selector. |
| [internal/app/migrate.go](../internal/app/migrate.go) | Generation 2 baseline, legacy rejection, additive column reconciliation and helpers. Audit every function. |
| [internal/app/store.go](../internal/app/store.go) | Baseline checks, table creation, column reconciliation, report-version reconciliation, indexes, adoption, and fresh stamping. Replace legacy startup path. |
| [internal/app/upgrade_v04.go](../internal/app/upgrade_v04.go) | Legacy announcement import guarded by `announcements_imported`; remove from new runtime. |
| [internal/app/backup.go](../internal/app/backup.go) | Separate backup format, application version, and schema generation; source validation must enforce the new boundary. |
| [AGENTS.md](../AGENTS.md) and [ADR 0013](adr/0013-v2-schema-consolidation.md) | Replace ongoing release-line squash rules with the reset plus bounded future retention policy. |
| [ADR 0027](adr/0027-backup-and-restore.md) | Backup/restore guarantees to preserve and amend explicitly for the boundary. |
| [docs/releases/README.md](releases/README.md) | Update release and upgrade instructions without rewriting historical notes. |

Inspect product displays in `web/src/components/AppLayout.tsx` and
`web/src/pages/manage/ManageLayout.tsx`. Files such as `internal/app/version*.go` and
`web/src/lib/versionLabel.ts` concern report editions; do not rewrite them by filename alone.

The reviewed legacy code directs certain old databases through `v0.2.26` and certain SSO states
through `v0.4.2`. Generation 2 spans multiple legacy releases and is not a precise final-shape
marker. Verify the real source shape; do not treat generation 2 as proof that conversion finished.
The reviewed checkout has tag `v0.4.72` but no corresponding release-note file. Record this limitation
when choosing a legacy bridge; do not fabricate missing historical notes.

## 5. Clean database boundary

### 5.1 Removal inventory

Before editing, inventory all startup and CLI paths that mutate an old database. For each function,
record whether it performs historical conversion, current-schema initialization, or a current
runtime operation. The boundary removes behavior by purpose, not merely files named "migrate".

Remove historical behavior including:

- `upgradeV04`, legacy announcement import, and their startup calls.
- `ensureColumns` and supporting parsing/repair code used to reconcile old table shapes, unless a
  helper has a separately proven current operation that must remain.
- Old report-version backfills and identity-index conversion paths. Separate any required fresh
  report-version seeding from historical conversion before deleting the latter.
- Remaining copy/drop/rename/adoption routines, obsolete completion markers in fresh initialization,
  and opportunistic legacy schema repairs discovered during the audit.
- Tests that solely execute retired runtime migrations, replacing them with boundary tests and
  retaining transition fixtures where useful.

Do not remove current schema creation, required seed data, ordinary business writes, transaction
helpers still in use, backup validation, or the compatibility guard. The resulting binary must
create all required tables, columns, constraints, and indexes on a truly empty database.

Read relevant subsystem ADRs before moving their initialization or data-handling logic. Pay
particular attention to report identity, grants, SSO secrets, announcement initialization, and
backup table enumeration.

### 5.2 Startup behavior

The new baseline must be distinguishable from every legacy database. Propose the exact baseline
marker value and storage/bootstrap rules for confirmation; do not assume changing the product
number or blindly incrementing `schemaBaseline` is sufficient.

| Database state | Required result |
| --- | --- |
| Truly empty database | Create the complete schema, seed required state, and record the new baseline only after successful initialization. |
| Valid new-baseline database | Start normally without running historical repair/adoption. |
| Legacy database, including the final legacy shape | Reject before DDL, seed writes, key generation, or background workers; link the transition instructions. |
| Nonempty database with missing/invalid marker | Reject without guessing or modifying it. |
| Incompatible newer baseline or migration state | Reject before mutation. |
| Partial failed fresh initialization | Follow a tested retry or explicit recovery path; never silently bless incomplete schema as complete. |

Serialize initialization where multiple processes can target one database. Specify how SQLite
and Postgres handle transactional DDL, connection ownership, and interrupted initialization.
Do not let a marker of the expected value alone certify a structurally invalid database.

### 5.3 Legacy data transition

The new runtime must contain no legacy upgrade chain. Choose and document a separate transition
route before claiming existing deployments can retain their data across the boundary:

1. Audit the actual source version and database shape on a copy.
2. If necessary, run preserved legacy bridge binaries to reach the final verified legacy shape.
3. Take a consistent, restorable backup and retain configuration and key material.
4. Either install the new release on a fresh database with an explicitly accepted empty-data start,
   or run a separately delivered, offline conversion/export tool that produces a verified new-baseline
   database or import representation.
5. Validate records, identifiers, permissions, secrets, and baseline state before switching service.
6. Retain the untouched old database and old binary for rollback until cutover is accepted.

A legacy bridge by itself does not cross this new baseline: the new binary rejects even the final
legacy database. Data-preserving conversion must live outside the new runtime, in a separately
versioned transition utility or dedicated transition release. Keep any such code out of the
normal application startup path and deliverable binary.

The owner has not selected empty-data installation versus a data-preserving transition mechanism.
This is a deployment decision to resolve before touching real data, not permission to discard it.
If data retention is required, deliver and test the conversion route as a prerequisite to production
cutover. Never replace conversion with manually editing the generation marker.

If conversion is delivered, verify at least report IDs and identity, users/groups/grants, encrypted
credentials and required keys, app data, queue/schedule state, and announcements. Stop writers and
prevent bridge/transition runs from dispatching scheduled jobs or external workflows. Define how
IDs, sequences, and foreign relationships survive on both supported drivers.

### 5.4 Backup and rollback

The new restore implementation must inspect the source backup baseline before destructive work.
Opening an empty target with the new schema does not prove that a legacy dump is compatible.
Reject legacy, future, and ambiguous unmarked dumps unless a separately approved conversion path
handles them. Do not retain permissive missing-generation behavior merely for legacy compatibility.

Keep backup format version, product version, and database baseline distinct. Preserve rollback on
failed restore and coverage of every current table. Changing the backup format requires its own
explicit compatibility decision, not a blanket renumbering.

Rollback across this boundary means restoring the old binary with its pre-cutover database and
required configuration. Do not open the converted database with an old binary. Already published
old binaries cannot retroactively acquire new-baseline rejection guards.

Use consistent backups. A raw copy of a live SQLite file without WAL coordination is not an adequate
procedure. Document the actual tested SQLite and Postgres backup and recovery commands.

## 6. Future migration policy after the reset

The first full CalVer baseline release starts the new compatibility window. For a target full
release at T, support direct upgrades from full releases in this baseline family first made stable
within the preceding 12 calendar months, inclusive of the cutoff. Record first-stable dates for
promoted pre-releases; do not infer them from series week or later edits.

This is a target-release support promise, not a runtime date check or a promise of backported fixes.
An existing target binary must not reject a source merely because time has passed. Pre-release
schema compatibility is stated explicitly and is not covered by an unconditional stable guarantee.

Do not implement speculative domain migrations now. When a real future change needs migration,
introduce the minimal approved tracking design, ordered execution, atomic completion recording,
concurrency protection, and failure recovery. Database baseline, completed migration steps, and
product identity remain separate concepts.

At a later deliberate cleanup:

1. Prove that no source still inside the promised window loses direct-upgrade support.
2. Publish and preserve a tested bridge that completes the retiring migration chain.
3. Verify source -> bridge -> target on both drivers and publish the exact supported routes.
4. Only then remove retired migration code and update the base schema and acceptance guard.
5. Preserve bridge assets, checksums, image digests, fixtures, and operational instructions.

A future bridge must actually produce a state the target accepts. Very old installations may need
multiple bridges or an external converter; do not promise a single intermediate version forever.
This initial reset is the explicitly accepted exception for legacy lines.

## 7. Release lifecycle

### 7.1 Preparation and publication

Recommended implementation:

1. Tag push validates the CalVer tag and peeled commit, then prepares a draft Release.
2. Build the SPA once, six binaries, and both image architectures. Preserve identity, packaging,
   checksums, font coverage, and immutable-tag checks already present.
3. Push only the fixed version image and attach complete archives/checksums to the draft. Record
   durable tag-commit and image-digest metadata. Do not move rolling channels yet.
4. Verify completeness. The maintainer chooses Pre-release or full Release through GitHub metadata.
   A workflow may initialize that metadata from explicit input; tag spelling never chooses maturity.
5. Reconcile channels after publication and again after promotion, without rebuilding artifacts.

One-day Actions artifacts are insufficient for later promotion; use durable published/draft assets
and recorded digests. A draft GitHub Release does not make a pushed fixed GHCR tag private. If an
incomplete draft is published manually, reconciliation must refuse channel updates and report what
is missing. Retrying must not append duplicate notes or overwrite published assets.

### 7.2 Channels

| Channel | Rule |
| --- | --- |
| `:vYYYY.W.R` | Fixed artifact set; never replace published bytes. |
| `:latest` | Verified recommended full release selected as GitHub Latest. |
| `:beta` | Preserve existing compatibility alias: newest eligible published release, including full releases and pre-releases. |

The existing `:beta` image alias is not a product-version suffix. Renaming it is separate scope.
For ordinary publication, choose the highest eligible full CalVer release as Latest explicitly.
An old-series patch cannot displace a newer recommended version. Preserve the legacy stable target
until a full CalVer release is available. Document an explicit operator override for withdrawal or
intentional rollback; reconciliation must not undo that override.

Promotion retains tag commit, binary checksums, image digest, and build time. Update only Release
metadata and rolling pointers. Do not embed mutable maturity into the binary or require the portal
to contact GitHub to display its installed version.

### 7.3 Events and reliability

Handle initial publication, UI/API promotion, and relevant edits. Re-read current Release state,
verify artifact completeness, and serialize all channel writers. Under that serialization, recheck
ordering: serialization alone does not prevent stale jobs from moving a channel backward.

Provide manual reconciliation for missed events and partial failures. GitHub and GHCR updates are
not one transaction; retries must converge from durable state without rebuilding. Unknown versions,
API failures, missing assets, and drafts must not be treated as stable by default.

Verify event wiring in an isolated repository. GitHub documents `published` as covering both full
and pre-release publication, including from drafts; do not rely solely on `prereleased`. Test
promotion/edit behavior. See [Release events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release).

Default workflow-token actions generally do not trigger subsequent workflows. Explicitly connect
jobs or invoke shared reconciliation when necessary, and verify the repository's existing release
PAT arrangement. See [workflow triggering](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow).

Use GitHub's draft, prerelease, and Latest fields rather than issue labels or naming heuristics.
See [Releases API](https://docs.github.com/en/rest/releases/releases).

## 8. Detailed work packages

### A. Inventory and decisions

1. Read the files in section 4 and current relevant ADRs. Inventory actual deployments separately
   from repository tags; identify which data must survive cutover.
2. Produce a deletion map for every legacy upgrade path and its callers/tests; identify current
   initialization that must be retained separately.
3. Propose the exact new baseline/bootstrap design for owner confirmation before DB-shaped edits.
4. Select and document the legacy-data transition route. Resolve production data retention before
   deployment; other implementation work may proceed independently.
5. Add an ADR recording the accepted reset and future retention policy. Update AGENTS.md and mark
   the ongoing squash policy in ADR 0013 as superseded, preserving historical rationale.

Deliverables: deletion map, approved persisted-state design, transition decision, updated policy.

### B. Numbering and release workflow

1. Write failing tests for validation, numeric ordering, ISO year/week boundaries, invalid week 53,
   suffix rejection, revision >9, old maintenance releases, and legacy-history handling.
2. Update `scripts/tag-release.sh`; keep annotated tags and separate push. Use the release note
   from the selected commit, not an uncommitted working-file variant, for the annotation.
3. Implement draft preparation, readiness checks, durable identity, fixed-image publication, and
   lifecycle reconciliation from section 7. Remove all hyphen-based stability inference.
4. Test status changes, stale events, partial failures, image manifest promotion, and manual repair.
5. Verify the complete lifecycle in an isolated repository and registry before production use.

Deliverable: pure CalVer publication and promotion without unintended stable-channel updates.

### C. Clean schema and rejection guard

Dependency: confirmed baseline design from A.

1. Write failing tests for fresh installation and rejection of all legacy shapes before mutation.
2. Build the complete fresh schema/seed path and initialization recovery behavior.
3. Delete the historical paths in the inventory, including additive repair and adoption callers.
   Keep fresh report-version/default-group initialization as current behavior where needed.
4. Replace old runtime upgrade guidance with boundary/transition instructions. Do not retain an
   inline legacy migration chain as a fallback.
5. Tighten restore source validation for legacy, missing, corrupt, and future baseline markers.
6. Validate both drivers, concurrency, interrupted initialization, backup coverage, and seed state.

Deliverable: new runtime contains no existing historical upgrade code and never mutates a legacy DB.

### D. Transition and operations

1. Preserve the exact legacy binary/image needed for recovery and any prerequisite bridges.
2. If preserving data, deliver a separately versioned offline transition utility or transition
   release, with verified source acceptance and output matching the new baseline.
3. Rehearse stop writers -> backup -> convert or fresh-install -> verify -> switch -> rollback.
4. Record source/target compatibility, required configuration/key material, downtime, data checks,
   commands, and failure recovery. Do not invent commands before the implementation exists.
5. If an empty-data start is chosen, record that explicit deployment decision; do not infer it
   from permission to delete upgrade code.

Deliverable: a tested cutover runbook appropriate to the chosen data-retention requirement.

### E. Product surfaces and documentation

1. Verify CLI, startup log, authenticated version API, footer, management UI, and backup header.
   Display CalVer without `v` where appropriate while preserving full diagnostic identity.
2. Keep build-change detection sensitive to changed version/commit/buildDate and to rollbacks.
   Do not replace it with a numeric greater-than test or an external release lookup.
3. Update README, release instructions, release-note template, and future migration policy.
4. Keep new developer-facing text in English. Use all supported locales for any new UI strings.
5. Clearly state that the first CalVer release rejects legacy databases and starts the new
   12-month support policy; remove earlier promises of a compatibility-neutral naming cutover.

Deliverable: internally consistent operator and developer documentation.

### F. Verification and release handoff

1. Run section 9 checks and record evidence, skipped checks, and limitations.
2. Choose the real first tag from the series-creation week; examples are not reserved tags.
3. Prepare the explicit boundary release notes and complete artifacts.
4. Rehearse initial pre-release, identical-artifact promotion, channel updates, and failure repair.
5. Hand over tested publication, deployment, transition, and rollback commands. Publish/deploy only
   as a separate execution action; preparing this document performs neither.

Keep reviews focused: policy/design; release validation/workflows; schema cleanup/guards; separate
transition tool if needed; operations/display/docs. Do not merge an intermediate state that accepts
legacy databases after removing the code required to interpret them.

## 9. Acceptance and validation

| Area | Required evidence |
| --- | --- |
| Numbering | UTC ISO-year boundaries, week 53 validity, numeric ordering, malformed-tag rejection, readable legacy records. |
| Tag helper | Correct committed note/commit, duplicate refusal, no implicit push. |
| Release preparation | Six archives, checksums, embedded identity, both image architectures; no rolling update on failure. |
| Publication | Pre-release cannot become Latest; eligible full release updates stable channel. |
| Promotion | Identical archive checksums, image digest, commit, and build time; no rebuild. |
| Ordering/recovery | Older patches, stale/duplicate events, old reruns, partial failure, withdrawal, and manual recovery behave predictably. |
| Cleanup | Audited legacy upgrade/adoption/repair functions and callers are absent from the new runtime. |
| Fresh database | Complete schema, indexes, required seed data, baseline marker, and working primary application flows. |
| Legacy rejection | Final legacy DB, generation-1 DB, SSO legacy shapes, and unmarked nonempty DB are rejected before any writes. |
| Invalid/future state | Corrupt/unknown/future states fail clearly without mutation. |
| Initialization | Interrupted initialization and concurrent startup have tested safe outcomes on both drivers. |
| Restore | Legacy/unmarked/future dumps rejected before destructive restore; compatible backups round-trip; failed restore rolls back. |
| Transition | Chosen data-retaining route preserves records, IDs, relationships, permissions, secrets, and baseline state on both drivers; or explicit fresh-start decision is recorded. |
| Rollback | Old binary plus retained old database/configuration recovers service in rehearsal. |
| UI/build identity | CalVer display, CLI/API identity, offline operation, stale-tab reload and rollback detection work. |
| Policy | First CalVer boundary and prospective 12-month promise are consistent in current instructions and release notes. |

Write meaningful failing tests before implementation. Existing tests to inspect include
`internal/app/migrate_boundary_test.go`, `migrate_test.go`, `announcement_test.go`,
`backup_test.go`, and `backup_pg_test.go`. Keep ordinary announcement/report-version tests while
removing retired migration-only assertions. Use synthetic legacy fixtures and compare data/state
before and after rejection; an error string alone does not prove nonmutation.

Required repository checks:

```sh
go build ./...
go vet ./...
go test ./...
```

Run relevant database and backup suites with `TEST_POSTGRES_DSN` set against an isolated Postgres
instance. Skipped Postgres tests do not demonstrate cross-driver compatibility.

From `web/`:

```sh
npm ci
npm run typecheck
npm run test
npm run build
```

Also execute release-helper tests and an isolated GitHub/GHCR lifecycle rehearsal. YAML parsing
alone cannot verify events, permissions, publication, or manifest promotion.

## 10. Boundary release-note template

The committed note and annotated tag contain stable release content. Mutable maturity lives in
GitHub metadata; promotion must not require changing the tag annotation.

```markdown
# vYYYY.W.R - CalVer and database baseline reset

## Compatibility boundary

This release requires the new database baseline. Legacy databases cannot be opened directly.
Historical upgrade code has been removed from the application.

## Installation and transition

- Fresh installation: tested initialization commands.
- Existing data: link to the selected offline transition route and supported source states.
- Required legacy bridges: exact versions, if applicable.
- Backup, configuration/key material, downtime, and disabled background work requirements.
- Verification: record/permission checks and baseline validation before switching traffic.

## Future upgrades

The 12-month direct-upgrade support policy starts with this baseline's full release.
Product year/week changes do not automatically retire migrations.

## Rollback

Restore the previous binary with its retained pre-cutover database and configuration.
Do not run the old binary against the new-baseline database.

## Artifacts

- Fixed image tag and immutable digest.
- Archive checksums and build commit.
- Publication/channel instructions.
```

## 11. Completion checklist

- [ ] Accepted boundary/reset policy replaces the earlier compatibility-neutral cutover proposal.
- [ ] Exact baseline storage/bootstrap design is confirmed before database-shaped edits.
- [ ] Legacy upgrade code is removed, including hidden reconciliation/adoption paths.
- [ ] Current fresh initialization and application behavior remain complete.
- [ ] Legacy and incompatible states are rejected before mutation on both drivers.
- [ ] Backup source validation enforces the new baseline.
- [ ] Existing-data treatment is explicit; data-preserving transition is tested if required.
- [ ] Pure CalVer publication and immutable-artifact promotion are rehearsed.
- [ ] Channel selection and repair cannot silently regress or misclassify releases.
- [ ] Prospective support window and future bridge-retirement process are documented.
- [ ] Required checks have results and the implementer supplies executable cutover/recovery steps.
