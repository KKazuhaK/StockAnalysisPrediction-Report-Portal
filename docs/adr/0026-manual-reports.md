# ADR 0026 — Hand-written reports: an editor, and a version a workflow cannot take

## Context

A report could arrive exactly one way: a Dify workflow pushing to `POST /api/v1/reports` with a
machine token carrying the `ingest` scope. There was no browser path to write one, no path to
correct one, and no path to bring an existing document in. Deleting one was possible only through
the same machine token or through the storage cleanup console's age cutoff.

That is a gap with three separate shapes:

- **The workflow missed something, or got it wrong.** The portal's answer was to re-run it and hope,
  or to leave the wrong report up. A person who knows the right answer had no way to publish it.
- **The document already exists.** A note written elsewhere — a summary of a broker's research, an
  operator's incident write-up — has to be re-created as a workflow to enter the portal at all.
- **Nobody could be given the authority to write one.** Permissions were `manage` and `run_batch`.
  Publishing words under the portal's name was not a thing anyone could be granted, so it fell to
  whoever held admin.

There is also a constraint the design has to survive rather than work around. Report identity is a
unique index on `(symbol, rdate, rtype, title, version)` and re-ingesting an identity **overwrites
the row in place**. So the most obvious implementation — let a person write a report the same way a
workflow does — has a failure mode on its own main use case: an author writes the report the
workflow missed, the workflow catches up, `UpsertReport` finds the identity, replaces the body, and
returns success. The author's words are gone with nothing anywhere to say so.

## Decision

### A hand-written report is an ordinary report row carrying a reserved version

Not a new table, not a flag, not a second kind of thing. `reports.version = "manual"`, seeded into
the ADR 0024 registry beside `default` by `ensureManualVersion`.

Version already joins the identity tuple, which is what makes this work rather than merely tidy: a
workflow's output and the hand-written report of the same analysis differ in a component of the
identity, so they are two rows and neither can overwrite the other. Everything else follows without
being built. The version switcher shows both — `VersionsOfReport` groups on `(symbol, rdate, rtype)`
and deliberately not on the title, so retitling a hand-written report does not break the link to the
machine one it was written from. The diff, the exports, the tracking items, the report list, the
per-report read scope and the storage cleanup all operate on `reports` and needed no change at all.

Two alternatives were considered.

**A `source='manual'` flag, with the ingest path refusing to overwrite a flagged row.** Cheaper, and
it does stop the overwrite. Rejected on what it leaves undone: visibility would still need a
mechanism of its own, the flag means nothing to the version switcher so the two forms of one report
appear as unrelated rows, and the refusal surfaces to the workflow as a failure it cannot explain
or act on. It solves one of the four problems the version solves for free.

**A separate `manual_reports` table.** Rejected immediately and worth writing down anyway: every
read path in the product — list, search, diff, export, scope filter, cleanup — would need a second
branch, and the read scope is the last place that should have two ways of being right (ADR 0024).

### The reservation is a mechanism, not a convention

`v1Ingest` refuses `version: "manual"` with `400 invalid_param`, and `isManualVersion` trims before
comparing so `" manual "` cannot slip past. Without this, "a workflow cannot overwrite what you
wrote" rests on no producer ever happening to send that version name — and the failure would be
silent, because the overwrite path reports success.

`DeleteVersion` refuses it too, for the mirror reason it already refuses `default`: the write path
would be left pointing at an unregistered name, and every hand-written report ungrantable, which
means unreadable to exactly the people it was addressed to.

### Editing a machine-generated report does not modify it

Opening one in the editor seeds a **new** report at the manual version. The machine row keeps saying
what the run said — which the diff, the version comparison and the tracking review all assume — and
the next run of that workflow overwrites its own row without touching the hand-written one.

This makes "write a new report" and "edit a machine report" the same operation, which is why the
API has one create and one update rather than a third `fork` verb. A fork endpoint would be a second
write path into the same table with its own chance to get the identity, the audience and the
concurrency token wrong.

The constraint is in the store, not only in the handler: `UpdateManualReport`'s `WHERE ... AND
version=?` is what makes it true regardless of caller. The handler's check exists to tell the editor
*what* is wrong.

### Updating is by id, because the identity fields are editable

An author may correct the title, the date, the code or the subtype of their own report. Routed
through `UpsertReport`, correcting a title would insert a row at the new identity and leave the
original behind — in every list, forever, with no way to tell which is current. By id, the row
moves. A rename onto an identity another report holds surfaces as the same `report_exists` collision
the create path reports, found by asking the database who holds it rather than by reading a driver's
error text, which SQLite and Postgres word differently.

`report_viewers` is keyed by `(principal, rdate, report_id)` — denormalized on the date so the list
page's sort is an index walk — so moving a report to another date moves its audience in the same
transaction. Otherwise the report becomes readable by nobody it was addressed to, silently, because
a viewer row whose date no longer matches simply never joins.

### `sent_at` is the concurrency token, at nanosecond precision

Reports have no revision counter and adding one to the hottest table in the schema to serve the
rarest write is not worth it. Hand-written saves stamp `sent_at` with `RFC3339Nano` rather than the
`RFC3339` ingest uses, because at second precision two editors saving within the same second each
compute a token equal to the other's and the second save is accepted as if it had seen the first.
It is still an RFC3339 instant, so every existing reader of `sent_at` takes it unchanged.

### Audience is `report_viewers` — the table the ingest path already writes

The ingest path records who asked for a report from the run's signed owner token. A hand-written
report has no run, so somebody chooses instead; the rows are the same rows, in the same `g:<id>` /
`u:<name>` encoding as `version_grants` and announcement grants.

The manual version is seeded **`VisibilityGroup`**, and that is the whole per-report audience
mechanism rather than a default worth changing. Under `VisibilityAll` the viewer list is never
consulted, so granting the version would hand a reader every hand-written report there is. Under
`group` a reader needs both:

| gate | question it answers | where it is configured |
| --- | --- | --- |
| `version_grants` on `manual` | may this account read hand-written reports at all? | 管理 → 报告版本 |
| `report_viewers` on the row | is this one for them? | the editor's audience picker |

Internal accounts are not scoped at all and never were, so none of this applies to them.

"Everyone" is stored as the Default OU principal rather than as a column: `groupChain` always
appends the Default OU, so that principal is on every reader's chain already. Choosing it from the
picker is refused and points at the "所有人" option, exactly as the announcements console does — it
reads like a subset and means everything.

A hand-written report addressed to nobody is refused at save. Unlike an announcement, whose audience
can be emptied later by deleting a group, this state is only reachable by choosing it.

### `report_edit` is a permission of its own, and 编辑员 a role that holds only it

Not folded into `run_batch`. Running a workflow spends quota and produces a machine record of what
the workflow said; writing a report publishes a person's words under the portal's name. In practice
the analyst who writes the commentary is often not the person trusted to spend run quota, and one
permission makes that distinction unexpressible. `admin` holds it too.

Deleting is scoped to hand-written reports for the same reason editing is: removing the record of a
run is a storage decision, and it already has a console and a retention policy.

## Consequences

**A workflow that deliberately names the manual version now fails.** No shipped producer does — the
version is new — but a portal that had adopted the name for its own purposes would have to rename it
first. The refusal is explicit rather than a silent redirect, so it is discovered at the first
attempt rather than in a report nobody can explain.

**Two hand-written forms of one report cannot exist.** They would share `(symbol, rdate, rtype)` and
the switcher shows the newest per version, so the second would silently hide the first. The editor
redirects to the existing one instead of creating it, and the identity index refuses it if the
redirect is raced.

**A thematic hand-written report inherits an existing quirk.** `VersionsOfReport` groups on
`(symbol, rdate, rtype)`, and a thematic report has no code, so all thematic reports of one date and
subtype group together and the switcher shows the newest per version. This predates this ADR and is
not made worse by it, but hand-written reports are likely to be thematic more often than generated
ones, so it will be seen more.

**An editor can read what they can write.** Every write path loads through `GetNew(id,
viewerScope(user))`, so an author may only start from a report they could already read. An editor
who is themselves scoped is a coherent, if unusual, configuration.

**Editing has no history yet.** A save replaces the body. The next release adds a bounded revision
log with a configurable retention, riding the storage cleanup console's existing category shape; the
audit log already records who created and edited what, and when.

## Not decided here

**Whether a hand-written report can carry tracking items.** The table supports it (`SetTracking` is
keyed by report id) and the review queue would pick them up. Left out because nobody has asked for
it and the editor is already the largest form in the product.

**Whether an author should be shown on the report.** `reports` has no author column and the audit
log answers "who wrote this" today. The version label already tells a reader the thing that changes
how they read it — that a person wrote it, not a workflow.

**Whether editing should be restricted to one's own reports or one's own OU.** Currently anyone
holding `report_edit` may edit any hand-written report they can read. A portal with several editors
who should not touch each other's work would want the narrower rule; nobody has that shape yet, and
the audit log makes the current rule accountable.
