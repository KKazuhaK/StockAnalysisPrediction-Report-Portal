# ADR 0029 — Prediction scoring: an append-only claim ledger, and no accuracy number

**Status: Proposed — deferred by the project owner. No code in this repository implements any of
it.**

> Nothing below exists. There is no `predictions` table, no `predictions[]` on the ingest payload,
> no evaluator loop, no open-claim panel, and no machine verdict written anywhere. Every table,
> column, code and enumerated value in this document is a proposal. It is written down because the
> research that produced it cost more than the writing, and because the two load-bearing findings
> are facts about code that ships today and will keep being true whether or not this is ever built.
> Of the four-stage investigation this came out of, only the first two stages shipped: the
> `names.go` hardening, and the quote endpoint of ADR 0028. If you are reading this to find out how
> the portal scores its predictions — it does not, and one section here argues that it should never
> publish a number that says it does.

## Context

The request was a scoreboard: take what the reports predicted, compare it against what the market
did, show an accuracy percentage. Two things came back from investigating it, and the order matters.

The first is that market data is not the hard part. `web.ifzq.gtimg.cn/appstock/app/fqkline/get`
returns daily OHLC, a live snapshot, session state and an adjusted/unadjusted switch in one call,
addressed by the prefix `marketPrefix` (`names.go:224`) already produces. That collapsed the
"history is a whole new dependency" worry and funded the price strip and the chart that shipped in
this release under ADR 0028.

The second is that **this repository has never recorded a falsifiable claim**, and its one
claim-shaped table is designed to be rewritten wholesale. That is the finding that reorders the
project, and it is the reason a scoreboard built on today's schema would be systematically
flattering no matter how good the market data behind it was.

### The bottleneck: `tracking_items` is rewritten on every ingest

`SetTracking` (`store.go:1511-1556`) DELETEs every tracking row for a report
(`store.go:1534`) and re-INSERTs from the newest body. It carries exactly two fields across that
rewrite — `status` and `review_point` — and only when the status is neither empty nor `pending`
(`store.go:1528`). This is correct, and the comment above it explains why: the body is the truth
about which assumptions a report makes, but the verdict is the one part a person puts there, and a
nightly re-run that reset every human review to pending would make the review queue worthless.

The consequence for scoring is total. **Any machine-computed outcome stored on a tracking row is
erased by the next re-ingest.** An `outcome_*` column added to `tracking_items` lands at NULL on the
next nightly run of the same report. Carry-over will not save it either: the prior map is keyed on
the exact `itype + "\x00" + content` within one `report_id`, so one character of LLM rewording is a
different key, and `rdate` is part of report identity (`reportIdentExpr`, `store.go:252`) so
tomorrow's report is a different row that inherits nothing at all.

### And a dropped claim leaves no trace

At ingest, `apiv1.go:271` reads `if len(in.Tracking) > 0`. An empty array is a silent no-op:
`SetTracking` is never called, the previous rows stay attached to a report whose body no longer makes
those claims, and the report and its tracking quietly disagree. A non-empty array that simply omits
one item is worse in the other direction — the DELETE removes the row and nothing anywhere records
that the claim was ever made. Either way, **the claim most worth keeping is the one it is easiest to
drop**, and the portal cannot tell the difference between "we never said that" and "we stopped
saying that once it started going wrong."

A track record whose rows can be revised and deleted by the party being scored is not a track
record. Everything in this ADR follows from fixing that, and the scoreboard is what we refuse.

## Decision (proposed, not built)

### A claim is a row in its own table that `SetTracking` never touches

A new `predictions` table, declared in `baseSchemaStmts()` alongside the others, fed by an optional
`predictions[]` array on `POST /api/v1/reports`. Backward compatible for free: the ingest decoder
(`apiv1.go:196`) does not set `DisallowUnknownFields`, and every existing producer omits the field.

Required per claim: an emitter-supplied `claim_key`; a per-item `symbol`; `kind`; `comparator`;
`threshold`; `unit`; `baseline` and `baseline_date`; an explicit ISO `horizon_end`; and `basis`.
Optional: `confidence`, `claim_text`.

The per-item symbol is not a nicety. Today the tracking symbol is copied from the enclosing report
payload (`apiv1.go:276`), so every item of a thematic report — which legitimately posts an empty
`symbol` — carries `symbol=''` and is unresolvable. A ledger row that cannot name the instrument it
is about is not scorable, so the symbol belongs on the claim.

Prices are **integer 分**, never `REAL`. There are zero `REAL`/`DOUBLE`/`FLOAT`/`NUMERIC` columns in
`store.go` today, and no numeric dialect helper beside `pkAuto`/`blobType`/`likeOp` because nothing
has ever needed one. `REAL` is an 8-byte double on SQLite and float4 (~7 significant digits) on
Postgres, and this store runs on both — so a `>= 45.00` boundary could resolve differently per driver
in the one table whose entire purpose is being citable.

### `made_at` is server receipt time, never payload-derived

This is the single most important column and the least obvious one. `validReportDate`
(`apiv1.go:28`) accepts any well-formed calendar date, past or future, and `ingestInstant`
(`apiv1.go:74`) honours a client-supplied RFC3339 `time` verbatim. Both are right for what they do:
report dates are business dates and `import-legacy` backdates them on purpose. But it means any
holder of an ingest token can post a report dated last quarter whose horizon has already passed. If
`made_at` came from the payload, **that would enter the ledger as a call made in advance**, and the
ledger would be forgeable with no trace by the party it exists to hold to account.

So `made_at` is stamped from receipt, and `horizon_end <= made_at` is rejected at ingest. A claim
about a horizon that already closed is not a prediction.

### Rows are immutable; a re-emitted key supersedes rather than overwrites

Insert-only. A re-emitted `claim_key` whose scored fields changed inserts a **new row** carrying
`supersedes_id`, so moving a target price is recorded as having moved rather than performed
silently. This is the property `tracking_items` structurally cannot have, and the only reason the
table is separate.

### Withdrawal is reconciled across reports, not within one

At ingest, compare the incoming claim keys against the **prior report in the same series** — same
symbol, rtype and version, the latest earlier `rdate` — and stamp `withdrawn_at` on keys that were
present then and are absent now.

Cross-report is not an optimisation, it is the whole mechanism. Report identity is
`symbol | rdate | rtype | title | version` (`store.go:252`), so tomorrow's report is *always* a new
row. A withdrawal check keyed on `(report_id, claim_key)` would fire only when the same row is
re-ingested — and never on the daily cadence, which is where the workflow actually lives. A
same-row-only check is a withdrawal detector that cannot fire on the normal case.

### The machine verdict must not be written to `tracking_items.status`

Even with the ledger in place, the tempting shortcut is to mirror the outcome into the tracking
status so the existing review queue lights up. It must not happen. `SetTracking` carries over any
status that is neither empty nor `pending` (`store.go:1528`), so a machine verdict written there
would be **inherited by the next re-run as if a person had decided it** — and, worse, would
overwrite a human judgement that disagreed. Column separation buys tamper-resistance and provenance
for free; a shared column throws both away. `decided_by` plus a `recordChange` audit entry is the
genuinely missing piece on the human side, since `apiTrackingUpdate` records nothing today, unlike
ingest (`apiv1.go:253`).

### Cascade in **both** delete sites, in the same commit as the table

`predictions` is a `report_id` child table, and there are exactly two places that hand-delete a
report's children:

- `DeleteReport` (`store.go:1817`) — the API/manual path;
- `deleteReportChunk` (`cleanup_store.go:217`) — the storage-cleanup retention path (ADR 0017).

Each currently deletes `tracking_items`, `report_viewers` and `report_revisions` by hand. **Missing
either one is not wasted rows, it is an access-inheritance bug**, and the comment at the *first*
site already says why — inside `DeleteReport`, `store.go:1839`: ids are reassigned, so a later
report inherits the orphans. The second site does not repeat it. `deleteReportChunk`'s doc comment
(`cleanup_store.go:211-216`) gives the ADR 0024 reason for the viewer rows and the ADR 0026 reason
for the revisions and stops there, so anyone who opens the cleanup path looking for the id-reuse
argument has to be sent back to `store.go`.

A ledger whose rows drift onto an unrelated report counts another report's claims as that report's,
and is unreadable in the meantime, because scope is inherited through a join to a row that no
longer exists (`QueryTracking`'s pattern, ADR 0024).

The consequence is stated plainly rather than engineered around: **the ledger's lifetime is the
reports' retention floor.** Purge a report and its claims go with it. Denormalising report identity
and `owner_group` onto the row and nulling `report_id` on cascade is the right long-term shape, but
it is a whole extra ADR 0017 target, floor const, `cleanup_runs` column and usage category. Nobody
should believe the ledger outlives a purge.

### Scoring happens on unadjusted closes, and only unadjusted

The evaluator resolves claims whose `horizon_end` has passed by fetching `bfq` (unadjusted) bars
from both vendors and requiring exact agreement on the close.

`qfq` (front-adjusted) prices are **not stable for a fixed date** — the whole series is re-based on
every corporate action — so a bar cached in July is a different number in December. A nominal
threshold like 目标价 45 元 compared against a front-adjusted close is compared against a price that
never printed on any screen, and the error has a direction: adjustment pushes historical prices
down, so every upside target is scored against a number biased low. It is a **systematic bias
against every upside target, clustered in the May-to-July dividend season**, and corroboration
cannot catch it, because both vendors re-base the same way and will agree exactly on the same wrong
basis. This is the one error mode where two sources agreeing is worth nothing.

Which is worth saying about corroboration generally: it catches parser drift and field-position
drift on one vendor — genuinely valuable, and the best reason to do it — but both vendors restate
the same exchange feed, so agreement says nothing about adjustment convention, halt state or
trading-date semantics, which is where wrong numbers actually come from. The "corroborated" label
must not be allowed to do work it cannot support.

### Path-dependent comparators are answerable, because the endpoint returns OHLC

`cross_above` and `cross_below` cannot be settled from a single close, which is why a snapshot-only
vendor would have forced them out of scope. They are in scope here: the same `fqkline` call returns
full OHLC per bar, so `cross_above` is `max(high)` over `horizon_start..horizon_end` and `gte` is
the close at the end, from one fetch and one parser.

### Refusal is a first-class outcome with a retry, not a gap

The evaluator must be able to say it does not know. Enumerated refusals: `sources_disagree`,
`no_bar`, `stale_bar`, `suspended`, `unverifiable` — each stored with `attempts` and
`next_attempt_at`, so a one-day vendor outage is a retry rather than a permanent hole in the ledger.
A refusal is a recorded state of the claim, not a missing row, which is what keeps the open-claim
panel honest about what it could not check.

### The evaluator's shape and switch

A `geoUpdater`-shaped loop (`geo_update.go:501-526`): one-minute tick, interval re-read from `meta`
each pass, single-flight mutex, `lastAt` advanced even on failure — explicitly *not* `cadenceDue`,
which fires once per civil day and is shared by `cleanupTick` and `recurringTick`.

It ships **disabled**, behind `prediction_auto` defaulting `"0"`, with its admin writer and its panel
in the same stage.

The precedent is not that a background loop never reaches the network — `scheduleLoop`
(`batch_run.go:412`) ticks every 30 seconds for the process lifetime with no setting in front of it,
and its `scheduleTick` (`:426`) reaches outbound HTTP two ways: `admitLocked` (`:429`) launches Dify
runs, and `fireEvent(EventBatchFinished, …)` (`:451`) becomes `go s.deliverWebhook(...)`
(`webhook_run.go:40`). The precedent is narrower and it holds: a loop that calls a **third party of
the loop's own choosing, on every install, whether the operator wanted it or not** carries a switch.
`autoLoop` opens with `if GetSetting(setGeoAuto, "0") != "1" { continue }` (`geo_update.go:503`)
before it goes anywhere near MaxMind, and every ADR 0017 target ships off (decision 7,
`docs/adr/0017-storage-cleanup.md:77`, "Every target ships disabled"). A sixth always-on loop
egressing to Chinese market-data vendors by default on every install — including the ones where
those hosts are unreachable — inverts that. `scheduleLoop` is not a counter-example to it either:
its outbound calls happen only because an operator configured a webhook or queued a job, whereas
this one would call out on a schedule nobody asked for.

Equally, the switch must be wired end to end in the stage it appears in — reader, admin writer and
panel — the way `setGeoAuto` is (read at `geo_update.go:503`, written at `geo_update.go:579`). A
setting with a reader and no writer is not configuration; it is a constant with a misleading name.

### Beijing codes are refused at ingest

`marketPrefix` maps the 4/8/9 prefixes to `bj` today, so these codes are already in scope. Neither
vendor has usable daily history for them: Tencent returns an empty `day` array while its snapshot
works fine (`internal/app/testdata/quote/tencent_fqkline_bj830799.json`, `qt` length 87, `day`
length 0), and Sina's series for `bj830799` stops at 2025-04-29 — sixteen months stale, and it
looks like data, which makes it worse than nothing. A `bj` price claim would score silently and
wrongly, or never resolve at all.

That second measurement is the one number in this document a reader cannot re-check from the
repository alone: it comes from a live probe on 2026-09-06, and there is no Sina `bj` body in
`internal/app/testdata/quote/` to hold it. The Beijing refusal rests on it, so it is labelled.

So a price-kind claim on a `bj` symbol is rejected at ingest with a `v1err`, rather than stored as a
row that can only ever become a permanently-open zombie on the ledger. Refusing at the edge is the
only place the emitter can be told.

## WHY THERE IS NO ACCURACY PERCENTAGE

This is the part of the original request that is refused hardest, and it is refused for five
independent reasons. Any one of them would make the number misleading; together they make it
indefensible.

1. **There is no defined verdict domain to count over.** `confirmed` / `invalidated` / `expired`
   exist in exactly one place in this repository: a three-element array of suggestion buttons in
   `ReviewPage.tsx:27`, commented "Offered as suggestions, not enforced." There is no Go constant
   (the only one is `trackingPending`, `tracking_review.go:239`), no database constraint
   (`tracking_items.status` is `TEXT DEFAULT 'pending'`, `store.go:446-448`), and no OpenAPI enum —
   `openapi.json` contains no `enum` anywhere. A percentage is a count over a domain, and the domain
   is three strings in a React file that the Select will happily let anyone type past.

2. **There is no baseline.** A hit rate over heterogeneous claims rewards vague thresholds and
   punishes aggressive ones. 62% is unreadable without knowing what a null strategy scores on the
   same claim set, and nothing in the design produces that comparison.

3. **The denominator is self-selected.** It counts only the claims the workflow chose to emit
   structurally. A prompt that structures its confident calls and leaves its hedged ones in prose
   produces an impressive number that measures prompt behaviour, not analysis.

4. **Refusals are not random, and they correlate with being wrong.** Corroboration fails precisely
   on corporate actions, halts and delistings — which is where predictions go wrong. Dropping
   refusals from the denominator therefore removes disproportionately many losses; keeping them
   scores them as something they are not. There is no neutral handling.

5. **Horizon vintage makes it drift for reasons unrelated to quality.** The first number ever shown
   is the accuracy of fast-resolving claims, because they are the only ones that have resolved. It
   will fall for a quarter as slower claims mature, and that fall says nothing about whether the
   analysis got worse.

**What would be built instead, if any of this were ever built: the open-claim panel.** It does not
exist — the blockquote at the top of this document lists it by name among the things that do not,
and nothing in `web/src` or `internal/app` implements it. What is proposed is this: on the reading
page, every still-open claim earlier reports made for this symbol, with distance-to-threshold and
days-to-horizon, read off the same in-memory bars the chart already fetched. It would answer "does
what we said still hold" at the moment an analyst is deciding — which is the question that changes
the work — and it would need **no aggregation at all**, so it would deliver the value while touching
none of the five traps above.

## What we deliberately do NOT do

- **A per-analyst score. Ever.** `reports.author` is `''` for everything a workflow produced
  (`store.go:268-283`) with no backfill, and ADR 0022 describes the only report→person chain as
  "fragile, unenforced" (`reports.run_id ≈ batch_items.run_id → batch_jobs.created_by`). The schema
  cannot substantiate a claim about an identifiable person, and asserting one anyway is a different
  legal category from rating a workflow.

- **LLM-extract claims from existing report text.** See the blocker below.

- **A `PermReviewTracking` gate on the verdict write.** Restricted external clients hold role `user`,
  whose permission set is empty, so the gate would make every external tenant's review queue
  read-only and invert what `TestTrackingAPIRefusesToReviewWhatYouCannotRead`
  (`tracking_api_test.go:51-95`) asserts — a restricted role-`user` client reviewing their own item
  and having it stick — while leaving the actual tamper vector,
  the unscoped `PATCH /api/v1/tracking/{id}`, untouched. Column separation achieves the goal without
  it.

- **A periodic price poller.** Fetch-on-read with TTL and a hand-rolled single-flight gives the same
  freshness with no sixth ticker and no per-minute write burst.

## The blocker that gates all of it

**The Dify workflow must agree to emit structured claims, and that is a conversation, not a
task.** Without a `claim_key`, an explicit horizon date and a baseline coming from the emitter,
there is nothing to evaluate and no portal-side work unblocks it.

There is nothing to retro-parse. `content` is free prose by design, and `review_point` is free text —
`trackingDueDate` (`tracking_review.go:51-71`) opportunistically regexes a date out of it **on read**
and stores nothing, and it is deliberately narrow because guessing wrong is worse than admitting
there is no date. Having the portal LLM-extract claims from the existing bodies is the option that
would demo beautifully and be **silently wrong into a human-reviewed queue with no attribution** —
the reviewer would be checking claims the analysis never made, believing an analyst made them.

Expect resistance, and understand what it means: pre-committing to a falsifiable threshold is
exactly what an LLM report is currently free not to do, and removing that comfort **is** the entire
value of the feature. A "no" from the workflow owner is a hard stop, and should be treated as one
rather than routed around.

## Consequences

- **The portal continues to have no track record.** Claims made in reports are still rewritten by
  the next ingest and still droppable without trace. That is the status quo, and this ADR is the
  record of what it would take to change it, not a change to it.

- **Effort, honestly:** the claim ledger is 5-7 engineering days and the evaluator plus panel a
  further 6-9, so **11-16 days** for the deferred half, on top of the 7-10 already spent on the
  hardening and the quote work — and that is before anyone sees a number, which this ADR recommends
  they never do. The quote strip and the K-line chart that shipped in this release under ADR 0028
  are the part of the original request people will actually notice; the ledger is the part that
  would make the other half honest.

- **Two findings outlive the deferral.** `SetTracking`'s destructive rewrite and the traceless
  dropped claim are true of the code today and will bite anything else built on `tracking_items`.
  Anyone proposing a verdict column, an outcome field or a scoreboard on that table should be
  pointed here first.

- **If this is picked up later, the order is fixed:** the workflow conversation, then the table with
  both cascade sites in the same commit, then the evaluator. Building the contract speculatively is
  the failure mode this document exists to prevent.
