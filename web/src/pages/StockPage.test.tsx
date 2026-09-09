import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import StockPage from './StockPage'

// StockPage renders a loading spinner first (data null), then re-renders with the report.
// A hook called after those early returns (Grid.useBreakpoint) would run on the second
// render but not the first — "rendered more hooks than during the previous render" — and
// blank the page. This mounts through that transition to guard against the regression.
// The reading pages render an "edit this report" button, which asks the session what it may do.
// In the app they only ever mount inside <Protected>, so the context is always there; here it is not.
vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, can: () => true, logout: vi.fn() }),
}))
// The page now issues TWO independent requests, so the mock has to answer by URL. A mock that
// returns the report shape for every call hands the quote panel a body with no snapshot, which is
// not a shape any build of the server can send — it would fail the page for a reason that exists
// only in the test.
const REPORT = {
  symbol: '001238',
  name: 'Test Co',
  selDate: '2026-07-07',
  selKind: 'Research',
  selId: 1,
  // Two of each so both strips render: one option apiece and they are hidden.
  kinds: ['Research', 'Trading'],
  subtabs: [
    { label: 'Sub', id: 1 },
    { label: 'Sentiment', id: 2 },
  ],
  timeline: [],
  rep: { id: 1, name: 'Test Co', title: 'Report Title', displayTitle: '001238 Test Co Report Title', date: '2026-07-07', source: 'x', html: '', md: '# hi', time: '' },
}
// Real numbers, in 分, from internal/app/testdata/quote/tencent_fqkline_sh601899.json.
const QUOTE = {
  symbol: '001238',
  name: 'Test Co',
  market: 'sz',
  kind: 'stock',
  currency: 'CNY',
  tz: 'Asia/Shanghai',
  source: 'tencent',
  snapshot: {
    last: 3335, prevClose: 3331, open: 3385, high: 3405, low: 3307,
    change: 4, changePct: '0.12', volume: 176934100, amount: 5939060556,
    asOf: '2026-09-04T16:14:58+08:00', session: 'close',
  },
  bars: [
    { d: '2026-09-03', o: 3325, h: 3366, l: 3295, c: 3331, v: 169589800 },
    { d: '2026-09-04', o: 3385, h: 3405, l: 3307, c: 3335, v: 176934100 },
  ],
  barsSource: 'tencent',
  barsUnavailable: '',
  adjusted: false,
  cached: false,
}
const quoteCalls: string[] = []
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (String(url).startsWith('/api/quote/')) {
        quoteCalls.push(String(url))
        return Promise.resolve(QUOTE)
      }
      return Promise.resolve(REPORT)
    },
  },
  qs: (o: Record<string, string>) => {
    const p = new URLSearchParams(o).toString()
    return p ? `?${p}` : ''
  },
  ApiError: class extends Error {},
}))

// Desktop width. Without this jsdom reports no breakpoints at all, so `compact` is true and a test
// about the DESKTOP layout would pass against the very code it is meant to reject.
vi.mock('antd', async (importOriginal) => {
  const actual = await importOriginal<typeof import('antd')>()
  return { ...actual, Grid: { ...actual.Grid, useBreakpoint: () => ({ xs: true, sm: true, md: true, lg: true, xl: true }) } }
})

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('react-router', () => ({
  useParams: () => ({ symbol: '001238' }),
  useSearchParams: () => [new URLSearchParams('date=2026-07-07'), vi.fn()],
  useNavigate: () => vi.fn(),
  // A real anchor, because the assertion below is about the href a reader would middle-click.
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../lib/datetime', () => ({ isInstant: () => false, formatReportDateTime: (s: string) => s }))
vi.mock('../components/Markdown', () => ({ default: () => <div>md</div> }))
vi.mock('../components/TimelinePanel', () => ({ default: () => <div>timeline</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/CompareModal', () => ({
  default: ({ open, reportId }: { open: boolean; reportId: number }) => (open ? <div>compare-open:{reportId}</div> : null),
}))
// The chart stands in for itself so this suite can read the ONE prop that is the whole point of
// this change: a height in CSS pixels, chosen by the page rather than derived from the container
// width. It also makes "is the chart on screen" a question with a single unambiguous answer — the
// real component renders a measurement skeleton under jsdom, and antd's icons put <svg> elements
// all over the page, so `querySelector('svg')` would answer yes with no chart mounted at all.
const chartProps: { bars: unknown[]; height?: number; currency?: string }[] = []
vi.mock('../components/PriceChart', () => ({
  default: (p: { bars: unknown[]; height?: number; currency?: string }) => {
    chartProps.push(p)
    return <div data-testid="chart">{`chart:${p.bars.length}`}</div>
  },
}))
const lastChart = () => chartProps[chartProps.length - 1]

vi.mock('../components/ExportButtons', () => ({
  ExportPdfButton: () => <div>pdf</div>,
  ExportDayButton: () => <div>day</div>,
  ExportMenu: () => <div>export-menu</div>,
}))

// The open/closed choice is persisted per browser, so without this one test's click decides the
// next test's first render.
beforeEach(() => {
  chartProps.length = 0
  // Counted per test below ("expanding costs no second fetch"), so it cannot be cumulative.
  quoteCalls.length = 0
  delete (QUOTE as typeof QUOTE & { refreshAfterSecs?: number }).refreshAfterSecs
  try {
    window.localStorage.clear()
  } catch {
    /* nothing stored, nothing to clear */
  }
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('StockPage', () => {
  it('renders the report after data loads (no hook-order crash)', async () => {
    render(<StockPage />)
    // Reaching the report heading proves the component rendered past the loading→loaded
    // transition without a hooks-count mismatch. The heading uses the server-composed
    // displayTitle (company name folded in), not the bare stored title.
    expect(await screen.findByText('001238 Test Co Report Title')).toBeTruthy()
    expect(screen.getByText('stock.back')).toBeTruthy()
  })

  // The compare button set state that nothing read: CompareModal was imported and never rendered,
  // so the button was inert. Clicking it has to put the modal on screen.
  it('opens the compare modal when the compare button is pressed', async () => {
    render(<StockPage />)
    const btn = await screen.findByText('compare.button')
    expect(screen.queryByText(/compare-open/)).toBeNull()
    await userEvent.click(btn)
    expect(await screen.findByText(/compare-open:1/)).toBeTruthy()
  })

  // rc-segmented defaults each item's `title` to its own label text, so the browser drew a native
  // tooltip repeating the button — flush under it, reading as though the two were one control stuck
  // together. These strips scroll instead of truncating, so the label is always fully visible and
  // the tooltip could only ever restate it.
  it('does not repeat a category or report-type label as a native tooltip', async () => {
    render(<StockPage />)
    for (const label of ['Trading', 'Sentiment']) {
      const el = await screen.findByText(label)
      expect(el.getAttribute('title') ?? '', `${label} tooltip`).toBe('')
    }
  })

  // Both quote components shipped orphaned once: written, tested in isolation, imported by nothing,
  // and therefore tree-shaken straight out of the bundle. These two tests are the wiring itself —
  // they fail if the panel is ever unhooked from the page again.
  it('requests the quote for the symbol on the page, separately from the report', async () => {
    render(<StockPage />)
    await screen.findByText('001238 Test Co Report Title')
    expect(quoteCalls.some((u) => u.startsWith('/api/quote/001238'))).toBe(true)
  })

  it('refreshes the quote on an idle visible reading page when the server advises it', async () => {
    vi.useFakeTimers()
    ;(QUOTE as typeof QUOTE & { refreshAfterSecs?: number }).refreshAfterSecs = 2
    render(<StockPage />)
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(quoteCalls).toHaveLength(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(quoteCalls).toHaveLength(2)
  })

  it('renders the quote strip beside the report', async () => {
    render(<StockPage />)
    await screen.findByText('001238 Test Co Report Title')
    // The strip renders the vendor's own percentage string verbatim, so finding it proves the real
    // component mounted with the real payload rather than a placeholder.
    expect(await screen.findByText(/0\.12/)).toBeTruthy()
    expect(screen.getByTestId('quote-strip')).toBeTruthy()
  })

  // A reader opened a report and got a chart filling the viewport with the report entirely below
  // the fold. The report is what this page is for; the chart is a decoration on it, and a
  // decoration does not get the screen before the thing it decorates.
  it('opens on the report, with the chart not rendered at all', async () => {
    render(<StockPage />)
    expect(await screen.findByText('001238 Test Co Report Title')).toBeTruthy()
    expect(screen.getByText('md')).toBeTruthy()
    expect(screen.queryByTestId('chart')).toBeNull()
    expect(chartProps).toHaveLength(0)
    expect(screen.getByRole('button', { name: /quote\.expandChart/ })).toBeTruthy()
  })

  it('draws the chart on request, at a height in pixels rather than one derived from the width', async () => {
    render(<StockPage />)
    await userEvent.click(await screen.findByRole('button', { name: /quote\.expandChart/ }))
    expect(await screen.findByTestId('chart')).toBeTruthy()
    // The bug this replaced: a 720x300 viewBox at width:100%/height:auto made the height a
    // function of the width, so a wide monitor drew a 792px chart. A number here — and a modest
    // one — is the fix, so the assertion is on the number and not merely on the prop existing.
    expect(lastChart().height).toBe(260)
    // The bars came back with the snapshot in the same response; expanding must not cost a second
    // round trip to the vendor.
    expect(quoteCalls.filter((u) => u.startsWith('/api/quote/001238'))).toHaveLength(1)
  })

  it('remembers the choice for the next report this browser opens', async () => {
    const first = render(<StockPage />)
    await userEvent.click(await screen.findByRole('button', { name: /quote\.expandChart/ }))
    await screen.findByTestId('chart')
    first.unmount()

    // A remount is the next reading page: the reader who wants a chart every time pays one click,
    // once, not one click per report.
    const second = render(<StockPage />)
    expect(await second.findByTestId('chart')).toBeTruthy()
    await userEvent.click(second.getByRole('button', { name: /quote\.collapseChart/ }))
    second.unmount()

    // And closing it again has to stick too — a stored "closed" is a choice, not an absent key.
    render(<StockPage />)
    await screen.findByText('001238 Test Co Report Title')
    expect(screen.queryByTestId('chart')).toBeNull()
  })

  // A private window, cleared site data or a browser set to block storage throws on the property
  // access itself. The report must still render: a preference that cannot be read is not a reason
  // to lose the page.
  it('renders the default when localStorage throws instead of answering', async () => {
    const boom = () => {
      throw new Error('storage blocked')
    }
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(boom)
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(boom)

    render(<StockPage />)
    expect(await screen.findByText('001238 Test Co Report Title')).toBeTruthy()
    expect(screen.queryByTestId('chart')).toBeNull()
    // And the toggle still works for this page view; it simply will not be remembered.
    await userEvent.click(screen.getByRole('button', { name: /quote\.expandChart/ }))
    expect(await screen.findByTestId('chart')).toBeTruthy()
  })

  it('keeps the range switcher out of sight while the chart is', async () => {
    render(<StockPage />)
    await screen.findByText('001238 Test Co Report Title')
    // Chrome for something that is not on screen — and each press spends a vendor request.
    for (const r of ['1m', '3m', '6m', '1y']) expect(screen.queryByText(`quote.range.${r}`)).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: /quote\.expandChart/ }))
    expect(await screen.findByText('quote.range.3m')).toBeTruthy()
  })

  // The full-size chart moved to the Quotes app, which is what makes keeping this one small fair.
  // A link that lands there on a blank search box would not be that.
  it('links to the Quotes app for the symbol being read', async () => {
    render(<StockPage />)
    const link = await screen.findByText('quote.openInApp')
    expect(link.closest('a')?.getAttribute('href')).toBe('/apps/quotes?symbol=001238')
  })

  // Three labelled export buttons ate a row on desktop too. One menu everywhere — the same control
  // the phone already had.
  it('collapses the exports into one menu regardless of width', async () => {
    render(<StockPage />)
    expect(await screen.findByText('export-menu')).toBeTruthy()
    expect(screen.queryByText('pdf')).toBeNull()
    expect(screen.queryByText('day')).toBeNull()
  })
})
