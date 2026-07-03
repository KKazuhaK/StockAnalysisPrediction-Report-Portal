# 4. Run queue with priority scheduling

## Status

Proposed — 2026-07-03

## Context

Batch runs today start immediately (`launchJob` in `internal/app/batch_run.go`):
there is no ordering across jobs and nothing bounds the aggregate load on the
backend (Dify), so several concurrent jobs can flood it. The product wants:

- submit runs with a **priority** (加急 / 普通 / 其他, extensible with custom levels
  later — e.g. a level for batch),
- unattended overnight execution,
- see **"N ahead of me in the queue"**, and let an owner/admin **re-prioritise**
  a waiting item (插队),
- 加急 should jump ahead but a lower tier must not **starve** forever.

The runnable unit is a Dify workflow run (minutes each), driven over Dify's HTTP
API. A run cannot be cheaply preempted — killing it wastes a paid execution.

## Decision

A single **native run queue + non-preemptive priority scheduler**, sitting *below*
batch (and, later, app-triggered runs). It is core infrastructure, **not** an
iframe app: it needs privileged orchestration (a worker pool, DB writes, cancel),
which the sandboxed app model (ADR 0003) deliberately does not expose. Batch stays
native and becomes the queue's first consumer.

1. **Priority as a small registry** — `{name, value, reserved}` rows, integers with
   gaps so custom levels slot in without code changes (like the role / report-type
   registries). Seed: `urgent=10`, `normal=100`, `other=200` (room for a batch
   level later). Lower value = runs first.

2. **Aging via a virtual scheduling key — no background timer.** Compute once at
   enqueue: `sched_key = enqueued_at + offset(level)`, where a lower tier gets a
   larger `offset` (seconds). Dequeue = smallest `sched_key`
   (`ORDER BY sched_key ASC`, or a `container/heap`).

   A lower-tier item is overtaken by freshly-arriving higher-tier items only until
   it has waited `offset(low) − offset(high)` seconds; after that its older
   enqueue time wins. So **the offset delta between levels is the starvation cap** —
   predictable and tunable, with no periodic recomputation. (Same family as EDF /
   CFS vruntime.) Example offsets: `urgent 0s, high 300s, normal 1800s, low 7200s`.

3. **Non-preemptive.** A running job always finishes; the scheduler only chooses
   which *waiting* item starts when a slot frees. At concurrency > 1, urgent and
   lower tiers naturally run side by side — it is neither "urgent drains first" nor
   forced interleave, and no in-flight work is wasted.

4. **Reserved slot for the top tier only.** One worker serves 加急 exclusively
   (borrowable when no 加急 waits), bounding urgent latency regardless of how many
   lower tiers exist. Reservation is tied to "top tier", not per-level, so it scales
   to any number of levels; anti-starvation for the rest is the aging key (2).

5. **Global concurrency budget** (admin-set) caps total in-flight runs across all
   jobs — not per-job — so ordering actually controls backend load.

6. **Re-prioritise = update the row's level + recompute `sched_key`** (插队, owner/
   admin, auditable). **"N ahead"** = count of waiting items that would dequeue
   before it (smaller `sched_key`, honouring the reserved-slot rule).

7. **Cancel.** Queued → drop. Running → cancel the local context and, optionally,
   call Dify's stop endpoint so the *remote* run truly aborts (the manifest gains an
   optional `stop` request template).

## Dify API usage

- **Run:** `POST /v1/workflows/run` (`response_mode: blocking`) — already used by the
  manifest; the worker's blocking call *is* "run and wait", the queue gates when it
  starts.
- **Status:** `GET /v1/workflows/run/:workflow_run_id` — available; the blocking
  response already carries `data.status`, so not required for the MVP.
- **Stop:** `POST /v1/workflows/tasks/:task_id/stop` — used only for true cancel of a
  running run.

## Alternatives rejected

- **Preemptive priority** (kill a running lower-tier run for an incoming 加急):
  wastes a paid Dify execution; the reserved slot + aging give bounded urgent
  latency without discarding work.
- **A reserved slot per level:** doesn't scale past a couple of tiers; reserve the
  top tier only and let aging protect the rest.
- **WFQ / stride / lottery scheduling:** more machinery than needed; the
  virtual-timestamp key delivers priority + bounded aging in one sortable field.
- **Batch as an iframe app:** would require privileged write scopes in sandboxed
  code and can't host the Go engine; batch stays native (ADR 0003).
- **A background aging loop** that periodically bumps waiting items: unnecessary
  state and timers; the virtual key makes aging a pure function of enqueue time.

## Consequences

- `launchJob` becomes *enqueue*; one scheduler goroutine assigns freed slots by the
  key + reserved-slot rule + global budget. Crash recovery mirrors the existing
  engine (re-queue in-flight rows on restart).
- Priority is a **per-job** attribute (the submitted job is the unit a user
  prioritises); within a running job the existing engine still parallelises its rows
  up to the job's concurrency, drawing from the shared global budget. Exact
  budget accounting (whole-job vs per-row admission) is settled in implementation.
- New state: a `priority` + `sched_key` on the job (extend `batch_jobs`, or a
  dedicated `queue_items` table) and a small priority-levels registry.
- Frontend: a queue view (position + live "N ahead"), a priority picker on submit,
  and re-prioritise / cancel controls.

## Rollout

1. **Core queue + scheduler:** priority registry, `sched_key` aging, non-preemptive
   dequeue, reserved top slot, global concurrency budget. Unit-tested in isolation
   (deterministic clock injected, like the app-token registry).
2. **Fold batch in:** `CreateBatchJob` enqueues at a chosen priority instead of
   starting immediately; the scheduler admits jobs against the budget.
3. **Frontend:** queue view + "N ahead" + re-prioritise/cancel; optional Dify `stop`
   wired for true cancel.
