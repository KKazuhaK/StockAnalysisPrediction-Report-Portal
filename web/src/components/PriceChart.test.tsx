import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act, within } from '@testing-library/react'
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

// The chart's geometry is now a function of a MEASURED width, and jsdom performs no layout: every
// box is 0x0 and the ResizeObserver the setup file installs is inert. Without a controllable
// observer every test below would assert against the skeleton. This fake is the only input the
// chart's width has, which is also what makes "the height does not follow the width" testable —
// the width is set from here, twice, and the height must not move.
interface FakeEntry {
  target: Element
  contentRect: { width: number; height: number }
}
type FakeCallback = (entries: FakeEntry[], observer: unknown) => void

const observers: { cb: FakeCallback; targets: Element[] }[] = []

class FakeResizeObserver {
  private entry: { cb: FakeCallback; targets: Element[] }

  constructor(cb: FakeCallback) {
    this.entry = { cb, targets: [] }
    observers.push(this.entry)
  }

  observe(el: Element) {
    this.entry.targets.push(el)
  }

  unobserve(el: Element) {
    this.entry.targets = this.entry.targets.filter((t) => t !== el)
  }

  disconnect() {
    this.entry.targets = []
    const i = observers.indexOf(this.entry)
    if (i >= 0) observers.splice(i, 1)
  }
}

;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = FakeResizeObserver

beforeEach(() => {
  observers.length = 0
})

/** Report `px` to every live observer, the way a browser reports a resized container. */
function setWidth(px: number) {
  act(() => {
    for (const o of [...observers]) {
      const entries = o.targets.map((target) => ({ target, contentRect: { width: px, height: 0 } }))
      if (entries.length > 0) o.cb(entries, null)
    }
  })
}

/** Render and immediately hand the chart a container width, as a laid-out browser would. */
function renderChart(ui: React.ReactElement, width = 900) {
  const result = render(ui)
  setWidth(width)
  return result
}

/** Every attribute of every node, so an unparseable coordinate cannot hide in one of them. */
function nanAttributes(container: HTMLElement): string[] {
  const offenders: string[] = []
  container.querySelectorAll('*').forEach((el) => {
    for (const attr of Array.from(el.attributes)) {
      if (attr.value.includes('NaN')) offenders.push(`${el.nodeName}[${attr.name}]="${attr.value}"`)
    }
  })
  return offenders
}

/** The x of the vertical crosshair — the bar the chart believes is selected, in CSS pixels. */
function crosshairX(): number {
  const line = screen.getByTestId('price-crosshair').querySelector('line')
  return Number(line?.getAttribute('x1'))
}

function tooltipLeft(): number {
  return Number.parseFloat(screen.getByTestId('price-tooltip').style.left)
}

// jsdom has no PointerEvent, and fireEvent.pointerMove therefore cannot carry a clientY. A
// MouseEvent typed 'pointermove' can, and React dispatches on the event's type, so this is the only
// way to test the half of the crosshair that follows the pointer rather than the bar.
function pointerMoveAt(el: Element, clientY: number) {
  fireEvent(el, new MouseEvent('pointermove', { bubbles: true, clientY }))
}

/** Give the chart a real box, since jsdom gives every element a 0x0 one. */
function stubBox(el: Element, width: number, height: number) {
  el.getBoundingClientRect = () =>
    ({
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: width,
      bottom: height,
      width,
      height,
      toJSON: () => ({}),
    }) as DOMRect
}

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
    renderChart(<PriceChart bars={BARS} />)
    expect(screen.getAllByTestId('price-candle')).toHaveLength(BARS.length)
    expect(screen.getAllByTestId('price-volume')).toHaveLength(BARS.length)
  })

  it('paints an up bar red and a down bar green, the A-share way round', () => {
    renderChart(<PriceChart bars={BARS} />)
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
    renderChart(<PriceChart bars={BARS} />)
    expect(screen.getByRole('img', { name: 'quote.chartLabel' })).toBeTruthy()
  })

  // THE REGRESSION THIS WHOLE COMPONENT WAS REWRITTEN FOR. The chart used to be a fixed 720x300
  // viewBox scaled by width:100%/height:auto, so its height was its width times 0.417: a 1900 px
  // reading column rendered it 792 px tall and pushed the report off the screen. A reader reported
  // it as "the chart takes over the whole screen". Height is now a prop and nothing measured may
  // move it.
  it('is as tall as the caller asked, at every measured width', () => {
    const { rerender } = renderChart(<PriceChart bars={BARS} height={260} />, 900)
    const svg = screen.getByTestId('price-chart')
    expect(svg.getAttribute('viewBox')).toBe('0 0 900 260')
    expect(svg.style.height).toBe('260px')

    // The container more than doubles. One SVG unit is one CSS pixel, so the viewBox widens with
    // it — and the height in that same viewBox, and in the CSS, must not move by one pixel.
    setWidth(1900)
    expect(svg.getAttribute('viewBox')).toBe('0 0 1900 260')
    expect(svg.style.height).toBe('260px')

    setWidth(320)
    expect(svg.getAttribute('viewBox')).toBe('0 0 320 260')
    expect(svg.style.height).toBe('260px')

    // And the quotes app's taller chart is taller because it asked to be, not because it is wide.
    rerender(<PriceChart bars={BARS} height={520} />)
    setWidth(1900)
    expect(screen.getByTestId('price-chart').getAttribute('viewBox')).toBe('0 0 1900 520')
    expect(screen.getByTestId('price-chart').style.height).toBe('520px')
  })

  it('lays the drawing out in the measured pixels, not in a constant', () => {
    renderChart(<PriceChart bars={BARS} />, 900)
    const narrow = screen.getAllByTestId('price-candle').map((g) => Number(g.querySelector('rect')?.getAttribute('x')))
    setWidth(1900)
    const wide = screen.getAllByTestId('price-candle').map((g) => Number(g.querySelector('rect')?.getAttribute('x')))
    // Same series, wider box: the last candle has to move right. A drawing that ignored the
    // measurement would place it identically and the viewBox would just be scaled — which is the
    // old aspect-ratio behaviour wearing new numbers.
    expect(wide[wide.length - 1]).toBeGreaterThan(narrow[narrow.length - 1] + 100)
    expect(narrow.every((x) => Number.isFinite(x))).toBe(true)
  })

  it('renders a skeleton of the requested height, and no chart, before it has been measured', () => {
    // Zero is what a collapsed container, a hidden tab and the first paint all report. Nothing may
    // divide by it, and an axis frame drawn in a zero-width box is a row of ticks on top of itself.
    const { container } = render(<PriceChart bars={BARS} height={300} />)
    const skeleton = screen.getByTestId('price-chart-skeleton')
    expect(skeleton.style.height).toBe('300px')
    expect(screen.queryByTestId('price-chart')).toBeNull()
    expect(nanAttributes(container)).toEqual([])

    // …and it becomes the chart, at the same height, the moment the container has a width.
    setWidth(640)
    expect(screen.queryByTestId('price-chart-skeleton')).toBeNull()
    expect(screen.getByTestId('price-chart').getAttribute('viewBox')).toBe('0 0 640 300')
  })

  it('emits no NaN at a zero measurement, a zero price range or a single bar', () => {
    const flat: QuoteBar[] = [
      { d: '2026-09-02', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
      { d: '2026-09-03', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
      { d: '2026-09-04', o: 1000, c: 1000, h: 1000, l: 1000, v: 0 },
    ]
    const cases: Array<[string, QuoteBar[], number]> = [
      ['unmeasured', BARS, 0],
      ['flat window', flat, 900],
      ['single bar', [BARS[0]], 900],
      ['single bar, narrow', [BARS[0]], 90],
    ]
    for (const [name, bars, width] of cases) {
      const { container, unmount } = render(<PriceChart bars={bars} />)
      if (width > 0) setWidth(width)
      // Hovering is where the crosshair, the chips and the card get their coordinates, so the scan
      // has to run with a selection live rather than only on the resting chart.
      const hits = screen.queryAllByTestId('price-hit')
      if (hits.length > 0) fireEvent.pointerMove(hits[hits.length - 1])
      expect(nanAttributes(container), name).toEqual([])
      expect(container.textContent, name).not.toContain('NaN')
      expect(container.textContent, name).not.toContain('Infinity')
      unmount()
    }
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
    const { container } = renderChart(<PriceChart bars={flat} />)
    expect(nanAttributes(container)).toEqual([])
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
    renderChart(<PriceChart bars={BARS} />)
    const readout = screen.getByTestId('price-readout')
    // Nothing pointed at yet: the newest session, so a screen reader arriving at the chart is
    // told where it is rather than nothing at all.
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

  // The user-visible half of the same complaint: "hovering a data point does nothing". It did
  // something — it updated a strip below an 800 px-tall chart, off the bottom of the screen.
  it('shows no tooltip until the pointer arrives, then the hovered bar in full', () => {
    renderChart(<PriceChart bars={BARS} />)
    expect(screen.queryByTestId('price-tooltip')).toBeNull()

    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[1])
    const tip = screen.getByTestId('price-tooltip')
    expect(within(tip).getByTestId('tt-date').textContent).toBe('2026-08-31')
    expect(within(tip).getByTestId('tt-open').textContent).toBe('33.00')
    expect(within(tip).getByTestId('tt-high').textContent).toBe('33.75')
    expect(within(tip).getByTestId('tt-low').textContent).toBe('32.60')
    expect(within(tip).getByTestId('tt-close').textContent).toBe('33.67')
    // 33.67 against the previous bar's 34.65 close: -0.98, -2.83%. Not a vendor number and not
    // available as one — there is no percentage column on a `day` row.
    expect(within(tip).getByTestId('tt-change').textContent).toBe('-0.98')
    expect(within(tip).getByTestId('tt-changepct').textContent).toBe('-2.83%')
    expect(within(tip).getByTestId('tt-volume').textContent).toContain('296,639,900')
    // A card that took the pointer would end the hover that drew it, on every move.
    expect(tip.style.pointerEvents).toBe('none')

    // Both scales carry the crosshair's own readout, the way every terminal does it.
    expect(screen.getByTestId('price-xchip').textContent).toBe('2026-08-31')
    expect(screen.getByTestId('price-ychip').textContent).toBe('33.67')
  })

  it('flips the tooltip to the other side of the crosshair at the right edge', () => {
    renderChart(<PriceChart bars={BARS} />, 900)
    const hits = screen.getAllByTestId('price-hit')

    fireEvent.pointerMove(hits[0])
    expect(screen.getByTestId('price-tooltip').getAttribute('data-flip')).toBe('right')
    expect(tooltipLeft()).toBeGreaterThan(crosshairX())

    // The newest bars are the ones a reader hovers most, and they are the ones with no room to
    // the right. The card has to change sides rather than hang off the container.
    fireEvent.pointerMove(hits[hits.length - 1])
    expect(screen.getByTestId('price-tooltip').getAttribute('data-flip')).toBe('left')
    const left = tooltipLeft()
    expect(left).toBeGreaterThanOrEqual(0)
    // 184 is the card's width. The whole card has to be on the far side of the crosshair, which is
    // a stronger statement than "inside the container": merely clamping the card to the right edge
    // also keeps it in the box, and leaves it sitting on top of the bar and the pointer.
    expect(left + 184).toBeLessThanOrEqual(crosshairX())
    expect(left + 184).toBeLessThanOrEqual(900)

    // Vertically it is clamped into the chart whatever the crosshair is doing.
    const top = Number.parseFloat(screen.getByTestId('price-tooltip').style.top)
    expect(top).toBeGreaterThanOrEqual(0)
    expect(top).toBeLessThanOrEqual(260)
  })

  it('reports no change for the first bar rather than inventing a previous close', () => {
    renderChart(<PriceChart bars={BARS} />)
    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[0])
    const tip = screen.getByTestId('price-tooltip')
    // The oldest bar's real previous close exists in the market and is not in this window. Its own
    // open standing in for it (34.50 against a 34.65 close) prints a perfectly plausible +0.15 /
    // +0.43% that is not the close-to-close move every other row in this column is.
    expect(within(tip).getByTestId('tt-change').textContent).toBe('—')
    expect(within(tip).getByTestId('tt-changepct').textContent).toBe('—')
    expect(within(tip).getByTestId('tt-close').textContent).toBe('34.65')
    expect(tip.textContent).not.toContain('+0.15')
    expect(tip.textContent).not.toContain('0.43%')
  })

  it('walks the series with the arrow keys, not only with a pointer', () => {
    renderChart(<PriceChart bars={BARS} />)
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

  it('shows the same card for a keyboard selection, positioned at the focused bar', () => {
    renderChart(<PriceChart bars={BARS} />)
    const chart = screen.getByRole('img', { name: 'quote.chartLabel' })

    // At rest the live region labels the newest bar and says nothing about the move — the strip
    // above the chart is describing that same session, and a screen reader should not hear the
    // day's 涨跌幅 twice.
    expect(screen.getByTestId('price-readout').textContent).not.toContain('quote.changePct')

    fireEvent.keyDown(chart, { key: 'Home' })
    fireEvent.keyDown(chart, { key: 'ArrowRight' })
    const tip = screen.getByTestId('price-tooltip')
    expect(within(tip).getByTestId('tt-date').textContent).toBe('2026-08-31')
    expect(within(tip).getByTestId('tt-open').textContent).toBe('33.00')
    expect(within(tip).getByTestId('tt-close').textContent).toBe('33.67')
    expect(within(tip).getByTestId('tt-change').textContent).toBe('-0.98')
    expect(within(tip).getByTestId('tt-changepct').textContent).toBe('-2.83%')

    // Everything the card shows a pointer user, the live region says to a screen reader — the card
    // is a pointer affordance and is announced by nothing.
    const spoken = screen.getByTestId('price-readout').textContent ?? ''
    expect(spoken).toContain('2026-08-31')
    expect(spoken).toContain('33.67')
    expect(spoken).toContain('-0.98')
    expect(spoken).toContain('-2.83%')

    // Positioned at the bar the keyboard is on, not left at the last pointer position.
    const at31 = tooltipLeft()
    expect(Math.abs(at31 - crosshairX())).toBeLessThan(220)
    fireEvent.keyDown(chart, { key: 'End' })
    expect(within(screen.getByTestId('price-tooltip')).getByTestId('tt-date').textContent).toBe('2026-09-04')
    expect(tooltipLeft()).not.toBe(at31)

    // Escape puts the chart back to resting.
    fireEvent.keyDown(chart, { key: 'Escape' })
    expect(screen.queryByTestId('price-tooltip')).toBeNull()
  })

  it('follows the pointer up and down the price axis, and falls back to the close off it', () => {
    renderChart(<PriceChart bars={BARS} />, 900)
    const svg = screen.getByTestId('price-chart')
    stubBox(svg, 900, 260)
    const hit = screen.getAllByTestId('price-hit')[3]

    // The price panel runs from y=16 to y=176 at this height. Higher on the axis is a higher price:
    // if the crosshair ignored the pointer, both of these would read the bar's close instead.
    pointerMoveAt(hit, 40)
    const high = Number(screen.getByTestId('price-ychip').textContent?.replace(/,/g, ''))
    pointerMoveAt(hit, 150)
    const low = Number(screen.getByTestId('price-ychip').textContent?.replace(/,/g, ''))
    expect(high).toBeGreaterThan(low)
    // Both are prices inside the window's padded range, not screen coordinates.
    expect(low).toBeGreaterThan(30)
    expect(high).toBeLessThan(36)

    // Below the price panel the pointer is over volume, where there is no price to name, so the
    // chip goes back to the bar's own close rather than labelling the axis with the panel floor.
    pointerMoveAt(hit, 230)
    expect(screen.getByTestId('price-ychip').textContent).toBe('32.95')
  })

  it('does not run off either end of the series', () => {
    renderChart(<PriceChart bars={BARS} />)
    const chart = screen.getByRole('img', { name: 'quote.chartLabel' })
    fireEvent.keyDown(chart, { key: 'Home' })
    for (let i = 0; i < 4; i += 1) fireEvent.keyDown(chart, { key: 'ArrowLeft' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-08-28')
    for (let i = 0; i < 20; i += 1) fireEvent.keyDown(chart, { key: 'ArrowRight' })
    expect(screen.getByTestId('price-readout').textContent).toContain('2026-09-04')
  })

  it('thins the date ticks so they cannot overlap, at any measured width', () => {
    // A year of sessions. The no-overlap promise used to hold because the viewBox was a constant
    // and label width and bar spacing were in the same made-up units. Now both are real pixels, so
    // the same inequality has to hold at each width the container can actually be.
    const many: QuoteBar[] = Array.from({ length: 244 }, (_, i) => ({
      d: new Date(Date.UTC(2026, 0, 1) + i * 86_400_000).toISOString().slice(0, 10),
      o: 1000 + i,
      c: 1010 + i,
      h: 1020 + i,
      l: 990 + i,
      v: 1000 + i,
    }))
    renderChart(<PriceChart bars={many} />, 900)
    for (const width of [320, 900, 1900]) {
      setWidth(width)
      const ticks = screen.getAllByTestId('price-xtick')
      const xs = ticks.map((el) => Number(el.getAttribute('x')))
      for (let i = 1; i < xs.length; i += 1) expect(xs[i] - xs[i - 1], `width ${width}`).toBeGreaterThanOrEqual(76)
      expect(ticks[ticks.length - 1].textContent).toBe(many[many.length - 1].d)
      // A wider box shows more of them; a fixed viewBox would have shown the same count forever.
      expect(xs[xs.length - 1]).toBeLessThanOrEqual(width)
    }
  })

  it('survives a single bar', () => {
    renderChart(<PriceChart bars={[BARS[BARS.length - 1]]} />)
    expect(screen.getAllByTestId('price-candle')).toHaveLength(1)
    expect(screen.getByTestId('price-readout').textContent).toContain('33.35')
  })

  it('re-derives the selected bar when the series shrinks under it', () => {
    // The range switcher swaps a 1y series for a 1m one under a live selection, so an index taken
    // against the old array outlives it. Nothing else in this file re-renders with a different
    // bars prop, and reading bars[5] out of a two-bar array throws during render — which unmounts
    // the whole reading page, not merely the chart.
    const { rerender } = renderChart(<PriceChart bars={BARS} />)
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
    renderChart(<PriceChart bars={rich} />)
    const text = screen.getByTestId('price-readout').textContent ?? ''
    expect(text).toContain('1,234,567.89')
    expect(text).not.toContain('1234567.89')

    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[1])
    expect(within(screen.getByTestId('price-tooltip')).getByTestId('tt-close').textContent).toContain('1,234,567.89')
  })

  it('names the currency on the axis and in the card, from the frozen keys only', () => {
    // A US instrument's 190.00 under a 元 label is wrong by a factor of seven, and the strip that
    // carries the currency is not always on the same screen as the chart.
    const { rerender } = renderChart(<PriceChart bars={BARS} currency="USD" />)
    expect(screen.getByTestId('price-currency').textContent).toBe('quote.currency.USD')
    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[1])
    expect(within(screen.getByTestId('price-tooltip')).getByTestId('tt-close').textContent).toContain(
      'quote.currency.USD',
    )

    rerender(<PriceChart bars={BARS} currency="HKD" />)
    expect(screen.getByTestId('price-currency').textContent).toBe('quote.currency.HKD')

    // i18next echoes a key it has no string for, so anything outside the three frozen keys prints
    // no unit at all rather than `quote.currency.XYZ` on the price axis.
    rerender(<PriceChart bars={BARS} currency="XYZ" />)
    expect(screen.queryByTestId('price-currency')).toBeNull()
    expect(screen.getByTestId('price-chart').textContent).not.toContain('quote.currency')
  })

  it('falls back to the default height for one it cannot draw with', () => {
    // `height: number` does not keep NaN or Infinity out of it. The quotes app derives the number
    // from window.innerHeight, and a detached document, a hidden tab and jsdom all measure that as
    // something other than a length. Nothing below re-checks the prop, so an unusable value flows
    // into the viewBox and out through every y in the drawing — a chart of NaNs, rendered without
    // a single error in the console.
    const { container, rerender } = renderChart(<PriceChart bars={BARS} height={Number.NaN} />, 900)
    expect(screen.getByTestId('price-chart').getAttribute('viewBox')).toBe('0 0 900 260')
    expect(screen.getByTestId('price-chart').style.height).toBe('260px')
    expect(nanAttributes(container)).toEqual([])

    rerender(<PriceChart bars={BARS} height={Number.POSITIVE_INFINITY} />)
    expect(screen.getByTestId('price-chart').getAttribute('viewBox')).toBe('0 0 900 260')
    expect(nanAttributes(container)).toEqual([])
  })

  it('drops the turnover estimate rather than printing it as a shape', () => {
    // The turnover row is the one number on this chart that is a PRODUCT of two integers off the
    // wire rather than one of them, so it is the only one that can leave the exact-integer range.
    // These are the server's own ceilings — quotePriceCeiling 10^12 分 and roughly quoteMaxLots
    // shares — which no real body comes within twelve orders of magnitude of, but which the parser
    // does admit. c x v / 100 is then 9e26: past 2^53 it is no longer an exact integer and past
    // 1e21 String() gives exponential, so groupDigits returns the ungrouped "9e+26" and the ≈ ends
    // up disclaiming the precision of something that is not a number a person can read at all.
    const absurd: QuoteBar[] = [
      { d: '2026-09-03', o: 1000000000000, c: 1000000000000, h: 1000000000000, l: 1000000000000, v: 1 },
      { d: '2026-09-04', o: 1000000000000, c: 1000000000000, h: 1000000000000, l: 1000000000000, v: 90000000000000000 },
    ]
    const { unmount } = renderChart(<PriceChart bars={absurd} />)
    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[1])
    const wild = screen.getByTestId('price-tooltip')
    expect(within(wild).queryByTestId('tt-amount')).toBeNull()
    expect(wild.textContent).not.toMatch(/e\+/)
    // One row is dropped, not the card: everything that IS an integer off the wire still prints,
    // including the nine-hundred-quadrillion share count, which groups fine.
    expect(within(wild).getByTestId('tt-close').textContent).toContain('10,000,000,000.00')
    expect(within(wild).getByTestId('tt-volume').textContent).toContain('90,000,000,000,000,000')
    unmount()

    // And an ordinary session still carries the estimate, so the guard above cannot quietly become
    // "this chart has no turnover row".
    renderChart(<PriceChart bars={BARS} />)
    fireEvent.pointerMove(screen.getAllByTestId('price-hit')[DOWN_INDEX])
    const real = within(screen.getByTestId('price-tooltip')).getByTestId('tt-amount')
    // close x volume / 100, grouped — and the ≈ that says it is close x volume and not the
    // vendor's own 成交额, which no `day` row carries.
    expect(real.textContent).toBe('≈ 5,900,752,235')
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
    const { container } = renderChart(<PriceChart bars={withHalt} />)
    const vols = screen.getAllByTestId('price-volume')
    expect(vols[1].getAttribute('height')).toBe('0')
    expect(Number(vols[0].getAttribute('height'))).toBeGreaterThan(0)
    expect(nanAttributes(container)).toEqual([])
  })
})
