# 18. Recurring tasks — a time-triggered producer for the run queue (计划任务)

## Status

Accepted — 2026-07-13. Realizes the "recurring (cron) is a separate feature: repeat rule + job
templating" that ADR 0007 (§Consequences) and ADR 0014 (§2) both explicitly deferred out of scope.
Builds on the run queue (ADR 0004), run-level scheduling (ADR 0011, "batch is a producer"), the
storage-cleanup cadence loop (ADR 0017), and the app model (ADR 0003/0009).

## Context

Everything the run subsystem schedules today is **one-shot**: `run_at` defers a single job to a
future instant (ADR 0007) and `run_preset` makes a single job *eligible* only inside a recurring
window (ADR 0014) — a retry-until-it-runs-once mechanism, not a repeat. There is no way to say
"generate this report **every** trading day at close" or "**every** month run this review". Both
prior ADRs named this gap and deferred it to a future "repeat rule + job templating" feature. This
is that feature.

The owner's ask (2026-07-13): a built-in **计划任务** ("scheduled tasks") app — pick a workflow +
inputs, choose a cadence (e.g. daily at a time, or monthly), and have the portal fire the run for
you on that cadence, indefinitely, until disabled.

## Decision

A recurring task is a **time-triggered producer**, exactly the ADR 0011 shape batch already is: it
owns no execution path and adds no concurrency gate. On its cadence a background loop creates an
ordinary `queued` batch job from the task's saved template and hands it to the **one** run queue,
which owns priority, tickets, fair-share, and the single concurrency budget unchanged. A recurring
task is "batch, but the trigger is a clock instead of a CSV upload."

### 1. Cadence = the storage-cleanup engine, shared

The repeat rule is the same `freq (daily|weekly|monthly) + time (HH:MM, panel tz) + weekday |
monthday` model the storage-cleanup pass already uses (ADR 0017), interpreted in the panel timezone
(`meta['timezone']`). The due-check is factored into one tested pure function **`cadenceDue`**
(`cadence.go`) that both `cleanupDue` and the recurring scheduler call — fire at most once per
matching civil day, at/after `time`, with a `YYYY-MM-DD` period-stamp guarding against a restart
double-fire. `nextCadence` (same file) resolves the next fire instant for the UI's "next run" label.

- **Why not cron expressions / intervals** — the owner's use cases ("每天几点", "每月") are exactly
  daily/weekly/monthly-at-a-time; reusing the cleanup cadence gives an identical, already-shipped UI
  and one shared, tested engine, with zero new vocabulary. An "every N hours" interval freq and a
  trading-calendar-aware "trading days only" filter are natural later additions (a new `freq` branch
  + resolver); deferred until asked. There is **no** trading-calendar in the system today, so
  "weekdays only" is approximated by a weekly cadence, not a holiday calendar.

### 2. Job templating — the template is a `rows[]`

A task stores a `rows` JSON array in the exact shape `CreateBatchJob` already takes: **1 row → a
single run, N rows → a batch** each fire. This makes single-run and batch the same feature and reuses
the entire create path. Inputs are static per the target's discovered fields (the Dify workflow
resolves "today" itself via `/api/v1/now`, so the template needs no per-fire variable substitution;
dynamic template variables are a possible later addition, out of scope now).

### 3. The target is a **reference**, not a snapshot

Unlike `report.name` / `run_preset` (which snapshot to freeze in-flight history), a recurring task is
a living template meant to track the current workflow: it stores `target_id` and reads the live
`batch_targets` row at each fire. If the target is deleted, the fire is logged-and-skipped (the task
is not auto-deleted; an admin may re-point or remove it). This was the owner's explicit call.

### 4. Priority: urgent is admin-only, and ticketless

A recurring task fires unattended and repeatedly. For a **regular user**, allowing an urgent run
would silently drain the scarce urgent-run ticket allocation (ADR 0005) on every occurrence — so a
non-admin's task priority is limited to `idle` (run-when-queue-free, ADR 0014) or normal, where
"normal" resolves at each fire to the creator's group-default base priority (`resolveBasePriority`,
ADR 0008).

An **admin**, however, is the governance layer, and the fire path never charges a ticket (tickets are
spent only on the interactive run-submit, not in `fireRecurringTask`). So an admin may pin a recurring
task to an explicit base priority (`0..100`) or to `urgent` (top priority) as a standing configuration
decision — **ticketless**. The stored value is used verbatim at each fire (`''` → resolve owner base;
otherwise `idle` / `urgent` / the number, passed straight to `CreateBatchJob`). The trade-off, taken
deliberately: an urgent recurring task occupies a reserved urgent slot on every firing and can outrank
ad-hoc urgent runs — which is the admin's explicit intent for a critical scheduled report. A
non-admin's attempt to set urgent or a base number is coerced to normal at the API. (This **amends**
the original "never urgent" decision, which was too strict for the governance case.)

### 5. Misfire policy: at-most-once-per-period, no backfill

Mirrors the cleanup loop verbatim: the loop stamps the period **before** firing (and only fires if the
stamp write succeeds — a failed stamp skips the period rather than risk an unguarded duplicate paid
run), so a crash or a slow fire can't double-fire the same day; a period missed entirely (server down)
is simply skipped, not backfilled. Under continuous operation the 60s ticker bounds fire lateness;
after an outage spanning the scheduled time, the same-day period still fires once when the server
returns (as late as the outage lasted) — a whole missed period is what gets skipped, never a same-day
catch-up. Non-preemptive and idle/priority semantics are the queue's, unchanged.

### 6. Schema — two additive tables (owner-approved shape)

Both follow the additive-table precedent (`run_presets`, `cleanup_runs`); picked up by
`createBaseSchema` on the next startup, no versioned migration, squashed into the base schema at the
next major boundary (v0.3.0 line already; folds at v0.4.0).

```sql
CREATE TABLE IF NOT EXISTS recurring_tasks(
  id <pk>, name TEXT, target_id BIGINT,
  rows TEXT DEFAULT '[]',            -- JSON job template = rows[] (1 row = single run, N = batch)
  concurrency INTEGER DEFAULT 1, priority TEXT DEFAULT '', max_retries INTEGER DEFAULT 0,
  freq TEXT, at_time TEXT, weekday INTEGER DEFAULT 1, monthday INTEGER DEFAULT 1,
  enabled INTEGER DEFAULT 1, created_by TEXT, created_at TEXT,
  last_fired TEXT DEFAULT '');       -- YYYY-MM-DD period-stamp (the restart double-fire guard)

CREATE TABLE IF NOT EXISTS recurring_runs(          -- fire → job audit chain (like cleanup_runs)
  id <pk>, task_id BIGINT, job_id BIGINT, fired_at TEXT);
CREATE INDEX IF NOT EXISTS idx_recurring_runs_task ON recurring_runs(task_id, id);
```

`priority` stores `''` (normal → resolve owner base at fire), `'idle'`, or — only when set by an admin —
`'urgent'` or an explicit base number (`0..100`); a non-admin is coerced to `''`.
`recurring_runs` is a genuine history collection (not a 1:1 side table), so it doesn't run against
ADR 0013's fold-side-tables direction; it is trimmed per task to a bounded ring so it can't grow
unbounded.

### 7. App shape — first-party page on `/api/admin/batch/*`, consistent with batch

The scheduling engine is **pure backend** (a `recurringLoop` ticker beside `scheduleLoop` /
`cleanupLoop`): it calls `CreateBatchJob` directly, server-side, with no browser and no token — the
app-bridge (scoped `/api/v1` token, ADR 0003/0009) is irrelevant to a trusted server loop. The UI is
therefore a **management surface** (create/list/edit/enable/delete a schedule + its fire history),
structurally like the storage-cleanup console, the run-presets editor, or batch targets — all of
which are `/api/admin/*`, session-cookie, none through the bridge.

It ships as a **first-party React app card** in the Apps hub (like `BatchConsole`), gated by
`PermRunBatch` so operators can schedule their own recurring reports (not admin-only like the
cleanup console). Data flows over new `/api/admin/batch/recurring/*` routes (`PermRunBatch`;
ownership checked in-handler — a non-admin sees/edits only their own tasks, an admin all). This is
deliberately the **same** channel batch uses today: when ADR 0009's bridge migration finally lands,
batch and 计划任务 move to the `/api/v1` token contract **together**, rather than this new app
inventing a second, half-built bridge path while batch still uses cookies.

## Consequences

- **One queue, still the only gate.** A fired task is an ordinary job; the run-level scheduler
  (ADR 0011) admits it by priority against everything else. No new concurrency mechanism.
- **Two shared, tested units.** `cadenceDue` (now used by cleanup + recurring) and `nextCadence`
  are pure and table-tested; the cleanup refactor is behaviour-preserving (its existing tests guard
  it). The recurring loop is the third always-on ticker.
- **No 加急 drain, no backfill surprise, panel-tz correct** — the three policies in §4/§5 are the
  same safe defaults cleanup already ships.
- **Schema:** two additive tables, no migration; folds at the next major boundary.
- **Deferred, on purpose:** interval/"every N hours" freq, trading-calendar "trading-days-only",
  per-fire template variables, and email-on-finish for a scheduled run — each a clean later addition
  that needs no rework of this shape. The bridge migration is tracked jointly with batch (ADR 0009).

## Rollout

1. This ADR + failing tests: `cadenceDue`/`nextCadence` table tests; `recurring_store` round-trip;
   `recurringTick` due/stamp/fire matrix (SQLite; PG via `TEST_POSTGRES_DSN`).
2. Backend: `cadence.go` (extract + `nextCadence`); schema; `recurring_store.go`; `recurring.go`
   (loop/tick/fire); `recurring_api.go` handlers + routes; start the loop.
3. Frontend (vitest first): `RecurringConsole` page (task list + create/edit modal reusing the
   cleanup cadence controls and the batch CSV-rows editor + target select + history), Apps-hub card,
   route; i18n (zh-CN / zh-TW / en, parity test).
4. `go build`/`vet`/`test` + web typecheck/test/build; then `verify` the create → fire → job → history
   loop end-to-end.
