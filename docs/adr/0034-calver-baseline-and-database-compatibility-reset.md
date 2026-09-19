# ADR 0034 — CalVer release identity and a database compatibility reset

**Status: Accepted.** 2026-09-19. Supersedes the squash-at-every-major-boundary policy of
[ADR 0013](0013-v2-schema-consolidation.md) (whose historical rationale is preserved there).
Amends the boundary guarantees of [ADR 0027](0027-backup-and-restore.md).

## Context

Two things were tangled together and are now separated.

**Release identity.** Numbering was SemVer (`v0.4.72`), and several rules keyed off that shape: CI
inferred pre-release maturity — and whether `:latest` moved — from a hyphen in the tag, and the
migration policy in ADR 0013 keyed "major boundary" to the `0.y` bump. Neither is a good fit: the
tag spelling was silently deciding product stability, and a calendar-driven boundary forced
migration retirement on a schedule nobody chose.

**Migration debt.** Every release line accumulated migration code, and each `0.y` boundary had to
squash it into the base schema. The v0.4 line's accumulation is `upgrade_v04.go` plus the additive
column reconciliation in `ensureColumns` plus the report-version backfill in
`reconcileReportVersions`. Retiring the line's boundary rule without retiring the code would leave
the new runtime carrying conversion paths for shapes it no longer needs to serve.

The owner's decision (2026-09-19): the last SemVer release, **v0.4.72**, is the last upgradeable
version and carries every prior migration. A database reaches the new line by first being started
once by v0.4.72; from there the move is seamless — the new binary accepts v0.4.72's shape directly
and writes nothing. The hard boundary is against everything *older* than v0.4.72, not against the
legacy line as a whole.

## Decision

### 1. CalVer release identity

Product display is `YYYY.W.R`; git and fixed image tags are `vYYYY.W.R`.

- `YYYY` is the ISO week-numbering year, `W` the UTC ISO week in which the series starts, `R` a
  counter starting at 1 that increases for every changed published artifact set. No leading zeroes.
- **No maturity suffix.** There is no `-beta` / `-rc`. Maturity lives in GitHub Release metadata
  (draft / pre-release / full release / Latest), never in the tag, and never in the binary.
- Promotion of an identical artifact set keeps its number; changed artifacts require a new number.
- Comparison is numeric on the `(YYYY, W, R)` tuple, never lexical. A series keeps its original
  year/week across delayed publication and maintenance.
- Historical tags, assets and Git history are preserved and must stay readable.

### 2. The database baseline is the v0.4.72 shape

- The base schema in `store.go` is unchanged by this reset: it **is** the fully-migrated v0.4.72
  shape. A reset here means deleting the code that converts *older* shapes, not changing the shape.
- `schema_version` stays `2`. The marker value is not bumped: bumping it would either reject a
  v0.4.72 database (not seamless) or require a startup write to re-stamp it (not zero-write).
- The boundary is therefore enforced by **shape verification, not by a generation number alone**:
  every table, every column and every index name declared by `baseSchemaStmts()` must already
  exist. A marker of the expected value never certifies a structurally invalid database on its own.
  A superset is accepted — a database carrying a leftover column from a dropped feature is still a
  v0.4.72 database.
- **Verification runs before any DDL.** `init` classifies a read-only state first and only then
  chooses a path, so a rejected database is never partially mutated (previously `createBaseTables`
  ran before the checks that could refuse the database).

| Database state | Result |
| --- | --- |
| Truly empty (no `meta`; none of `reports`/`users`/`links`/`batch_jobs`) | Create the complete schema, seed, then stamp `schema_version=2` |
| Interrupted fresh initialization (`meta` present, no `schema_version` row, every base table empty) | Resume as a fresh install — every statement is `IF NOT EXISTS`/idempotent. Never bless a *nonempty* database this way. |
| v0.4.72 shape (`schema_version=2`, shape verification passes) | Start normally. No DDL, no writes. |
| Older generation (marker missing, unparseable or `<2`, or shape verification fails) | Reject before any mutation, naming v0.4.72 as the required bridge |
| v0.4.1 database (`sso_keyring` / `sso_group_rules` / `sso_auth_requests` present) | Reject, with the existing explanation (boot would mint a second data key) |
| Newer generation (`schema_version>2`) | Reject; upgrade the portal first |
| Unknown or corrupt (nonempty, marker absent or damaged) | Reject; never guess and never modify |

### 3. Historical upgrade code is removed from the runtime

Deleted, by purpose rather than by filename:

- `upgrade_v04.go` whole — `upgradeV04` and `importLegacyAnnouncement` — and the `s.upgradeV04()`
  call in `init`. v0.4.72 has already run the announcement import and written
  `announcements_imported`, so the new runtime has nothing to adopt.
- `ensureColumns`' reconciliation body: the `ALTER TABLE … ADD COLUMN` loop. The function is
  retained because its *place* in the startup order is still the right one, but its body becomes a
  read-only verification of the same `baseSchemaStmts()` declaration list, and it is renamed
  `verifyBaseSchema` so the name describes what it does. `duplicateColumnErr` existed only to serve
  the ADD loop and is deleted with it.
- `reconcileReportVersions`' backfill (`UPDATE reports SET version='default' …`),
  its `DROP INDEX IF EXISTS idx_reports_ident`, and `identIndexCoversVersion`. A v0.4.72 database
  already carries a version on every row and already has the five-column identity index.
- The retired-migration tests and the v0.3.10 fixture's *upgrade* assertions. The fixture is kept
  and repurposed: it is now the evidence that an older shape is refused.

Retained, because they are current operations that merely live near the deleted code:

- `ensureDefaultVersion` / `ensureManualVersion` — the version-registry seeds. They were only ever
  *reached* through `reconcileReportVersions`, but they are what makes a version-less ingest and
  the manual-report editor work. Both startup paths call them directly.
- `tableExists`, `columnExists`, `parseCreateTable`, `splitTopLevel`, `setSchemaVersion`,
  `createBaseTables`, `createBaseIndexes`, `EnsureDefaultGroup`. Note that `parseCreateTable` and
  `splitTopLevel` are also used by `backupTables` (ADR 0027) — deleting them with the migration
  code would break backups.

The resulting binary must create every table, column, constraint and index on a truly empty
database, and must refuse to touch anything it cannot fully interpret. There is no inline legacy
chain kept behind an unreachable condition.

### 4. Transition route: the v0.4.72 bridge

Data is preserved, not discarded, and not converted by a new offline tool. The route is the
release that already exists:

1. Start the database once with v0.4.72, which runs the accumulated migrations and leaves the
   database at the accepted shape.
2. Take a consistent, restorable backup, and retain `config.yaml` and the key material
   (`secret_key`, the SSO keyring held in `meta`).
3. Start the new release against that database. It verifies the shape and writes nothing.
4. Rollback is the old binary plus the retained pre-cutover database. A converted database must
   never be opened by an old binary.

No data-preserving conversion utility is shipped: the bridge is v0.4.72 itself. v0.4.72's binary
and image must therefore stay retrievable for as long as a rollback must be possible.
`docs/releases/README.md` carries the runbook and the tested SQLite and Postgres backup commands.

### 5. Future migration retirement is deliberate, not calendrical

The first CalVer full release starts a new compatibility window: for a target full release at T,
direct upgrades are supported from full releases in this baseline family first made stable within
the preceding 12 calendar months, inclusive of the cutoff. First-stable dates come from the
promotion of a pre-release, not from the series week.

This is a *support promise about the target*, not a runtime date check: an existing binary never
rejects a source because time passed. Retiring migration code later requires proving no source
inside the window loses support, publishing and preserving a tested bridge that completes the
retiring chain, verifying source → bridge → target on both drivers, and only then updating the base
schema and the acceptance guard. A future bridge must actually produce a state the target accepts;
very old installations may need several.

### 6. Backups carry the same boundary

The dump header keeps format version, product version and database baseline as three distinct
fields. Restore inspects the dump's baseline *before* any destructive work and rejects a legacy,
future or unmarked dump; an empty target opening successfully proves nothing about the dump. The
permissive "dump has no generation field" path is dropped — the field shipped with the format. A
dump's per-table column set must cover the current base columns, so a dump from an older shape
cannot be restored into the new baseline and silently lose the columns it never had.

## Consequences

- The new runtime carries no code that converts a pre-v0.4.72 database. A database older than
  v0.4.72 fails loudly with instructions instead of half-upgrading.
- The base schema declaration list becomes load-bearing in a new way: it is now the acceptance
  contract for an existing database, not just a creation script. Adding a column to
  `baseSchemaStmts()` no longer silently back-fills it into every existing database — a changed
  column set now means a changed acceptance contract, and that is a deliberate decision.
- The development loop is unaffected: delete `data/portal.db` and let it rebuild.
- Maturity moves out of the tag and into GitHub metadata, so the release workflow gains a
  reconciliation step that owns the rolling channels and can be re-run after a missed event.
- Historical releases and tags are untouched. `v0.4.72` has no release-note file (the note
  convention post-dates some tags); that gap is recorded rather than back-filled, because a note
  written now would not match the tag's annotation.
