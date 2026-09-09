# ADR 0028 — Live A-share quotes on the reading page (shown, never stored)

> **Amended by [ADR 0030](0030-quotes-app-and-multi-market.md).** The title above is no longer
> accurate on either count: quotes now cover Hong Kong, the US and indices as well, and the
> full-size chart lives in its own app rather than on the reading page. Everything below about
> HOW a quote is fetched, gated and rendered still holds — the drift gate, integer 分, the
> unadjusted basis, and the vendor percentage. [ADR 0032](0032-session-aware-live-quote-refresh.md)
> further amends the absence-of-a-poller decision: there is still no server-side whole-market loop,
> but a visible quote surface may repeat its own demand on session-aware advice from the server.

## Context

A report is read on `web/src/pages/StockPage.tsx`, which knows a six-digit code, a date and a body of
markdown, and can say nothing whatever about what that code is doing right now. A reader opening a
two-week-old report cannot tell from the portal whether its thesis has already played out. The ask is
a quote line and a daily K-line chart beside the report body.

Most of the plumbing is already here, and the piece that is not was built one stage earlier in this
same body of work. `vendorfetch.go` — new on this branch, extracted while hardening `names.go`, so a
reader arriving from `main` will not find it — is one pooled `http.Client` shared by every
Chinese market-data vendor this portal calls, carrying the per-call context deadline, the 2xx check,
the `io.ReadAll(io.LimitReader(...))` ceiling, the browser agent and the `Referer` that Sina answers
403 without. `names.go` already resolves a company name live from the Tencent and Sina realtime quote
endpoints, and `FetchAShareNames` already pages the whole A-share name table out of eastmoney.
`marketPrefix` (`internal/app/names.go:224`) already turns six digits into the `sh` / `sz` / `bj`
prefix those URLs are keyed by, and is already the URL-construction boundary that refuses anything
which is not exactly six ASCII digits.

What does not exist is a rule for what a *price* is allowed to be — and the rule the name path uses
is precisely the wrong one to inherit. `fetchNameWithRetry` (`internal/app/names.go:312`) walks an
ordered source list and returns **the first non-empty parse**, with no second opinion and no
cross-check. For a company name that is correct and cannot really fail dangerously: a wrong name is
visibly wrong to any reader who knows the stock, and it is corrected by the next fetch. Give a price
the same treatment and the failure mode inverts. `33.31` where `33.35` belongs is not visibly wrong
to anybody. It is a real number, in the right currency, for the right stock, on the wrong day, and no
reader will ever catch it.

Two further constraints shaped this more than any vendor detail. First, **the schema has never held a
number this portal calls a price** — `internal/app/store.go` declares 222 `TEXT`, 61 `INTEGER` and 24
`BIGINT` columns and not one `REAL`, `DOUBLE`, `FLOAT` or `NUMERIC`. Second, **there is no interval
scheduler here.** The shared cadence engine (`internal/app/cadence.go`) fires at most once per
matching civil day, and ADR 0018 explicitly defers an "every N hours" frequency as a later addition.
Five goroutines run for the process lifetime (`internal/app/server.go:145` and `:177`–`:180`; the
`:176` between them is `s.resumeBatchJobs()`, a synchronous call, not a loop): a 30s tick
that admits scheduled runs whose `run_at` has passed, two 60s ticks whose entire job is to ask
`cadenceDue` whether a civil day's schedule has come round, a sweep of expired auth rows, and the
GeoIP updater. That last one is worth stating precisely, because it is the closest thing here to an
interval poller and it is not one: `geoUpdater.autoLoop` (`internal/app/geo_update.go:501`, interval default `geoDefaultHours = 12` at `:48`) ticks
every minute, re-reads its interval each pass (default 12 hours, `geoDefaultHours`), and returns
immediately unless an admin has switched it on — it is off by default. Thirty days is MaxMind's
licence requirement, which is the motive for the feature, not a clock anything runs on. Nothing in
this tree polls anything on a market's clock, and no existing primitive would let it.

## Decisions

### 1. The durability split: a shown price and a stored price are different objects

This is the decision every other one falls out of, so it is stated first and in the strongest form.

**A price shown on screen may fail over between sources. A price written into a permanent record must
be corroborated and immutable.** The two have different failure economics. A displayed price is
disposable: it is stamped with its source and its vendor timestamp, it is replaced on the next load,
and a reader who doubts it can reload. A stored price is an assertion the portal will keep repeating
after everybody has forgotten where it came from — and if it is used to score a report's call, it
decides whether a piece of analysis is recorded as right or wrong. `fetchNameWithRetry`'s first-answer
policy is exactly right on one side of that line and catastrophic on the other.

So the split is drawn where the durability changes, not where the code is convenient:

- The chart and the quote line are a **memory-only cache with failover**. Tencent, then Sina; the
  serving source is named in the payload; the whole thing evaporates on restart.
- **This ADR persists nothing at all.** No table, no column, no migration. There is no writer, so
  there is nothing to be wrong forever.

Storing bars is not deferred out of laziness — it is deferred because a stored bar needs a
corroboration rule, an adjustment basis, a trading-calendar semantics and an immutability rule that
this feature does not need and cannot honestly design from one afternoon of probes. What the
storage-shaped decisions *do* get, below, is settled now while they are still free (§3, §10), because
those are the ones a later scoring feature cannot revisit cheaply. ADR 0029 sketches what such a
feature would have to be and is deferred; nothing in it is built, and nothing here presumes it will
be.

### 2. Tencent's `fqkline` endpoint is primary because it answers the whole question in one call

`https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=sh601899,day,,,60,bfq` returns, in a
single response: the daily OHLC series (`day`), the live snapshot (`qt`), and the exchange session
state (`qt.market[0]`, a `SH_close_未开盘` / `SZ_open_…` pipe-delimited line). It is keyed by exactly
the `sh` / `sz` / `bj` prefix `marketPrefix` already produces, and the adjustment basis is a segment
of the same URL (`bfq` unadjusted, `qfq` front-adjusted), so switching it later is a constant, not a
second integration.

That "one call" property is worth more than it sounds. The alternative shape — a history endpoint plus
a separate snapshot endpoint plus something to infer whether the market is open — is three fetches
that can each fail differently and, worse, three answers that can disagree with each other about what
day it is. A page that renders a chart ending Thursday beside a last price from Friday's open is
wrong in a way no single response can be.

Sina is the fallback: `CN_MarketData.getKLineData` for history and `hq.sinajs.cn` (GBK, hence
`vendorGetGBK`) for the snapshot. Its prices agree with Tencent's exactly; its volume is in 股 where
Tencent's is in 手, a factor of 100 that the parser normalizes so the two sources cannot produce a
chart whose bars mean different things depending on which vendor answered.

**The fallback is not the primary with a different hostname, and the difference has to be stated
here rather than left to §4 and §5, whose promises a reader will otherwise take as covering both
sources.** Sina's snapshot line carries the day's prices and nothing derived from them: there is no
涨跌 column and no 涨跌幅 column anywhere in it. So when Sina answers, both of those numbers are
computed by this portal (`parseSinaSnapshot`, in `internal/app/quote.go`) — which is the one
act §4 forbids — and the arithmetic identity §5 calls the gate has nothing left to check there,
because the percentage it would test is one we derived a moment earlier from the two prices it
would test it against. Both sections carry the exception at the point where they make the promise.

**eastmoney was rejected as a history source**, and this is the rejection worth writing down, because
it is the vendor the existing batch name fetch already uses and therefore the obvious choice. Two
independent reasons:

- **Its history endpoint returned an empty body from the deployment network.** Not a 4xx, not a
  parse failure — nothing. A source that answers with silence from the machine that will actually run
  this is not a fallback; it is a dependency that fails open into "no data" on the day the primary
  breaks, which is the one day a fallback exists for.
- **It is keyed by a numeric `secid` (`1.601899`, `0.000001`), which `marketPrefix` does not
  produce.** Adopting it means a second, parallel code→market mapping in a codebase that already has
  one, and the two would be free to disagree. `marketPrefix` carries a hard-won invariant — it is
  where six-digit-ness is enforced, because a symbol with a control byte in it once reached
  `http.NewRequest` and took the process down on the nil request it returned. A second mapping is a
  second place to relearn that.

Note what this does *not* say: eastmoney stays the name-table source. It is fine there. The batch name
fetch tolerates a missing page (it retries, rotates hosts and stops), and a name that fails to arrive
is a blank label, not a wrong number.

### 3. We serve unadjusted (`bfq`) prices, and this is a storage decision made early

Front-adjusted (`qfq`) series are **re-based on every corporate action**. A bar for 2026-07-01 read in
July and the same bar read in December are different numbers if a dividend or a split fell in
between — not because the market changed, but because the series was rewritten backwards around the
new event. That is the correct behaviour for a chart whose only job is showing continuous returns, and
the wrong behaviour for anything that is meant to still mean the same thing later.

Nothing is stored today, so on the face of it this could not matter less. It is decided now anyway,
because **the adjustment basis is the one thing a later scoring feature (ADR 0029) cannot revisit
cheaply.** The day something stores a "price at the time of the report" and compares it against a
"price now", a mixed-basis history is unfixable after the fact: the stored numbers carry no record of
which corporate actions had happened when they were captured, so there is no function from the old
basis to the new one. Choosing `bfq` now costs a URL segment. Choosing it later costs the archive.

`QuoteResp.adjusted` is therefore serialized as a constant `false` rather than omitted. A field that
says "these are unadjusted" is a claim a consumer can check; the absence of a field is a claim nobody
made.

### 4. The percentage is the vendor's own string on the primary source, and ours on the fallback

`QuoteSnapshot.ChangePct` is a `string`, carried verbatim from the vendor and rendered verbatim with a
`%`. On the Tencent path — every request that is not a failover — it is not parsed, not reformatted,
and above all not computed from our own `last` and `prevClose`. Sina publishes no such string at all,
so on that path it is computed from exactly those two numbers; the exception is spelled out below,
in this section, because it is not a detail of the implementation.

The reason is ex-rights days. On the morning a stock goes ex-dividend or ex-rights, the exchange
restates the previous close to the adjusted reference price, and the vendor's percentage is computed
against *that*. Recompute it from the raw previous close and a stock that opened flat is reported to
the reader as down eight percent — a fake crash, on the reading page, next to somebody's research. The
vendor is authoritative here for the same reason it is authoritative about the trading date: it is
restating the exchange's own reference price, and we are not.

The sign shown to the reader is taken from `Change` (an integer, in 分), never from the string, so the
red-up/green-down colouring cannot be flipped by a vendor writing `+0.12` or `０.12` one day.

**The exception, and it is not a small one: on the Sina fallback path both of those numbers are
ours.** Sina's snapshot line has no 涨跌 column and no 涨跌幅 column, so `parseSinaSnapshot`
(`parseSinaSnapshot`) sets `Change` to `last - prevClose` and derives the percentage
from those same two numbers — the exact recomputation this section forbids, and it takes the sign
of the colouring with it. The sign is still taken from an integer rather than from a string, so the
last paragraph survives; the vendor-authority argument in the two before it does not. On this path
nobody is restating the exchange's reference price — the number is a division we performed — and
that is the state of every request served while Tencent is down.

So the worked example is not hypothetical; it is what the fallback does. Take a stock that closes
at 20.00 元 and goes ex-dividend the next morning on a 2.00 元 distribution. The exchange restates
the reference close to 18.00, the stock opens flat there, and Tencent's own percentage — the one
this portal passes through untouched — reads 0.00%. Now Tencent 5xx's that morning. Sina answers,
and the portal divides Sina's last by Sina's 昨收 column. If that column carries the raw 20.00, the
reading page renders **−10.00% in green** beside a stock that has not moved, next to somebody's
research. And the portal cannot tell whether it does: the line has one 昨收 field and nothing that
says which of the two closes it holds. Telling them apart is exactly the judgement this section
relies on the vendor to have already made, and there is no second opinion to fall back on, because
the second opinion is the source that just failed.

This is recorded rather than fixed, and the reason is that fixing it needs something this feature
does not have: a corporate-action calendar, or the discipline of showing no percentage at all
rather than a derived one. A warning badge on the Sina path is the smallest thing that would help
and it is not built. Until it is, `Source: "sina"` in the payload is the only marker anywhere that
the percentage beside the price is this portal's arithmetic rather than a vendor's.

This does not contradict the gate below, which on the Tencent path computes the percentage and
compares it against the vendor's. Verifying a
vendor's arithmetic and replacing it are different acts: the first can only refuse to serve, and the
second is the one that can invent a crash out of a corporate action.

### 5. The drift gate, and why a range check is not the gate

These parsers are **positional index reads over bodies with no schema and no changelog**. Tencent's
`qt` is a bare array of 88 strings for `sh`/`sz` and 87 for `bj`; a `day` row is six strings in the
order `[date, open, close, high, low, volume]` — close at index 2, *not* last. Nobody publishes these
layouts and nobody announces when they change. The realistic drift is not a vendor deleting a field or
switching to XML. It is **one inserted field**, shipped quietly, shifting every index after it by one.

The gate, in order, is: (1) length floors — `qt` ≥ 39, Sina snapshot ≥ 32, each `day` row ≥ 6; (2)
**echo** — `qt[2]` must equal the requested six digits exactly, compared as a field and not as a
substring of the body, since `param=` contains the code too; (3) the **arithmetic identity**
`|(last-prevClose)/prevClose*100 − vendorPct| ≤ 0.02`, skipped when `prevClose == 0`; (4) per-bar
sanity, `low ≤ min(open,close)`, `high ≥ max(open,close)`, all positive; (5) `low ≤ last ≤ high`,
skipped when `high` or `low` is zero.

Item 3 carries a precondition that belongs to the gate rather than to its implementation: every
price it multiplies is first bounded by `quotePriceCeiling` (10^12 分, `internal/app/quote.go`).
The identity folds its two factors into a single multiply by 10^6, and a vendor-controlled price
large enough to wrap that multiply in an `int64` lands the product on exactly zero — where it
agrees with a vendor `"0.00"` perfectly, and the one check the whole design rests on becomes a
check that accepts anything. A ceiling over a million times the most expensive share ever listed on
a Chinese exchange refuses nothing real, and it is what makes the multiply mean what it says.

**Item 3 is the gate on the Tencent path. The rest are its supporting cast.** A range check asks
each field, in isolation, whether it is a plausible number — and on an A-share snapshot that
question is nearly vacuous, because `last`, `prevClose`, `open`, `high` and `low` are five numbers
within a couple of percent of one another, and a vendor inserting a field inserts it *among the
fields it belongs with*. A shift that lands one of those five where another belongs produces a
number that is a real, current price for the right stock, inside every band anybody would think to
write, and wrong.

Work it on the captured body. `testdata/quote/tencent_fqkline_sh601899.json` reads `qt[3]="33.35"`
(last), `qt[4]="33.31"` (previous close), `qt[5]="33.85"` (open) and `qt[32]="0.12"` (the vendor's
percentage). Insert one field at index 3 — a vendor adding, say, an average price next to the last
price — and each of those reads slides by one: the portal now publishes 33.35 as the *previous* close
and 33.31 as the day's *open*. Both are genuine prices for this stock from the last two sessions.
Nothing about either is out of range. The quote line built from them is simply wrong about what
happened today, permanently and invisibly.

The identity catches it because it is not a check on a value; it is a check on a **relation between
three separately-positioned reads**. `last`, `prevClose` and the vendor's own percentage agree only
when all three landed on the fields the parser believes they did. After that insertion the percentage
slot reads `0.04` — which was the change in 元 — so the gate passes only if the newly inserted field
happens to be a price inside `[33.3567, 33.3700]`, a window about one 分 wide. Anything else fires it.

The range check *would* also reject this particular shift, because Tencent happens to place `high`
and `low` after the percentage, so the same slide drops a percentage into the high slot where it is
obvious — but it is never reached and a reader should not try to reproduce it. The identity runs
first (`quoteCheckIdentity`, called before any other field is read), and with the identity taken out
the body still dies before the range check — though not where an earlier draft of this document
claimed. Measured rather than reasoned: it dies in `quoteCheckSnapshotPrices`, because the same slide
moves the vendor's own timestamp `"20260904161458"` into the 涨跌 slot, where it reads as 2.03e15 分
and fails the price ceiling. The `qt[30]` timestamp read three lines further down is never reached at
all. (Line numbers are deliberately absent here: `internal/app/quote.go` is the file this ADR is
about, so every edit to it moves them, and a stale pointer into the middle of a parser is worse than
a function name a reader can grep for.) That coverage
is in any case a property of one vendor's field order, not a property of range checking: five
questions about five numbers in isolation can never notice that the numbers came from the wrong
offsets, and the accident disappears for any shift whose neighbours are also prices. The identity
notices by construction, which is the only kind of noticing worth relying on.

**On the Sina path item 3 is not the gate, because it is not run.** Sina publishes no percentage, so
the one shown for it is derived from `last` and `prevClose` a few lines further down (§4); asking
the identity whether that derivation agrees with `last` and `prevClose` is asking a division to
agree with itself, and it could only ever pass. `parseSinaSnapshot` therefore skips it and runs
`quoteCheckSnapshotOrder` in its place: the day's `low` at or below
both `open` and `last`, the `high` at or above both, and `prevClose` a positive number. Sina's line
runs name, open, prevClose, last, high, low in that order, so a single field inserted at index 1
slides `low` onto the old `high` and the ordering breaks — which is the same one-inserted-field case
the identity exists for, asked as a question about Sina's own columns.

**The second source therefore carries less protection than the first, and this document should be
read that way.** The identity is a relation between three separately-positioned reads that can hold
only if all three landed on the columns the parser believes they did. The ordering check is a
question about five numbers considered together, and any shift that leaves those five in a
plausible order satisfies it. It is a real check and it catches the realistic drift; it is not the
identity, and no sentence here should be read as saying that a drift breaking the identity is
caught on both sources. Two further asymmetries come from the same place:

- On a **suspended** stock the substitute is skipped on the same zero bounds as item 5 and for the
  same reason, so a halted stock served by Sina is guarded by items 1 and 2 and the price ceiling
  and nothing else. On Tencent the identity still runs, because it is skipped only when `prevClose`
  is zero: the captured `bj830799` fixture has `high`, `low` and `volume` all zero beside a
  `prevClose` of 34.28, so there item 3 still runs — and passes — while item 5 is the one that
  stands down.
- Sina's **history** is keyed JSON rather than a positional array, so an inserted column cannot
  shift it and items 1 and 2 have nothing to do on those rows; item 4 is the whole gate there.

Two more honesty notes about the gate:

- **The tolerance is 0.02 absolute and there is no band on the percentage itself.** A-share daily
  limits are ±5/10/20/30% by board and a first-day listing has no limit at all, so any band tight
  enough to be worth having rejects real limit-up days as a source failure — the portal would go dark
  precisely on the days a reader most wants it.
- **The gate protects the fields it can tie together, and no others.** `volume` (index 6) and
  `amount` (37) enter no identity, so a shift confined to the tail passes everything here. (37 is
  also the highest index the parser reads; `quoteTencentQtFields` is 39 rather than 38 because a
  floor sitting exactly on the last field read today would have to move the next time the parser
  reads one more.)
  Those are the fields where being wrong is embarrassing rather than dangerous, and that asymmetry is
  the reason the gate is built around the price relation rather than around field count.

**And the fixtures pin the layout on the day they were captured, and pin nothing afterwards.**
`internal/app/testdata/quote/` holds real bodies fetched from the live feed on 2026-09-06. Tests
assert the parser against those bytes, which is worth doing — it is what catches *us* breaking the
parser. It is not a vendor contract. Nothing in this repository will notice the day Tencent ships an
89th field; the fixtures will still pass, and the running portal will be caught by the identity check
or not at all. Anyone reading these green tests as "the vendor format is verified" has misread them.

### 6. What two vendors do and do not prove

Corroboration is the honest reason to carry Sina, and the word has to be kept on a short leash.

**It proves:** that a parser or a field position on one vendor has drifted. If Tencent and Sina report
the same last price and the same close for the same date, the odds that both bodies shifted a field on
the same day in the same direction are negligible. That is a real, valuable, cheap check and it is
the best argument for the second source existing at all.

**It does not prove any of the things the word invites you to assume.** Both vendors restate the same
exchange feed. So two agreeing sources say nothing about:

- **adjustment convention** — both can hand back front-adjusted numbers, agree with each other
  perfectly, and be a different series from the one stored last quarter;
- **halt state** — a suspended stock returns a stale last price from both, agreeing precisely, with no
  indication anywhere that no trade has occurred in a week;
- **trading-date semantics** — what "today" means after 15:00, whether an after-hours block trade is
  in, when the date rolls. Both restate the same upstream convention, so they cannot disagree about it,
  and their agreement is therefore not evidence about it.

Agreement between two mirrors of one feed is evidence about *our parsing*, not about *the world*. If
this portal ever stores a price, that distinction is where the corroboration rule has to start, and
"two sources agreed" will not be a sufficient answer.

### 7. Beijing Stock Exchange: snapshot yes, history refused, with the measurements

Codes on the `4`/`8`/`9` prefixes get a live quote and **no chart**, and the UI says so in words.

The measurements, from the captured fixtures and live probes: Tencent returns `"day": []` — a
syntactically valid response with an empty series — for `bj830799` while the same response's `qt`
snapshot works fine, carrying a live `34.28` at index 3 (`testdata/quote/tencent_fqkline_bj830799.json`,
`qt` length 87, `day` length 0). Sina's series for the same code exists, parses, and **stops at
2025-04-29** — sixteen months behind a live price of 34.28.

Sina is therefore not used for Beijing history at all, and that is the decision worth arguing.
Falling back to it looks strictly better than an empty chart: something renders, the axes have labels,
the page looks finished. It is worse. An empty chart with a sentence saying the market is unsupported
is a portal admitting a limit. A chart of sixteen-month-old bars beside a live price is a portal
lying, and a reader who scrolls past the axis labels — which is every reader — has no way to know.
**Stale data that looks like data is worse than no data**, and the response says which of the two it
is: `bars: []` with `barsUnavailable: "market_unsupported"`, distinct from `"source_failed"`, so the
reader is told whether the data does not exist or merely did not arrive. An empty array is never
rendered as a flat line.

The related discovery from the same fixture: a suspended stock returns `high`, `low` and `volume` all
zero. So gate item 5 skips when `high` or `low` is zero — otherwise every halted stock in the market
is reported as a source failure, which is a quote line saying "unavailable" about the one situation a
reader would most like explained.

### 8. No poller, no sixth loop: fetch-on-read with a TTL and a hand-rolled single-flight

The obvious design is a background loop that refreshes quotes on a timer. It is rejected on two
independent grounds.

**There is nothing to build it on.** The cadence engine both existing schedulers share fires *once per
civil day* at a wall-clock time; there is no every-N-minutes vocabulary anywhere, and ADR 0018 says in
so many words that an interval frequency is deferred until asked for. A quote poller would therefore
not be a sixth loop reusing a fifth primitive — it would be the first interval scheduler in the
codebase, arriving as a side effect of a reading-page feature, with no console, no audit trail and no
way for an operator to turn it off.

**And the arithmetic is absurd.** The name table this portal maintains is the whole A-share universe —
`FetchAShareNames` pages up to 80×100 codes and `ensureFull` rejects a result under 3000 — so call it
five thousand codes. Polling all of them once a minute is 5,000 × 60 × 24 ≈ **7.2 million writes a
day**, against a store whose SQLite driver is deliberately capped at a single open connection
(`internal/app/store.go:97`), to keep fresh a number for the handful of codes anyone actually opened.

Fetch-on-read gives the same freshness for none of that. A page view fetches; a short TTL serves the
next viewer of the same code from memory (and says so, via `cached`); a hand-rolled single-flight
collapses concurrent misses on one code into one vendor call, so a link shared into a group chat is
one fetch rather than fifty. The single-flight is a mutex and a map of waiters, in the same
spirit as the LRU `mermaid_pdf.go` hand-rolls out of `container/list` and a `sync.Mutex` (ADR 0020
decision 5). Not because `golang.org/x/sync/singleflight` is unavailable — it is resolvable today,
`go.mod` already carries `golang.org/x/sync v0.22.0` as an indirect dependency of something else —
but because promoting an indirect dependency to a direct one is still a dependency this repository
would then own and keep current, for about forty lines it can write itself. Cold start costs one
fetch, which is the cost of the fetch that was going to happen anyway.

### 9. It ships enabled, with no setting, against compile-time constant hosts

No admin toggle and no configurable source URLs.

**No toggle**, because a setting needs a reader *and* a writer, and half of one is a defect this repo
has already paid for: v0.4.45 spent a section on it
— "settings that were read and never written" — reconnecting six settings that had a reader, a
default and a comment describing a choice an operator could make, with nothing anywhere able to make
it. Its `internal/app/wired_settings_test.go` exists to stop it recurring. Adding a
`quotes_enabled` key read by the handler and written by no form would be that same defect,
committed knowingly. If quotes
should be switchable, they get a real switch on a real page with a real audit entry; until somebody
asks for that, they are simply on.

**No configurable hosts**, because an admin-editable source URL is an admin-supplied outbound target,
and that is precisely the surface `safefetch.go` exists for: the dial-time IP control hook, the
per-hop redirect re-check, the private-range policy, the whole DNS-rebinding apparatus built for SSO
(ADR 0023). Every host reached here is a compile-time constant, so that machinery would buy nothing —
and `vendorfetch.go`'s header comment already records why these calls deliberately do *not* go through
`newSafeClient`: it sends no request headers, and Sina answers 403 to a request with no `Referer`.
Making the hosts editable would drag in the SSRF apparatus and break the fetches at the same time.

Failures are reported through `jsonErrorCode` (`internal/app/apiui.go:50`) so the SPA translates them:
`quote_bad_symbol` (400) when the code is not six digits or carries an unknown market prefix — the
same judgement `marketPrefix` already makes — and `quote_unavailable` (503) when every source failed,
whether by transport, non-2xx, or the drift gate. A gate rejection is a source failure and is reported
as one. There is no partial mode in which a body that failed the identity check is rendered with a
warning.

### 10. Every price is an integer of 分, in Go and on the wire

`QuoteBar` and `QuoteSnapshot` carry `int64` 分 throughout; the SPA divides by 100 at the point of
display and nowhere else.

The direct reason is the schema's own precedent — 222 `TEXT`, 61 `INTEGER`, 24 `BIGINT`, zero
`REAL`/`DOUBLE`/`FLOAT`/`NUMERIC` — and this JSON is the seed of a record the portal may later
persist. The sharper reason is that this store is **dual-driver**, and `REAL` is not one type across
the two: SQLite's `REAL` is an 8-byte IEEE double, Postgres's is a 4-byte `float4`. A boundary
comparison — did the price reach the target, is this bar's close above that one — could therefore
resolve differently depending on which database the deployment runs, which is the worst class of bug
this codebase can produce: correct in the test suite, correct in staging, wrong in production, and
reproducible nowhere. Integers make the question not arise. `amount` is likewise an integer of 元,
with an empty vendor field reading as 0 rather than as a parse failure.

## Consequences

**When a vendor changes shape, the portal goes dark rather than wrong.** A drift that breaks the
identity fails Tencent and falls over to Sina — where the identity is not evaluated at all and the
weaker ordering check stands in for it (§5), so the two sources are not equally guarded against the
same class of drift — and if both fail their own gate the handler returns `quote_unavailable` and a
reading page whose quote line says 行情暂不可用 with a retry button. The report itself, the chart's
absence and every other surface are unaffected, because nothing else depends on this. That is the
trade taken deliberately: this feature is allowed to be unavailable and is not allowed to be
confidently wrong.

**When both sources are down, the reader sees an explicit failure, not an empty space.** The two
unavailable states are distinguishable in the payload and in the UI: `market_unsupported` for a
Beijing code, which is permanent and explained, and `source_failed`, which is transient and offers a
retry. A blank area where a chart belongs reads as a broken page; a sentence reads as a portal that
knows what it does not have.

**The fixtures will go stale and the tests will not say so.** They are captured bodies from
2026-09-06. They keep the parser honest and they will pass forever regardless of what the vendors do.
Re-capturing them is a manual act that nothing schedules and nothing reminds anybody about. This is
stated here so that a future reader does not mistake a green suite for a live contract.

**Nothing here is available to a non-browser consumer, and that is deliberate.** No `/api/v1` surface,
no webhook, no report body enrichment. A machine client asking this portal for a price would be asking
for exactly the durable, corroborated price §1 says we are not yet in a position to give.

**A quote is a per-view vendor fetch.** A page view that misses the cache adds one outbound call to the
page's latency and the process holds a small map of recent quotes. Both are bounded by the TTL and the
single-flight; neither is durable, and a restart loses all of it, which is the intended cost of
storing nothing.

**Finally, an existing quirk this feature sits next to and does not fix.** `stocks.updated_at`
(`internal/app/store.go:452`) is stamped by `SyncStocks` (`:1605`) on every name sync and is read by
**no `SELECT` anywhere in this tree**. The column appears in exactly two places in the whole
repository: the `CREATE TABLE` above and the `INSERT … ON CONFLICT` that writes it. The table itself
has six readers — three search queries `LEFT JOIN` it and filter on `s.name` (`:1202`, `:1237`,
`:1476`), `ListSymbols` (`:1646`) joins it to select the name outright, and `AllStockNames`
(`:1630`) and `FreezeReportNames` (`:1952`) read `code, name` and `s.name` — and not one of them
asks how old a name is, because nothing can. And nothing re-fetches the table either —
`Names.ensureFull` returns immediately the moment `data/names.json` exists on disk, so the bulk fetch
happens once in a deployment's life and never again. The name table is therefore effectively frozen
after its first successful bulk fetch, corrected only opportunistically, one code at a time, when an
ingest payload arrives with no name and `Names.Resolve` goes and asks a vendor.

Quotes make that visible rather than worse: `QuoteResp.Name` carries the vendor's *live* name from the
snapshot, so a renamed company can now show its current name in the quote line beside the older one
this database still holds. (A report's own frozen `name` column is a separate and deliberate thing —
earlier reports are meant to keep the name they were written under.) Fixing the table means either
reading `updated_at` to drive a refresh policy or dropping the column as the dead weight it currently
is. Both are name-table decisions, not quote decisions, and neither is made here.
