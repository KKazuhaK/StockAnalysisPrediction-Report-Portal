import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import ReviewPage from './ReviewPage'

// The review queue turns "what this report assumed" into a list someone works through. Two things
// have to hold: the empty state must TEACH (it is the state this portal is in until the workflow
// starts emitting assumptions), and recording a verdict must actually record it.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() }))
vi.mock('../api/client', () => ({
  api: apiMock,
  ApiError: class extends Error {},
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

const ROW = {
  id: 1, symbol: '600519', name: '贵州茅台', itype: 'assumption', content: '毛利率维持 20%',
  status: 'pending', review_point: '2026-10-31 三季报', due: '2026-10-31',
  created_at: '2026-07-31 10:00:00', report_id: 12, report_title: '投资决策建议',
  report_date: '2026-07-31', report_kind: '投资决策', report_type: '投资决策建议',
}
const FULL = { items: [ROW], total: 1, counts: { pending: 1 }, itypes: ['assumption'], statuses: ['pending'] }
const EMPTY = { items: [], total: 0, counts: {}, itypes: [], statuses: [] }

const mount = () =>
  render(
    <App>
      <MemoryRouter>
        <ReviewPage />
      </MemoryRouter>
    </App>,
  )

describe('ReviewPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.patch.mockReset()
    apiMock.patch.mockResolvedValue({ ok: true })
  })

  // The state this portal is actually in. "No data" would teach nobody how to get some, so the
  // page carries the ingest payload that fills it.
  it('the empty state explains how to populate it', async () => {
    apiMock.get.mockResolvedValue(EMPTY)
    mount()
    expect(await screen.findByText('review.emptyTitle')).toBeTruthy()
    expect(screen.getByText('review.emptyBody')).toBeTruthy()
    // The literal contract, not a link to it.
    expect(screen.getByText(/"tracking"/)).toBeTruthy()
    expect(screen.getByText(/review_point/)).toBeTruthy()
    // No filters over nothing.
    expect(screen.queryByPlaceholderText('review.search')).toBeNull()
  })

  it('lists an assumption with the report it came from, and its parsed due date', async () => {
    apiMock.get.mockResolvedValue(FULL)
    mount()
    expect(await screen.findByText('毛利率维持 20%')).toBeTruthy()
    // A claim with no context behind it cannot be judged.
    expect(screen.getByText(/600519 贵州茅台 · 2026-07-31 · 投资决策建议/)).toBeTruthy()
    // The parsed date AND the raw text: the text usually says WHAT to check, not only when.
    expect(screen.getByText('2026-10-31')).toBeTruthy()
    expect(screen.getByText('2026-10-31 三季报')).toBeTruthy()
  })

  it('records a verdict against the item', async () => {
    apiMock.get.mockResolvedValue(FULL)
    mount()
    fireEvent.click(await screen.findByText('review.verdict.confirmed'))
    await waitFor(() =>
      expect(apiMock.patch).toHaveBeenCalledWith('/api/tracking/1', { status: 'confirmed' }),
    )
  })

  it('defaults to the pending items, since those are the ones that need doing', async () => {
    apiMock.get.mockResolvedValue(FULL)
    mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    expect(String(apiMock.get.mock.calls[0][0])).toContain('status=pending')
    expect(String(apiMock.get.mock.calls[0][0])).toContain('sort=due')
  })
})
