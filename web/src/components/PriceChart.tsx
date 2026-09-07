import { useId, useMemo, useState, type KeyboardEvent } from 'react'
import { Empty, Spin, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { QuoteBar } from '../api/types'
// Prices are integer 分 everywhere in this file except at the display sites in the readout and on
// the price axis, and those go through the same formatter the strip above the chart uses. A local
// (fen / 100).toFixed(2) here would print an ungrouped 1234567.89 directly under the strip's
// 1,234,567.89 — the same money, on the same screen, under two rules.
import { fenToYuan, groupDigits } from '../lib/money'

// A daily candlestick chart, drawn by hand into one SVG element.
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

interface Props {
  bars: QuoteBar[]
  loading?: boolean
  /** Why there is no history. '' / undefined means there simply is none yet. */
  unavailable?: string
}

// The chart lives in a FIXED viewBox and is scaled to its container by preserveAspectRatio. That
// is what makes the "date ticks never overlap at any width" promise checkable at all: label width
// and bar spacing are both expressed in viewBox units, so their RATIO is the same at 320 px as at
// 1600 px. A chart laid out in real pixels would have to re-measure on every resize and would
// still collide at some width nobody happened to test.
const VIEW_W = 720
const PRICE_H = 200
const VOL_H = 54
const PANEL_GAP = 14
const PAD_T = 8
const PAD_B = 24
const PAD_L = 48
// Half a date label plus a little, so the newest bar's tick can be centred under it without being
// clipped by the right edge. Every tick can then use the same text-anchor, which is what keeps the
// no-overlap argument to a single inequality.
const PAD_R = 42
const VIEW_H = PAD_T + PRICE_H + PANEL_GAP + VOL_H + PAD_B
const VOL_TOP = PAD_T + PRICE_H + PANEL_GAP
const GRID_LINES = 5
const AXIS_FONT = 11
// "2026-09-04" is ten glyphs; 0.62 em per glyph over-estimates the digit-heavy string in all three
// of the UI's font stacks. Ticks are thinned until one step of the x scale clears this.
const DATE_LABEL_W = 10 * AXIS_FONT * 0.62 + 8
const MAX_BODY_W = 13

interface Scale {
  slot: number
  bodyW: number
  /** Centre of bar i, in viewBox units. */
  cx: (i: number) => number
  /** y of a price, in viewBox units. */
  py: (fen: number) => number
  /** Height of a volume bar, in viewBox units. */
  vh: (shares: number) => number
  /** Indices of the bars that get a date label. */
  ticks: number[]
  /** Gridline prices, in 分. */
  levels: number[]
}

// Only ever called with at least one bar; the empty case never reaches a scale.
function buildScale(bars: QuoteBar[]): Scale {
  const n = bars.length
  const plotW = VIEW_W - PAD_L - PAD_R
  const slot = plotW / n

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

  let vMax = 0
  for (const b of bars) if (b.v > vMax) vMax = b.v

  const cx = (i: number) => PAD_L + slot * (i + 0.5)
  const py = (fen: number) => PAD_T + PRICE_H * (1 - (fen - lo) / span)
  // A whole window of suspended sessions has vMax 0. Zero-height bars are the honest picture of
  // that; what matters is that the division is never reached.
  const vh = (shares: number) => (vMax > 0 ? Math.max(0, (shares / vMax) * VOL_H) : 0)

  // Walk BACK from the newest bar, so the right-hand edge — the date a reader looks for first —
  // always carries a label whatever thinning factor the width works out to.
  const step = Math.max(1, Math.ceil(DATE_LABEL_W / slot))
  const ticks: number[] = []
  for (let i = n - 1; i >= 0; i -= step) ticks.push(i)
  ticks.reverse()

  const levels: number[] = []
  for (let k = 0; k < GRID_LINES; k += 1) levels.push(Math.round(lo + (span * k) / (GRID_LINES - 1)))

  return {
    slot,
    bodyW: Math.max(1, Math.min(slot * 0.68, MAX_BODY_W)),
    cx,
    py,
    vh,
    ticks,
    levels,
  }
}

export default function PriceChart({ bars, loading, unavailable }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const readoutID = useId()
  const [active, setActive] = useState<number | null>(null)

  const n = bars.length
  const scale = useMemo(() => (n > 0 ? buildScale(bars) : null), [bars, n])

  // A-SHARE COLOUR CONVENTION: 红涨绿跌 — RED is UP and GREEN is DOWN, the OPPOSITE of US and
  // European markets. Swapping these does not make the chart merely unfamiliar to a Chinese
  // reader, it makes it say the reverse of what happened. antd's semantic tokens are the right
  // source for both because they are already tuned for contrast in the light and the dark theme;
  // it is only the mapping onto direction that is inverted here.
  const upColour = token.colorError
  const downColour = token.colorSuccess

  if (loading) {
    return (
      <div
        data-testid="price-chart-loading"
        style={{ display: 'grid', placeItems: 'center', minHeight: 220 }}
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
      <div data-testid="price-chart-unavailable" style={{ minHeight: 220, display: 'grid', placeItems: 'center' }}>
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={why} />
      </div>
    )
  }

  // No bars and no stated reason. Drawing the axis frame anyway would put a gridline at the bottom
  // of an empty panel, and a horizontal rule across a price chart is read as a price.
  if (n === 0 || scale === null) {
    return (
      <div data-testid="price-chart-empty" style={{ minHeight: 220, display: 'grid', placeItems: 'center' }}>
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      </div>
    )
  }

  // The range switcher swaps a 1y series for a 1m one under a live hover, so an index captured
  // against the old array can outlive it. Re-derive rather than trusting the stored one.
  const cur = active !== null && active >= 0 && active < n ? active : null
  // The readout always shows something — the newest bar when nothing is being pointed at. A panel
  // that is blank until hovered both hides the number people came for and changes height the
  // moment the pointer arrives.
  const shown = bars[cur ?? n - 1]
  const shownUp = shown.c >= shown.o

  const clampIndex = (i: number) => Math.min(n - 1, Math.max(0, i))

  const onKeyDown = (e: KeyboardEvent<SVGSVGElement>) => {
    const delta = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0
    if (delta !== 0) {
      // Without this the arrow key also scrolls the page, dragging the chart out from under the
      // reader who is stepping along it.
      e.preventDefault()
      setActive((prev) => clampIndex((prev ?? n - 1) + delta))
      return
    }
    if (e.key === 'Home') {
      e.preventDefault()
      setActive(0)
      return
    }
    if (e.key === 'End') {
      e.preventDefault()
      setActive(n - 1)
      return
    }
    if (e.key === 'Escape') setActive(null)
  }

  return (
    <div style={{ width: '100%', maxWidth: '100%' }}>
      <svg
        data-testid="price-chart"
        role="img"
        aria-label={t('quote.chartLabel')}
        aria-describedby={readoutID}
        tabIndex={0}
        viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
        preserveAspectRatio="xMidYMid meet"
        // width 100% + height auto is what keeps a wide series from widening the page: the drawing
        // shrinks to the column instead of the column growing to the drawing.
        style={{ display: 'block', width: '100%', height: 'auto', touchAction: 'pan-y' }}
        onKeyDown={onKeyDown}
        onFocus={() => setActive((prev) => (prev === null ? n - 1 : prev))}
        onBlur={() => setActive(null)}
        onPointerLeave={() => setActive(null)}
      >
        {/* Keyed by position, not by price: a tight range rounds two levels to the same 分. */}
        {scale.levels.map((fen, k) => (
          <g key={k}>
            <line
              x1={PAD_L}
              x2={VIEW_W - PAD_R}
              y1={scale.py(fen)}
              y2={scale.py(fen)}
              stroke={token.colorSplit}
              strokeWidth={1}
            />
            <text
              x={PAD_L - 6}
              y={scale.py(fen) + AXIS_FONT / 3}
              textAnchor="end"
              fontSize={AXIS_FONT}
              fill={token.colorTextTertiary}
            >
              {fenToYuan(fen)}
            </text>
          </g>
        ))}

        <line
          x1={PAD_L}
          x2={VIEW_W - PAD_R}
          y1={VOL_TOP + VOL_H}
          y2={VOL_TOP + VOL_H}
          stroke={token.colorSplit}
          strokeWidth={1}
        />

        {bars.map((b, i) => {
          // 阳线 / 阴线: the body's colour is the close against the OPEN of the same session, which
          // is what 阳/阴 means. The change against 昨收 belongs to the quote bar above the chart,
          // not to the candle.
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
        })}

        {bars.map((b, i) => {
          const h = scale.vh(b.v)
          return (
            <rect
              key={b.d}
              data-testid="price-volume"
              x={scale.cx(i) - scale.bodyW / 2}
              y={VOL_TOP + VOL_H - h}
              width={scale.bodyW}
              height={h}
              fill={b.c >= b.o ? upColour : downColour}
              opacity={0.5}
            />
          )
        })}

        {scale.ticks.map((i) => (
          <text
            key={bars[i].d}
            data-testid="price-xtick"
            x={scale.cx(i)}
            y={VIEW_H - 8}
            textAnchor="middle"
            fontSize={AXIS_FONT}
            fill={token.colorTextTertiary}
          >
            {bars[i].d}
          </text>
        ))}

        {cur !== null && (
          <g data-testid="price-crosshair" pointerEvents="none">
            <line
              x1={scale.cx(cur)}
              x2={scale.cx(cur)}
              y1={PAD_T}
              y2={VOL_TOP + VOL_H}
              stroke={token.colorTextTertiary}
              strokeDasharray="3 3"
            />
            <line
              x1={PAD_L}
              x2={VIEW_W - PAD_R}
              y1={scale.py(bars[cur].c)}
              y2={scale.py(bars[cur].c)}
              stroke={token.colorTextTertiary}
              strokeDasharray="3 3"
            />
          </g>
        )}

        {/* One transparent slot per bar, last so it sits above the drawing. Picking the bar from
            the rect the pointer is actually over, rather than from clientX against the element's
            box, means the chart never has to measure itself — no getBoundingClientRect, so no
            division by a zero width when it is rendered inside a collapsed or hidden container. */}
        {bars.map((b, i) => (
          <rect
            key={b.d}
            data-testid="price-hit"
            x={PAD_L + scale.slot * i}
            y={PAD_T}
            width={scale.slot}
            height={VIEW_H - PAD_T - PAD_B}
            fill="transparent"
            onPointerMove={() => setActive(i)}
          />
        ))}
      </svg>

      <div
        id={readoutID}
        data-testid="price-readout"
        aria-live="polite"
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'baseline',
          gap: '2px 14px',
          marginTop: 6,
          fontSize: 12,
          color: token.colorTextSecondary,
          fontVariantNumeric: 'tabular-nums',
        }}
      >
        <span style={{ color: token.colorText }}>{shown.d}</span>
        {/* The close is the headline number rather than one more labelled pair: it is the value
            the bar is about, and the frozen i18n key set has no label for it. */}
        <strong style={{ fontSize: 14, color: shownUp ? upColour : downColour }}>{fenToYuan(shown.c)}</strong>
        <span>
          {t('quote.open')} {fenToYuan(shown.o)}
        </span>
        <span>
          {t('quote.high')} {fenToYuan(shown.h)}
        </span>
        <span>
          {t('quote.low')} {fenToYuan(shown.l)}
        </span>
        <span>
          {t('quote.volume')} {groupDigits(shown.v)} {t('quote.shares')}
        </span>
      </div>
    </div>
  )
}
