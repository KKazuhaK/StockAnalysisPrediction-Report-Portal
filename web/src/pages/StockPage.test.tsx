import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import StockPage from './StockPage'

// StockPage renders a loading spinner first (data null), then re-renders with the report.
// A hook called after those early returns (Grid.useBreakpoint) would run on the second
// render but not the first — "rendered more hooks than during the previous render" — and
// blank the page. This mounts through that transition to guard against the regression.
vi.mock('../api/client', () => ({
  api: {
    get: () =>
      Promise.resolve({
        symbol: '001238',
        name: 'Test Co',
        selDate: '2026-07-07',
        selKind: 'A',
        selId: 1,
        kinds: ['A'],
        subtabs: [{ label: 'Sub', id: 1 }],
        timeline: [],
        rep: { id: 1, name: 'Test Co', title: 'Report Title', displayTitle: '001238 Test Co Report Title', date: '2026-07-07', source: 'x', html: '', md: '# hi', time: '' },
      }),
  },
  qs: () => '',
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

  // Three labelled export buttons ate a row on desktop too. One menu everywhere — the same control
  // the phone already had.
  it('collapses the exports into one menu regardless of width', async () => {
    render(<StockPage />)
    expect(await screen.findByText('export-menu')).toBeTruthy()
    expect(screen.queryByText('pdf')).toBeNull()
    expect(screen.queryByText('day')).toBeNull()
  })
})
