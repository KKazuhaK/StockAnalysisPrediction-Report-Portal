# ADR 0022 — External-user access: OU tenancy, owner-scoped reads, run quotas, account validity

## Context

The portal is opening to **external** (non-company) users. Today's model has no answer for
any of the four things that admission requires:

- **RBAC-lite is coarse.** `roleRegistry` has three roles (`admin`/`operator`/`user`) and two
  permissions (`PermManage`/`PermRunBatch`); `PermRunBatch` is all-or-nothing.
- **Reports carry no owner.** `reports` has no creator column; identity is `symbol|rdate|rtype|title`
  and re-ingest **upserts** the shared row. Ingest (`POST /api/v1/reports`) authenticates with a
  machine Bearer token that carries no human identity. The only report→user trail is the fragile,
  unenforced chain `reports.run_id ≈ batch_items.run_id → batch_jobs.created_by`.
- **Every logged-in user sees every report.** `apiHome`/`apiStock`/`apiRun`/`apiRepBody` and the v1
  read endpoints apply zero row-level scoping.
- **Surfaces are global.** `batch_targets.surfaces` is a per-target allow-list, not per-group; and
  `AllowsSurface` has **no server-side caller** — surface filtering is client-only and craftable.
- **No run rate limit and no account expiry.** `MaxQueued` caps concurrency, not daily volume;
  tickets are urgent-only. Accounts have `active` (a manual on/off) but no validity period.

Four requirements drive the change:

- **R1** — a restricted principal may read only reports **its OU** generated; but if the requested
  report was already generated **today**, show it directly instead of re-running.
- **R2** — cap a restricted principal's runs (e.g. 2/day).
- **R3** — per group, specify **which** reports may be run and **where** they appear.
- **R4 (account validity)** — an account may carry an expiry after which it cannot authenticate.

The design is modelled on Google Workspace: an **Organizational Unit** decides *what you see / how
much / where*, a **Role** decides *what you can do*, and **quotas** bound usage.

## Decision

Treat "external/restricted" as a **property of an OU (group)**, not a new subsystem. The existing
`user_groups` (single primary group per user via `users.group_id`, layered resolution in
`EffectiveGroupSettings`) is promoted from a run-economics container into a full **OU tree** that is
both the **tenant boundary** and the **policy container**. Every new rule fires only when the
caller's effective OU is restricted, so internal staff (who resolve to the unrestricted root subtree)
are unaffected **by construction**.

Design-review decisions locked in (this ADR's non-negotiables):

- **Ownership unit = OU, not the individual.** Per-user "who ran it" audit already lives in
  `batch_jobs.created_by`; reports are stamped with `owner_group` only.
- **Same-day reuse pool = own OU + the internal (unrestricted root) subtree.** External OUs are
  **mutually isolated** even for same-day reports; they may reuse internally-generated reports of an
  entitled subtype. (A future per-OU `shared_today_feed` knob can widen this within one external org.)
- **Structure = OU tree** (`parent_id` + inheritance), not a flat flag or a separate "plan" object.
- **Additive-only schema, reconciled by `ensureColumns`.** Every change below is a new nullable/
  defaulted column, a new index, or a new side table, declared once in `baseSchemaStmts()`; no
  hand-written migration. Existing rows/behaviour are untouched, zero backfill.

### Master predicate

```
restricted(user) := EffectiveGroupSettings(user).Restricted && !isAdmin(user)
```

Admins are always exempt (matches today's run-governance exemption). Every internal account resolves
to the unrestricted root subtree → `restricted=false` → every new check is skipped.

### Data model (all additive)

| Object | Declaration | Purpose |
| --- | --- | --- |
| `user_groups.parent_id` | `BIGINT` (NULL = root) | OU tree; Default group is root |
| `user_groups.restricted` | `INTEGER DEFAULT 0` | internal/external switch, inherited down the tree |
| `user_groups.daily_run_quota` | `INTEGER` (NULL = inherit, 0 = unlimited) | R2 |
| `reports.owner_group` | `BIGINT` (NULL = internal/legacy/unattributed) | R1 attribution |
| `idx_reports_owner` | `INDEX ON reports(owner_group, rdate)` | R1 read/reuse path |
| `group_targets` | `(group_id BIGINT, target_id BIGINT, surfaces TEXT DEFAULT '', PRIMARY KEY(group_id,target_id))` | R3 allow-list |
| `users.expires_at` | `TEXT` (NULL = never) | R4 account validity |

`reports` identity index `idx_reports_ident(symbol,rdate,rtype,title)` is **unchanged**; `owner_group`
is not part of identity, so two OUs requesting the same `symbol|date|subtype|title` still share one row
(first-writer stamps the owner; the same-day clause makes this harmless the day it happens).

Not schema at all: the R2 daily counter (a windowed `SUM(batch_jobs.total)`), owner attribution (an
HMAC correlation token over `secret_key` + the `run_id→created_by` fallback), and a target's produced
`output_subtype` (stored in the existing `batch_targets.config` JSON).

### R1 — owner-scoped reads + same-day dedup

**Attribution (server-authoritative, payload not trusted).** A signed owner token
(`mintOwnerToken`: `HMAC(secret_key, ot1|ou_id|exp)`, prefix-tagged so it can't be confused with a
session cookie) is injected into a run's Dify inputs under the reserved key `_rp_owner_token` — but
**only for a restricted OU's run** (`Server.runInputs`, gated on `EffectiveGroupSettings(createdBy).Restricted`).
Internal runs pass their inputs through **byte-for-byte**, so existing (possibly undeclared-variable-strict)
workflows are untouched and the money-path is unchanged. The workflow echoes the token back in the
`/api/v1/reports` payload as `owner_token`; `v1Ingest` verifies it (`ownerFromToken`) and stamps
`owner_group` **first-writer-wins** (`StampReportOwner`: `UPDATE ... WHERE id=? AND owner_group IS NULL`).
A missing/invalid/expired token, and every internal run, leaves `owner_group` **NULL** (internal/
unattributed). A payload-supplied owner is never trusted. (The `run_id → batch_items.run_id →
batch_jobs.created_by` chain remains an optional best-effort fallback, deferred — the token is the
authoritative path and internal-as-NULL is acceptable, see Read scoping.)

**Read scoping.** For restricted viewers only, AND this into the WHERE of every read path
(`apiHome`/`apiStock`/`apiRun`/`apiRepBody`, `/api/symbols`, PDF export; v1 read endpoints stay
internal machine surfaces):

```
owner_group = :myOU
  OR (rdate = :panelToday AND rtype IN :allowedSubtypes
      AND (owner_group IS NULL OR owner_group IN :internalOUs))
```

The same-day branch's `owner_group IS NULL OR owner_group IN :internalOUs` is the internal pool: since
internal runs are intentionally left NULL (no token injected), NULL counts as internal here, so a
restricted OU can reuse an entitled report generated **today** by internal staff — but never one owned
by *another restricted* OU. `:internalOUs` = OU ids whose effective `restricted=0` (the root subtree;
small, cached). Unrestricted viewers get **no** predicate; NULL-owner reports stay fully visible to them.

**Same-day rule (unambiguous).** "Same request" = `(symbol, subtype)` on the panel-tz civil date —
**title excluded** (title is generator output the requester can't predict); this is exactly the key the
Dify app already dedups on. `:subtype` is the target's declared `output_subtype`;
`:panelToday = now.In(panelLocation()).Format("2006-01-02")`.
- **Display**: the second read clause admits any entitled same-day report from the own/internal pool.
- **Reuse gate** at run submit, *before* quota and *before* creating a job: if a matching same-day
  report exists in the own/internal pool → return `{reused:true, report_id}`, create no job, consume no
  quota; else run.

### R2 — daily run quota

`user_groups.daily_run_quota` (NULL = inherit, 0 = unlimited) resolves through the extended
`EffectiveGroupSettings`. No counter table, no cron — a lazy windowed query mirroring the ticket-period
pattern:

```
RunsToday(user) = SUM(total) FROM batch_jobs WHERE created_by=:user AND created_at >= :panelMidnightUTC
```

Rows, not jobs, are counted (a multi-row job can't dodge the cap; restricted users are held to one-row
submits). Enforced in `apiBatchJobCreate`'s existing `if !isAdmin` block, **after** the R1 reuse
short-circuit and the R3 allow-check, **before** enqueue: `q>0 && RunsToday+len(rows) > q → 429`.
Counted at submit (a later failure still costs the quota; abuse-resistant). Reset = panel-tz civil
midnight, implicit in the sliding window. `429` body `{error:"rate_limited", limit, used, resets_at}`;
the run modal shows a remaining-runs chip via a small `GET` endpoint; all copy through `t()`.

### R3 — runnable reports + where they appear

`group_targets(group_id, target_id, surfaces)`: a row = "this OU MAY run this target"; `surfaces` = the
OU's subset of `run|batch|recurring|chat` (`'' = inherit the target's global `batch_targets.surfaces`).
For a restricted OU the allow-list is **default-deny** and resolves up the OU tree (nearest ancestor with
rows wins). Unrestricted OUs ignore the table.

- **Submit-time** (`apiBatchJobCreate`): restricted ⇒ require `(ou,target) ∈ group_targets` (else 403)
  AND requested surface ∈ `(row.surfaces ∩ target.surfaces)` (else 403). This gives the dormant
  `AllowsSurface` its authoritative server-side call site, closing the client-only bypass. The submit
  payload gains an explicit `surface` field; every run-adjacent route shares this gate.
- **Display-time**: `GET /api/admin/batch/targets` returns only the caller's allowed targets (surfaces
  intersected), so the run modal only offers permitted workflows; run-shortcut entry buttons are derived
  from `group_targets`. Feed/stock row visibility is already handled by R1's owner+subtype predicate.

### R4 — account validity

`users.expires_at` is a **panel-tz civil date** `YYYY-MM-DD` (NULL/'' = never), consistent with how the
codebase treats date-only values (`rdate`, `date=today`) — not a UTC instant. The account is valid
*through* that whole day and is expired only once the panel-tz civil date is strictly greater
(`Server.accountExpired`: `today := now.In(panelLocation())`, expired when `today > expires_at`; ISO
dates compare lexicographically = chronologically). Orthogonal to `active`: *can authenticate* is
`active=1 AND (expires_at = '' OR panelToday <= expires_at)`. Enforced at **login** (`apiLogin`, beside
the existing `!u.Active` check, returning a 403 account-expired message) **and** on every request in
`currentActiveUser`, so an already-issued session dies the moment expiry passes. The admin user API
validates the date and refuses an already-passed cutoff on your own or the last-admin account (mirroring
the self-disable guard); `userJSON` exposes it and the UI edits it with a date picker.

## Enforcement points (audit checklist)

| Concern | Hook |
| --- | --- |
| R1 attribution | `Server.runInputs` (mint + inject token for restricted OUs, at run execution) + `v1Ingest` (verify, stamp `owner_group`) |
| R1 read filter | `apiHome`, `apiStock`, `apiRun`, `apiRepBody`, `/api/symbols`, PDF export |
| R1 reuse gate | `apiBatchJobCreate`, before quota + job creation |
| R2 quota | `apiBatchJobCreate` (`if !isAdmin` block) + `GET` remaining-runs endpoint |
| R3 run/surface | `apiBatchJobCreate` + run-adjacent routes; `GET /api/admin/batch/targets` filter |
| R4 validity | `apiLogin`, `currentActiveUser`, admin user API |
| Resolution | `EffectiveGroupSettings` walks `parent_id`; gains `Restricted`, `DailyRunQuota` |

## Rollout

- **P0** — schema (all seven declarations) + all new checks written but dormant behind
  `restricted && !isAdmin`. No restricted OU exists ⇒ internal behaviour byte-for-byte unchanged.
  Verified on SQLite and Postgres.
- **P-Validity** — R4 end-to-end (login + `currentActiveUser` + admin set + tests + UI field). Small,
  OU-independent; ships first as a complete vertical.
- **P1** — attribution: instrument the Dify workflow to echo `rp_run_token`; stamp `owner_group`.
- **P2** — read scoping on every read path + per-path tests + query-plan check.
- **P3** — R2 quota wiring + `429` UX + remaining-runs chip.
- **P4** — R3 `group_targets` + server gate (activates `AllowsSurface`) + targets filter + entry-button
  derivation + the group-edit "OU × target" matrix + target `output_subtype` UI.
- **P5** — onboarding: create restricted OU(s), populate `group_targets`, set quotas/expiry, create
  external accounts. Removing restriction = move the account to an unrestricted OU.

## Consequences

- **Internal users unchanged by construction** — the master predicate + fail-closed NULL owner mean no
  internal path changes behaviour until a restricted OU is created.
- **Read-filter completeness is the top risk.** The owner predicate must be added to *every* read path;
  missing one silently defeats R1. Guarded by a per-path audit + tests (the checklist above).
- **Attribution depends on the Dify workflow echoing the token.** A blank/mismatched token lands
  `owner_group=NULL`; mitigated by fail-closed invisibility + never trusting a payload owner + alerting
  on unattributed restricted-OU runs. External launch is gated on instrumenting the workflow (P1).
- **Shared-upsert ownership is first-writer-wins**; documented, harmless same-day, and next-day the
  non-owner simply re-runs into its own date/row.
- **Panel-tz correctness**: quota reset and the today-key reuse the proven `panelLocation()` civil-day
  math (DST-safe), not a fresh implementation.
- **Roles stay coarse in MVP** — no delegated external-org admin; external accounts are managed by
  internal admins until a later DB-backed-roles ADR.

## Alternatives considered

- **Flat restricted-group overlay** (no tree). Smallest change, but the moment an external org needs
  sub-teams it forks. Rejected: the OU tree subsumes it (a flat set of OUs under root is the degenerate
  depth-1 tree), so we commit the structure once and start shallow.
- **Entitlement "plan" object** attached to groups (SaaS-tier). Cleaner for many external tiers, but adds
  an indirection the four requirements don't need yet. Deferred; can layer on later.
- **Forking per-owner report copies** instead of a shared upsert row. Breaks the dedup identity and
  multiplies storage. Rejected.
