# ADR 0030 — The quote panel leaves the reading page, and stops being A-share only

**Status: Accepted.** Amends [ADR 0028](0028-live-quotes.md), whose title — *"Live A-share quotes on
the reading page"* — is wrong on both counts after this. 0028's decisions about *how* a quote is
fetched, gated and rendered all stand unchanged; what changes is where the surface lives, how many
markets it covers, and what an operator can see of it.

> **Amended by [ADR 0032](0032-session-aware-live-quote-refresh.md).** The quote-source panel gains
> an automatic-refresh switch. Existing TTLs remain the rate controls, and clients receive refresh
> advice rather than reading administrator-only configuration.

## Context

The quote panel shipped in v0.4.45 and the first feedback from the running deployment was blunt and
correct: opening a report gave you a chart filling the viewport with the report entirely below the
fold, hovering a candle appeared to do nothing, and — the sentence that reframed the whole thing —
*the report is the core, this is not a data-viewing page*.

Two of those were one defect. `PriceChart` rendered `viewBox="0 0 720 300"` with `height: auto`, so
its aspect ratio was fixed and **its height was a function of the window width**: 450px at 1080,
600px at 1440, **792px on the 1900px-wide screen the feedback came from**. The wider your monitor,
the more of the report it ate. And the hover readout was a strip *below* the SVG, so at 792px it was
off the bottom of the screen — the interaction worked and was invisible.

The third complaint had no defect behind it at all. Nothing was wrong with putting a price beside a
report; what was wrong was putting a *workstation* there.

## Decisions

### 1. Deep data viewing moves to its own built-in app; the reading page keeps a strip

`/apps/quotes` is a first-party app in the hub beside 批量执行 and 计划任务. Filling the screen with
a chart is the correct behaviour **there**, and that is what earns the reading page the right to stay
small. On a report the strip is always visible, the chart is collapsed on arrival, and the choice is
remembered per viewer — so someone who does want it every time pays one click, once.

This is the load-bearing trade. Without somewhere for the full-size chart to live, "make it smaller"
is just a worse version of the feature; with it, the reading page can be honest about what it is.

The app is ungated (`perm: ''`). It reads public market data and nothing about this portal, and every
signed-in user can already see a price on a reading page.

### 2. A chart's height is a number of pixels, never a ratio

The chart measures its container and draws in real pixels — `viewBox="0 0 <measuredPx> <heightPx>"`,
one SVG unit to one CSS pixel — so the height is exactly what the caller asked for at 320px wide and
at 1900px. The reading page asks for about 260; the app asks for a viewport-relative figure with a
floor.

Every geometry constant became a function of the measurement rather than of 720, and one of them was
already wrong before the change: the left gutter was a constant 48 units, which is 127 real pixels at
1900px and clips `1,234,567.89` at 900px. It is now sized from the widest label the chart actually
prints.

A measured width is zero on first paint, inside a collapsed container, and in jsdom. The chart
renders its skeleton until the measurement is positive and never divides by it.

### 3. The tooltip follows the pointer, because that is where the eye is

A crosshair on both axes, a price chip on the Y axis and a date chip on the X axis, and a floating
card carrying the bar's date, open, high, low, close, change, percentage and volume — flipping to the
other side of the cursor near the right edge, clamped vertically, and never capturing pointer events.
Keyboard selection shows the same card at the focused bar, and an aria-live readout survives for
screen readers.

One asymmetry worth writing down: a **historical bar's** change and percentage are computed here from
the previous bar's close, which directly contradicts ADR 0028's rule that the percentage is always
the vendor's own string. The rule is right and this is not an exception to it — there simply is no
vendor-supplied percentage *per bar*; the snapshot's percentage is a property of the snapshot. The
first bar in a window has no previous close and reports no change rather than inventing one.

### 4. Quote viewing covers four markets. Reports do not.

The same two Tencent endpoints serve Shanghai, Shenzhen, Beijing, Hong Kong, the US and indices. Each
difference below was measured against a committed fixture, and every one of them is a trap:

| | A-share | Beijing | Hong Kong | US |
|---|---|---|---|---|
| `qt` length | 88 | 87 | 78 | **71** |
| `qt[30]` timestamp | `20260907144136` | same | `2026/09/07 14:41:33` | `2026-09-04 16:00:01` |
| timezone | Asia/Shanghai | Asia/Shanghai | Asia/Hong_Kong | **America/New_York** |
| `qt[6]` volume | 手 (×100) | 手 | **already shares** | **already shares** |
| 成交额 | 万元 | 万元 | whole HKD | whole USD |
| daily history | yes | **no** | yes | **no — see below** |

- The lot conversion is **A-share only**. Applied to AAPL it reports about four billion shares a day.
- The US echoes `AAPL.OQ` at the code-echo index, not `AAPL`. Left alone, ADR 0028's drift check
  would have failed the entire US market. It now compares case-insensitively against the part before
  the first dot — and only for the US, so the loosening cannot leak into the markets that echo
  exactly.
- **The US has no usable daily series.** A 60-bar request for `usAAPL` returns *two* rows —
  2011-06-02 and 2026-09-04. This document's own working table said "yes" until the fixture settled
  it; a line drawn through two points fifteen years apart and labelled 3个月 is worse than saying
  there is no history, so the US gets a snapshot and no chart, for the same reason Beijing does.
- ADR 0028's field-count **floor** of 39 survived all four lengths. An equality against 88 — the
  obvious way to write that check — would have died on the US's 71.

**Reports stay A-share.** The `stocks` table, the name fetch, ingest and search are untouched. A US
ticker has a price here and no reports anywhere, and the app says so rather than implying otherwise.

Currency and timezone are on the wire because a price without them is a trap: a USD figure beside a
CNY one, both bare numbers, reads as the same kind of quantity, and a New York close stamped `+08:00`
lands fourteen hours in the future for a reader in Shanghai — it looks like a live quote rather than
last night's. The strip shows the exchange's own wall clock with a zone label rather than converting
into the browser's zone, because converting is what destroys the information.

### 5. An operator can see the sources and choose among them. They cannot type a URL.

ADR 0028 gave the hosts no setting at all, and was right about URLs for reasons that do not extend to
everything else. 管理 → 行情源 now shows, per source, which is primary and which is fallback, the
markets it serves, its last success, its last error and *when*, and its consecutive-failure streak —
all of which the cache had been recording since v0.4.45 and exposing nowhere. Plus cache occupancy
and a button to clear it. Editable: the order, which sources are enabled, and the two cache TTLs.

**Not editable: the URLs, and the page says why.** A source here is not an address, it is a *parser*
— positional index reads against these exact response shapes, behind a drift gate. Pointing it at
another host produces a gate refusal, not another source. Editing a URL would additionally drag in
the whole SSRF apparatus (a check at save *and* the guarded transport at request time; the app-market
pattern does only the first and is still open to DNS rebinding) for a field that cannot work.
Choosing among compiled-in sources is safe and useful; typing a host is neither.

Every setting here has a reader **and** a writer in the same change, per the rule
`internal/app/wired_settings_test.go` enforces. The TTLs are clamped on save and on read, with both a
floor and a ceiling: `2000000000` seconds is a valid `Duration` (2e18ns, under the int64 wrap) and
would have pinned every symbol in the cache for about sixty-three years, showing a frozen price with
only the 缓存 chip as a hint. A cache that outlives the session is indistinguishable from a broken
feed.

Clearing the cache deliberately does **not** clear the health counters. They are the record of what
the vendors have been doing, not cached data, and an operator who clears the cache to test a flapping
source would otherwise erase the evidence they pressed the button to read.

## Consequences

Two surfaces now render quotes, and they will drift unless the components stay shared — they do:
`QuoteStrip` and `PriceChart` are the same components in both places, differing only by props.

The US and Beijing show a price and an explicit "no history for this market" rather than a chart.
That is a vendor limitation this portal cannot fix, and stating it is the whole point; if a source
that serves US dailies is ever added, it is a row in the source table and a parser, not a redesign.

The market table above is a snapshot of four vendor response shapes with no schema and no changelog
behind them. The drift gate is what makes that survivable, and the fixtures pin the layouts on the
day they were captured and nothing after. When a market breaks, expect it to break as a gate refusal
naming the check — which is the failure mode this was built for, and it is why the panel in §5 shows
the last error rather than only an up/down light.
