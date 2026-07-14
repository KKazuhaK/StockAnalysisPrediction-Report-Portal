# Recurring Tasks (计划任务)

A built-in app that runs a saved workflow **on a repeating schedule** — every day at a time, every
week on a weekday, or every month on a day-of-month. It is the portal's answer to "generate this
report for me every trading day after close" or "run this review on the 1st of every month," without
anyone having to click *run* each time.

Design rationale and the decisions behind this shape live in
[ADR 0018](adr/0018-recurring-tasks.md). This document is the how-it-works + how-to-use + reference
guide.

---

## 1. What it is, in one sentence

A recurring task is a **time-triggered producer for the run queue**: on its cadence, a background
loop creates an ordinary batch job from the task's saved `rows` template and hands it to the *one*
run queue, which executes it exactly like any other run.

It is "batch, but the trigger is a clock instead of a CSV upload." It adds **no** new execution path
and **no** new concurrency gate — a fired task competes for a slot against every other run, ordered by
the same priority rules (ADR 0004/0008/0011).

## 2. Who can use it

Anyone with the **`run_batch`** permission (operators + admins). It appears as a card in the **Apps**
hub (`/apps/recurring`). A non-admin sees and manages only **their own** tasks; an admin sees all.
There is no new permission and no change to the `user` role.

---

## 3. How it works

```
                          recurringLoop (60s ticker)
                                   │
              ┌────────────────────┼─────────────────────┐
              │  for each ENABLED recurring task:         │
              │    cadenceDue(freq, at_time, weekday,     │   (panel timezone)
              │               monthday, last_fired, now)? │
              │            │ yes                          │
              │            ▼                              │
              │    MarkRecurringFired(id, "YYYY-MM-DD")   │   ← stamp BEFORE firing (idempotency)
              │            │ ok                            │
              │            ▼                              │
              │    fireRecurringTask ─► CreateBatchJob ───┼──►  the ONE run queue
              │            │                              │      (priority, tickets,
              │            ▼                              │       fair-share, budget)
              │    recurring_runs row (fire → job)        │
              └───────────────────────────────────────────┘
```

- **The loop.** `recurringLoop` (`internal/app/recurring.go`) is a 60-second ticker started at
  boot alongside `scheduleLoop` (one-shot scheduling) and `cleanupLoop` (storage retention). Each
  tick scans the enabled tasks and fires any whose cadence is due.
- **The cadence engine.** Due-checking is the shared, tested `cadenceDue` in
  [`internal/app/cadence.go`](../internal/app/cadence.go) — the same engine the storage-cleanup pass
  uses. `nextCadence` (same file) computes the "next run" time shown in the UI. All times are valued
  in the **panel timezone** (`meta['timezone']`), so "daily at 09:30" means 09:30 civil time.
- **The producer.** When due, `fireRecurringTask` reads the task's `rows` JSON (the job template),
  resolves the target, picks the priority, and calls `CreateBatchJob(...)` — the identical entry point
  the batch console uses. The resulting job enters the shared queue and is admitted by priority.
- **The audit chain.** Every firing writes a `recurring_runs` row linking the task to the batch job
  it created, so the UI can show a per-task fire history with each job's current status.

### Idempotency: fire at most once per period

`last_fired` (a `YYYY-MM-DD` period stamp) is the **sole** guard against double-firing. The loop
stamps the period **before** creating the job, and — critically — **only fires if the stamp write
succeeds**. If the stamp write fails (a transient DB error), the task **skips this period** rather
than risk an unguarded duplicate *paid* run; the next tick retries. This mirrors the reconcile-not-
retry / no-double-charge invariant the rest of the run subsystem is built on.

### Misfire (server was offline)

- **Same-day late catch-up.** If the server is down at the scheduled time and comes back up later the
  same civil day, the period still fires **once** when it returns (as late as the outage lasted).
- **Whole period skipped, never backfilled.** If an entire period passes while the server is down, it
  is simply skipped — the task does **not** fire N times to "catch up." Under continuous operation the
  60s ticker bounds fire lateness to at most a minute.

---

## 4. Using it

Open **Apps → Recurring Tasks**. (You need a run target first — create a Dify workflow target in the
batch console.)

### Create / edit a task

Click **New task** (or the edit icon on an existing row). The modal has:

| Field        | Meaning |
|--------------|---------|
| **Name**     | A label for the task, e.g. "Daily A-share close review." |
| **Target workflow** | Which run target (Dify workflow) to run. Followed live — see §5. |
| **Rows (CSV)** | The job template. **One row per run** — a single row runs once, N rows run as a batch each firing. The CSV is header-based: the first line names the target's input columns, each subsequent line is one run's inputs. |
| **Repeat**   | `daily` / `weekly` / `monthly` + a **time** (HH:MM, panel tz). Weekly adds a **weekday**; monthly adds a **day-of-month** (day 31 clamps to the last day of short months). |
| **Priority** | `Normal` or `Idle` (run only when the queue is otherwise free). **Admins** additionally get `Top priority` (urgent) and a custom base score (0–100) — see §5. |
| **Concurrency / Max retries** | Per-firing batch settings, same meaning as the batch console. |
| **Enabled**  | Off = paused (kept, but never fires). |

### Row actions

- **Run now** — fire the task immediately, out of cadence, for a one-off/test. It does **not** touch
  `last_fired`, so the next scheduled firing is unaffected.
- **History** — the task's recent firings and the job each produced (with status), newest first.
- **Edit** — change any field. Editing preserves the template.
- **Enable switch** — pause/resume without deleting.
- **Delete** — removes the task and its fire history. Jobs it already created are kept.

The list also shows each task's **Next run** (computed) and **Last fired** date at a glance.

---

## 5. Semantics you should know

- **Panel timezone.** Cadence times are civil times in the business/panel timezone, not UTC and not
  the server's local zone.
- **Priority: urgent is admin-only, and ticketless.** A recurring task fires unattended and
  repeatedly; for a regular user, allowing urgent would silently drain the scarce urgent-run ticket
  allocation on *every* occurrence — so a non-admin is limited to **idle** or **normal** (where
  "normal" resolves at each firing to the creator's group-default base priority). An **admin** — the
  governance layer — may instead pin a task to an explicit **base score (0–100)** or to **Top
  priority (urgent)**; because the fire path never charges a ticket, this is free of ticket cost. The
  trade-off: an urgent recurring task takes a reserved urgent slot on every firing and can outrank
  ad-hoc urgent runs (the admin's explicit intent). A non-admin's attempt at urgent or a base number
  is coerced to normal by the API.
- **The target is a live reference, not a snapshot.** A task stores the target *id* and reads the
  current target at each firing, so editing the workflow takes effect on the next run. If the target
  is **deleted**, the firing is logged-and-skipped (the task is not auto-deleted). In the editor, a
  task whose target was deleted still shows its template and warns you to pick a new target before
  saving.
- **Inputs are static per firing.** The template's values are fixed; the Dify workflow resolves
  "today" itself via `/api/v1/now`. (Per-firing template variables are a possible future addition —
  see §8.)
- **Ownership.** Every mutating endpoint is owner-or-admin gated; a non-admin can only touch their
  own tasks.

---

## 6. HTTP API

All routes require the `run_batch` permission (cookie session), and each mutation additionally checks
ownership. Handlers: [`internal/app/recurring_api.go`](../internal/app/recurring_api.go).

| Method & path | Purpose |
|---|---|
| `GET /api/admin/batch/recurring` | List tasks (own for a non-admin; all for an admin). Each carries `target_name`, `row_count`, and a computed `next_run`. |
| `POST /api/admin/batch/recurring` | Create. Body: `{name, target_id, rows[], freq, at_time, weekday, monthday, priority, concurrency, max_retries, enabled}`. |
| `GET /api/admin/batch/recurring/{id}` | Detail — the task plus its full `rows` template and `history` (recent firings + job status). |
| `PUT /api/admin/batch/recurring/{id}` | Update the editable fields (identity, `created_by`, and `last_fired` are never rewritten). |
| `POST /api/admin/batch/recurring/{id}/enable` | Body `{enabled: bool}` — pause/resume. |
| `POST /api/admin/batch/recurring/{id}/run` | Fire now. `200 {job_id}` on success; `400` for a genuine no-op (missing target / empty template); `500` for an internal create failure. |
| `DELETE /api/admin/batch/recurring/{id}` | Delete the task and its history. |

Validation on create/update: non-empty `name`; the target must exist; `freq ∈ {daily, weekly,
monthly}`; a valid `HH:MM`; `weekday ∈ 0..6` (weekly); `monthday ∈ 1..31` (monthly); at least one
row; priority coerced to `''` (normal) or `idle`, with `urgent` / a base number honored only for an admin.

---

## 7. Data model

Two additive tables (SQLite + Postgres), created on startup with no migration — see
[`internal/app/store.go`](../internal/app/store.go) and
[`internal/app/recurring_store.go`](../internal/app/recurring_store.go).

```sql
CREATE TABLE recurring_tasks(
  id, name, target_id,
  rows,                       -- JSON job template = rows[] (1 row = one run, N = a batch)
  concurrency, priority,      -- '' (normal) | 'idle' | (admin) 'urgent' | (admin) a base number 0..100
  max_retries,
  freq, at_time, weekday, monthday,   -- the cadence (panel tz)
  enabled, created_by, created_at,
  last_fired);                -- 'YYYY-MM-DD' period stamp — the double-fire guard

CREATE TABLE recurring_runs(  -- fire → job audit chain, trimmed to a per-task ring
  id, task_id, job_id, fired_at);
```

`last_fired` is the scheduler's idempotency state; the `recurring_runs` history is derived and
trimmed, so trimming can never cause a re-fire.

---

## 8. Limitations & deliberate non-goals

Each is a clean future addition that needs no rework of the current shape:

- **Cadence granularity** — `daily/weekly/monthly` only. No "every N hours" interval yet (would be a
  new `freq` branch) and no cron expressions.
- **Trading-calendar awareness** — there is no market/holiday calendar; "weekdays only" is
  approximated with a weekly cadence, not a real trading-day filter.
- **Per-firing template variables** — inputs are static; there is no `{{today}}`-style substitution
  (the workflow gets the date from `/api/v1/now`).
- **Email-on-finish** — a scheduled run does not (yet) email the owner when it completes.
- **App-bridge migration** — like the batch console, this app talks to `/api/admin/*` with the
  session cookie today; migrating both to the `/api/v1` scoped-token bridge (ADR 0009) is tracked
  jointly, not per-app.

---

## See also

- [ADR 0018 — Recurring tasks](adr/0018-recurring-tasks.md) (decisions & rationale)
- [ADR 0011 — Schedule at the run level; batch is a producer](adr/0011-run-level-scheduling.md)
- [ADR 0007 — Run analysis + one-shot scheduling](adr/0007-run-analysis-and-scheduling.md)
- [ADR 0017 — Storage cleanup](adr/0017-storage-cleanup.md) (the cadence-loop prior art)
