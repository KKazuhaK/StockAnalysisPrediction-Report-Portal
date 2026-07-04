# ADR 0008 — Slurm-style multifactor run priority

## Context

ADR 0004 gave the run queue three fixed named tiers (加急 / 普通 / 其他) ordered by a
`SchedKey = enqueue_time + tier_offset`. Operators want a **numeric, customizable**
priority instead of two vague non-urgent tiers, and asked for Slurm's approach: a
job's priority is a **weighted sum of normalized factors**, recomputed as it waits,
and the highest-scoring job runs next.

## Decision

Replace the named-tier ordering with a multifactor score. For each queued job, at
every scheduling tick:

```
score = w_base · base_norm      // configured base priority (per group / system default)
      + w_age  · age_norm       // how long it has waited (anti-starvation)
      + w_fair · fair_norm      // fair-share: recent usage vs the pack (heavy users yield)
      + (urgent ? URGENT_BOOST : 0)   // 加急 escalation (ticket-gated) + a reserved slot
```

Higher score is admitted first. Factors are each normalized to `[0,1]`; the weights
`w_*` are admin settings (Slurm's `PriorityWeight*`).

### Factors

- **base_norm** = `clamp(base, 0, BASE_MAX) / BASE_MAX`, `BASE_MAX = 100`.
  `base` for a run = the **max** base priority across the submitter's groups, else the
  system default (`run_default_priority`). This is the numeric knob (0–100) that
  replaces 普通/其他. Group priority is a number, set per group.
- **age_norm** = `min(1, wait_seconds / (age_max_hours·3600))`. `wait` = now − enqueue
  time (the job's `run_at` if scheduled, else `created_at`). A long-waiting job climbs
  to 1, so nothing starves. `age_max_hours` default 24.
- **fair_norm** = `2^(−decayed_usage[user])`, where
  `decayed_usage[u] = Σ over u's recent jobs of 0.5^(job_age_hours / fair_halflife_hours)`.
  A user who has run little recently → ~1; a heavy recent user → →0. `fair_halflife_hours`
  default 168 (7 days). Computed from `batch_jobs.created_by/created_at` — no new table.
- **URGENT_BOOST** — a constant larger than the max achievable factor sum, so a 加急 run
  outranks any non-urgent one, while age/base/fair still order urgents among themselves.

### Weights + config (admin settings, DB-backed)

`batch_prio_w_base`, `batch_prio_w_age`, `batch_prio_w_fair` (default 1000 each),
`batch_prio_age_hours` (24), `batch_prio_fair_halflife_hours` (168), and
`run_default_priority` (now a number, default 50). Configured on the 运行/队列 tab.

### Queue package (internal/queue) becomes score-based

The pure package no longer knows tiers or the clock. `internal/app` computes each
job's score (it owns the clock, usage history, and settings) and hands the queue:

```go
type Item struct { ID int64; Score float64; Urgent bool }
type Plan struct { Budget int; Reserved int }
func Admit(waiting []Item, inFlight int, plan Plan) []Item  // sort by Score desc, tie by lower ID; hold Reserved slots for Urgent items
func Ahead(item Item, waiting []Item) int                   // count items ranked ahead
```

`Registry` / `Level` / `SchedKey` (named tiers) are removed. The reserved-slot rule
(a lane always kept for 加急 while any urgent waits; lower runs drain the rest so they
don't starve) is preserved, keyed on `Item.Urgent`.

### Storage (additive, no ALTER)

- `job_queue.priority TEXT` now holds `"urgent"` for a 加急 run, else the **base number**
  as a string (`"50"`). Legacy tier names map on read: `normal → 50`, `other → 20`.
- `group_priority.priority TEXT` holds the group's base number as a string; same legacy
  mapping. The group UI switches from a 普通/其他 select to a 0–100 InputNumber.
- No schema change — the existing TEXT columns carry numbers-as-text.

## Consequences

- 普通/其他 disappear from the UI; priority is a number. 加急 stays the ticket-gated
  escalation (unchanged tickets, ADR 0005).
- `resolvePriority` now yields `(base_number, urgent_bool)`; the run modal drops the
  tier picker and keeps only the 加急 checkbox. The queue page shows each run's base
  number (and can show the live score).
- Scoring runs each tick over the queued jobs; at budget=1 it just orders a short queue,
  and scales with concurrency. Fair-share reads recent `batch_jobs` per tick (bounded by
  the fair window) — cheap for realistic queue sizes.
- Aging is now a normalized factor rather than a fixed offset; starvation is bounded by
  `w_age` relative to the other weights (raise `w_age` to make waiting matter more).
