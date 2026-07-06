# 11. Schedule at the run level; batch is a producer

## Status

Accepted — 2026-07-06 (realizes the intent of ADR 0004, supersedes its job-granular implementation)

## Context

ADR 0004 said the global concurrency budget "caps total in-flight **runs** across all
jobs — not per-job" and the scheduler "only chooses which *waiting item* starts when a
slot frees." The implementation drifted from that:

- The scheduler admits whole **jobs** up to the budget (`scheduleTick` → `queue.Admit`
  over `QueuedJobs()`), then each admitted job spins up **its own worker pool**
  (`batch.Engine.RunJob`, a `sem` sized to that job's concurrency) that drives its rows.
- So the unit of scheduling is a job, but the unit of work that hits Dify is a row. With
  budget 3 and three admitted jobs whose row-concurrencies sum to 4, four rows are
  dispatched. v0.1.45 papered over this with a second limiter — a global `runGate`
  semaphore every row must acquire — so at most `budget` rows actually call Dify.

That is **two limiters counting different units against the same number**. It works but:

- the row-level gate is a plain semaphore, **not priority-ordered** — a low-priority
  batch row can grab a freed slot ahead of a high-priority single run that is also
  waiting;
- a job can be `running` while all its rows are parked in the gate (confusing state);
- concurrency is enforced in two places, so reasoning about "how many run at once"
  requires holding both in your head. This is the class of bug the drift caused
  ("cap = 3 but 4 showed running").

The owner's model (and ADR 0004's original intent): **one queue, one slot per run.**
Concurrency is enforced in exactly one place. A batch is not a privileged job with its
own pool — it is a **producer** (ADR 0009: batch is an app) that drip-feeds its rows
into the shared run queue and lets the queue own execution.

## Decision

Make the **individual run (a `batch_items` row)** the unit the scheduler admits. There
is one global run queue; each running item occupies one of the `budget` slots. A batch
job is a producer that contributes at most `concurrency` **active** (running) items at a
time; as one of its rows finishes, the next becomes eligible.

### Scheduling (item-granular)

`scheduleTick` (serialized by `schedMu`), on enqueue / item-finish / 30s tick / startup:

1. `free = budget − runningItemCount()`. If `free ≤ 0`, return.
2. Build **item candidates** from every **schedulable** job (status `queued` or
   `running`, `run_at` due, not cancelling/cancelled): for each such job, its
   **window** = `concurrency − (its running items)`; take up to `window` of its
   `queued` items (lowest `row_index` first). Each candidate carries **its job's**
   priority factors (base/age/fair) and urgent flag — so all rows of a job share the
   job's score, and a higher-priority job's rows outrank a lower one's. The per-job
   window is the producer's in-flight throttle (so one 500-row batch can't flood the
   queue or starve others past its share).
3. `queue.Admit(candidates, runningItemCount, plan)` picks the winners — reusing the
   existing priority scheduler unchanged (it is already generic over scored items;
   `Item.ID` is now an **item** id, not a job id). Reserved-slot / urgent semantics
   carry over verbatim, now at the run level.
4. For each admitted item: `MarkItemRunning(id)` (atomic `queued`→`running`; the winner
   of the race launches it), ensure the parent job is `running` (`MarkJobRunning` once),
   and start one `runItem` goroutine.

### Running one item

`runItem(job, item)`: build the provider, call `batch.RunItem` (a **stateless** retry
wrapper: call the provider, retry only transient transport errors up to `MaxRetries`;
a ran-but-failed result is terminal — the money-invariant reconcile-not-retry stays
inside the Dify provider, untouched), persist the outcome via `FinishItem`, then run
`finalizeJobIfDone(job)` and `scheduleTick()` (a slot just freed).

`finalizeJobIfDone`: when a job has **0 running items** and (0 queued items **or** it is
cancelling), write the aggregate counts + terminal status via `FinishJob`, fire
`batch.job.finished`, and send the done-notification — the completion bookkeeping that
`Engine.RunJob` used to do at the end of a job now happens cooperatively.

### Cancellation / restart

- A per-**job** cancellable context (`jobRuns`) is shared by all of that job's `runItem`
  goroutines; cancelling a job aborts its in-flight Dify calls at once, the scheduler stops
  admitting its remaining `queued` rows, and the last in-flight run to return finalizes it
  as `cancelled`. Never-run rows are simply left `queued`. A job cancelled while it had **no**
  in-flight run (its rows parked behind a saturated budget) has no finishing run to trigger
  that finalize, so the cancel handler finalizes it directly and the `scheduleTick` backstop
  also sweeps `cancelling` jobs — otherwise such a job would strand in `cancelling`.
- **Per-row cancel.** Because a batch is a producer of individual runs, a single row can be
  cancelled without touching the rest: a `queued` row is marked `cancelled` (the scheduler
  never admits it); a `running` row is aborted via a per-row context (`itemCancels`, a child
  of the job's) and its goroutine records it `cancelled` — a distinct terminal status, not
  `failed`, since the operator stopped it deliberately. Job-level cancel is the same
  mechanism applied to every row, so its in-flight rows also land `cancelled`. `cancelled`
  rows are terminal-but-neutral: `total − succeeded − partial − failed`, counted as done for
  progress. `POST /jobs/{id}/items/cancel` takes a list of row ids (one for the per-row ⊘,
  many for checkbox multi-select), authorized like job cancel (owner or admin).
- Restart resume becomes trivial: `ResetInFlightItems` (running→queued) then
  `scheduleTick`; the scheduler re-admits from the persisted item state. No per-job
  engine to relaunch.

### What is deleted

- `runGate` + `gatedProvider` (v0.1.45) — the scheduler's `free = budget − running` **is**
  the single gate now.
- `batch.Engine.RunJob` + its worker pool + the `Gate` interface + the `batch.JobStore`
  port. `internal/batch` shrinks to: the `Provider`/`RunResult`/`Outcome` types, manifest
  compilation, `IsTransient`, and the stateless `RunItem` retry wrapper. Concurrency,
  persistence, and cancellation are the app scheduler's job.

### No schema change

`batch_jobs.concurrency` is reinterpreted (max concurrent **running** items for that job
— the producer window; same column, and single runs keep concurrency 1). No table is
altered; the change is entirely in scheduling logic. New store helpers are read/write
queries only: `MarkItemRunning`, `SchedulableJobs` (queued+running), a per-job running
count (from the existing `LiveJobCounts`).

## Consequences

- **One cap, priority-ordered.** At most `budget` runs execute at once, chosen strictly
  by priority across all runs — single "运行分析" and batch rows compete on equal footing.
  A high-priority single run beats a low-priority batch's rows for the next free slot.
- **Honest state.** A row is `running` iff it holds a slot and is calling Dify; the
  displayed running count equals the real concurrent-run count by construction, so
  "同时上限 X/Y" (X = `RunningItemCount`) can never lie. No parked-but-"running" rows.
- **Simpler core.** One limiter, one place. `internal/batch` becomes a small stateless
  helper; resume is two lines.
- **Batch is genuinely a producer** (ADR 0009), consistent with "batch is an app": it
  enqueues runs and throttles its own in-flight share; it has no privileged execution path.
- **Non-preemptive still.** A running row always finishes (a killed Dify run wastes a paid
  execution); lowering the budget stops new admissions but never preempts in-flight runs.

## Phasing

1. `internal/batch`: replace `Engine.RunJob`/pool/`Gate`/`JobStore` with stateless
   `RunItem`; rewrite engine tests around it (TDD).
2. `internal/app`: `MarkItemRunning` + `SchedulableJobs`; rewrite `scheduleTick`
   item-granular; add `runItem` + `finalizeJobIfDone`; delete `runGate`/`gatedProvider`;
   simplify `resumeBatchJobs`; item-level "N ahead"/waiting counts. Tests first.
3. Verify the whole matrix (cap, priority order, urgent/reserved, cancel, retry, resume,
   job aggregate + notifications, money-invariant) then ship.
