# ADR 0031 — A source declares what it can do; intraday; a third vendor; prices on the home cards

**Status: Accepted.** Builds on [ADR 0028](0028-live-quotes.md) (how a quote is fetched, gated and
rendered) and [ADR 0030](0030-quotes-app-and-multi-market.md) (where the surface lives, and the four
markets). Neither is reversed. What changes is that "which source can answer this" stops being a
property of the market table and becomes a declaration each source makes about itself — and three
features that were impossible to build honestly until it did.

## Context

Three asks arrived together from the running deployment:

1. the home cards should show a live price, not only the reading page;
2. the chart is too coarse — there should be a finer window, "某天的更细精度";
3. more sources, including overseas ones, not only mainland China.

They look independent and are not. Each one asks the same question — *which vendor answers this
request?* — and v0.4.46 had no place to put the answer. Capability was implied by a per-market table
(`quoteMarket.history`, `quoteMarket.sina`) that the resolver read as though it were a statement
about vendors. It is not: it is a statement about **this portal's current sourcing** of a market.

That conflation was not theoretical. It shipped, and it is the defect this ADR is mostly about.

## Decisions

### 1. Capability is DECLARED by the source, as a list of grants — not derived from the market table

```go
type quoteCapability struct {
    markets   func(*quoteMarket) bool  // nil means every market
    intervals []quoteInterval          // EMPTY GRANTS NOTHING
}
```

A source carries a **list** of these and serves a request when any single grant covers it. The list
is the load-bearing part. Tencent's real capability is *"every market's price, and every market's
daily series except two"* — and no single (markets × intervals) rectangle says that. Flattening it
into one would either hand Tencent a US series it answers with two rows fifteen years apart, or take
away the US **price** it answers correctly.

Two details that are easy to get backwards and are written into the type:

- **`markets` is a predicate, not a list of ids**, wherever the fact it answers already lives in the
  market table. Sina's grant is literally the `sina` column that `quoteSinaTarget` itself refuses on,
  so the declaration cannot drift away from the parser that actually decides. Where the fact is about
  the *source* rather than the market — Tencent's daily grant — the ids are named here instead, and
  the reason is written beside them.
- **An empty `intervals` grants nothing.** This is why a source declares grants rather than two
  independent sets: a market list with no intervals beside it reads as "everything" and means
  "nothing", and that is exactly the failure a reviewer's stub walked into while this was being
  tested.

`serves(target, interval)` is `covers` plus one more question — see §2.

**The bug this fixes.** `quoteMarket.history == false` meant "Tencent has no usable US daily series",
and the range resolver read it as "the US has no daily interval". So `range=6m` on a US symbol
resolved to `interval=snapshot` **before any source was consulted**, and the chain for `(us, daily)`
— the one Yahoo exists to fill — was never asked. Enabling Yahoo produced no US chart at all, while
the admin panel's capability column cheerfully advertised one. A range now says what the **reader**
asked for; `servableInterval` asks the enabled sources what this **deployment** can actually serve,
and degrades only when the answer is nobody. Pinned by
`TestQuoteDailyRangeAsksTheResolverAndNotTheMarketTable` and, end to end,
`TestYahooUSChartAppearsTheMomentAnOperatorEnablesTheSource`.

### 2. A grant is about markets; `knows` is about codes

`quoteSource.knows func(quoteTarget) bool` answers the half a grant cannot: whether this source has a
**spelling for this code**.

Yahoo's Hong Kong problem is not about the market. `canonicalCode` accepts any five digits there, and
only the ones beginning with the zero Yahoo drops have a measured symbol. Without `knows`, `hk 80737`
resolved to a source whose fetcher refused it *before any HTTP request* — and `load` then recorded
that refusal, a fact about **our own** code, against the vendor's health counters, and answered 503
for a gap no retry closes. `sourcesFor` exists precisely so a skip cannot reach the counters; a skip
it cannot see is a skip it cannot keep out of them.

It is the **fetcher's own symbol function** that answers, never a second rule written beside it, so
the resolver and the fetcher cannot disagree.

### 3. Intervals: `1d` and `5d` are different answers, not shorter ranges

`snapshot`, `daily`, `intraday` (1-minute), `intraday5D` (5-minute). Measured endpoints:
`web.ifzq.gtimg.cn/appstock/app/minute/query` answers ~267 minute points for an A-share session, and
Yahoo answers 391 for `AAPL?range=1d&interval=1m` and 331 for `600519.SS?range=5d&interval=5m`.

Three consequences, each of which was a defect before it was a rule:

- **The interval is part of the cache key.** 400 one-minute points and 400 daily bars are the same
  market, the same code and the same bar count. Without the interval in the key they share a slot,
  and whichever of 分时 and 1年 was fetched first is drawn under **both** labels until it expires.
- **The interval is asked FIRST when choosing a TTL.** A one-minute series wants a one-minute cache
  whatever the session says. Asking the session first hands a 分时 chart the 30-second open-market
  TTL, and — worse — the five-minute closed one at 09:31 while the vendor still reports the previous
  session as closed. `quote_ttl_intraday_secs`, default 60, floor 15, clamped on save **and** on
  read, like the other two.
- **The chart is TOLD its interval and never sniffs it.** A range switch deliberately keeps the
  previous window's bars up for the whole round trip, so between the click on 分时 and the answer the
  chart holds daily bars while `range` already reads `1d`. Deriving the interval from `range` there
  draws three months of candles as a minute line under a clock axis.

### 4. Yahoo Finance is compiled in, listed in the panel, and OFF

`optIn: true` keeps it out of `quoteShippedOrder`. An unconfigured portal never calls it and nothing
about v0.4.46's behaviour changes.

**Why it ships that way is not caution about the parser.** The decision is not the build's to make.
`query1.finance.yahoo.com/v8/finance/chart` is undocumented and unlicensed: no contract, no
changelog, no terms this repository can point an operator at, and no availability commitment. Whether
a deployment depends on it is an operator's call about their own deployment. The repo has the
precedent — ADR 0017's cleanup targets and the GeoIP refresh loop both ship compiled-in and switched
off. An operator turns it on by adding `yahoo` to `quote_source_order`; there is no second flag.

What it buys is the hole ADR 0030 §4 spends a paragraph on: `AAPL?range=6mo&interval=1d` answers with
**128 daily bars** where Tencent answers a sixty-bar request with **two rows fifteen years apart**.

**Its gate is not the same five checks**, because the response is not the same shape — nothing here
is positional, so an inserted column cannot rename a field, but the parallel arrays can disagree in
length with each other and with `timestamp[]`, and any element can be null. Three traps, all measured
against committed fixtures:

1. **`meta.chartPreviousClose` is the close before the REQUESTED RANGE**, not yesterday's close. Same
   AAPL, same afternoon: 328.21 at `range=1d`, 319.70 at `5d`, **262.52 at `6mo`**. Using it as
   prevClose on the six-month chart renders Apple at **+21.9%** on a day it fell **2.51%**. It is
   therefore *not a field of* `yahooChartMeta` at all; prevClose is derived from the series.
2. **`00700.HK` answers HTTP 200 with a well-formed body describing a different instrument** — a
   mutual fund on "YHD", `currency: null`, a June 2019 timestamp. Not an error: a wrong answer. Hong
   Kong on Yahoo is four digits (`0700.HK`) where this portal's canonical code is five. Three
   independent layers stop it — the symbol function that never asks the wrong question, the
   symbol-echo check, and the currency check — and it is the single case that argues for putting
   every new source behind a gate rather than trusting a 200.
3. **Prices are JSON floats** — `328.30999755859375` is how the wire spells 328.31. Every number is
   decoded as `json.Number`, keeping the vendor's own literal, and parsed by `quoteFen`. No float64
   touches a price, per ADR 0028 §10.

ADR 0028's **check 3** — the vendor's own percentage against its own two prices — still applies and
is run, but only because the prevClose it needs is derived. It is what makes trap 1 impossible to
reintroduce at runtime rather than merely covered by a test.

One consequence worth naming rather than discovering: on a US daily range with Yahoo enabled the
**whole response including the price** comes from Yahoo, because the chain for `(us, daily)` is Yahoo
alone. Yahoo's absence of a snapshot grant governs snapshot-*interval* requests only.

### 5. The home cards get prices from ONE batch call, and the feed never waits on it

`GET /api/quotes?symbols=a,b,c` — cookie session, same gate, same cache as the single endpoint, so a
symbol warmed by a card is warm for the reading page. `qt.gtimg.cn/q=sh605003,sz300150,…` returns one
line per symbol in a single request, so a whole page of cards is **one** upstream call.

- **Capped at 50 and REFUSED beyond it**, not truncated: a truncated batch is a tail of cards whose
  prices never arrive and nothing on the page can tell that from a slow vendor. The client asks for
  the first 50 rather than sending 60 and losing every price on the page.
- **The hook is deliberately incapable of reporting a failure.** A dead vendor, a refused request, a
  switched-off feature and a body this build cannot read all converge on the same observable state as
  "not arrived yet" — an empty map, rendered as no price. There is no honest error to render: a
  banner about a third-party feed over somebody's report list is noise about a decoration.
- **The card's price line is reserved at render time**, 22px, from the report list alone. antd
  stretches a row's cards to the tallest, so a card that grew when its quote landed would re-flow the
  grid a second after the reader started reading. The empty line is the price of never paying it
  while the feature works.
- **`home_quotes`, default on** (the user asked for it), reader and writer in the same change. It
  exists because switching it on sends the codes currently on screen to a third-party vendor on
  **every home page view** — small and read-only, but an outward disclosure, which is why it is a
  switch and not only a default. Off means the endpoint sends nothing to a vendor at all, checked
  before anything is parsed.

### 6. Two dedupes, and they are not the same guard twice

The handler dedupes the symbols it resolved; `fetchBatchUnder` dedupes again. Both are load-bearing
and for different callers, which was worth establishing by mutation because the comment originally
credited the wrong one with the wrong job:

- the **cache's** is the vendor's: it keeps a repeat out of the vendor's URL no matter who assembled
  the list — a promise about every future caller, not only today's handler;
- the **handler's** shapes its own `missing` list, which is built by walking the targets it
  assembled and never reaches the cache. Without it, a code on three cards that nothing answers for
  is named three times in a list the page renders.

### 7. What the admin panel now shows, and one thing it deliberately does not

Per source: the markets it serves a **daily** series for and the markets it serves an **intraday**
one for, as separate columns — because with a US-daily source available the choice an operator is
making is no longer "which vendor", it is "which vendor for *what*". Empty is a real answer and is
rendered as one, not as a missing value.

The panel's honesty is now a test rather than a promise:
`TestQuotePanelAdvertisesNothingTheResolverCannotBeAskedFor` walks the panel's own columns and
requires that for every pair it advertises there is a range a reader can actually pick that resolves
to that source. That test exists because the panel *did* advertise a US chart that could not be
reached.

Still not editable: **URLs**, for ADR 0030 §5's reasons, unchanged.

## Consequences

**Reports are still A-share only.** The `stocks` table, the name fetch, ingest and search are
untouched. Multi-market and multi-source remain quote **viewing**.

**A gate-refused batch is now neither a success nor a failure.** A page whose only card is a
suspended or delisted code is a batch of one refused line; marking the vendor failed for it made the
consecutive-failure streak an operator reads climb on every page view while Tencent answered
perfectly. It is not a success either — every line failing the gate is what a drifted array shape
looks like, and resetting the streak there would erase a real outage in progress. The vendor half is
enforced where it can be: a body carrying no line for **any** symbol asked about is an error from the
parser, so a source answering rubbish all day still lands as a failure.

**A third vendor doubles the surface the drift gate has to cover, and it is a different shape.** The
positional checks that protect the Tencent parsers say nothing about parallel arrays that can
disagree in length. Yahoo's checks are written out in the order they run, at `parseYahooChart`, and
the fixtures pin its layout on the day they were captured and nothing after.

**The four `quoteInterval` values are now in the cache key, the TTL choice, the range table, the
source declarations and the chart's props.** Adding a fifth is five edits, and the type is exhaustive
in none of them — that is the honest cost of having made the interval a first-class thing rather than
an inference, and it is cheaper than the class of bug §3 lists.
