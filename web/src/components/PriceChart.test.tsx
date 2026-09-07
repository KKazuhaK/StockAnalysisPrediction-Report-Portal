import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { QuoteBar } from '../api/types'
import PriceChart from './PriceChart'

// t() returns the key, so an assertion names the key the contract froze rather than whichever of
// the three bundles happened to be loaded.
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

// Real 紫金矿业 (sh601899) sessions, lifted from internal/app/testdata/quote/
// tencent_fqkline_sh601899.json and converted the way the Go parser converts them: prices from
// 元 to integer 分, volume from 手 to 股 (x100). Made-up round numbers would have hidden the two
// things these tests are actually about — a price range that is a few percent of the price, and
// a share count with nine digits.
const BARS: QuoteBar[] = [
  { d: '2026-08-28', o: 3450, c: 3465, h: 3467, l: 3370, v: 199711500 },
  { d: '2026-08-31', o: 3300, c: 3367, h: 3375, l: 3260, v: 296639900 },
  { d: '2026-09-01', o: 3367, c: 3373, h: 3419, l: 3349, v: 159114200 },
  { d: '2026-09-02', o: 3289, c: 3295, h: 3323, l: 3219, v: 237215500 },
  { d: '2026-09-03', o: 3325, c: 3331, h: 3366, l: 3295, v: 169589800 },
  // The only 阴线 in the window: opened at 33.85 and closed at 33.35.
  { d: '2026-09-04', o: 3385, c: 3335, h: 3405, l: 3307, v: 176934100 },
]

const UP_INDEX = 4
const DOWN_INDEX = 5

// #rrggbb or rgb(...) -> [r, g, b]. antd's semantic colours are hex in both themes, but reading
// the channels rather than a literal keeps the assertion about "red-ish" instead of about which
// shade of red antd happens to ship this major version.
function channels(colour: string): [number, number, number] {
  const hex = /^#([0-9a-f]{6})$/i.exec(colour.trim())
  if (hex) {
    const v = parseInt(hex[1], 16)
    return [(v >> 16) & 255, (v >> 8) & 255, v & 255]
  }
  const rgb = /rgba?\(([^)]+)\)/.exec(colour)
  if (!rgb) throw new Error(`unparseable colour: ${colour}`)
  const parts = rgb[1].split(',').map((p) => Number(p.trim()))
  return [parts[0], parts[1], parts[2]]
}

function bodyFill(candle: Element): string {
  const rect = candle.querySelector('rect')
  expect(rect).not.toBeNull()
  return rect?.getAttribute('fill') ?? ''
}

describe('PriceChart', () => {
  it('draws one candle per bar', () => {
    render(<PriceChart bars={BARS} />)
    expect(screen.getAllByTestId('price-candle')).toHaveLength(BARS.length)
    expect(screen.getAllByTestId('price-volume')).toHaveLength(BARS.length)
  })

  it('paints an up bar red and a down bar green, the A-share way round', () => {
    render(<PriceChart bars={BARS} />)
    const candles = screen.getAllByTestId('price-candle')
    const up = bodyFill(candles[UP_INDEX])
    const down = bodyFill(candles[DOWN_INDEX])
    expect(up).not.toBe(down)

    // 红涨绿跌. If these two ever swap, the chart reports the opposite of what the market did,
    // so the assertion is on the channels rather than on "not equal".
    const [ur, ug, ub] = channels(up)
    expect(ur).toBeGreaterThan(ug)
    expect(ur).toBeGreaterThan(ub)
    const [dr, dg, db] = channels(down)
    expect(dg).toBeGreaterThan(dr)
    expect(dg).toBeGreaterThan(db)
  })

  it('exposes the chart with a role and the frozen accessible name', () => {
    render(<PriceChart bars={BARS} />)
    expect(screen.getByRole('img', { name: 'quote.chartLabel' })).toBeTruthy()
  })

  it('emits no NaN when every price in the window is identical', () => {
    // The classic SVG scale bug: high === low makes the price span zero, every y becomes
    // (v - lo) / 0, and the browser drops each attribute it cannot parse — leaving a full axis
    // frame with no data in it, which reads as a real flat line rather than as a broken chart.
    const flat: QuoteBar[] = [
      { d: '2026-09-02', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
      { d: '2026-09-03', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
      { d: '2026-09-04', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
    ]
    const { container } = render(<PriceChart bars={flat} />)
    const offenders: string[] = []
    container.querySelectorAll('*').forEach((el) => {
      for (const attr of Array.from(el.attributes)) {
        if (attr.value.includes('NaN')) offenders.push(`${el.nodeName}[${attr.name}]="${attr.value}"`)
      }
    })
    expect(offenders).toEqual([])
    expect(container.textContent).not.toContain('NaN')
    expect(screen.getAllByTestId('price-candle')).toHaveLength(3)
  })

  it('says why there is no history, and draws nothing, for market_unsupported', () => {
    render(<PriceChart bars={[]} unavailable="market_unsupported" />)
    expect(screen.getByText('quote.noHistory')).toBeTruthy()
    expect(screen.queryByText('quote.noHistorySrc')).toBeNull()
    expect(screen.queryByTestId('price-chart')).toBeNull()
  })

  it('says why there is no history, and draws nothing, for source_failed', () => {
    render(<PriceChart bars={[]} unavailable="source_failed" />)
    expect(screen.getByText('quote.noHistorySrc')).toBeTruthy()
    expect(screen.queryByText('quote.noHistory')).toBeNull()
    expect(screen.queryByTestId('price-chart')).toBeNull()
  })

  it('renders an Empty, not an axis frame, for an empty series with no stated reason', () => {
    render(<PriceChart bars={[]} />)
    expect(screen.getByTestId('price-chart-empty')).toBeTruthy()
    expect(screen.queryByTestId('price-chart')).toBeNull()
  })

  it('reads out the bar under the pointer', () => {
    render(<PriceChart bars={BARS} />)
    const readout = screen.getByTestId('price-readout')
    // Nothing pointed at yet: the newest session, so the panel is never blank.
    expect(readout.textContent).toContain('2026-09-04')

    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[1])
    const text = screen.getByTestId('price-readout').textContent ?? ''
    expect(text).toContain('2026-08-31')
    expect(text).toContain('33.00') // open, 3300 分
    expect(text).toContain('33.75') // high
    expect(text).toContain('32.60') // low
    expect(text).toContain('33.67') // close
    expect(text).toContain('296,639,900')
    expect(text).not.toContain('2026-09-04')
  })

  it('walks the series with the arrow keys, not only with a pointer', () => {
    render(<PriceChart bars={BARS} />)
    const chart = screen.getByRole('img', { name: 'quote.chartLabel' })

    // From nothing selected, ArrowLeft steps back off the newest bar.
    fireEvent.keyDown(chart, { key: 'ArrowLeft' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-03')

    fireEvent.keyDown(chart, { key: 'ArrowLeft' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-02')

    fireEvent.keyDown(chart, { key: 'ArrowRight' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-03')

    fireEvent.keyDown(chart, { key: 'Home' })
    const first = screen.getByTestId('price-readout').textContent ?? ''
    expect(first).toContain('2026-08-28')
    expect(first).toContain('34.65')

    fireEvent.keyDown(chart, { key: 'End' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-04')
  })

  it('does not run off either end of the series', () => {
    render(<PriceChart bars={BARS} />)
    const chart = screen.getByRole('img', { name: 'quote.chartLabel' })
    fireEvent.keyDown(chart, { key: 'Home' })
    for (let i = 0; i < 4; i += 1) fireEvent.keyDown(chart, { key: 'ArrowLeft' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-08-28')
    for (let i = 0; i < 20; i += 1) fireEvent.keyDown(chart, { key: 'ArrowRight' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-04')
  })

  it('thins the date ticks so they cannot overlap, and always labels the newest bar', () => {
    // A year of sessions in the same fixed viewBox: every tick must still clear a date label.
    const many: QuoteBar[] = Array.from({ length: 244 }, (_, i) => ({
      d: new Date(Date.UTC(2026, 0, 1) + i * 86_400_000).toISOString().slice(0, 10),
      o: 1000 + i,
      c: 1010 + i,
      h: 1020 + i,
      l: 990 + i,
      v: 1000 + i,
    }))
    render(<PriceChart bars={many} />)
    const ticks = screen.getAllByTestId('price-xtick')
    const xs = ticks.map((el) => Number(el.getAttribute('x')))
    for (let i = 1; i < xs.length; i += 1) expect(xs[i] - xs[i - 1]).toBeGreaterThanOrEqual(76)
    expect(ticks[ticks.length - 1].textContent).toBe(many[many.length - 1].d)
  })

  it('survives a single bar', () => {
    render(<PriceChart bars={[BARS[BARS.length - 1]]} />)
    expect(screen.getAllByTestId('price-candle')).toHaveLength(1)
    expect(screen.getByTestId('price-readout').textContent).toContain('33.35')
  })

  it('re-derives the selected bar when the series shrinks under it', () => {
    // The range switcher swaps a 1y series for a 1m one under a live selection, so an index taken
    // against the old array outlives it. Nothing else in this file re-renders with a different
    // bars prop, and reading bars[5] out of a two-bar array throws during render — which unmounts
    // the whole reading page, not merely the chart.
    const { rerender } = render(<PriceChart bars={BARS} />)
    fireEvent.keyDown(screen.getByRole('img', { name: 'quote.chartLabel' }), { key: 'End' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-04')

    rerender(<PriceChart bars={BARS.slice(0, 2)} />)
    const text = screen.getByTestId('price-readout').textContent ?? ''
    expect(text).toContain('2026-08-31')
    expect(text).toContain('33.67')
    expect(text).not.toContain('undefined')
  })

  it('formats money the same way as the strip above it, separators and all', () => {
    // A seven-figure amount is the only kind that can tell the two formatters apart: the digits
    // agree, the grouping does not. A local (fen / 100).toFixed(2) here would print 1234567.89 in
    // the readout while the strip a few lines above printed 1,234,567.89 for the same money.
    const rich: QuoteBar[] = [
      { d: '2026-09-03', o: 123400000, c: 123450000, h: 123460000, l: 123390000, v: 1200 },
      { d: '2026-09-04', o: 123450000, c: 123456789, h: 123460000, l: 123440000, v: 900 },
    ]
    render(<PriceChart bars={rich} />)
    const text = screen.getByTestId('price-readout').textContent ?? ''
    expect(text).toContain('1,234,567.89')
    expect(text).not.toContain('1234567.89')
  })

  it('shows a spinner rather than an empty frame while the series is on the wire', () => {
    render(<PriceChart bars={[]} loading />)
    expect(screen.getByTestId('price-chart-loading')).toBeTruthy()
    expect(screen.queryByTestId('price-chart')).toBeNull()
    expect(screen.queryByTestId('price-chart-empty')).toBeNull()
  })

  it('draws a zero-volume session without collapsing the volume scale', () => {
    const withHalt: QuoteBar[] = [
      { d: '2026-09-03', o: 3325, c: 3331, h: 3366, l: 3295, v: 169589800 },
      { d: '2026-09-04', o: 3385, c: 3335, h: 3405, l: 3307, v: 0 },
    ]
    const { container } = render(<PriceChart bars={withHalt} />)
    const vols = screen.getAllByTestId('price-volume')
    expect(vols[1].getAttribute('height')).toBe('0')
    expect(Number(vols[0].getAttribute('height'))).toBeGreaterThan(0)
    container.querySelectorAll('*').forEach((el) => {
      for (const attr of Array.from(el.attributes)) expect(attr.value).not.toContain('NaN')
    })
  })
})
