# ADR 0007 — Home-page "运行分析" + one-shot scheduling + user-facing queue view

## Context

Running a Dify workflow today requires the operator-only 批量控制台 (`/apps/batch`,
CSV upload). We want a first-class, single-run entry point on the home page, plus
a way to see the live run queue and to schedule a run for later.

Prior art this builds on: the priority run queue (ADR 0004), 加急 tickets from
group weight (ADR 0005), and Dify-native targets with auto-discovered inputs
(ADR 0006). The single job-create endpoint already accepts an arbitrary `rows[]`,
so a one-row submit needs no engine change.

## Decisions

1. **Audience = `run_batch` only.** The "运行分析" button renders only when the
   client holds `run_batch` (operator/admin). No new permission, no change to the
   `user` role. All existing `/api/admin/batch/*` routes (gated by `PermRunBatch`)
   are reused as-is from the home page — the `admin` path prefix is a naming
   artifact, the real gate is the permission.

2. **Single-run reuses `POST /api/admin/batch/jobs`** with `rows:[oneObject]`,
   `concurrency:1`. The modal renders one field per the target's discovered input
   (`{key,label,required}`, incl. Dify), enforces `required`, and assembles the
   one row client-side. 加急 gating, ticket spend, and the `downgraded` response
   are unchanged.

3. **One-shot scheduling via an additive side table**, mirroring the `job_queue`
   precedent (no `ALTER`, per the no-migration rule):

   ```sql
   CREATE TABLE IF NOT EXISTS job_schedule(job_id BIGINT PRIMARY KEY, run_at TEXT);
   CREATE INDEX IF NOT EXISTS idx_job_schedule_run_at ON job_schedule(run_at);
   ```

   A scheduled run is an ordinary `status='queued'` job **plus** a `job_schedule`
   row. `run_at` is stored in the same local `"2006-01-02 15:04:05"` basis as
   `batch_jobs.created_at` (the existing aging clock), so due-comparison and aging
   share one time basis.

4. **`queuedItems()` excludes not-yet-due jobs.** This single filter is
   load-bearing: it keeps a future job out of both admission (`Admit`) and the
   "N ahead" count, and it makes startup safe — `resumeBatchJobs` → `scheduleTick`
   won't launch a future job early.

5. **One new runtime mechanism: a background ticker.** The queue is otherwise
   event-driven (no timer). A single goroutine started after `resumeBatchJobs`
   calls `scheduleTick()` every 30s; once a job's `run_at` passes, `queuedItems()`
   surfaces it and the normal admission path runs it. The pure `internal/queue`
   package stays pure — the ticker lives in `internal/app`.

6. **New endpoints** (all `PermRunBatch`, under `/api/admin/batch/`):
   - `GET  .../queue` — summary `{waiting, running, scheduled, budget, reserved}`
     for the drawer + the "还有 X 个在等待 / 队列空" banner (waiting excludes
     not-yet-due scheduled jobs).
   - `DELETE .../jobs/{id}` — remove a terminal (finished/cancelled) job and its
     items/queue/schedule rows (the 删除 action). Running/queued jobs must be
     cancelled first.
   - `POST .../jobs/{id}/schedule` — set/clear `run_at` (改时间 / 立即运行 = clear).

   Reused unchanged: create, cancel, retry, `.../priority` (插队/调整顺序), tickets,
   targets. `run_at` is added to the create body and surfaced in job JSON.

7. **Two views:**
   - **顶部队列抽屉** — a header 队列 button → drawer with running/waiting/scheduled
     summary + the caller's runs (live progress / "前面 N 个" / 加急·定时 tags),
     polled every 2–3s (the established pattern; no SSE — there is no browser push
     channel).
   - **完整 `/queue` 页** — submitter, submit time, scheduled time, status,
     priority, progress; actions: cancel, delete, reprioritize (调整顺序), reschedule,
     run-now, retry, view detail (inputs + generated report link); filters by
     status/submitter/workflow, search, auto-refresh (pausable), empty state.

8. **Priority resolution (weight ≠ tickets).** `user_groups.weight` stays the
   加急 ticket allocation. A separate, additive notion of *default priority* is
   added so groups can bias where their members' runs land in the queue, distinct
   from the scarce 加急 escalation:

   - `CREATE TABLE IF NOT EXISTS group_priority(group_id BIGINT PRIMARY KEY, priority TEXT)`
     — a group's default priority level (additive, no `ALTER`).
   - Setting `run_default_priority` — the fallback for users in no (weighted) group.
   - `resolvePriority(user, explicit) -> level`: precedence is
     **explicit (submit/batch/加急 escalation) → max over the user's groups →
     system default**. The single function is the extension point for future
     priority sources.
   - Group/system default priority is limited to the **non-urgent** tiers
     (普通/其他); 加急 remains ticket-gated via the explicit-escalation path only.
     (A "VIP group defaults to 加急 without a ticket" variant is deliberately out
     of scope unless requested — it would erode the reserved-slot scarcity.)

9. **Run/queue settings move out of the batch tab.** The queue budget, reserved
   slots, ticket period, max concurrency, and the new `run_default_priority` live
   in a standalone 运行/队列 admin area — they govern the whole run system (home
   单次运行 + CSV 批量), not just batch. The 批量任务 tab keeps only 执行目标 +
   CSV bulk + advanced plugins.

## Consequences

- No schema migration; `job_schedule` and the group-weight/ticket tables all
  follow the additive-side-table pattern.
- Scheduling is one-shot only. Recurring (cron) is explicitly out of scope; if
  wanted later it is a separate feature (repeat rule + job templating).
- The ticker is the only always-on timer; its 30s cadence bounds scheduled-run
  lateness. Ticket spend still happens at submit (a scheduled 加急 run spends its
  ticket when created, not when it fires) — documented so it isn't surprising.
- `run_at` uses the local time basis of `created_at`; if the portal's business
  timezone handling later needs UTC-on-wire for this field, it changes with the
  rest of the instant-handling work.
