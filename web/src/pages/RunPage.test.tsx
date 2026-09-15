import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import RunPage from './RunPage'

// The run page's type strip collapses the tabs the server sends. What it is FOR is the version axis
// (ADR 0024): two written forms of one analysis share every identity component but version, so
// showing both would read as two identical tabs. What it must NOT swallow is two DIFFERENT reports
// that merely share a type — title is part of a report's identity, so one code+date+subtype
// legitimately carries several, and the server has already numbered them apart.

const REP = {
  id: 11,
  name: 'Test Co',
  title: 'B plan',
  displayTitle: '000021 Test Co B plan',
  date: '2026-09-03',
  source: 'x',
  html: '',
  md: '# hi',
  time: '',
}
const RUN = {
  key: '000021|2026-09-03',
  symbol: '000021',
  name: 'Test Co',
  date: '2026-09-03',
  selId: 11,
  tabs: [
    // Two different reports of ONE type: the server labelled the second one "Trade 2".
    { id: 10, label: 'Trade', rtype: 'Trade', title: '000021 Test Co A plan', version: 'default' },
    { id: 11, label: 'Trade 2', rtype: 'Trade', title: '000021 Test Co B plan', version: 'default' },
    // Two written FORMS of one report: these are what the strip is supposed to collapse.
    { id: 12, label: 'Decision', rtype: 'Decision', title: '000021 Test Co Decision', version: 'default' },
    { id: 13, label: 'Decision · manual', rtype: 'Decision', title: '000021 Test Co Decision', version: 'manual' },
  ],
  rep: REP,
}

vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, can: () => true, logout: vi.fn() }),
}))
vi.mock('../api/client', () => ({
  api: { get: () => Promise.resolve(RUN) },
  qs: (o: Record<string, string>) => {
    const p = new URLSearchParams(o).toString()
    return p ? `?${p}` : ''
  },
  ApiError: class extends Error {},
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('react-router', () => ({
  useParams: () => ({ key: '000021|2026-09-03' }),
  useSearchParams: () => [new URLSearchParams('r=11'), vi.fn()],
  useNavigate: () => vi.fn(),
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ fontSize: 15, fontWeight: 400, wide: false }) }))
vi.mock('../components/Markdown', () => ({ default: () => <div>md</div> }))
vi.mock('../components/ReaderControls', () => ({ default: () => <div>controls</div> }))
vi.mock('../components/EditReportButton', () => ({ default: () => <div>edit</div> }))
vi.mock('../components/VersionSwitcher', () => ({ default: () => <div>versions</div> }))
vi.mock('../components/ExportButtons', () => ({ ExportPdfButton: () => <div>pdf</div> }))

const itemTitle = (el: HTMLElement) => el.closest('.ant-segmented-item-label')?.getAttribute('title') ?? null

describe('RunPage type strip', () => {
  // The regression this exists for: collapsing by TYPE alone drops the un-numbered sibling, leaving
  // a tab that reads "Trade 2" with no "Trade" anywhere on screen — a number pointing at nothing,
  // and the other report unreachable.
  it('keeps two different reports of one type as two tabs', async () => {
    render(<RunPage />)
    expect(await screen.findByText('Trade')).toBeTruthy()
    expect(screen.getByText('Trade 2')).toBeTruthy()
  })

  // Still collapsed: these are two forms of ONE report, which is what the strip is for.
  it('collapses the written forms of one report into a single tab', async () => {
    render(<RunPage />)
    await screen.findByText('Trade')
    expect(screen.queryByText('Decision · manual')).toBeNull()
    expect(screen.getByText('Decision')).toBeTruthy()
  })

  // A tab names the type, so hovering is what says which report it opens.
  it('names the report each tab opens, on hover', async () => {
    render(<RunPage />)
    expect(itemTitle(await screen.findByText('Trade 2'))).toBe('000021 Test Co B plan')
  })
})
