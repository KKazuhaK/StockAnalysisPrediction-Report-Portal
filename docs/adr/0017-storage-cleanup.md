# ADR 0017 — Storage cleanup console (retention + manual purge)

## Context

The portal accumulates data with no retention mechanism anywhere. A survey of the schema found the only
unbounded-growth tables are `batch_jobs` / `batch_items` (the base schema itself comments that
`batch_items` is "the fastest-growing table") and, secondarily, `reports` / `tracking_items` (core
content); expired `api_tokens` rows are also never removed. There is deliberately **no** webhook
delivery log, audit log, session table, or Dify run-history table — those "logs" don't exist to clean.

Admins want a single **storage-management console**: see how much space each category uses, clear old
data manually with a preview + confirm, and optionally run a scheduled retention pass on an admin-set
cadence (daily / weekly / monthly at a preset time). Reports are core content, so their deletion is
manual-first, off-by-default, and heavily guarded.

Prior art this builds on: the `scheduleLoop` background ticker (ADR 0007), the preset recurrence
resolvers in `run_preset.go` (ADR 0014), the run-level scheduler serialization (`schedMu`, ADR 0011),
and the reconcile-not-retry `untracked` outcome (ADR 0015).

## Decisions

1. **A second always-on ticker (`cleanupLoop`).** Modeled exactly on `scheduleLoop` (a
   `time.NewTicker`, `go s.cleanupLoop()` from `RunServer`, no cancellation), it checks once a minute
   whether a scheduled cleanup is due. **This amends ADR 0007's claim that its ticker is "the only
   always-on timer" — there are now two.** A missed pass (process down) is stateless: it simply waits
   for the next period.

2. **Admin-set cadence, reusing the `run_preset.go` primitives.** The schedule is `off` / `daily` /
   `weekly` (weekday) / `monthly` (month-day, clamped to month length) at an `HH:MM` time in the panel
   timezone. `cleanupDue` reuses `parseHHMM` and the same month-length clamping as `atClamped`; it fires
   at most once per matching day, deduped by a `YYYY-MM-DD` stamp in `meta['cleanup_last_run_period']`
   written **before** the run (so the 60s ticker can't re-fire the same day after a slow pass or crash).

3. **Set-based, predicate-re-asserting DELETE as the race-safe primitive.** Batch history is purged by
   one `DELETE ... WHERE status IN ('finished','cancelled','expired') AND finished_at <> '' AND
   finished_at < ?` (children first via a subquery), **not** a select-then-`DeleteBatchJob(id)` loop. A
   job requeued by `RequeueItems` between a count and the delete (status → `queued`, `finished_at`
   cleared) simply no longer matches the predicate, so it is never removed mid-flight.
   `RequeueItems` does not hold `schedMu`, so a lock would give *false* safety — the deliberate choice is
   to **not** hold `schedMu` across deletes (it would stall run admission) and rely on the disjoint-set
   invariant instead. Purging at job granularity means an unreconciled `untracked` item (ADR 0015) is
   only ever removed with its already-terminal parent job.

4. **Reports age off `sent_at`, parsed fail-closed.** `sent_at` is the report's last-write instant (it
   is overwritten on every re-ingest — the updated_at semantic), and there is no server-stamped
   `created_at` column, so it is the age signal. It is heterogeneous: production data is ~16% zoned
   RFC3339 (`...Z`, the v1 `ingestInstant` stamp) and ~84% a timezone-less microsecond form
   (`2006-01-02T15:04:05.575420`). `parseReportInstant` tries a list of layouts and treats a zone-less
   value as UTC (the on-the-wire convention); a value carrying no precise instant (date-only, empty —
   and `rdate`, which is client-supplied and backdated by `import-legacy`) matches none and is **kept**
   (fail-closed: never delete a report we cannot prove is old). This biases to under-cleaning — the safe
   failure mode for core content. Reports also default **off**, carry a ≥365-day floor clamped on both
   save and read, and enabling or running them requires a live-count confirmation. No `created_at` column
   was needed: every production `sent_at` parses, so a schema change was avoided.

5. **Config lives in `meta`; only the audit history is a table.** All schedule/target/retention config
   plus the last-run summary ride the existing `meta` k/v store. One new table, `cleanup_runs`, records
   one row per real pass (scheduled or manual; previews are not recorded) for a durable, browsable trail
   of what a destructive auto-delete removed — trimmed as a ring buffer to the last 200 rows.
   Idempotency of the scheduler stays in `meta['cleanup_last_run_period']`, **never** derived from the
   trimmed table.

6. **Retention floors are Go consts, not settings** (`minBatchRetentionDays = 7`,
   `minReportsRetentionDays = 365`), clamped on both save (400 below floor) and read, so a hand-edited
   `meta` value can never bypass a floor.

7. **Every target ships disabled; `PermManage` gates everything.** No new permission — the console is
   admin-only via `requireAdminJSON`. The manual "clean now" acts on the admin's explicit target
   selection regardless of the scheduled-enable toggle; the toggle governs only the scheduled pass.

## Consequences

- `batch_jobs`/`batch_items` and expired tokens can now be bounded; reports have a manual, guarded purge
  path but no silent auto-delete by default.
- Legacy/date-only/imported reports are never eligible for auto-delete. Making them eligible would
  require a server-stamped `reports.created_at` column — a schema change deferred until asked for.
- The `cleanup_runs` table is the first "log-like" accumulating table introduced deliberately; the ring
  trim keeps it bounded.
- A single set-based delete holds the SQLite writer for the duration of one pass; cleanup runs off-peak
  and by cutoff, so this is acceptable. Chunked deletes are a future optimization if a first-run backlog
  proves too large.
