# ADR 0024 — Report versions: publishing one analysis in several written forms

## Context

An investment-decision report exists in more than one written form. The internal edition carries the
scoring table, the factor weights and the shape of the prompt that produced them. What a client may
read is the conclusion and the score, and nothing about how we got there.

The portal had no way to express that, and the gap was not merely a missing feature — it was an
active disclosure. ADR 0022 R1 gave a restricted OU a read predicate of
`owner_group = :myOU OR (rdate = :panelToday AND owner_group IS NULL AND rtype IN :entitled)`. The
owner token is injected only for restricted OUs, so **every internal report is NULL-owner**, which
means the second clause is "today's internal reports". An external OU entitled to run 投资决策 was
therefore handed the internal analysis verbatim — scoring table, weights and all — by the very
same-day-reuse rule that was supposed to save it a run. This was reproduced on a running server
before the change and is the reason the work happened when it did.

Two further problems surfaced while designing the fix, both pre-existing:

- **A restricted OU with no run allow-list saw every internal report from today.** `entitledSubtypes`
  returned `nil` for "no allow-list", and `nil` meant "do not narrow". The half-configured state —
  exactly when a disclosure rule must be closed — was open.
- **Read permission was derived from run permission**, and the code called that a feature ("what you
  may run and what you may see can never drift apart"). It makes a read-only client impossible: to
  let someone *see* a published report you had to grant them the right to *generate* it, and so to
  spend quota and money.

## Decision

### A version is a column on the report

`reports.version` joins the identity tuple, which becomes `symbol | rdate | rtype | title | version`.

Two alternatives were considered and rejected.

**Marked-up sections in one document** — one body, blocks tagged by audience, redacted at read time.
Attractive because nothing is duplicated, and it was built far enough to run before being discarded.
It fails on the shape of the work: each form is produced by **its own run of its own workflow**, so
there is no single document to mark up. It also puts a parser between an external reader and the
internal analysis, which is a bug waiting to have a CVE number.

**A second report type (subtype)** — cheaper, since it needs no index rebuild. Rejected because its
cost is multiplicative: one subtype per report type per version, so eight report types and two
versions is sixteen registry entries, and it degrades precisely as versions multiply — which is the
direction this is expected to grow. Version and report type are orthogonal dimensions; encoding one
inside the other also forces `kind` inheritance and doubles the type list an admin reads.

### Adding a component to the identity is safe; removing one is not

v0.3.0 removed `kind` from the identity tuple and merged 626 genuinely distinct reports. This change
*adds* a component, and the two are not symmetric: every pre-version row resolves to the same default
version, so `(symbol, rdate, rtype, title, default)` is unique exactly where
`(symbol, rdate, rtype, title)` was. The rebuild can neither merge two reports nor fork one.

That is asserted, not argued: `TestUpgradeFromV0310` runs against `testdata/schema_v0.3.10.sql`, which
is the schema a v0.3.10 database actually has — dumped from one created by the v0.3.10 binary itself,
not written from memory. Production runs v0.3.10 and v0.4.0 was never deployed anywhere, so
v0.3.10 → here is the one upgrade path that has to work, and it skips a release.

### The migration is ordered, and the order is the safety argument

`reconcileReportVersions` runs between `ensureColumns` and `createBaseIndexes`:

1. seed the registry, so the default version exists to point rows at;
2. give every row a version — **before** the index is built, because NULLs compare DISTINCT inside a
   unique index on both drivers, so building it first would admit exactly the duplicate rows the
   index exists to forbid;
3. drop the four-column index so the new definition can take its name.

This is the project's first hand-written migration; the additive auto-fill machinery cannot change an
index's shape. Per the squash contract in CLAUDE.md it is folded into the base schema and deleted at
the next major boundary.

It is guarded by `identIndexCoversVersion()`: an index that already covers version proves step 2
completed, so the backfill never runs again. Unguarded, it scanned the whole reports table on **every
boot** — 706ms at 200k reports, a one-time migration billing itself to every restart.

### Reading is version grants plus visibility, and nothing else

A scoped reader may see a report when both hold:

- the report's **version is granted** to them, and
- the version's **visibility** admits them.

Visibility is a property of the version, with three values, and the narrowest is the default for a
newly created one — a forgotten setting must under-disclose:

| | who sees whose reports |
|---|---|
| `owner` | only reports they asked for |
| `group` | anything anyone in their OU asked for |
| `all` | every report of the version, whoever asked |

The difference that matters in practice is whether history a reader never requested is visible: under
`all`, a client onboarded today can browse the whole back catalogue.

Everything else that used to decide this is **gone**. There is no same-day internal pool, and no
narrowing by which subtypes you may run. An empty grant list compiles to `1=0` rather than to an
absent clause: default-deny has to be spelled, not implied.

### Grants name a principal, which is an OU **or** an account

One column holds `g:<id>` or `u:<name>`. Two tables would give the read path two shapes, and the read
path is the last place that should have two ways of being right. It also makes "no OU tree configured
at all" a first-class case rather than a workaround — which is the setup an external user is most
likely to arrive into first. `users.restricted` throws the scoping switch on one account for the same
reason; it ORs with the OU flag, and admins are never scoped because someone has to be able to
diagnose a tenancy problem.

Resolution is **nearest-wins** — the account's own grants, else the nearest ancestor OU with any —
not union. The reason is this project's tree shape: external OUs hang off the Default OU, so a union
would push whatever the root was granted down into every tenant, the internal version included.

### `report_viewers` is the only ownership mechanism the read path consults

`owner_group` survives on the report row for attribution and audit, but the security-critical filter
must not have two spellings. The viewer list is keyed `(principal, rdate, report_id)`; `rdate` is
carried on the row on purpose, so the list page's sort is an index walk rather than a temp B-tree
(0.014ms vs 0.235ms at 200k reports — level with the old `owner_group` filter). Denormalizing it is
safe because `rdate` is part of report identity and never changes after ingest.

Both principals are recorded when a report is attributed — the person and, where there is one, their
OU — so widening a version from `owner` to `group` later needs no backfill.

### Reuse looks up by content, then grants access

This is the part that would have shipped broken. The old gate looked the report up *scoped to the
caller*, so under per-person visibility a first-time requester — on no viewer list by definition —
found nothing, and every request ran for real. Reuse exists precisely for the person who has not
asked before.

The lookup is now by content (symbol, subtype, version, today) and the entitlement check sits in the
caller: the requester passed the run allow-list, so they could have generated the identical content
themselves, and the version must be granted, so they may read that form of it. On a hit the requester
**joins** the report's viewer list.

That is what makes strict per-person isolation cost nothing: two people asking for the same analysis
produce one run and two private-looking reports, and neither learns of the other. The residual signal
is timing — a request that returns instantly reveals that *somebody* asked today, though not who —
which was considered and accepted.

Recording the viewer is fail-closed: if it errors we run rather than hand back a report the requester
then cannot read.

### The switcher groups by (symbol, date, subtype), never by title

Each form comes from its own run of its own workflow, so two forms of one analysis will almost never
carry a byte-identical title. Grouping on it would silently fail to collect exactly the reports the
switcher exists for. Where several reports share the key within one version the newest wins — the rule
`FindSameDayReport` already uses, and for the same reason: the difference is generator output nobody
can predict.

The reading page shows the switcher only when the reader may open more than one form, and the
report-type strip is collapsed to one entry per type, so the two axes have one control each.

## What we deliberately do NOT do

- **Store one body per version in one row.** Rejected with the marked-up-sections design: the forms
  are separate generations, not slices of one document.
- **Change how report identity is derived.** A producer-supplied stable key would fix the unrelated
  problem that a re-run with a drifted (LLM-generated) title creates a second report rather than
  overwriting. Real, but unproven in production and a much riskier change; it stays a separate
  question.
- **Version the report body's history.** "Version" here means audience-facing edition, not revision
  control. Re-ingesting a version still overwrites it in place.

## Consequences

- **Nothing changes until a workflow sends a `version`.** Every existing producer omits it, every row
  resolves to the default, and internal readers get a `nil` scope whose SQL is byte-for-byte what it
  was.
- **A restricted OU that was relying on the same-day internal pool loses it on upgrade.** That is the
  leak; the replacement is an explicit grant.
- **Per-version cost is one registry row.** Adding 客户版 or 监管版 costs no schema change, no new
  subtypes and no code.
- **Producing a version nobody is granted publishes nothing readable.** It is registered on sight so a
  workflow's output is never dropped behind a 400 nobody watches, but it discloses nothing until an
  admin grants it.
