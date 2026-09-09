# ADR 0032 — Session-aware refresh for visible live quotes

**Status: Accepted.** Amends [ADR 0028](0028-live-quotes.md),
[ADR 0030](0030-quotes-app-and-multi-market.md), and
[ADR 0031](0031-quote-capabilities-intraday-and-home-cards.md). The server remains demand-driven and
keeps no durable market data. What changes is that a visible quote surface may repeat that demand
without a click while the market appears to be trading.

## Context

The current quote path has two useful protections but no refresh policy:

- a bounded, memory-only LRU avoids repeated vendor calls for the same answer;
- a single-flight collapses concurrent cache misses into one upstream request.

The SPA asks only on mount, on a range change, or on a manual retry. A home-page poll can replace the
report list every minute without asking for quotes again when the set of symbols is unchanged. A
reader who leaves a page open through a trading session therefore sees a price that looks live but
does not move.

Refreshing every quote continuously is not the answer. ADR 0028 rejected a server loop over the
whole instrument universe because almost all of that work would serve nobody. It also rejected a
new process-wide interval scheduler arriving as a side effect of a reading-page feature. Those
arguments still stand. The missing operation is narrower: repeat the existing fetch-on-read request
for the symbols a signed-in reader can currently see.

The desired lifecycle is:

1. while a market is trading, the visible page refreshes without requiring mouse, keyboard, or
   navigation activity;
2. after the market closes, the page keeps the last answer and stops high-frequency requests;
3. near the next regular opening, the page probes once and lets the vendor decide whether the market
   is actually open.

"Closed" cannot be derived safely from the portal host's wall clock alone. Tencent supplies a market
session field on the full quote endpoints, but the compact batch endpoint used by home cards does
not. Sina and Yahoo also return `unknown`. A holiday calendar maintained in this repository would
become stale, and an operating-system timer in a hidden browser tab is not a reliable background
scheduler. The policy must state these limits rather than hide them.

## Decisions

### 1. Refresh is demand-scoped to visible quote surfaces

The home cards, the report quote strip, and the Quotes app refresh only the symbols and range they
currently render. The home page continues to use one batch request for its visible card set. The
other two surfaces make one request for their one active instrument and range.

There is no server-side whole-market poller, no new process-lifetime loop, and no write path for
quotes. Every repeated request goes through the existing cache, single-flight, source order,
concurrency limit, and drift gates. Ten visible browsers requesting the same warm answer still cost
the vendor nothing; concurrent misses still collapse as they do today.

Page idleness is irrelevant. A visible tab refreshes even when the reader does not interact with it.
Changing the symbol, range, custom window, filter, or home-page pagination cancels the old schedule
and creates one for the new request key. A surface never overlaps two requests for the same key.

### 2. One operator switch controls automatic refresh, and existing TTLs set its pace

Manage > Quote Sources gains **Automatically refresh open markets**, enabled by default. Turning it
off preserves the current behavior: one load on arrival, loads caused by explicit view changes, and
manual retry.

The setting lives in the existing metadata settings mechanism. It adds no table, column, migration,
or `config.yaml` field.

There is no second set of interval controls. For an `open` response, the existing open-market TTL is
both the server freshness bound and the earliest next snapshot or daily-series refresh. The existing
intraday TTL serves the same role for intraday ranges. A response already served from cache exposes
the remaining lifetime, not the original full TTL, so the browser cannot retain it longer than the
server intended.

An `unknown` response uses a conservative two-minute probe interval while it is inside a candidate
regular session. Its server cache lifetime is capped to the same interval; otherwise a browser could
ask every two minutes and receive the same five-minute cached value. Two minutes is deliberately
slower than the normal open-market path because `unknown` includes failover sources that cannot
prove the market state. It is a policy constant rather than a fourth operator field.

The existing closed-market TTL remains the retention policy for a later page view. It does not cause
an already-open page to poll after that page has observed `close`; the page can retain its last
successful response after the server entry expires. This distinction keeps the cache bounded and
allows a fresh page load to revalidate a vendor without turning every open tab into a closed-market
poller.

### 3. The server returns refresh advice; clients do not reproduce market policy

The quote responses carry advisory scheduling metadata calculated from the same configuration and
market table the cache uses:

- `refreshAfterSecs` is present when a visible client should request the same key again;
- `refreshAt` is present when polling is paused and one future wake-up is appropriate;
- neither is present when automatic refresh is disabled.

The single-quote response carries the fields at its top level. The batch response carries one
top-level aggregate calculated as the earliest advice among all recognized targets. This keeps the
home hook on one timer and, crucially, gives an empty batch caused by a temporary vendor failure a
time to retry. One batch can contain markets in different time zones and states, but its next useful
action is still the earliest one. The batch remains `private, no-store`; its members have different
remaining cache lifetimes.

These fields are advice, not a second claim about market state. The vendor `session` and `asOf`
fields remain in the payload and remain the evidence displayed and tested. The client does not keep
its own exchange-hours table and does not read administrator-only settings to reconstruct a delay.

### 4. Open, close, and unknown have different state transitions

For `session=open`, the server returns `refreshAfterSecs` from the remaining applicable TTL. Each
successful refresh repeats this decision. No user interaction is required.

For `session=close`, the server omits the repeating delay and returns `refreshAt` for the next
candidate regular session boundary in the instrument's IANA time zone. The candidate sessions are
09:30–11:30 and 13:00–15:00 for A-shares in `Asia/Shanghai`, 09:30–12:00 and 13:00–16:00 in
`Asia/Hong_Kong`, and 09:30–16:00 in `America/New_York`. The IANA zone handles US daylight-saving
changes. These hours are used only to choose a future request, not to override a vendor that says
`open`.

The candidate schedule is a wake-up mechanism, not a trading calendar. At `refreshAt`, the client
makes one request. If the vendor still says `close`, as on a holiday or an exceptional closure, the
server returns the next candidate boundary and the page sleeps again. A close reported during the
first five minutes after a candidate opening gets one grace probe at the end of that window before
sleeping; this allows for a vendor that has not yet moved its session marker from the previous close
without issuing repeated probes throughout the grace period.

For `session=unknown`, the server uses two pieces of limited evidence:

- outside a candidate regular session, it returns the next `refreshAt` and does not poll;
- inside a candidate regular session, a vendor `asOf` inside the current session receives the
  two-minute `refreshAfterSecs`; a stale `asOf` receives the same opening grace probe and then the
  next `refreshAt`.

The home batch currently reports `unknown` for every card, so this is a main path rather than an
edge case. When at least one card in a market has a current `asOf`, the whole visible batch continues
on the aggregate delay. A page containing only suspended instruments may stop because their
timestamps do not move; their prices cannot move either. If a batch returns no cards during a
candidate session, its top-level advice uses the two-minute unknown retry rather than stopping
forever. Outside a candidate session it sleeps until the next boundary. This avoids polling through
an entire holiday while still recovering from a temporary batch outage.

### 5. Hidden tabs pause and catch up when they become visible

When `document.visibilityState` is not `visible`, client timers do not issue quote requests. The
schedule keeps its due time. Becoming visible causes one immediate refresh when the answer is due or
its state cannot be established, then resumes from the new server advice.

This is both a load rule and a correctness boundary. Browsers throttle and may suspend hidden-tab
timers, so the application cannot promise exact background cadence there. A reader returning to the
tab gets a current answer immediately instead of waiting for the next interval. An installed PWA in
the foreground follows the same rule.

### 6. Cache remains memory-only; durable close data is a separate feature

"Use the cache after close" means two things in this decision:

- an open page retains the last successful closed response without polling;
- later requests may reuse the bounded server LRU until its closed-market TTL expires.

It does not mean that the last close survives a server restart, deployment, LRU eviction, or an
arbitrarily long weekend. On a cold request the portal may call a vendor once, learn that the market
is closed, return that answer, and schedule no further high-frequency work.

Making close data durable would require a stored-price contract: schema ownership, source
corroboration, adjustment basis, exchange-calendar semantics, correction policy, and immutable
provenance. ADR 0028 and ADR 0029 deliberately keep that outside the live display path. This ADR
adds no database shape and makes no durability promise.

### 7. Refresh failures do not silently become current data

An initial failure keeps the current surface behavior. A background failure may leave the last
successful value on screen only if the surface continues to show its original vendor `asOf` and an
unambiguous stale or retry state. It must not replace the timestamp or mark the retained value as a
fresh response.

A single-quote `503` supplies `Retry-After`: the conservative unknown delay during a candidate
session, or the next candidate boundary outside one. A client-side network error follows the same
rule from the market and time zone in its last successful response. This prevents one transient
failure from silently disabling automatic refresh for the rest of the session.

Home cards remain decorative and do not gain a page-level outage banner. A failed batch does not
block or fail the report feed. The implementation must choose either to remove an unanswered card
price or retain it with a compact stale marker; it may not retain it while presenting it as current.

## Verification contract

Implementation is accepted only when tests demonstrate all of the following:

1. a visible, idle surface repeats an `open` request after the advised delay;
2. no two refreshes for one request key overlap;
3. home cards use one batch per refresh, deduplicate symbols, and never exceed the existing cap;
4. an observed `close` stops interval polling and produces one request at the next candidate wake;
5. a holiday-like close after that probe sleeps until the following candidate boundary;
6. `unknown` cannot be hidden behind the longer closed cache and follows the conservative path;
7. a hidden tab makes no quote request and becoming visible performs at most one catch-up request;
8. changing a symbol, range, custom window, filter, or page cancels the superseded schedule;
9. disabling automatic refresh preserves the current fetch-on-read behavior; and
10. every automatic request still passes through the existing cache and single-flight.

Tests use fake timers, fixed market-zone clocks, and fixture responses. They do not call a live
vendor and do not depend on the machine's local time zone.

## Consequences

A quote can now stay current for a reader who leaves the page open, without creating a crawler or a
server scheduler. Vendor traffic scales with visible demand and configured freshness, not with the
number of instruments known to the portal.

The regular-session table becomes operational code and must be covered for session boundaries,
lunch breaks, weekends, and New York daylight-saving transitions. It deliberately does not encode
holidays. Vendor state and vendor timestamps remain the authority; regular hours only decide when a
sleeping client should ask again.

Automatic refresh increases outbound traffic while readers keep quote surfaces visible. The
operator switch provides an immediate control, and the existing TTL settings remain the rate limit.
Source health counters, cache occupancy, and cache clearing continue to work as before.
