import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../lib/datetime', () => ({ isInstant: () => false, formatReportDateTime: (s: string) => s }))
vi.mock('../components/Markdown', () => ({ default: () => <div>md</div> }))
vi.mock('../components/TimelinePanel', () => ({ default: () => <div>timeline</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/CompareModal', () => ({
  default: ({ open, reportId }: { open: boolean; reportId: number }) => (open ? <div>compare-open:{reportId}</div> : null),
}))
vi.mock('../components/ExportButtons', () => ({
  ExportPdfButton: () => <div>pdf</div>,
  ExportDayButton: () => <div>day</div>,
  ExportMenu: () => <div>export-menu</div>,
}))

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

  it('renders the quote strip and the chart beside the report', async () => {
    render(<StockPage />)
    await screen.findByText('001238 Test Co Report Title')
    // The strip renders the vendor's own percentage string verbatim, so finding it proves the real
    // component mounted with the real payload rather than a placeholder.
    expect(await screen.findByText(/0\.12/)).toBeTruthy()
    expect(document.querySelector('svg')).toBeTruthy()
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
