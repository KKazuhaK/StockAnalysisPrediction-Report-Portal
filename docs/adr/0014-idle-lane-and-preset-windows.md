# ADR 0014 — Idle lane + preset low-peak scheduling windows

## Status

Accepted — 2026-07-08. Shipped in v0.2.2. Targets the v0.2 line (schema generation 2).
Extends the run subsystem of ADR 0004 (queue), 0007 (run-analysis + one-shot scheduling),
0008 (multifactor priority) and 0011 (run-level scheduling); relocated `priority`/`run_at`
onto `batch_jobs` per ADR 0013.

## Context

Two operator asks on the 运行分析 / 批量运行 forms:

1. **Run when the queue is idle.** A no-rush run that should yield to everything else and
   only consume a slot when nothing normal/urgent wants it. ADR 0008 makes base priority a
   0–100 number, but base 0 is **not** "idle": age and fair-share still lift a base-0 job, so
   it keeps climbing and will overtake later jobs. "Only when idle" needs its own semantics,
   symmetric to how 加急 gets a boost constant above every reachable score.

2. **Schedule into a preset low-peak window** (e.g. an API-price off-peak period). Admin
   configures a set of recurring windows; a user picks one instead of hand-typing a 定时 time.
   The window is a *range* with a recurrence (daily / weekly / monthly / yearly), and the admin
   decides per window what happens if the window closes before the run ever started.

Prior art / adjacent feature — **group `run_window` governance** (`user_groups.run_window` =
`"H1-H2"` hours, panel tz): an admin **restriction** on when a *group's* members may run,
enforced at submit against the effective run hour (`runWindowOpenAt`, `batch_api.go`). That is a
mandatory gate on a group; this ADR's presets are an **optional per-run scheduling target** the
user picks. Different roles — kept as separate features (distinct names), and they compose: the
existing group-window gate still applies to a preset-scheduled run's resolved hour.

## Decision

### 1. Idle lane — a bottom-anchor symmetric to the urgent boost

- New sentinel value `"idle"` in `batch_jobs.priority` (alongside `"urgent"` and a base
  number). `parsePriority` returns `(base int, urgent bool, idle bool)`.
- `internal/queue`: `Factors.Idle` / `Item.Idle`; `Score` subtracts `idlePenalty` (a constant
  more negative than the minimum reachable weighted sum, mirroring `urgentBoost`) when `Idle`.
  An idle run therefore always sorts **below every non-idle run**. Among idle runs, the base/
  age/fair terms still order them — oldest-first, i.e. an orderly FIFO backlog. The reserved-slot
  / urgent rules are untouched (idle is never urgent).
- **No ticket gating** — idle only ever *lowers* priority, so any user may pick it (unlike an
  arbitrary base number, which stays admin-only per ADR 0008).
- **UI scope:** the idle checkbox is offered only in *immediate* ("立即运行") mode — a preset
  window or an explicit 定时 time already answers "when", so idle (a *no specific time, whenever
  free* choice) would be contradictory there. Admin default via `run_default_idle`.
- **Starvation is intended:** if the queue never idles, an idle run waits indefinitely — that is
  the literal contract. An optional admin fuse (escalate an idle run older than N hours) is
  deliberately **out of scope / default-off**; revisit only if asked.

### 2. Preset windows — recurring *eligibility*, still a one-shot run

A preset makes a **single** run eligible only within a recurring window; it does **not** spawn
multiple runs. This stays inside ADR 0007's "scheduling is one-shot" — it only generalizes the
eligibility from a point (`run_at`) to a recurring range, where "recurring" is a
*retry-until-it-runs-once* mechanism, not cron job-templating (which remains out of scope).

- **Admin config — a `run_presets` table, not a `meta` blob.** Presets are an ordered,
  admin-managed collection that populates a picker — structurally the same as `links` (entry
  buttons), `type_config` (report types), and `batch_targets`, which are all tables with an `ord`
  column. Keeping them a table (rather than a JSON array in `meta`) matches that precedent, keeps
  `meta` scalar-only, and gives clean per-row edit/delete/reorder. A new `CREATE TABLE IF NOT
  EXISTS` is picked up on the next startup like any additive schema — no versioned migration step.

  ```sql
  CREATE TABLE IF NOT EXISTS run_presets(
    id <pk>, label TEXT, freq TEXT,              -- freq: daily|weekly|monthly|yearly
    intervals TEXT,                              -- JSON [{start:anchor, stop:anchor}] — the union of sub-windows
    on_overrun TEXT DEFAULT 'next',              -- continue|next|cancel
    enabled INTEGER DEFAULT 1, ord INTEGER DEFAULT 0)
  ```

  `id` is a plain auto-increment surrogate (row addressing for CRUD/reorder and the submit-time
  `preset_id` pick, and the React list key) — **not** a foreign key: the job snapshots the rule,
  so nothing references the preset long-term.

  **A preset's eligible time is the *union* of one-or-more sub-windows** (`intervals`), so a split
  low-peak like "09:00–12:00 **and** 14:00–18:00" is one preset. Each interval is a `{start, stop}`
  anchor pair; anchor fields used depend on `freq` (daily → `time`; weekly → `weekday`+`time`;
  monthly → `day`+`time`; yearly → `month`+`day`+`time`), interpreted in the **panel timezone**
  (`meta['timezone']`). A single-window preset is just one interval. Stored as JSON (like
  `batch_targets.config`) since the shape varies by `freq` and count. All four `freq` values are
  implemented now; adding one later is a resolver + UI branch, no schema change. Month/leap edges
  (`day 31` in a short month, yearly `2/29`) **clamp** to the last valid day.

- **Resolution** — `nextInterval` resolves one sub-window's current-or-next occurrence (handling a
  `stop` earlier than `start`, which wraps to the next day/week/month/year, and month/leap clamps);
  `nextWindow(freq, intervals, now, panelLoc)` picks, across the union, the occurrence with the
  **earliest end still after `now`** — the next moment the run becomes eligible. If `now` is inside
  a sub-window, that one is returned (start in the past, so immediately due).

- **Job storage — one new column** `batch_jobs.run_preset TEXT DEFAULT ''` holding a JSON
  **snapshot** taken at submit: `{ freq, intervals, on_overrun, until }`, where `until` = the
  current occurrence's end. `run_at` (existing column) carries the occurrence **start**. Both
  instants are stored in the existing **local wall-clock** string form (`"2006-01-02 15:04:05"`),
  but their *values* are computed via the panel timezone — so `runAtDue`, the group-window gate,
  and the aging clock all keep working **unchanged** while the schedule is panel-tz-correct.
  Snapshotting the rule onto the job (rather than referencing the mutable preset) mirrors the
  `report.name` snapshot philosophy: a later edit/delete of the preset never rewrites an in-flight
  run's window.

- **Due gate + overrun (union semantics):** `runAtDue(run_at)` (unchanged) keeps the job hidden
  until the current sub-window opens. A new check in `scheduleTick` reads `run_preset.until`: for a
  **not-yet-started** job whose `until` has passed, it computes the next occurrence across the
  union and asks `samePeriod(freq, now, nextStart)` — is there another sub-window later **in the
  same period** (same civil day for daily / ISO week / month / year)?
  - **More windows left this period** → **auto-advance** to the next sub-window (roll `run_at`/
    `until`), *regardless of policy* — the run just tries the day's next window (09:00–12:00 missed →
    wait for 14:00–18:00).
  - **Period exhausted** (no more sub-windows until the next period) → apply `on_overrun`:
    `next` rolls to the next period's first sub-window (keep waiting); `continue` clears the window
    (run ASAP); `cancel` marks the job terminal `expired`.

  A single-interval preset degenerates to the original behavior: every close is a period boundary,
  so the policy fires each time. Because `run_at` rolls with each advance, the age factor never
  accrues across windows, so a rolled run competes fairly rather than jumping ahead.
- **Running runs are never touched** (non-preemptive, ADR 0004/0011): the overrun policy governs
  only runs that had not started when the window closed. A run that started inside the window and
  runs past `until` simply finishes.

### 3. `expired` — a new terminal status value (not a schema change)

Produced only by the `cancel` overrun policy. `status` is already a free `TEXT` column, so this is
a new value, not DDL. It is terminal-but-neutral (like `cancelled`): excluded from waiting/active
counts, shown as a neutral tag ("已过期 / 未在时段内执行") so it never reads as a failure.

### 4. Admin settings (on the 运行/队列 tab, `/api/admin/batch/config`)

- `run_default_mode` — `now | preset | scheduled` (default `now`): which button the run forms
  open on. Scalar → stays in `meta`.
- `run_default_idle` — bool (default off): whether the idle checkbox starts checked (immediate
  mode only). Scalar → stays in `meta`.
- The preset windows themselves live in the `run_presets` table (§2), edited as an add / remove /
  reorder list on the same tab.

### 5. Frontend

- The mode toggle grows to **three** buttons — `立即运行 | 预设时间 | 定时运行` (preset in the
  middle). `预设时间` reveals a `Select` of the configured presets (same drop-down affordance as
  the 定时 `DatePicker`). The idle checkbox shows only under `立即运行`.
- **Mobile:** the preset select / date picker drops to its **own full-width row** instead of the
  desktop inline `marginLeft` (via `Grid.useBreakpoint()`), so it never crowds the button group on
  a phone.
- The run-time + priority controls are extracted into **one shared component** reused by both
  `RunAnalysisModal` (single 运行分析) and `BatchConsole` (批量运行) — today they duplicate the
  toggle/picker/urgent/notify blocks.
- Payload: `priority:"idle"` (immediate only); `preset_id` (preset mode → the backend resolves
  `run_at` + snapshots `run_preset`; the client sends no time). `run_at` still carries a manual
  定时 time. Preset resolution runs **before** the group-window governance gate so a preset that
  lands outside a group's allowed hours is rejected at submit, as today.

## Alternatives rejected

- **Base priority 0 for "idle"** — still ages/fair-shares up; not "only when idle" (ADR 0008).
- **Store only the preset `id` on the job** — a later admin edit/delete would mutate or orphan an
  in-flight run's window; the snapshot is robust and matches `report.name`.
- **A separate `run_before` column** — folded into the single `run_preset` JSON (the rule and its
  policy must ride together to compute the *next* occurrence for `on_overrun:next` anyway).
- **RFC3339-UTC storage for `run_at`/`until`** — would force changes to `runAtDue`, the group-window
  gate, and the aging parse; the existing local wall-clock string, valued via panel tz, is
  correct and zero-blast-radius.
- **Reusing/renaming the group `run_window` feature** — different role (mandatory group gate vs
  optional per-run target) and granularity (hours vs HH:mm × four freqs); kept separate.

## Consequences

- Schema: **one** additive column `batch_jobs.run_preset` (picked up automatically by
  `ensureColumns`, ADR 0013 machinery) **plus one additive table `run_presets`** (created by its
  base-schema `CREATE TABLE IF NOT EXISTS`) — neither needs a versioned migration step. `expired`
  is a new status value. Both are squashed into the base schema at the next boundary (v0.3.0). The
  `run_presets` table is a genuine entity collection (like `type_config`/`links`), not a 1:1 side
  table, so it does not run against ADR 0013's fold-the-side-tables direction.
- `internal/queue` gains an idle bottom-anchor symmetric to `urgentBoost`; ordering, reserved
  slots, and "N ahead" are otherwise unchanged.
- One new pure, well-tested unit — `nextWindow` (four freqs + wrap/clamp/leap edges) — plus the
  `scheduleTick` overrun branch. Both TDD'd.
- The run forms converge on a shared control; three run modes and an idle option are exposed
  without touching the machine `/api/v1` surface or webhooks.

## Rollout

1. ADR (this) + failing tests: `internal/queue` idle ordering, `nextWindow` table tests, store
   round-trip of `run_preset`, `parsePriority` idle, `scheduleTick` overrun matrix.
2. Backend: schema column; queue idle; window resolver; priority/gate/overrun wiring; batch API +
   config get/save + store helpers.
3. Frontend (vitest first): shared control, three modes, preset select, idle checkbox, admin
   presets editor, mobile layout, i18n (zh-CN / zh-TW / en, parity test).
4. `go test ./...` + web typecheck/test/build; then `verify` the run→schedule→overrun matrix
   end-to-end.
