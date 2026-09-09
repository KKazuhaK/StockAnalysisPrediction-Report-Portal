# ADR 0033 — Per-user stock favorites

**Status: Proposed.** Builds on [ADR 0024](0024-report-versions.md),
[ADR 0030](0030-quotes-app-and-multi-market.md),
[ADR 0031](0031-quote-capabilities-intraday-and-home-cards.md), and
[ADR 0032](0032-session-aware-live-quote-refresh.md).

## Context

The portal is organized around reports. A reader can search for a stock, open its newest report,
and return to the home feed later, but the portal does not remember which instruments that reader
wants to follow. The home feed therefore answers only "what was published recently," not "what do I
care about?"

This gap becomes more visible now that quotes cover Shanghai, Shenzhen, Beijing, Hong Kong, and US
instruments. A reader may want to keep an instrument even when it has no report in the portal. A
report-only filter cannot represent that case, and a server-side background watchlist would violate
the demand-scoped quote model in ADR 0032.

The first version needs a durable personal list, a small way to add or remove an instrument from the
places where it is already visible, and one content-level place to browse that list. It does not
need to become a notification or portfolio system.

## Decisions

### 1. A favorite belongs to one signed-in account

Favorites are private preferences. Every active signed-in user may add, remove, list, and reorder
their own favorites. The API obtains the owner from the authenticated session and never accepts a
username in a request body or path.

Administrators do not gain a cross-user favorites browser. Personal preference changes are not
written to the administrative audit log. Account deletion removes the account's favorites in the
same transaction that already sweeps the other username-owned rows, so a later account created with
the same username cannot inherit the previous person's list.

Restricted users follow the same rule. Quote access is already available to every signed-in user,
so an instrument does not need an accessible report before it may be favorited. Any report summary
joined onto a favorite still passes through that reader's existing report scope; favorites must not
become a directory of reports the reader cannot open.

### 2. Identity is the existing canonical market and code pair

The durable identity is `(market, symbol)`, validated and canonicalized through `quoteTargetFor`:

| Input | Stored market | Stored symbol | Quote request identity |
| --- | --- | --- | --- |
| `600519` | `sh` | `600519` | `sh600519` |
| `00700` | `hk` | `00700` | `hk00700` |
| `aapl` | `us` | `AAPL` | `usAAPL` |

Market is stored separately because a six-digit code is not globally unique: `000001` can name a
Shenzhen stock or a Shanghai index. US symbols are stored uppercase, and fixed-width numeric codes
retain their leading zeros. The API always returns the canonical form.

The row does not copy a name, price, currency, exchange time zone, report count, or latest report.
Those values already have owners: live identity and price come from the quote response, while report
metadata comes from the scoped report store. Avoiding snapshots means a renamed company does not
leave a stale favorite label and quote corrections never become database writes.

The quote resolver cannot determine whether a valid instrument is a stock or an index without a
vendor response. Addition therefore accepts any identity the existing resolver can validate. Making
the write depend on a live vendor would prevent a reader from saving a stock during an outage and
would make the same request alternate between accepted and refused.

### 3. The proposed schema is one additive side table

Subject to the repository's schema approval gate, the table is:

```sql
CREATE TABLE IF NOT EXISTS user_stock_favorites(
    username TEXT NOT NULL,
    market TEXT NOT NULL,
    symbol TEXT NOT NULL,
    ord INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    PRIMARY KEY(username, market, symbol)
)
```

`username` matches `users.username`, the account key used by the rest of the store. The application
performs lifecycle cleanup through `Store.DeleteUser`, consistent with existing username-owned
tables. A database foreign key is not introduced into one isolated side table while the surrounding
schema uses explicit transactional sweeps.

The composite primary key makes adding the same instrument idempotent and avoids a surrogate id
that has no product meaning. `ord` is a user-controlled display position. `created_at` is an RFC 3339
UTC instant used as the stable fallback when old rows share an order value.

There is no secondary index in the first version. A personal list is capped at 500 entries and read
as one bounded set; sorting that set is cheaper than charging an index on every add, remove, and
reorder. The cap also bounds reorder payloads and prevents one authenticated account from growing
an unbounded preference table.

This is a database shape change. Implementation does not begin until the table layout is explicitly
approved. Once approved, the table is added to the existing base-schema creation path. It needs no
feature setting in `config.yaml` and no separate migration file.

### 4. The browser API is idempotent and session-owned

The cookie-authenticated SPA API gains:

| Method and path | Meaning |
| --- | --- |
| `GET /api/favorites` | Return the caller's ordered canonical identities and bounded display metadata |
| `PUT /api/favorites/{market}/{symbol}` | Add the canonical identity; an existing row is success |
| `DELETE /api/favorites/{market}/{symbol}` | Remove it; an absent row is success |
| `PUT /api/favorites/order` | Replace the order of the caller's current set |

All routes sit behind `requireUserJSON`. Market and symbol use the same validation as quote
requests. A malformed or unsupported identity is rejected before touching the store.

The list response carries `market`, canonical `symbol`, quote request identity, order, and any
reader-visible latest-report summary needed to choose a destination. It does not inline live quote
data. The page asks the existing batch quote endpoint once for the favorites visible on the current
page, preserving the cache, single-flight, source failover, and refresh advice already implemented.

Reorder submits every canonical identity currently shown by the client. The server performs one
transaction, rejects duplicates, and returns `409` if the submitted set differs from the stored set.
This prevents a stale tab from resurrecting a removed favorite or dropping one added elsewhere. If
two tabs reorder the same unchanged set, the last completed reorder wins; there is no collaborative
editing requirement for a private preference list.

### 5. Favorites are a home content mode, not another global navigation item

The home content area gains a compact **Reports / Favorites** mode control near its existing filters.
It does not add another button to the global header. Switching modes changes the content grid and
keeps the header density unchanged.

The favorites mode is instrument-first rather than report-first. It lists every saved identity,
including Hong Kong or US instruments and A-shares with no accessible report. Its cards use the
existing visual hierarchy: current name when available, canonical symbol, quote line, and a compact
report summary when the reader can access one. It does not add report-category tags or duplicate
the report card's full metadata.

Selecting an A-share favorite with an accessible report opens the stock reading page at its newest
visible report. Selecting an instrument with no visible report opens the Quotes app with the
canonical explicit identity. A quote outage leaves the symbol usable and does not remove the
favorite.

The list is paginated in the browser. Only the current page of instruments is sent to
`GET /api/quotes`, staying under that endpoint's existing 50-symbol cap. Drag reorder is available in
favorites mode and persists only after drop.

### 6. Add and remove controls reuse existing surfaces

A single star icon represents membership:

- the stock reading page places it beside the quote identity;
- the Quotes app places it beside the resolved instrument identity, making non-report markets
  first-class;
- a home report card exposes the icon beside its symbol on hover or keyboard focus, while touch
  layouts keep an accessible compact target.

The report card remains one large navigation target. Its star is a real button that stops the card's
navigation event, has an accessible add/remove label, and exposes keyboard focus. Filled and outline
states are not distinguished by color alone.

Adding or removing updates the visible control optimistically and rolls back on failure. It sends
one idempotent request and does not refetch the quote. All surfaces use one shared favorites state
contract so a filled star cannot mean something different on the home, stock, and Quotes pages.

### 7. Quote refresh remains visible and demand-scoped

Opening favorites mode performs one batch quote request for the visible page. ADR 0032's top-level
refresh advice schedules later requests while that page remains visible. Hidden tabs pause and catch
up once when visible again; an observed market close sleeps until the next candidate session.

The server does not iterate over stored favorites, schedule jobs for them, warm them after login, or
persist their prices. A list containing 500 favorites costs no quote traffic beyond the page the
reader currently sees. Closing the page ends the demand.

### 8. Alerts, grouping, and portfolio semantics are deferred

The first version has one ordered list. It does not include:

- price or percentage alerts;
- notifications for newly ingested reports;
- named folders, tags, or multiple watchlists;
- holdings, cost basis, quantity, profit and loss, or transaction history;
- sharing, team lists, or administrator-managed defaults;
- background quote collection or durable close prices.

Those features require separate delivery, permission, and data-retention decisions. The proposed
identity table can be referenced by them later without putting their state into a favorite row now.

## Verification contract

Implementation is accepted only when tests demonstrate all of the following:

1. favorites are isolated by username and every mutation ignores caller-supplied ownership;
2. add and remove are idempotent, canonicalize symbols, and reject invalid markets or codes;
3. duplicate identities cannot be stored and the 500-entry bound is enforced transactionally;
4. deleting a user removes every favorite before the same username can be reused;
5. reorder is atomic and refuses a stale, duplicate, missing, or foreign set;
6. SQLite and Postgres execute the same store behavior;
7. report metadata in the list follows the caller's existing owner/version scope;
8. the home favorites mode requests quotes only for its visible page and uses one batch request;
9. hidden-tab and closed-market behavior remains the ADR 0032 behavior;
10. each star is keyboard accessible, does not navigate its surrounding card, and rolls back a
    failed optimistic mutation; and
11. an instrument with no report or no current quote remains listed and opens the Quotes app.

## Consequences

Readers gain a stable personal watchlist without turning the portal into a market-data collector.
The home page can answer both recency and personal relevance while the global header remains compact.
Multi-market identity is correct from the first stored row, so future report coverage does not force
a data rewrite.

The feature adds one user-owned table and one cleanup responsibility to account deletion. It also
adds a bounded request on the surfaces that display membership. Quote load remains controlled by the
existing batch cap, cache, visibility rule, and server refresh advice.

The first release deliberately provides no alerting. A favorite records interest, not a promise that
the portal will observe a market event while nobody is looking.
