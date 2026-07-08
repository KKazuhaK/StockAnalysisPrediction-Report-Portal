# 15. Reconcile-not-retry: crash-durable run recovery and the `untracked` outcome

## Status

Accepted — 2026-07-08. Shipped in v0.2.2. Extends ADR 0006 (Dify-native client, the
streaming/reconcile *mechanism*), ADR 0001 (batch engine), and ADR 0011 (run-level
scheduling); uses the additive-column machinery of ADR 0013.

## Context

A Dify run is expensive: a Deep Research run was observed at ~1M tokens / ~60 minutes. The
portal runs it over a streaming (SSE) connection (ADR 0006). Two things can sever the portal
from a run that is already executing (and already costing tokens) on Dify:

1. **A live stream drop** — the process is up, but the SSE connection broke mid-run.
2. **A process crash/restart** — all in-memory state is gone; only what was persisted to
   `batch_items` survives.

The naive recovery in both cases — re-run the item — **duplicates a charged run**. The
invariant this ADR exists to hold:

> A run that has **started on Dify is never re-run.** Recover its true outcome by reconciling;
> if that is impossible, record it as **unknown** — but never fire it again.

After a crash the only evidence is what was persisted before it. The signals, weakest-last:

- `run_id` / `conversation_id` — a **reconcilable** handle (poll `GetWorkflowRun`, or read the
  conversation's latest turn).
- `task_id` — proves the run **started**, but Dify has no get-by-task_id, so it is **not**
  reconcilable.
- `dify_started_at` — **new**: the stream opened (HTTP 2xx). Proves the run reached Dify even
  when it crashed before emitting a single event (so no id at all was captured).

## Decision

### 1. Persist the crash-durable signals as early as possible

- `SaveItemDifyRef` writes `run_id` / `conversation_id` / `task_id` the instant each streams in
  (each only when non-empty, so a later event never clobbers a captured value).
- `MarkItemDifyStarted` stamps `dify_started_at` the instant the stream opens (2xx), **before
  any id** — driven by a synthetic `stream_open` event the Dify client emits at the 2xx boundary
  (`internal/dify`), threaded through `onStarted`. Written once (guarded on empty).

### 2. A new terminal outcome: `untracked`

`batch.Outcome` gains `Untracked` (item status `"untracked"`): *the run reached Dify but its
outcome could not be confirmed* — no reconcilable handle, or a reconcile that never reached a
terminal state. It is **terminal-but-neutral**: not a failure (the run may well have succeeded
and cost tokens) and **never re-run**. Keeping it distinct from `failed` (Dify ran and reported
failure) stops the operator from retrying it and double-charging. `status` is a free `TEXT`
column, so this is a new value, not DDL — like `expired` (ADR 0014).

### 3. Live stream drop (in-process): reconcile, else `Untracked` with no error

When the stream drops, reconcile by whatever handle was captured. If reconcile can't reach a
terminal state — deadline, unknown id, or no id at all but the stream had opened — return
`RunResult{Status: Untracked}` with a **nil error**. A nil error is precisely what stops the
engine from retrying: the money invariant lives in *returning a terminal result*, not in an
error classification. Only a genuine **pre-stream** failure (connection refused / non-2xx →
nothing started) is a retryable error.

### 4. Crash resume: classify every orphaned `running` row four ways

`resumeBatchJobs` splits the orphaned `running` rows by their persisted signals so a started
run is never duplicated:

| Persisted signal | Recovery |
|---|---|
| `run_id` or `conversation_id` | **Reconcile** to the true outcome (never re-run). |
| `task_id`, or `dify_started_at`, with no reconcilable id | **`untracked`** (started, nothing to poll; never re-run). |
| No id **and** no `dify_started_at` | **Requeue and re-run** — the run never reached Dify. |

### 5. Manual reconcile

An admin endpoint reconciles an `untracked`-with-handle row on demand (settling a row that
looks failed in the portal but actually finished on Dify). A row with **no** handle is refused —
there is nothing to poll, and re-running would risk a duplicate charged run.

## Consequences

- **Schema:** `batch_items` gains `run_id` / `conversation_id` / `task_id` / `dify_started_at` —
  all additive, auto-reconciled by `ensureColumns` (ADR 0013), no versioned migration step.
- **One residual, by design:** a run whose HTTP request was in flight when the process died
  *before* a 2xx is re-run — without a 2xx we cannot confirm Dify accepted it, and re-running is
  data-idempotent (report ingest upserts on `uid`). Stamping `dify_started_at` at 2xx (not at
  send) is deliberate: a `connection refused` before 2xx really *is* safe to re-run, and marking
  at send would falsely abandon those.
- **Cost vs data:** requeue-and-re-run is *data*-idempotent (no duplicate report) but not
  *cost*-idempotent (a re-run still burns tokens) — which is exactly why the buckets above keep
  the requeue set as narrow as possible.
- **UI:** `untracked` renders as a neutral (gold, not red) tag with a hover explanation;
  `untracked`-with-handle rows expose the manual-reconcile action.
