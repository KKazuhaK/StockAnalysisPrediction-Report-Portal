# ADR 0010 — Group model B: one primary group + a Default fallback with per-field inherit

## Context

Groups were originally a many-to-many label (`user_group_members`): a user could belong
to several, and their run settings were resolved by taking the **max** across all of
them (ADR 0005 weight, ADR 0007/0008 priority). This had two problems operators hit:

- **Ambiguous resolution.** "Which group's weight applies?" was "the biggest one", which
  is surprising and makes it impossible to *lower* someone by adding them to a group.
- **No baseline.** Every setting had to be spelled out on every group; there was no
  single place to say "this is the house default for everyone".

## Decision

Adopt **group model B**:

1. **One primary group per user.** `user_primary_group(username PRIMARY KEY, group_id)`.
   A user has at most one. The old `user_group_members` table is left in place but is no
   longer consulted for resolution (see Migration).
2. **A Default group.** Exactly one group carries `user_groups.is_default = 1`, created
   idempotently at boot by `EnsureDefaultGroup`. Any user **without** a primary group
   inherits it. It is never deletable and always holds **concrete** baselines.
3. **Per-field inherit / override.** A non-default group stores each of `weight` and
   `urgent_unlimited` as either a concrete value (**override**) or SQL `NULL` (**inherit
   the Default group's value**). Base `priority` continues to live in the `group_priority`
   side table; empty there means "inherit the system default" (`run_default_priority`).

### Resolution

For a user `u` with effective group `g` = their primary group, else the Default group:

```
weight(u)          = g.weight            if g.weight IS NOT NULL else Default.weight
urgent_unlimited(u)= g.urgent_unlimited  if not NULL           else Default.urgent_unlimited
base_priority(u)   = group_priority[g]   if set                else run_default_priority
```

Implemented by `Store.EffectiveTicketSettings` (weight + urgent) and
`Server.resolveBasePriority` (priority). This **supersedes** ADR 0008's "max base priority
across the submitter's groups" — it is now the single primary group's override, else the
system default. Weight/urgent likewise stop being a max across groups.

## Schema

Additive only (no migrations, per the project's hard rules):

- `user_groups.weight` / `.urgent_unlimited` are **NULL-able** (NULL = inherit). Fresh
  DBs get `weight INTEGER, urgent_unlimited INTEGER`; existing DBs keep their columns
  (they already allowed NULL). `is_default INTEGER DEFAULT 0` is added via a guarded
  `ALTER TABLE ... ADD COLUMN` (the established `duplicateColumnErr` pattern).
- New table `user_primary_group(username TEXT PRIMARY KEY, group_id BIGINT)`.
- `EnsureDefaultGroup` runs at the end of schema init: if no `is_default=1` group exists,
  it inserts one (picking a free UNIQUE name: `Default`, `Default (1)`, …). Concrete
  baselines start at weight 0 / urgent off. `DefaultGroupID` resolves it with
  `ORDER BY id LIMIT 1` so resolution stays deterministic even in the pathological
  two-defaults case.

## Migration — wipe and reassign

On upgrade, existing `user_group_members` rows are **not** carried into
`user_primary_group`. Every user therefore starts with no primary group and inherits the
Default group until an admin explicitly assigns one. This was a deliberate operator
choice: a clean reset beats silently guessing a primary from a former multi-membership.
The old table is retained (additive-only schema; nothing reads it).

## Admin UX

`Manage → Users → Groups`:

- The Default group is pinned, tagged, and non-deletable; its editor shows only concrete
  weight / urgent (no inherit toggles).
- A named group's editor shows an "Inherit from Default" toggle per field; when on, the
  value input is disabled and the field is stored as `NULL`. Cards render inherited
  fields with an "inherits Default" note showing the effective value.
- The account editor assigns a **single** primary group (clearable → inherit Default);
  bulk actions are `set_group` / `clear_group`.

## Per-group governance (extension)

The same inherit/override + Default-fallback model carries three more run controls, added
as nullable `user_groups` columns (NULL = inherit; the Default group's NULL resolves to a
permissive baseline):

- **`allow_urgent`** — may members use the urgent lane at all (folded into `urgentAllowed`;
  a disallowing group downgrades an urgent submit even when the global lane is on).
- **`max_queued`** — cap on a member's active (queued + running) jobs; `0` = unlimited.
- **`run_window`** — allowed hours in the panel timezone (`"H1-H2"`, wraps midnight if
  `H1 > H2`; empty = any hour).

`Store.EffectiveGroupSettings` resolves all of them (weight / urgent-unlimited / allow /
max-queued / window) by layering permissive-baseline < Default < primary. Enforcement is at
submit (`apiBatchJobCreate`): outside the window → rejected; over the cap → rejected.
**Admins are exempt** from the window and the cap (operational necessity); `allow_urgent`
applies to everyone. These sit with the per-group weight config on the Groups tab.

## Consequences

- Resolution is unambiguous and lets an admin both raise and lower a user by moving their
  one primary group.
- The Default group is the single house-default knob for weight / urgent; base priority's
  house default remains the `run_default_priority` setting (edited on the run-queue
  settings page), which the Default group's members inherit.
- Non-admins can neither set their own primary group nor a run priority (both are
  admin-only endpoints; priority on submit stays admin-gated per ADR 0008).
