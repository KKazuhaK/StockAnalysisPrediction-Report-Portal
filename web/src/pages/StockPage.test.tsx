import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
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
        selRID: '1',
        kinds: ['A'],
        subtabs: [{ label: 'Sub', rid: '1' }],
        timeline: [],
        rep: { rid: 1, name: 'Test Co', title: 'Report Title', date: '2026-07-07', source: 'x', html: '', md: '# hi', time: '' },
      }),
  },
  qs: () => '',
  ApiError: class extends Error {},
}))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('react-router-dom', () => ({
  useParams: () => ({ symbol: '001238' }),
  useSearchParams: () => [new URLSearchParams('date=2026-07-07'), vi.fn()],
  useNavigate: () => vi.fn(),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../lib/datetime', () => ({ isInstant: () => false, formatReportDateTime: (s: string) => s }))
vi.mock('../components/Markdown', () => ({ default: () => <div>md</div> }))
vi.mock('../components/TimelinePanel', () => ({ default: () => <div>timeline</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/ExportButtons', () => ({
  ExportPdfButton: () => <div>pdf</div>,
  ExportDayButton: () => <div>day</div>,
  ExportMenu: () => <div>export-menu</div>,
}))

describe('StockPage', () => {
  it('renders the report after data loads (no hook-order crash)', async () => {
    render(<StockPage />)
    // Reaching the report title proves the component rendered past the loading→loaded
    // transition without a hooks-count mismatch.
    expect(await screen.findByText('Report Title')).toBeTruthy()
    expect(screen.getByText('stock.back')).toBeTruthy()
  })
})
