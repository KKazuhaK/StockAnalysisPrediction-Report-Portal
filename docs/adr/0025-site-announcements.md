# ADR 0025 — Site announcements: rows with an order, a window and an audience

## Context

The portal could show exactly one announcement. It lived in five `meta` keys —
`announcement_enabled` / `_popup` / `_level` / `_title` / `_content` — was edited on one form at
Manage → 站点 → 公告, was published by `siteSettingsJSON`, and was rendered by one component
mounted on the home page alone. That shape has four limits an operator hits in order:

- **One at a time.** A maintenance window and a data-source incident cannot both be up, so the
  second one overwrites the first and the first is simply lost.
- **No order.** With one message there is nothing to order; with several, which one a reader sees
  first is a decision somebody has to be able to make.
- **One popup switch for the whole feature.** "Interrupt people about this" is a property of a
  message, not of the portal.
- **Home only, everyone always.** A notice about the run queue is most useful to somebody looking
  at the run queue, and a notice about one client's data feed is of no use to anybody else.

There is also a disclosure the redesign has to deal with rather than inherit. `GET /api/site` is
registered with **no auth wrapper** (`server.go`) because the login page reads it for the brand
title and logo before anyone has signed in — and the SPA polls it every 60 seconds, including on
`/login`. The announcement text rode along on that payload. With one message meant for everybody
that was untidy; with a message that can be addressed to one OU it is the disclosure itself,
because *who is being told what* is exactly what a targeted announcement reveals.

## Decision

### An announcement is a row, not a setting

`announcements(id, level, title, content, ord, enabled, popup, dismissible, scope, audience,
starts_at, ends_at, created_at, created_by, updated_at)`, plus a side table
`announcement_grants(announcement_id, principal)`. Both are declared in `baseSchemaStmts()` and
arrive on existing databases through `createBaseTables` / `ensureColumns` — additive only, no
hand-written migration, per the project's standing schema rule.

The whole column set is declared from the first release that has the table, including the columns
whose write paths open in later phases. A column costs nothing here, and declaring it late would
mean a second DDL change plus a second `ensureColumns` pass for no gain.

`ord` is a sort key and **nothing else**: identity is always `id`. That is what lets the reader key
"don't show again" on `(id, wording)` and therefore lets an operator drag rows around without
re-interrupting everybody who had already dismissed something.

### The reader feed leaves the public endpoint

`GET /api/announcements`, behind `requireUserJSON`, answered with `writeJSONIfChanged` so the
60-second poll settles into an empty-bodied 304. The five keys leave `siteSettingsJSON`, whose key
set is now pinned by a test — the way this regresses is not somebody re-adding
`announcementTitle`, it is somebody adding a sixth field that "obviously" belongs with the
branding.

The reader payload carries `id, level, title, content, popup, dismissible, scope, endsAt` and
deliberately **not** `audience`, `grants`, `ord` or `enabled`. Telling a reader that a targeted
announcement exists, even without naming its target, discloses the shape of the OU tree.

`endsAt` is the one piece of scheduling state the reader gets, and it is there for a specific
reason: `startVisiblePoll` stops asking in a hidden tab, so without it a tab left open behind
others would keep painting an incident banner for hours after it was scheduled to come down.

### Audience resolves as a union along the OU chain

Principals reuse the existing encoding — `g:<id>` for an OU, `u:<name>` for one account, built by
`groupPrincipal` / `userPrincipal` — so there is one spelling of "who" in this codebase, not two.

**Resolution is the union of every OU on the reader's chain, and this is the opposite of
`GrantedVersions`.** Both are right for what they model, and the difference must be stated plainly
because anyone reading the grant table will otherwise assume the wrong one:

| | version_grants (ADR 0024) | announcement_grants |
| --- | --- | --- |
| Models | a **right** | a **broadcast** |
| Resolution | nearest ancestor OU with rows wins | union of every OU on the chain |
| Why | a child OU with its own grants must not silently inherit the root's | a notice sent to a parent OU has to reach the subtree, and a child OU having its own must not suppress the company-wide one |

Three consequences are load-bearing:

- **Default-deny is written out**, not inferred. Not an empty `IN ()` (a syntax error on both
  drivers), and never "no filter, so no narrowing" — `ownerScope.where` returns *un-narrowed* for a
  nil scope, so copying that shape in this direction would be a full disclosure.
- **The Default OU is refused as an audience.** `groupChain` always appends it and every
  unassigned account resolves to it, so granting it matches everybody — including every external
  tenant beneath it. `audience='all'` already says that, out loud.
- **Principals are normalized on save.** A hand-typed `u:Alice` stored verbatim would never match
  the lower-cased principal the read path builds: an announcement nobody receives, with nothing
  anywhere to say why. Existence is checked with `UsernameTaken`, which folds case the same way
  `userPrincipal` does, rather than with an exact-match `GetUser`.

The audience is **not** resolved through `viewerScope` / `isRestricted`. Those cost ~19 queries for
a restricted user on an endpoint polled once a minute per visible tab — but the real reason is that
they return "unrestricted" for every admin and every internal account, so deriving an audience from
them would broadcast a targeted announcement to precisely the people it was not addressed to. This
is the ADR 0024 lesson (read permission derived from run permission) in a new place.

**Admins are not exempt from the audience filter**, which is the one deliberate departure from this
codebase's usual "an admin is never narrowed". An announcement is a message, not a permission: an
admin who is not in 华东 does not need 华东's outage popup on every page, and training them to
ignore the band costs more than the diagnostic value. Diagnosis lives where it belongs — the
management list shows every row with its audience unconditionally.

**Audience is mutable from outside this subsystem.** Moving an account between OUs changes what it
receives, and today the three paths that do it behave differently (the single-user path audits but
does not bump `session_rev`; the bulk `set_group`/`clear_group` path audits nothing; the SSO path
bumps `session_rev` and kills every session). No OU snapshot is stored on the announcement — the
audience is evaluated at read time, every time.

### Time bounds are UTC instants

`starts_at` / `ends_at` are RFC3339 **instants**, compared in UTC, rendered in the reader's own
clock like every other instant in the app (`lib/datetime.ts`). Storing the operator's civil string
would let the panel-timezone setting silently change what a stored row means.

This is the one feature here nobody asked for that is in the first release anyway. With one
announcement, forgetting to take an incident banner down is embarrassing. With several, it is a
screen of stale warnings, and a band that is stale is a band nobody reads — which forfeits the
whole feature. The list also flags an enabled, end-less announcement that has not been touched in
14 days, for the operator who will not set a window.

### PATCH for the inline switches; whole-set reorder

Two handler shapes differ from `links`, which is otherwise the template.

**The list's inline switches use `PATCH` of one field.** A whole-row `PUT` is what LinksPage does
and it is safe there, because a link row carries nothing but decoration. Here it would write back
the title, body and audience the browser loaded minutes ago — a disclosure change wearing the
costume of a convenience. For the same reason `Grants` is `*[]string` on the update input: omitted
means *leave the audience alone*, so no partial save can silently empty it.

**Reorder replaces the whole order and refuses an id set that is not exactly the current one**
(409). One check covers two failures: concurrent admins half-overwriting each other, and a page
whose GET failed sending "replace the entire order" from a list it never rendered — the v0.4.35
lesson, one row multiplied by N. It is modelled on `apiRunPresetReorder`, not on `apiLinkLayout`,
which discards `readJSON`'s error and every store error and answers `ok` regardless while the page
swallows it with `.catch(() => {})`.

`updated_at` is `RFC3339Nano` rather than the store's usual `RFC3339` because the editor sends it
back as an optimistic-concurrency token. At second precision two saves inside the same second carry
the same token, and the stale one wins by being second.

Reordering is **not** audited: it is presentation, `links` reordering is not audited either, and a
log line per drag buries the changes an operator actually searches for. Content and switch changes
record `AuditPolicyChange` under `target_type="announcement"`; audience changes record
`AuditGrantChange` with before/after, written after the change lands.

### Dismissal is keyed on (id, wording), in the browser, per reader

The old scheme was one localStorage key holding one FNV hash: it could express one announcement, it
was shared by everybody who used the browser, and `JSON.parse` of it throws.

The replacement is a per-username map from `id` to `{sig, at}`, where `sig` hashes only what the
announcement *says* — level, title, body. So fixing a typo re-notifies (the behaviour all three
locales already promised), while changing the audience, the window, the scope, or the order does
not disturb anyone. Deliberately not `updated_at`, which moves on all of those.

**The signature is computed in the browser, not sent by the server**, and that is what makes the
one-time migration exact rather than approximate: the legacy value came from this same function
over this same shape, so the stored dismissal matches by construction. A Go-side hash would have
had to reproduce `JSON.stringify` plus UTF-16 iteration byte for byte, and a near miss would have
re-fired the popup for every user on upgrade day — the exact thing the migration exists to prevent.

**Garbage collection is by age and count, never by "absent from the current payload".** That
shortcut is wrong in a way that only shows up in production: the feed filters by enabled, by window
and by audience, so "absent" has four meanings and only one is "deleted". An announcement switched
off for a day, one whose window closed overnight, or a reader whose OU moved and moved back, would
all have their dismissals erased and be re-interrupted the next morning.

### One popup per page load

The queue takes the first eligible announcement in the operator's order. Advancing to the next one
after it closes would make a reader dismiss two modals in one interaction. The cost is that popup
switches below the first are decorative for that load, so the management list labels them —
an ignored setting should be visible where it is set, not discovered by an operator wondering why
nothing appeared.

### Scope is two values, with three exclusions

`home` (the home page, today's behaviour, and the fallback for anything unrecognized) and `app`
(every page behind the login). There is deliberately no path-prefix third value: it would bypass
the exclusions below — a `/chat`-scoped row would render on the mobile chat page whose header is
`display:none` precisely because that surface is stripped to the thread and composer.

The band is suppressed on **mobile chat** (`chatFocus`) and on **the admin console**
(`/manage/*`) whatever a row says. The console is full-bleed with a rail positioned off the header
height, so a band above it adds a scrollbar with nothing to scroll and pushes the rail's footer
off-screen. `AppLayout` has carried a comment saying the console suppresses this banner since
before anything actually did; now it does.

## Consequences

- **Rollback is clean but not lossless.** The five `meta` keys stay on disk — inert, like
  `old_base`/`old_user`/`old_pass` — but the import sets `announcement_enabled=false`, so an
  operator who rolls back sees *no* announcement rather than a stale incident banner they cannot
  take down from the old form. Announcements created after the upgrade are invisible to the old
  binary, and edits made from the old form during a rollback are not re-imported on the way back.
- **`POST /api/admin/settings` keeps its five announcement pointers for one release line.** Nothing
  sends them; they exist so a mid-upgrade rollback still has a working settings save. Delete them
  and their three error codes at the next line.
- **The v0.4 adoption step lives in `upgrade_v04.go`** — one file named for the release line it
  belongs to, deleted whole at the v0.5 boundary along with its single call in `Store.init`, after
  which `requireSchemaBaseline` refuses databases that never ran a v0.4 release. Two details there
  are not incidental: the marker `INSERT` **is** the guard, with no read in front of it (a read plus
  a write is two gates that can disagree, and on a rolling restart against one shared Postgres both
  instances would import); and every `meta` read happens before `Begin`, because SQLite runs on a
  one-connection pool and a query issued inside the transaction would wait on the connection the
  transaction holds — a hang at boot, not an error.
- **Two pre-existing bugs had to be fixed to ship this**, because the feed is the first
  session-backed poll that runs on every page for every reader. `getIfChanged`'s tag map is
  module-level and keyed by URL alone, so on a shared machine the next reader's poll would send the
  previous reader's ETag and be answered 304 — "keep what you have", to a component that has
  nothing; the tags are now dropped whenever the signed-in identity changes. And `conditionalGet`
  threw without calling `noteSessionLost`, so a dead session would leave a targeted announcement on
  screen indefinitely.

## Not decided here

- **What `audience='all'` should mean in a portal with external tenants.** ADR 0022 tenants hang
  off the Default OU, so "every signed-in account" includes them, and there is no third value
  between that and hand-picked grants. This is a product call, and the phase that opens the
  audience write path is where it has to be made.
- **A publicly visible announcement layer** (a maintenance notice on the login page). The data is
  already public today, so making it an explicit switch sounds like a bargain — but it would make
  the widest audience value reach the whole internet, turning every audience bug into an internet
  disclosure. If it is wanted, it belongs on `/api/site` as a separate, explicitly-named
  `loginNotice` scalar, not as a row in this table.
- **Server-side read receipts.** Cross-device dismissal and "how many people saw the maintenance
  notice" are real, but whether to record who read what is a product and compliance decision, and
  it should follow the dismissal semantics settling in practice.
