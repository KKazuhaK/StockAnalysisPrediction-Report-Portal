import {
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import { Empty, Spin, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { QuoteBar } from '../api/types'
// Prices are integer 分 everywhere in this file except at the display sites in the tooltip, the
// readout and the price axis, and those go through the same formatter the strip above the chart
// uses. A local (fen / 100).toFixed(2) here would print an ungrouped 1234567.89 directly under the
// strip's 1,234,567.89 — the same money, on the same screen, under two rules.
import { fenToYuan, groupDigits, signedFenToYuan } from '../lib/money'

// A price chart, drawn by hand into one SVG element: candlesticks for a daily series, a line for an
// intraday one.
//
// Hand-drawn rather than delegated to a charting library because the build already ships around
// 6 MB across ~236 chunks and vite's chunkSizeWarningLimit is set to 1600 kB, so a new charting
// dependency would land in the bundle without anything on the way in saying so. What this file
// needs from a library — a linear scale, a rectangle and a line — is about forty lines of
// arithmetic, and none of it is the part that is hard to get right. The hard parts are the
// degenerate inputs, and those are guarded explicitly below.
//
// MermaidBlock is deliberately NOT reused here. Its render effect is keyed by (source, theme) and
// feeds a process-wide server-side SVG cache used by the PDF exporter; a chart that re-renders on
// every pointer move would pour hover states into that cache.
//
// WHAT THE INTERACTION IS COPIED FROM. The tooltip below is the shape every market terminal uses,
// taken from four of them: a dashed crosshair with a filled READOUT CHIP on each scale (TradingView
// puts a label on the price scale and one on the time scale; 雪球's mini panel does the same with a
// coloured block behind each), and a floating card listing the hovered bar's 开/高/低/收/涨跌/涨跌幅/
// 成交量/成交额 (东方财富 and 富途 on a daily K-line). The positioning rules are lightweight-charts'
// own tooltip tutorial: anchor the card to the DATA POINT rather than to the raw cursor, offset it
// so it never sits under the pointer, clamp it inside the container with a min/max pair, flip it to
// the other side when there is no room, and set pointer-events: none so the card cannot steal the
// hover that produced it. What is NOT copied is the colour convention: 红涨绿跌, see below.
//
// WHY AN INTRADAY SERIES IS DRAWN AS A LINE AND NOT AS CANDLES. A single day of one-minute bars is
// about 400 points and five days of five-minute bars is 331 — both measured against the vendor,
// against the 20-odd to 244 a daily range carries. At 400 points in a 900 px column a candle gets
// about two pixels of slot: the body is narrower than the wick it is supposed to sit on, the two
// merge into one grey column, and a single minute's open-to-close range is mostly noise to begin
// with. The result is a smear that says less than a line through the closes does. This is why every
// market app draws this window as a 分时 line with the volume panel kept underneath — 东方财富,
// 富途, 雪球 and TradingView all switch shape rather than shrink the candle — and it is what the
// `interval` prop below selects. Candles are still the right picture for a daily range and stay.

/**
 * Which kind of series `bars` is — a daily one (`1m`/`3m`/`6m`/`1y`) or an intraday one (`1d` at
 * one minute, `5d` at five), per the quote contract's interval split.
 */
export type ChartInterval = 'daily' | 'intraday'

interface Props {
  bars: QuoteBar[]
  loading?: boolean
  /**
   * WHAT THE BARS ARE, DECLARED BY THE CALLER — never sniffed from `bars[0].d`.
   *
   * The temptation is obvious: an intraday bar's `d` is a full timestamp and a daily bar's is a
   * date, so a regex on the first bar would appear to answer this for free. It answers it WRONGLY
   * and SILENTLY in both directions. A daily series whose vendor happens to stamp midnight onto its
   * dates would be drawn as a line with time labels — five months of "00:00" down the axis. An
   * intraday series delivered by a source that dates its bars would be drawn as 400 candles two
   * pixels wide. Neither raises anything; both are a chart that quietly mislabels what it is
   * showing, which is the one failure a price chart must not have. The caller asked for a range and
   * therefore already knows the answer; it passes it.
   */
  interval?: ChartInterval
  /** Why there is no history. '' / undefined means there simply is none yet. */
  unavailable?: string
  /**
   * Chart height in CSS pixels. THE HEIGHT IS THE CALLER'S TO CHOOSE AND NEVER FOLLOWS THE WIDTH:
   * this chart used to have a fixed 720x300 viewBox scaled by width:100%/height:auto, which made
   * height a function of width (aspect 0.417), so a 1900 px column rendered it 792 px tall and it
   * buried the report it was decorating. The default suits the reading page's strip; the quotes app
   * passes something taller.
   */
  height?: number
  /**
   * ISO 4217 code from QuoteResp.currency — the chart now draws HK and US instruments, whose prices
   * are not 元. Anything outside the three frozen quote.currency.* keys prints no unit rather than
   * the literal key: i18next echoes a key it has no string for, and `quote.currency.XYZ` on a price
   * axis is worse than a bare number.
   */
  currency?: string
}

const CURRENCY_KEYS = new Set(['CNY', 'HKD', 'USD'])

const DEFAULT_HEIGHT = 260
// Below this a chart cannot hold two panels, five gridlines and a date axis without the labels
// landing on each other, so a caller asking for 40 gets a short chart rather than a broken one.
const MIN_HEIGHT = 140
const PAD_T = 16
const PAD_B = 26
// Half a date label plus a little, so the newest bar's tick can be centred under it without being
// clipped by the right edge. Every tick can then use the same text-anchor, which is what keeps the
// no-overlap argument to a single inequality. An intraday label is narrower still, so the same
// gutter clears it with room to spare.
const PAD_R = 42
const PANEL_GAP = 10
// The volume panel's share of the plot area. Kept as a FRACTION, not a constant number of pixels,
// so a 600 px chart in the quotes app spends its extra height on both panels rather than growing a
// 54 px volume strip into a rounding error.
const VOL_SHARE = 0.22
const GRID_LINES = 5
const AXIS_FONT = 11
// 0.62 em per glyph over-estimates a digit-heavy string in all three of the UI's font stacks.
const GLYPH_W = AXIS_FONT * 0.62
// "2026-09-04" is ten glyphs. Ticks are thinned until one step of the x scale clears this.
const DATE_LABEL_W = 10 * GLYPH_W + 8
// An intraday axis prints "09:31", and "09-04" at a session boundary — five glyphs either way, so
// one width covers both kinds of label and the thinning stays the single inequality it is for the
// daily axis. That the boundary label is exactly as wide as a time is why it can be dropped into
// the middle of a row of times without a second measurement.
const TIME_LABEL_W = 5 * GLYPH_W + 8
const MAX_BODY_W = 13
const MIN_PAD_L = 38

// Crosshair readout chips and the floating card.
const CHIP_H = 16
const TIP_W = 184
// The card's height, ARITHMETIC RATHER THAN MEASURED: a date line and eight rows at 18 px, seven
// 1 px row gaps, 8 px of padding top and bottom and a 1 px border. Nothing measures the rendered
// card, because measuring it means a second layout pass on every pointer move; being a few pixels
// out here moves the card a few pixels, which is the cheapest failure in this file. Add a row to
// the card and this has to grow with it.
const TIP_H = 22 + 8 * 18 + 7 + 16 + 2
const TIP_GAP = 16
const EDGE = 4

/**
 * A bar's stamp split into its date and its clock time, or null when it carries no time at all.
 *
 * TEXTUAL, AND NEVER `new Date(d)`. The wire carries the EXCHANGE's own wall clock and no offset —
 * ADR 0030 §4 is explicit that converting it is what destroys the information, because a New York
 * 09:31 read as local time and re-rendered for a reader in Shanghai becomes 21:31, a time at which
 * that market has been shut for eight hours. Splitting the string cannot introduce a zone, so the
 * label under a bar is the time the exchange itself printed on it. (`new Date('2026-09-07 09:31')`
 * is additionally implementation-defined — the space form is not an ISO 8601 date-time.)
 *
 * Both separators are accepted because this vendor uses both: ADR 0030 §4 measured Hong Kong
 * stamping `2026/09/07 14:41:33` where the mainland stamps `20260907144136`. Seconds, if present,
 * are dropped: a minute bar's label is a minute.
 */
function splitStamp(d: string): { date: string; time: string } | null {
  const m = /^(\d{4})[-/](\d{2})[-/](\d{2})[T ](\d{2}):(\d{2})/.exec(d)
  return m === null ? null : { date: `${m[1]}-${m[2]}-${m[3]}`, time: `${m[4]}:${m[5]}` }
}

/** The date half of a stamp, or the whole string when there is no time in it to split off. */
function dayOf(d: string): string {
  return splitStamp(d)?.date ?? d
}

/**
 * The indices at which a new trading day starts, for an intraday series that spans more than one.
 *
 * A five-day 分时 chart is five sessions of identical-looking clock times laid end to end: without
 * something on the axis marking where one ends and the next begins, 09:35 appears five times and
 * nothing says which day any of them belongs to. These indices are what earn a DATE label instead
 * of a time, and they get first refusal on the tick budget below.
 *
 * The first bar counts as a session start only when the window actually spans two dates. On a
 * single-day chart every label is a time — putting the date under the leftmost bar of a one-day
 * chart says nothing the header does not already say, and it costs the 09:30 label that does.
 */
function sessionStarts(bars: QuoteBar[]): Set<number> {
  const out = new Set<number>()
  let prev = dayOf(bars[0].d)
  for (let i = 1; i < bars.length; i += 1) {
    const day = dayOf(bars[i].d)
    if (day !== prev) {
      out.add(i)
      prev = day
    }
  }
  if (out.size > 0) out.add(0)
  return out
}

interface Scale {
  /** Left gutter, sized to the widest price label this window actually prints. */
  padL: number
  priceH: number
  volTop: number
  volH: number
  slot: number
  bodyW: number
  /** Centre of bar i, in viewBox units — which are CSS pixels. */
  cx: (i: number) => number
  /** y of a price, in viewBox units. */
  py: (fen: number) => number
  /** The inverse of py, for the price under the pointer. Clamped by the caller, not here. */
  yToFen: (y: number) => number
  /** Height of a volume bar, in viewBox units. */
  vh: (shares: number) => number
  /** Indices of the bars that get an x-axis label. */
  ticks: number[]
  /** Width one x-axis label is allowed, in CSS pixels — a date's or a time's. */
  labelW: number
  /** Indices that begin a trading day; empty for a daily series and for a one-day intraday one. */
  starts: Set<number>
  /** Gridline prices, in 分. */
  levels: number[]
}

/**
 * Every geometric property of the chart, as a function of the MEASURED width and the REQUESTED
 * height. Only ever called with at least one bar and a positive width; the empty case never reaches
 * a scale and the unmeasured case renders a skeleton instead.
 *
 * One viewBox unit is one CSS pixel here, which is the whole point of measuring: label widths and
 * bar spacing are compared in the units they are actually painted in, so "the x labels never
 * overlap" is decided against the real width instead of against a constant that matched one column.
 */
function buildScale(bars: QuoteBar[], w: number, h: number, intraday: boolean): Scale {
  const n = bars.length

  let lo = bars[0].l
  let hi = bars[0].h
  for (const b of bars) {
    if (b.l < lo) lo = b.l
    if (b.h > hi) hi = b.h
  }

  // A single bar, or a stock that never moved all window, has a price range of exactly zero — and
  // then every y is (v - lo) / 0, which is NaN, which SVG silently declines to draw. The chart
  // does not vanish; it renders a full frame with nothing in it, which reads as a real flat line.
  // Widening a degenerate range to an arbitrary but non-zero band puts the price in the middle of
  // the panel instead, and is the only reason this function can promise a finite y.
  let span = hi - lo
  if (span <= 0) {
    const halfBand = Math.max(1, Math.round(Math.abs(hi) * 0.01))
    lo -= halfBand
    hi += halfBand
  } else {
    const pad = Math.max(1, Math.round(span * 0.06))
    lo -= pad
    hi += pad
  }
  span = hi - lo

  const levels: number[] = []
  for (let k = 0; k < GRID_LINES; k += 1) levels.push(Math.round(lo + (span * k) / (GRID_LINES - 1)))

  // The left gutter is sized from the widest label it must actually hold. A constant 48 was fine
  // while the viewBox was 720 units wide and scaled up with the container — at 1900 px it was 127
  // real pixels. In real pixels a constant either wastes a third of a narrow chart or clips
  // 1,234,567.89 off the left edge of a wide one, so it is measured. The cap keeps a very narrow
  // container from spending its whole width on the axis.
  let widest = 0
  for (const fen of levels) widest = Math.max(widest, fenToYuan(fen).length)
  const padL = Math.min(
    Math.max(MIN_PAD_L, Math.round(10 + widest * GLYPH_W)),
    Math.max(MIN_PAD_L, Math.round(w * 0.3)),
  )
  // Never zero, never negative, whatever the container does: a collapsed column would otherwise
  // make slot zero or negative and the tick thinning below would loop forever.
  const plotW = Math.max(1, w - padL - PAD_R)
  const slot = plotW / n

  // The two panels split whatever vertical room the caller gave us. The floors are what make every
  // division below total; at the MIN_HEIGHT floor they are not reached.
  const inner = Math.max(60, h - PAD_T - PAD_B)
  const volH = Math.max(12, Math.round(inner * VOL_SHARE))
  const priceH = Math.max(24, inner - PANEL_GAP - volH)
  const volTop = PAD_T + priceH + PANEL_GAP

  let vMax = 0
  for (const b of bars) if (b.v > vMax) vMax = b.v

  const cx = (i: number) => padL + slot * (i + 0.5)
  const py = (fen: number) => PAD_T + priceH * (1 - (fen - lo) / span)
  const yToFen = (y: number) => lo + span * (1 - (y - PAD_T) / priceH)
  // A whole window of suspended sessions has vMax 0. Zero-height bars are the honest picture of
  // that; what matters is that the division is never reached.
  const vh = (shares: number) => (vMax > 0 ? Math.max(0, (shares / vMax) * volH) : 0)

  // X-AXIS TICKS. Two rules, and the first one is absolute: NO TWO LABELS MAY TOUCH. That is one
  // inequality — the centres of any two labels are at least a label's width apart — and it is
  // stated in REAL PIXELS, which is the whole reason this function takes a measured width. A
  // constant thinning factor was correct only for the one column it was tuned against.
  //
  // The second rule is that ticks are not equally worth keeping, so they are OFFERED in order of
  // worth and the sweep keeps whatever still fits: the newest bar first (the right-hand edge is
  // where a reader looks before anywhere else), then each session boundary on a multi-day intraday
  // chart, then an evenly spaced grid walking back from the right to fill what is left. The
  // boundaries are offered before the grid rather than added to it because a 5-day 分时 with four
  // of its five dates missing is the chart this ordering exists to prevent — but they are offered,
  // not forced: at a width where a whole session is narrower than one label even the dates have to
  // thin out, and a legible axis missing a date beats five labels printed on top of each other.
  //
  // The grid's spacing is not what keeps the labels apart — the sweep would produce the same axis
  // from every index — it is what keeps the sweep cheap and the surviving labels evenly spaced.
  // The inequality is the guarantee, and it is the only thing that has to be right.
  const labelW = intraday ? TIME_LABEL_W : DATE_LABEL_W
  const starts = intraday ? sessionStarts(bars) : new Set<number>()
  const step = Math.max(1, Math.ceil(labelW / slot))
  const offered: number[] = [n - 1]
  // Ascending, so that when two boundaries are closer together than one label — a width at which a
  // whole session is narrower than "09-04" — it is deterministically the earlier session that keeps
  // its date rather than whichever the Set happened to be built in.
  for (const i of [...starts].sort((a, b) => a - b)) offered.push(i)
  for (let i = n - 1; i >= 0; i -= step) offered.push(i)

  const ticks: number[] = []
  for (const i of offered) {
    const x = cx(i)
    // Against EVERY label already kept, not merely the last one: the offers do not arrive in x
    // order — a boundary can land between two grid ticks — so a running "previous x" would let one
    // slip in beside a label already placed to its right. This also dedupes for free, since an
    // index offered twice is zero pixels from itself.
    if (ticks.every((k) => Math.abs(cx(k) - x) >= labelW)) ticks.push(i)
  }
  ticks.sort((a, b) => a - b)

  return {
    padL,
    priceH,
    volTop,
    volH,
    slot,
    bodyW: Math.max(1, Math.min(slot * 0.68, MAX_BODY_W)),
    cx,
    py,
    yToFen,
    vh,
    ticks,
    labelW,
    starts,
    levels,
  }
}

function clamp(v: number, min: number, max: number): number {
  return Math.min(Math.max(v, min), max)
}

// Off-screen but in the accessibility tree. The tooltip is a pointer affordance and says nothing to
// a screen reader, so the text version of the same numbers stays in the DOM and stays live.
const SR_ONLY: CSSProperties = {
  position: 'absolute',
  width: 1,
  height: 1,
  margin: -1,
  padding: 0,
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
  border: 0,
}

export default function PriceChart({ bars, loading, unavailable, height, currency, interval }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const readoutID = useId()
  const [active, setActive] = useState<number | null>(null)
  // The pointer's y INSIDE the svg, or null when the selection came from the keyboard or from a
  // pointer that is not over the price panel. Null means "snap the crosshair to the close".
  const [pointerY, setPointerY] = useState<number | null>(null)
  const [box, setBox] = useState<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(0)
  const svgRef = useRef<SVGSVGElement | null>(null)

  // A number-typed prop can still hold NaN or Infinity, and either would flow straight into the
  // viewBox and out through every y in the drawing. Nothing below re-checks it.
  const h = Math.max(
    MIN_HEIGHT,
    typeof height === 'number' && Number.isFinite(height) ? Math.round(height) : DEFAULT_HEIGHT,
  )

  // The one measurement this component makes. It is a WIDTH only: the height is the prop above, so
  // nothing observed here can change how tall the chart is.
  useLayoutEffect(() => {
    if (!box) return
    // Round and ignore sub-pixel churn. A fractional width from a flex parent otherwise sets state
    // on every observation, and the state change resizes nothing, so it never settles.
    const apply = (raw: number) => {
      const next = Math.max(0, Math.round(raw))
      setWidth((prev) => (Math.abs(prev - next) >= 1 ? next : prev))
    }
    // First paint, and any environment without a ResizeObserver: the box may already have a width,
    // and if it does not we render the skeleton until something tells us otherwise.
    apply(box.getBoundingClientRect().width)
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect
      apply(rect && rect.width > 0 ? rect.width : box.getBoundingClientRect().width)
    })
    ro.observe(box)
    return () => ro.disconnect()
  }, [box])

  const n = bars.length
  // The default is `daily`, so a caller written before intervals existed keeps the chart it had.
  const intraday = interval === 'intraday'
  const scale = useMemo(
    () => (n > 0 && width > 0 ? buildScale(bars, width, h, intraday) : null),
    [bars, n, width, h, intraday],
  )

  /**
   * A bar's stamp as it is written out in full — in the card and in the live region, where there is
   * room and where a reader may be several sessions from where they started.
   *
   * The axis and the crosshair chip cannot afford this: five glyphs is what the thinning budget is
   * built on, and "2026-09-07 09:31" is sixteen. They print the time alone and let this say which
   * day it was.
   */
  const stamp = (d: string): string => {
    if (!intraday) return d
    const s = splitStamp(d)
    // A stamp this cannot parse is printed exactly as it arrived. Guessing at one is how a chart
    // ends up labelling a bar "Invalid Date" or "NaN:NaN" — the wire's own string is at least true.
    return s === null ? d : `${s.date} ${s.time}`
  }

  /** The clock time alone, for the axis and the chip. Falls back to the whole stamp, as above. */
  const clock = (d: string): string => splitStamp(d)?.time ?? d

  // A-SHARE COLOUR CONVENTION: 红涨绿跌 — RED is UP and GREEN is DOWN, the OPPOSITE of US and
  // European markets. Swapping these does not make the chart merely unfamiliar to a Chinese
  // reader, it makes it say the reverse of what happened. antd's semantic tokens are the right
  // source for both because they are already tuned for contrast in the light and the dark theme;
  // it is only the mapping onto direction that is inverted here.
  const upColour = token.colorError
  const downColour = token.colorSuccess

  const unit = currency && CURRENCY_KEYS.has(currency) ? t(`quote.currency.${currency}`) : ''

  if (loading) {
    return (
      <div
        data-testid="price-chart-loading"
        style={{ display: 'grid', placeItems: 'center', minHeight: h }}
      >
        <Spin />
      </div>
    )
  }

  if (unavailable) {
    // Any reason other than the contract's 'market_unsupported' is a failure to fetch, and saying
    // so is better than falling through to a bare Empty, which a reader takes to mean the stock
    // has no history rather than that we could not get it.
    const why = unavailable === 'market_unsupported' ? t('quote.noHistory') : t('quote.noHistorySrc')
    return (
      <div data-testid="price-chart-unavailable" style={{ minHeight: h, display: 'grid', placeItems: 'center' }}>
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={why} />
      </div>
    )
  }

  // No bars and no stated reason. Drawing the axis frame anyway would put a gridline at the bottom
  // of an empty panel, and a horizontal rule across a price chart is read as a price.
  if (n === 0) {
    return (
      <div data-testid="price-chart-empty" style={{ minHeight: h, display: 'grid', placeItems: 'center' }}>
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      </div>
    )
  }

  // The range switcher swaps a 1y series for a 1m one under a live hover, so an index captured
  // against the old array can outlive it. Re-derive rather than trusting the stored one.
  const cur = active !== null && active >= 0 && active < n ? active : null
  // The readout always names something — the newest bar when nothing is being pointed at. A live
  // region that is empty until hovered tells a screen-reader user nothing about where they are.
  const shownIdx = cur ?? n - 1
  const shown = bars[shownIdx]
  // THE CHANGE AND THE PERCENTAGE OF A HISTORICAL BAR ARE OURS, AND THAT IS NOT A VIOLATION OF THE
  // SNAPSHOT RULE. ADR 0028 §4 forbids recomputing the LIVE snapshot's percentage from last and
  // prevClose: on an ex-rights morning the exchange restates 昨收 to the adjusted reference price
  // and the vendor computes its percentage against THAT, so our division would report a stock that
  // opened flat as down eight percent. That argument has no purchase on a `day` row, because there
  // is no vendor percentage per bar to defer to: a row is [date, open, close, high, low, volume] on
  // every market this portal serves, and it carries neither a 涨跌 nor a 涨跌幅 column. The only two
  // numbers in existence are this bar's close and the previous bar's close, both unadjusted (§3)
  // and both from the same series, so their difference is exactly the move this chart draws.
  // It is NOT the exchange's official 涨跌幅 across a dividend or a split — that number is not
  // obtainable here — and printing nothing for every bar is the worse answer.
  // Do not "fix" this into the snapshot rule. On this path there is nothing to pass through.
  const prevClose = shownIdx > 0 ? bars[shownIdx - 1].c : null
  const change = prevClose === null ? null : shown.c - prevClose
  const changePct = prevClose !== null && prevClose !== 0 ? ((shown.c - prevClose) / prevClose) * 100 : null
  // The oldest bar in the window has no previous close IN THIS WINDOW. Its real one exists in the
  // market and simply was not fetched, so the honest render is a dash: using its own open instead
  // would print a number the market never quoted, in the same column where every other row is a
  // close-to-close move.
  const changeText = change === null ? '—' : signedFenToYuan(change)
  const changePctText = changePct === null ? '—' : `${changePct > 0 ? '+' : ''}${changePct.toFixed(2)}%`
  const moveColour = change === null || change === 0 ? token.colorText : change > 0 ? upColour : downColour

  const clampIndex = (i: number) => Math.min(n - 1, Math.max(0, i))

  const onKeyDown = (e: KeyboardEvent<SVGSVGElement>) => {
    const delta = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0
    if (delta !== 0) {
      // Without this the arrow key also scrolls the page, dragging the chart out from under the
      // reader who is stepping along it.
      e.preventDefault()
      // A keyboard selection has no pointer y, so the crosshair snaps to the bar's close.
      setPointerY(null)
      setActive((prev) => clampIndex((prev ?? n - 1) + delta))
      return
    }
    if (e.key === 'Home') {
      e.preventDefault()
      setPointerY(null)
      setActive(0)
      return
    }
    if (e.key === 'End') {
      e.preventDefault()
      setPointerY(null)
      setActive(n - 1)
      return
    }
    if (e.key === 'Escape') {
      setPointerY(null)
      setActive(null)
    }
  }

  /**
   * The pointer's y in viewBox units, or null when the element has no usable box.
   *
   * This is the ONLY place the drawing measures itself at request time, and it is guarded rather
   * than trusted: getBoundingClientRect returns a zero box inside a collapsed or `display:none`
   * container and in jsdom, and dividing the chart height by that zero is how a crosshair becomes
   * Infinity. The ratio (not a bare subtraction) is what makes this survive the one frame between a
   * resize and the observer callback, when the box and the viewBox disagree.
   */
  const svgY = (clientY: number): number | null => {
    const el = svgRef.current
    if (!el) return null
    const rect = el.getBoundingClientRect()
    if (!(rect.height > 0)) return null
    const y = (clientY - rect.top) * (h / rect.height)
    return Number.isFinite(y) ? y : null
  }

  const onHit = (i: number, e: ReactPointerEvent<SVGRectElement>) => {
    setActive(i)
    const y = svgY(e.clientY)
    // Only a pointer inside the PRICE panel names a price. Over the volume panel there is nothing
    // for the price chip to read, so the crosshair falls back to the bar's close rather than
    // labelling the axis with a price the cursor is not on.
    const inPrice = y !== null && scale !== null && y >= PAD_T && y <= PAD_T + scale.priceH
    setPointerY(inPrice ? y : null)
  }

  const readout = (
    <div id={readoutID} data-testid="price-readout" aria-live="polite" style={SR_ONLY}>
      {/* The full stamp, never the bare time the axis prints: a reader stepping through five days
          of minutes with the arrow keys has nothing else on the page telling them which day they
          have walked into. */}
      <span>{stamp(shown.d)}</span>{' '}
      <span>{fenToYuan(shown.c)}</span>{' '}
      <span>
        {t('quote.open')} {fenToYuan(shown.o)}
      </span>{' '}
      <span>
        {t('quote.high')} {fenToYuan(shown.h)}
      </span>{' '}
      <span>
        {t('quote.low')} {fenToYuan(shown.l)}
      </span>{' '}
      {/* The move is announced only once a bar is actually SELECTED. At rest this region labels the
          newest session — the very session the price strip above the chart describes — and for that
          bar the two are the same arithmetic on the same two numbers: the strip's 昨收 is the close
          of the bar before the newest one, so a resting readout would make a screen reader hear the
          day's 涨跌/涨跌幅 twice on one card. Stepping onto any other bar is where this move is not
          on the page anywhere else. */}
      {cur !== null && (
        <>
          <span>
            {t('quote.change')} {changeText}
          </span>{' '}
          <span>
            {t('quote.changePct')} {changePctText}
          </span>{' '}
        </>
      )}
      <span>
        {t('quote.volume')} {groupDigits(shown.v)} {t('quote.shares')}
      </span>
    </div>
  )

  // Not measured yet: a collapsed container, a hidden tab, the first paint, jsdom. Everything below
  // divides by a width, so nothing below runs until there is one. The box keeps its ref and its
  // requested height, so the observer fires the moment the container opens and the skeleton is
  // exactly as tall as the chart that replaces it — no reflow of the page around it.
  if (scale === null) {
    return (
      <div ref={setBox} style={{ position: 'relative', width: '100%', maxWidth: '100%' }}>
        <div
          data-testid="price-chart-skeleton"
          style={{ height: h, borderRadius: token.borderRadius, background: token.colorFillQuaternary }}
        />
        {readout}
      </div>
    )
  }

  const axisRight = width - PAD_R
  // One narrowed handle for the selection, so that nothing below has to re-assert that an index and
  // a bar belong to each other.
  const sel = cur === null ? null : { i: cur, bar: bars[cur] }
  // 分 x shares / 100 -> whole currency units. See the long note at the render site: this product
  // is the one value here that can leave the exact-integer range, and it is checked there rather
  // than clamped, because a clamped turnover would be a made-up number wearing an ≈.
  const amountEst = sel === null ? 0 : Math.round((sel.bar.c * sel.bar.v) / 100)
  // The crosshair's price: whatever the pointer is on, or the bar's close when the selection came
  // from the keyboard. 东方财富 and 富途 both snap the readout to the bar on a daily K-line, which is
  // why the fallback is the close and not the middle of the panel.
  const chY = sel === null ? 0 : pointerY !== null ? clamp(pointerY, PAD_T, PAD_T + scale.priceH) : scale.py(sel.bar.c)
  const chFen = sel === null ? 0 : pointerY !== null ? Math.round(scale.yToFen(chY)) : sel.bar.c

  let tip: { left: number; top: number; flipped: boolean } | null = null
  if (sel !== null) {
    const anchorX = scale.cx(sel.i)
    // Flip to the left of the crosshair when the card would cross the right edge — the last bars
    // are the ones a reader hovers most, and a card clipped by the container is the same bug as no
    // card at all. Never centred on the pointer: the gap is what keeps the card out from under it.
    const flipped = anchorX + TIP_GAP + TIP_W > width - EDGE
    const left = flipped ? anchorX - TIP_GAP - TIP_W : anchorX + TIP_GAP
    tip = {
      left: clamp(left, EDGE, Math.max(EDGE, width - TIP_W - EDGE)),
      // Centred on the crosshair and pushed back inside the chart at either end. The Math.max is
      // the case where the caller's chart is shorter than the card: the card then starts at the top
      // and overhangs downwards rather than being pushed off the top of its own chart.
      top: clamp(chY - TIP_H / 2, EDGE, Math.max(EDGE, h - TIP_H - EDGE)),
      flipped,
    }
  }

  // The chips are antd's tooltip pair — a solid block with light text — because they must read
  // against gridlines, candles and volume bars alike in both themes. A text-colour fill would be
  // white-on-light-grey in the dark theme.
  const chipText = token.colorTextLightSolid
  const chipFill = token.colorBgSpotlight
  // The chip labels the time axis, so it is exactly as wide as the labels on that axis — a chip
  // sized for a date over a row of times would cover the two labels either side of the crosshair.
  const xChipW = scale.labelW
  const xChipX = sel === null ? 0 : clamp(scale.cx(sel.i) - xChipW / 2, 0, Math.max(0, width - xChipW))

  /**
   * What goes under bar i on the x axis.
   *
   * A daily axis prints the vendor's date exactly as it dated the bar. An intraday one prints the
   * clock time, except at a session boundary, which prints that session's 月-日 — the one place on
   * a five-day 分时 where the axis can say that the 09:35 to its right is not the same 09:35 as the
   * one to its left. A stamp that parses as neither is printed whole: slicing a string that is not
   * a date produces a label that looks like one and is not.
   */
  const tickText = (i: number): string => {
    const d = bars[i].d
    if (!intraday) return d
    const s = splitStamp(d)
    if (s === null) return d
    return scale.starts.has(i) ? s.date.slice(5) : s.time
  }

  return (
    // position:relative is what lets the tooltip be positioned in the same pixels the drawing is
    // laid out in: one viewBox unit is one CSS pixel, so an SVG x is a `left` with no conversion.
    <div ref={setBox} style={{ position: 'relative', width: '100%', maxWidth: '100%' }}>
      <svg
        ref={svgRef}
        data-testid="price-chart"
        role="img"
        // `quote.chartLabel` is "日 K 线图" / "Daily candlestick chart", which on a minute-by-minute
        // line is simply false, and a false accessible name is worse than a terse one. The key list
        // for this feature is frozen, so the intraday name is composed from two keys that exist
        // rather than by inventing a third.
        aria-label={intraday ? `${t('quote.intraday')} ${t('quote.chart')}` : t('quote.chartLabel')}
        aria-describedby={readoutID}
        tabIndex={0}
        viewBox={`0 0 ${width} ${h}`}
        // The viewBox IS the measured box, so there is no aspect ratio to preserve — and "none"
        // keeps the vertical mapping exact during the one frame between a resize and the observer
        // callback, which is what svgY above inverts.
        preserveAspectRatio="none"
        style={{ display: 'block', width: '100%', height: h, touchAction: 'pan-y' }}
        onKeyDown={onKeyDown}
        onFocus={() => {
          setPointerY(null)
          setActive((prev) => (prev === null ? n - 1 : prev))
        }}
        onBlur={() => setActive(null)}
        onPointerLeave={() => {
          setActive(null)
          setPointerY(null)
        }}
      >
        {/* Keyed by position, not by price: a tight range rounds two levels to the same 分. */}
        {scale.levels.map((fen, k) => (
          <g key={k}>
            <line
              x1={scale.padL}
              x2={axisRight}
              y1={scale.py(fen)}
              y2={scale.py(fen)}
              stroke={token.colorSplit}
              strokeWidth={1}
            />
            <text
              x={scale.padL - 6}
              y={scale.py(fen) + AXIS_FONT / 3}
              textAnchor="end"
              fontSize={AXIS_FONT}
              fill={token.colorTextTertiary}
            >
              {fenToYuan(fen)}
            </text>
          </g>
        ))}

        {/* The unit the price axis is denominated in. A US instrument's 190.00 beside a 元 label is
            wrong by a factor of seven, and the strip above the chart is not always on screen. */}
        {unit !== '' && (
          <text
            data-testid="price-currency"
            x={scale.padL - 6}
            y={PAD_T - 5}
            textAnchor="end"
            fontSize={AXIS_FONT}
            fill={token.colorTextTertiary}
          >
            {unit}
          </text>
        )}

        <line
          x1={scale.padL}
          x2={axisRight}
          y1={scale.volTop + scale.volH}
          y2={scale.volTop + scale.volH}
          stroke={token.colorSplit}
          strokeWidth={1}
        />

        {intraday ? (
          // ONE polyline through the closes — see the note at the top of the file for why 400
          // two-pixel candles are not a chart. It is drawn in ONE neutral colour, and that is a
          // decision rather than an omission: 红涨绿跌 is a statement about a move, and the only
          // move a whole line could claim is last close against first close, which on this chart is
          // the first MINUTE's close and not 昨收. A stock that gapped up three percent at the open
          // and drifted half a percent down since would be painted green — the exact opposite of
          // the day it had. This component is not given 昨收 (the strip above it owns that number),
          // so the line states no direction and the volume panel below, which can, states one.
          <polyline
            data-testid="price-line"
            points={bars.map((b, i) => `${scale.cx(i)},${scale.py(b.c)}`).join(' ')}
            fill="none"
            stroke={token.colorPrimary}
            strokeWidth={1.5}
            strokeLinejoin="round"
          />
        ) : (
          bars.map((b, i) => {
            // 阳线 / 阴线: the body's colour is the close against the OPEN of the same session,
            // which is what 阳/阴 means. The change against 昨收 belongs to the quote bar above the
            // chart, not to the candle.
            const colour = b.c >= b.o ? upColour : downColour
            const x = scale.cx(i)
            const bodyTop = scale.py(Math.max(b.o, b.c))
            // A 十字星 — open exactly equal to close — has a body of zero height and would draw
            // nothing at all. One unit of height keeps it on the chart as the line it should be.
            const bodyH = Math.max(1, scale.py(Math.min(b.o, b.c)) - bodyTop)
            return (
              <g key={b.d} data-testid="price-candle" data-date={b.d} data-dir={b.c >= b.o ? 'up' : 'down'}>
                <line x1={x} x2={x} y1={scale.py(b.h)} y2={scale.py(b.l)} stroke={colour} strokeWidth={1} />
                <rect x={x - scale.bodyW / 2} y={bodyTop} width={scale.bodyW} height={bodyH} fill={colour} />
              </g>
            )
          })
        )}

        {bars.map((b, i) => {
          const vhh = scale.vh(b.v)
          // The volume panel survives the switch to a line — a 分时 without it is half a chart —
          // but its colour rule changes with the shape above it. A DAILY bar is coloured 阳/阴, the
          // close against its own open. An INTRADAY sample very often has open exactly equal to
          // close, because a minute of a thin book is one price: under the daily rule every one of
          // those is `c >= o` and the whole panel comes out red, which reads as a session that only
          // ever went up. Against the PREVIOUS sample's close it is the tick direction a 分时 panel
          // is supposed to show. The first bar has no previous one in this window and falls back to
          // its own open rather than inventing a reference price.
          const ref = intraday && i > 0 ? bars[i - 1].c : b.o
          return (
            <rect
              key={b.d}
              data-testid="price-volume"
              x={scale.cx(i) - scale.bodyW / 2}
              y={scale.volTop + scale.volH - vhh}
              width={scale.bodyW}
              height={vhh}
              fill={b.c >= ref ? upColour : downColour}
              opacity={0.5}
            />
          )
        })}

        {scale.ticks.map((i) => (
          <text
            key={bars[i].d}
            data-testid="price-xtick"
            x={scale.cx(i)}
            y={h - 8}
            textAnchor="middle"
            fontSize={AXIS_FONT}
            fill={token.colorTextTertiary}
          >
            {tickText(i)}
          </text>
        ))}

        {sel !== null && (
          // pointerEvents none on the whole group: a crosshair that swallowed the pointer would
          // take the hover away from the hit rect underneath and the tooltip would flicker off on
          // every move.
          <g data-testid="price-crosshair" pointerEvents="none">
            <line
              x1={scale.cx(sel.i)}
              x2={scale.cx(sel.i)}
              y1={PAD_T}
              y2={scale.volTop + scale.volH}
              stroke={token.colorTextTertiary}
              strokeDasharray="3 3"
            />
            <line x1={scale.padL} x2={axisRight} y1={chY} y2={chY} stroke={token.colorTextTertiary} strokeDasharray="3 3" />

            {/* Readout chips, one per scale — the price where the crosshair crosses the price axis
                and the date where it crosses the time axis. */}
            <g data-testid="price-ychip">
              <rect x={0} y={chY - CHIP_H / 2} width={Math.max(1, scale.padL - 4)} height={CHIP_H} rx={2} fill={chipFill} />
              <text
                x={scale.padL - 8}
                y={chY + AXIS_FONT / 3}
                textAnchor="end"
                fontSize={AXIS_FONT}
                fill={chipText}
              >
                {fenToYuan(chFen)}
              </text>
            </g>
            <g data-testid="price-xchip">
              <rect x={xChipX} y={h - PAD_B + 2} width={xChipW} height={CHIP_H} rx={2} fill={chipFill} />
              <text
                x={xChipX + xChipW / 2}
                y={h - PAD_B + 2 + CHIP_H - 4}
                textAnchor="middle"
                fontSize={AXIS_FONT}
                fill={chipText}
              >
                {intraday ? clock(sel.bar.d) : sel.bar.d}
              </text>
            </g>
          </g>
        )}

        {/* One transparent slot per bar, last so it sits above the drawing. Picking the bar from
            the rect the pointer is actually over, rather than from clientX against the element's
            box, means a zero-width box costs the reader the price chip and nothing else: the bar
            under the pointer is still identified, and no arithmetic divides by that zero. */}
        {bars.map((b, i) => (
          <rect
            key={b.d}
            data-testid="price-hit"
            x={scale.padL + scale.slot * i}
            y={PAD_T}
            width={scale.slot}
            height={Math.max(1, h - PAD_T - PAD_B)}
            fill="transparent"
            onPointerMove={(e) => onHit(i, e)}
          />
        ))}
      </svg>

      {tip !== null && sel !== null && (
        <div
          data-testid="price-tooltip"
          data-flip={tip.flipped ? 'left' : 'right'}
          style={{
            position: 'absolute',
            left: tip.left,
            top: tip.top,
            width: TIP_W,
            // TIP_W is what the flip and the clamp above are computed against, so it has to be the
            // card's OUTER width. Under content-box the padding and the border would put another
            // 22 px past the edge those two just fitted it inside.
            boxSizing: 'border-box',
            // The card must never take the pointer: it is drawn where the pointer is going, and a
            // card that captured the hover would end the hover that drew it, on every move.
            pointerEvents: 'none',
            padding: '8px 10px',
            borderRadius: token.borderRadiusLG,
            background: token.colorBgElevated,
            border: `1px solid ${token.colorBorderSecondary}`,
            boxShadow: token.boxShadowSecondary,
            fontSize: 12,
            lineHeight: '18px',
            color: token.colorTextSecondary,
            fontVariantNumeric: 'tabular-nums',
            zIndex: 2,
          }}
        >
          <div data-testid="tt-date" style={{ color: token.colorText, fontWeight: 600, marginBottom: 4 }}>
            {stamp(sel.bar.d)}
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', columnGap: 10, rowGap: 1 }}>
            <span>{t('quote.open')}</span>
            <span data-testid="tt-open" style={{ textAlign: 'right', color: token.colorText }}>
              {fenToYuan(sel.bar.o)}
            </span>
            <span>{t('quote.high')}</span>
            <span data-testid="tt-high" style={{ textAlign: 'right', color: token.colorText }}>
              {fenToYuan(sel.bar.h)}
            </span>
            <span>{t('quote.low')}</span>
            <span data-testid="tt-low" style={{ textAlign: 'right', color: token.colorText }}>
              {fenToYuan(sel.bar.l)}
            </span>
            <span>{t('quote.close')}</span>
            <span data-testid="tt-close" style={{ textAlign: 'right', color: moveColour, fontWeight: 600 }}>
              {fenToYuan(sel.bar.c)}
              {unit === '' ? '' : ` ${unit}`}
            </span>
            <span>{t('quote.change')}</span>
            <span data-testid="tt-change" style={{ textAlign: 'right', color: moveColour }}>
              {changeText}
            </span>
            <span>{t('quote.changePct')}</span>
            <span data-testid="tt-changepct" style={{ textAlign: 'right', color: moveColour }}>
              {changePctText}
            </span>
            <span>{t('quote.volume')}</span>
            <span data-testid="tt-volume" style={{ textAlign: 'right', color: token.colorText }}>
              {groupDigits(sel.bar.v)} {t('quote.shares')}
            </span>
            {/* AN ESTIMATE, AND MARKED AS ONE. The vendor's `day` row is [date, open, close, high,
                low, volume] on all four markets — there is no 成交额 column in it, and the real
                turnover is Σ(price × qty) over the session, which lies somewhere between low × v
                and high × v. close × v is the conventional stand-in and can be a few percent out on
                a wide-range day, so it keeps its ≈ and it is never presented as the vendor's own
                成交额 the way the live strip's amount is. Do not drop the ≈, and do not feed this
                number into anything that stores or scores.

                It is also the ONE number on this chart that leaves the integer domain: everything
                else is an integer 分 straight off the wire, while this is a PRODUCT of two of them.
                The Go ceilings (quotePriceCeiling 10^12 分, quoteMaxLots ≈ 9.2e16) admit a product
                past 2^53, where a double stops being an exact integer, and past 1e21 String()
                switches to exponential — at which point groupDigits hands back an ungrouped
                "9.2e+28" and the ≈ is covering for a number that is not even the right shape. Real
                bodies are around 1e11, twelve orders of magnitude below that, so what follows is a
                guard against a malformed body rather than a case anybody will meet.

                Above the safe range the whole ROW is dropped rather than printed, the same answer
                this file gives an unrecognised currency: a quantity that cannot be written down
                exactly is not an estimate, and a label with a shape beside it is worse than a card
                with one fewer row. TIP_H then over-estimates the card by 18 px and the card sits 18
                px off, which is the cheapest failure available here. */}
            {Number.isSafeInteger(amountEst) && (
              <>
                <span>{t('quote.amount')}</span>
                <span data-testid="tt-amount" style={{ textAlign: 'right', color: token.colorText }}>
                  ≈ {groupDigits(amountEst)}
                  {unit === '' ? '' : ` ${unit}`}
                </span>
              </>
            )}
          </div>
        </div>
      )}

      {readout}
    </div>
  )
}
