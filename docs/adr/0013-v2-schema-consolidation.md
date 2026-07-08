# ADR 0013 — V2 schema consolidation

## Status

Accepted — 2026-07-08. Targets the next release, **v0.2.0**. The project is pre-1.0, so
per semver each `0.y` bump (v0.1 → v0.2) is the breaking boundary that squashes migrations —
"major boundary" throughout this ADR means the `0.y` bump, not a `1.0`/`2.0` bump.

Owner acceptance bar (2026-07-08): proceed only if **(a)** external callers are unaffected and
**(b)** the structure gets simpler, not more complex. Both verified:

- **(a) External calls unchanged.** The folded attributes are already JSON fields on the domain
  structs (`display_name`/`email`/`active`, `group_id`, `priority`, `run_at`, `ord`); the fold
  only changes their source (column vs join), so every API response is byte-identical. The
  `/api/v1` machine API and the webhook layer reference none of the touched tables. The `rid`
  wire format is derived (`fmt.Sprintf("n%d", id)`), so `reports.rowid → id` does not change it.
- **(b) Simpler.** 27 → 20 tables, three joins removed, one consistent PK name. The only added
  code is the one-time `migrateV1toV2` function — transitional, isolated, deleted at v0.3.0.
  (`V1`/`V2` in the migration name = internal **schema generation** 1 → 2, independent of the
  release tag; schema generation 2 ships in release v0.2.0.)

## Context

The schema grew under the old hard rule "additive-only, tables are never `ALTER`ed"
(now retired — see CLAUDE.md "Hard rules"). Under that rule, **every** attribute added
to an existing table had to become its own 1:1 side table joined back at read time, and
a superseded design could only be left in place, never removed. The result is ~27 tables
where a meaningful fraction exist only as a workaround, not by design:

- **Pure 1:1 side tables** — one row per parent row, always read via `LEFT JOIN … COALESCE(default)`:
  `user_profiles`, `user_primary_group`, `group_priority`, `target_order`, `job_queue`,
  `job_schedule`.
- **Dead tables/columns left behind by a superseded design**: `user_group_members`
  (many-to-many membership, superseded by the single primary group of ADR 0010 group model B;
  no read path consults it for resolution — only delete-cleanup remains), and `links.collapsed`
  (superseded by `link_groups`; no Go reader).
- **A misnamed surrogate key**: `reports.rowid` is the only table whose surrogate PK column
  is not called `id` (every other `id %s` table uses `id`). `rowid` also shadows SQLite's
  implicit rowid, which is confusing.

v2.0.0 is the moment to pay this down: per the migration policy, a major bump squashes all
accumulated migration code into a fresh base schema, so the base `CREATE TABLE` set is rewritten
from scratch anyway. Consolidating now costs one V1→V2 migration (squashed again at V3) and a
sweep of the affected queries; it costs nothing in the dev loop (delete `data/portal.db`, rebuild).

## Decision

### 1. Fold the six 1:1 side tables into their parent as columns

| Side table (dropped) | Folds into | New column(s) | Default / NULL meaning |
|---|---|---|---|
| `user_profiles` | `users` | `display_name`, `email`, `active`, `last_login` | `active` DEFAULT 1; others empty |
| `user_primary_group` | `users` | `group_id` | NULL = the Default group |
| `group_priority` | `user_groups` | `priority` | NULL = inherit (unchanged semantics) |
| `target_order` | `batch_targets` | `ord` | NULL/absent sorts after ordered ones |
| `job_queue` | `batch_jobs` | `priority` | DEFAULT `'normal'` |
| `job_schedule` | `batch_jobs` | `run_at` | DEFAULT `''` (no schedule); index `idx_batch_jobs_run_at` |

Every one of these is today a `LEFT JOIN … COALESCE(x, <default>)`. The fold is behaviour-preserving:
the COALESCE default becomes the column default. Six tables and six joins removed.

`job_queue` / `job_schedule` are folded despite the entanglement with the run-queue redesign
(ADR 0004/0011, run-queue-single-gate direction) because:

- `job_queue` is a **misnomer** — it holds no queue order, only a job's `priority` attribute; the
  actual queue is computed in memory by the scheduler from `SchedulableJobs`.
- Run-level admission already moved to the item level (`MarkItemRunning`, ADR 0011); the job is
  the **producer**, and `priority` / `run_at` are producer attributes that survive any future
  gate redesign. Storing them as `batch_jobs` columns is *more* aligned with "batch = producer",
  not less. If a later redesign introduces a separate run-request entity, that is a future
  (squashable) migration — folding now locks nothing in.

### 2. Drop dead tables/columns (finish the group-model-B migration)

- **Drop `user_group_members`.** This commits the product to ADR 0010 group model B:
  **one user has at most one group** (`users.group_id`). The many-to-many table is gone, not
  merely unread. *(This is a semantic decision, not just a cleanup — confirmed in scope.)*
- **Drop `links.collapsed`.** Superseded by `link_groups`; no reader.

### 3. Naming normalization

- **Rename `reports.rowid` → `reports.id`.** Every table's surrogate PK is now `id`. The rid
  wire format (`n<id>`) and `parseNewRID` are unchanged — only the column name moves (~26 refs,
  concentrated in `store.go` + `apiv1.go`).
- **Table names are left as-is.** Renaming tables (`plugins`, `chat_conversations`, …) is high
  churn for aesthetic gain and would break any external SQL/BI tooling pointed at the DB. The
  naming win we take is the consistent `id` PK and the removal of the `job_queue` misnomer.

### Result

27 tables → **20**. Reads for accounts, target listing, and the run queue each lose a join.
No user-visible behaviour changes.

## Migration (V1 → V2)

Existing production DBs (SQLite and Postgres) must carry their data across; the dev loop does not
(rebuild from the new base schema). Approach:

1. A `meta['schema_version']` marker (absent = generation 1). On startup, if `< 2`, run `migrateV1toV2`.
2. Guarded `ADD COLUMN` (both dialects) for the new folded columns **and** for any kept column an
   older-than-final v0.1 DB may still lack (`links.group_id`, `chat_conversations.starred`, the
   `user_groups` governance columns) — the "catch-up" set.
3. Copy each side table into its new column (portable correlated-subquery `UPDATE`, scoped
   `WHERE … IN (SELECT …)` so a parent without a side row keeps the column default). Copy
   `user_primary_group.group_id` → `users.group_id`.
4. Drop the six side tables + `user_group_members`; drop `links.collapsed`.
5. Rename `reports.rowid` → `id`.
6. Create the `run_at` partial index (after the column exists); set `meta['schema_version'] = '2'`.

This migration file is the code that the **next breaking bump (v0.3.0) deletes and folds into the
base schema**. **Refinement (validated below): because `migrateV1toV2` carries the catch-up
`ADD COLUMN`s, v0.2 upgrades *any* v0.1.x database directly** — the general "reach the last release
of the previous line first" squash rule does not bind the 0.1 → 0.2 boundary in practice (a
behind-HEAD prod DB with no `link_groups` / `links.group_id` still upgrades cleanly).

**Validation (2026-07-08).** `modernc.org/sqlite v1.53.0` bundles SQLite well past 3.35, so
`DROP COLUMN` / `RENAME COLUMN` run natively on both drivers (no table-rebuild fallback needed).
Verified end-to-end:

- SQLite: `TestMigrateV1toV2` (seeds a real v1 shape, asserts every fold/drop/rename + data
  preserved + idempotent) and `TestFreshStoreIsV2` pass; full `go test ./...` green.
- **Real production Postgres 18 dump** restored into a throwaway cluster and migrated: all 7 side
  tables dropped, `reports.rowid → id` with the `GENERATED ALWAYS AS IDENTITY` intact (a fresh
  insert auto-generated the next id), `links.collapsed` dropped / `link_groups` + `links.group_id`
  added, 7184 reports + 7 users preserved, `batch_jobs.priority='30'` kept verbatim, the two
  profile-less users kept `active=1`, only the one assigned user got `group_id`, and re-opening was
  a clean no-op. The repo's `TEST_POSTGRES_DSN` integration test passes on the v0.2 schema.
- **`user_groups.priority` NULL semantics** stay "inherit" (column nullable, no default; empty
  string clears to NULL) — confirmed by `TestResolveBasePriority`.

## Non-goals

- **Not** retro-collapsing side tables that exist for a real reason beyond "avoid ALTER"
  (e.g. `batch_items` per-row state, `app_files`, `tracking_items`). Relaxing the rule is not a
  mandate to merge everything.
- **Not** changing the run-queue gate mechanism (ADR 0004/0011) — this ADR only relocates the
  `priority` / `run_at` attributes, not the scheduler.
- **Not** renaming tables.

## Consequences

- One-time migration complexity and a full sweep of batch/user store queries + their tests
  (TDD: write the failing store tests against the new shape first).
- Simpler store: fewer joins, no COALESCE-default indirection, one consistent PK name.
- The single-group commitment (dropping `user_group_members`) becomes irreversible without a
  new migration — acceptable, it matches the shipped group model B.
