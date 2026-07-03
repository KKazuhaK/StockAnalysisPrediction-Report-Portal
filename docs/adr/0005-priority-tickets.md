# 5. Priority tickets (group weight → 加急 quota)

## Status

Accepted — 2026-07-03. Implemented the same day. Extends the run queue (ADR 0004).

## Context

The queue (ADR 0004) lets a submitter pick 加急 (urgent) freely. With no cost,
everyone eventually marks everything urgent and the tier stops meaning anything —
priority inflation. We want urgency to be a **scarce, fairly-allocated resource**,
tied to the organizational groups from the user system.

## Decision

**加急 costs a "次票" (ticket). Tickets are a per-user quota allocated by group
weight and refilled each period.**

- **`user_groups.weight`** (int): how many 加急 tickets each member is granted per
  period. 0 = none.
- **Per-user allocation = the maximum weight across the user's groups.** Max, not
  sum, so allocation can't be inflated by joining many groups.
- **A ticket buys one 加急 submission.** Submitting at 加急 spends one; with none
  left the job is **downgraded to 普通** (never blocked — the run still happens).
- **Admins are exempt** (unlimited 加急). The quota governs operator/user.
- **Refill is lazy — no cron.** Per-user state is `(remaining, period_start)`. On
  any access, if one or more whole periods have elapsed since `period_start`,
  `remaining` resets to the allocation and `period_start` advances by whole periods
  (cadence preserved). This is the same compute-from-time trick as the queue's aging
  key: no background job, correct across restarts. Use-it-or-lose-it (a reset, not
  accumulation).
- **Allocation changes take effect next period.** Raising a group's weight mid-period
  does not retroactively top up (and can't be abused to hand out tickets by toggling
  weight); it applies at the next refill.
- **Refill period** is an admin setting (`batch_ticket_period_days`, default 7 =
  weekly; set 1 for daily).

Weight only mints tickets — it does **not** add a continuous bias to scheduling.
Once your tickets are spent you're back to 普通; the queue's existing priority +
aging (ADR 0004) does the rest. This keeps the mechanic discrete and predictable.

## Alternatives rejected

- **Continuous weight bias on `sched_key`** (a high-weight group's 普通 jobs also
  creep forward): fuzzier and harder to reason about than discrete tickets.
- **Allocation = sum of group weights**: gameable by joining/creating groups; max is
  simple and fair.
- **A background cron granting tickets**: unnecessary state and a moving part; the
  lazy time-based refill is exact and stateless.
- **Blocking the submit when out of tickets**: worse UX than a transparent downgrade
  to 普通 — the work still runs, just not jumped.

## Consequences

- New: `user_groups.weight` column (ships with the still-new groups table, so no
  migration) and a `priority_tickets(username, remaining, period_start)` table.
- `apiBatchJobCreate` calls `urgentAllowed`, which spends a ticket for a non-admin
  urgent submit and reports `downgraded` so the UI can say so.
- The run form shows "加急 N/额度" and disables the 加急 option at 0; admins see no
  meter. Group weight is edited in the Groups drawer; the refill period in Batch
  settings.
- Tests: the pure refill policy (first-use / mid-period / rollover / multi-period /
  misconfig), spend + allocation (max weight), and the submit-time downgrade.
