import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { ApiError } from '../../api/client'
import QuoteSourcesPage from './QuoteSourcesPage'

// The admin console for the quote sources. What is worth pinning is everything the page DERIVES
// rather than displays: which source is primary (a position among the enabled ones, not a field),
// what the save payload's order string is made of, and — the one that bites — which TTL is on screen
// after a save, since the server clamps it and the typed number is then a fiction.
//
// Dragging itself is not exercised here: dnd.test.tsx owns the dnd-kit wiring. What this file pins is
// that save reads the ROW ORDER, which is what dragging changes.

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

// Only `api` is stubbed — errText and ApiError stay real, because the failed-load test asserts what a
// server code actually turns into on screen.
vi.mock('../../api/client', async (orig) => ({
  ...(await orig<typeof import('../../api/client')>()),
  api: apiMock,
}))

// Same convention as every other test file here: an interpolated string keeps its arguments, so a
// test can assert the NUMBERS a hint states rather than merely that a hint appeared.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

// sina FIRST and failing, tencent second and healthy — an admin who has reordered them. The reversed
// order is deliberate: a save payload reading "tencent,sina" would be the compiled-in default rather
// than anything this page read off the screen.
const state = {
  sources: [
    {
      source: 'sina',
      enabled: true,
      position: 1,
      markets: ['sh', 'sz', 'bj'],
      // Go's zero time.Time, which is what a source that has never answered actually sends.
      lastSuccess: '0001-01-01T00:00:00Z',
      lastError: 'sina: snapshot column order check failed',
      lastErrorAt: '2026-09-07T06:41:33Z',
      consecutiveFailures: 4,
    },
    {
      source: 'tencent',
      enabled: true,
      position: 2,
      markets: ['sh', 'sz', 'bj', 'hk', 'us'],
      lastSuccess: '2026-09-07T06:41:36Z',
      lastError: '',
      lastErrorAt: '0001-01-01T00:00:00Z',
      consecutiveFailures: 0,
    },
  ],
  cacheEntries: 12,
  cacheBytes: 34567,
  ttlOpenSecs: 30,
  ttlClosedSecs: 300,
  ttlOpenFloor: 5,
  ttlClosedFloor: 30,
}

function renderPage() {
  return render(
    <App>
      <QuoteSourcesPage />
    </App>,
  )
}

describe('QuoteSourcesPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockResolvedValue(structuredClone(state))
    apiMock.post.mockResolvedValue(structuredClone(state))
  })

  it('renders each source with its role, markets and health', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/quote'))

    // Names fall back to the raw source when the bundle has no label for it (the mocked t echoes
    // every key), which is the behaviour a source added ahead of its i18n string gets.
    expect(await screen.findByText('sina')).toBeTruthy()
    expect(screen.getByText('tencent')).toBeTruthy()

    // Primary is the first ENABLED source in the order — sina here, not the compiled-in default.
    expect(screen.getByText('quoteAdmin.primary')).toBeTruthy()
    expect(screen.getByText('quoteAdmin.fallback')).toBeTruthy()

    // Markets are per source: only tencent serves HK and US.
    expect(screen.getByText('quote.market.hk')).toBeTruthy()
    expect(screen.getByText('quote.market.us')).toBeTruthy()
    expect(screen.getAllByText('quote.market.sh').length).toBe(2)

    // The failing source: its consecutive-failure count (nothing else on this page renders a 4) and
    // the vendor's own error sentence, verbatim.
    expect(screen.getByText('4')).toBeTruthy()
    expect(screen.getByText('sina: snapshot column order check failed')).toBeTruthy()

    // Never succeeded: the zero instant is "never", not the year 1 — and no green OK beside it,
    // since zero failures on a source nobody has called is not evidence of health.
    expect(screen.getByText('quoteAdmin.never')).toBeTruthy()
    expect(screen.queryByText(/0001-01-01/)).toBeNull()
    expect(screen.getAllByText('quoteAdmin.ok').length).toBe(1) // tencent only

    // Two stamps on this page now — when tencent last answered and when sina last failed — so
    // neither assertion may be a bare "some timestamp is somewhere on screen".
    expect(screen.getAllByText(/^2026-09-0\d /)).toHaveLength(2)

    // The line that explains the field an admin came looking for and will not find.
    expect(screen.getByText('quoteAdmin.whyNoUrl')).toBeTruthy()

    // Cache occupancy, as the server reports it.
    expect(screen.getByText('12')).toBeTruthy()
    expect(screen.getByText('33.8 KB')).toBeTruthy()
  })

  it('says WHEN the last error happened, not only what it said', async () => {
    renderPage()
    await screen.findByText('sina: snapshot column order check failed')

    // Four minutes ago and last April are an incident and a scar — act now versus already fixed —
    // and the failure counter does not separate them: consecutiveFailures resets on the next
    // success while the sentence stays, so a source reading 正常 can still be carrying an old one.
    const at = screen.getAllByTestId('quote-source-error-at')
    expect(at).toHaveLength(1) // sina only: tencent has never failed
    expect(at[0].textContent).toMatch(/^2026-09-0\d \d\d:\d\d:\d\d$/)

    // Go's zero time.Time is what a source that has never failed actually sends, and it is not a
    // date. An empty cell is the answer there — "never" is reserved for 最近成功, where a source
    // that has never answered is itself the finding.
    expect(screen.queryByText(/0001-01-01/)).toBeNull()
    expect(screen.getAllByText('quoteAdmin.never')).toHaveLength(1)
  })

  it('orders the rows by the server position, not by the order the array arrived in', async () => {
    // snapshotHealth walks a Go map and sorts it by source NAME, so the array can arrive in an
    // order that has nothing to do with the failover order it is describing. On this page the
    // ORDER IS THE SETTING: the first row is labelled 主源 and is written back as the first entry
    // of the order string, so a page that trusted the array would rename the primary source and
    // then save that rename.
    const shuffled = structuredClone(state)
    shuffled.sources = [shuffled.sources[1], shuffled.sources[0]] // tencent (position 2) first
    apiMock.get.mockResolvedValue(shuffled)

    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    const rows = screen.getAllByRole('row')
    expect(within(rows[1]).getByText('sina')).toBeTruthy()
    expect(within(rows[1]).getByText('quoteAdmin.primary')).toBeTruthy()
    expect(within(rows[2]).getByText('tencent')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ order: 'sina,tencent' })),
    )
  })

  it('saves the order read off the rows, plus both TTLs', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', {
        order: 'sina,tencent',
        ttlOpenSecs: 30,
        ttlClosedSecs: 300,
      }),
    )
  })

  it('a source switched off drops out of the order and hands primary to the next one', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    await user.click(screen.getByRole('switch', { name: 'sina quoteAdmin.enabled' }))
    // One enabled source left, so there is a primary and nothing to fall back to.
    await waitFor(() => expect(screen.queryByText('quoteAdmin.fallback')).toBeNull())
    expect(screen.getByText('quoteAdmin.primary')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ order: 'tencent' })),
    )
  })

  it('refuses to switch the last source off instead of accepting it and turning both back on', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    await user.click(screen.getByRole('switch', { name: 'sina quoteAdmin.enabled' }))
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'sina quoteAdmin.enabled' }).getAttribute('aria-checked')).toBe('false'),
    )

    // THE click. One field carries both the order and the flags, so an all-off page sends order:''
    // — and a blank order is the server's reset-to-default, which writes "tencent,sina" back. The
    // admin would have been shown a green "saved" and then watched both switches flip up: their
    // setting not merely rejected but inverted, with nothing on screen admitting it. The state is
    // prevented instead, and the refusal says why.
    await user.click(screen.getByRole('switch', { name: 'tencent quoteAdmin.enabled' }))
    expect(screen.getByRole('switch', { name: 'tencent quoteAdmin.enabled' }).getAttribute('aria-checked')).toBe('true')
    await waitFor(() => expect(document.querySelector('.ant-message')?.textContent).toContain('quoteAdmin.orderHint'))

    // The switch that was legitimately turned off stays off: refusing the last one is not a reset.
    expect(screen.getByRole('switch', { name: 'sina quoteAdmin.enabled' }).getAttribute('aria-checked')).toBe('false')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ order: 'tencent' })),
    )
  })

  it('refuses to save an empty order, which the server would read as a reset', async () => {
    // Reachable without touching a switch: an order setting naming a source this build no longer
    // compiles in leaves every row disabled, and the page renders exactly what it was sent. Saving
    // from there is the one request this page must never send — it looks like "keep what is on
    // screen" and means "put the shipped default back".
    const allOff = structuredClone(state)
    allOff.sources = allOff.sources.map((r) => ({ ...r, enabled: false }))
    apiMock.get.mockResolvedValue(allOff)

    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(document.querySelector('.ant-message')?.textContent).toContain('quoteAdmin.orderHint'))
    expect(apiMock.post).not.toHaveBeenCalled()
    expect(screen.queryByText('common.saved')).toBeNull()
  })

  it('renders a body missing its TTL fields as zeros rather than as undefined', async () => {
    // Only a malformed body arrives here — an older server, a proxy that dropped fields, a rename
    // — but the failure is silent and permanent: value={undefined} switches antd's InputNumber from
    // controlled to UNCONTROLLED, after which React stops writing to the box and the clamp
    // read-back that save() exists for never reaches the screen. The floor beside it would read
    // "≥ undefined" in the meantime.
    const partial = structuredClone(state) as Record<string, unknown>
    delete partial.ttlOpenSecs
    delete partial.ttlClosedSecs
    delete partial.ttlOpenFloor
    delete partial.ttlClosedFloor
    apiMock.get.mockResolvedValue(partial)

    renderPage()
    await screen.findByText('sina')
    expect((screen.getByLabelText('quoteAdmin.ttlOpen') as HTMLInputElement).value).toBe('0')
    expect((screen.getByLabelText('quoteAdmin.ttlClosed') as HTMLInputElement).value).toBe('0')
    expect(screen.getAllByText('quoteAdmin.ttlHint ≥ 0')).toHaveLength(2)
    expect(screen.queryByText(/undefined/)).toBeNull()
  })

  it('shows the TTL the server clamped to, not the one that was typed', async () => {
    const user = userEvent.setup()
    // The floor is stated up front, in the hint beside each field.
    renderPage()
    const open = (await screen.findByLabelText('quoteAdmin.ttlOpen')) as HTMLInputElement
    expect(screen.getByText('quoteAdmin.ttlHint ≥ 5')).toBeTruthy()
    expect(screen.getByText('quoteAdmin.ttlHint ≥ 30')).toBeTruthy()

    // 2 seconds is below the 5-second floor. The server takes the request and answers with what it
    // will actually run.
    apiMock.post.mockResolvedValue({ ...structuredClone(state), ttlOpenSecs: 5 })
    await user.clear(open)
    await user.type(open, '2')
    await user.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ ttlOpenSecs: 2 })),
    )
    // The point of the test: a green "saved" over a 2 would be the page claiming a setting the
    // server never accepted.
    await waitFor(() => expect((screen.getByLabelText('quoteAdmin.ttlOpen') as HTMLInputElement).value).toBe('5'))
  })

  it('clears the cache and shows the emptied occupancy', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('33.8 KB')

    apiMock.post.mockResolvedValue({ ...structuredClone(state), cacheEntries: 0, cacheBytes: 0 })
    await user.click(screen.getByRole('button', { name: /quoteAdmin\.clearCache/ }))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote/cache/clear'))
    expect(await screen.findByText('quoteAdmin.cleared')).toBeTruthy()
    // Read back from the answer rather than assumed: the counters are the only evidence the button
    // did anything.
    await waitFor(() => expect(screen.getByText('0 B')).toBeTruthy())
    expect(screen.queryByText('33.8 KB')).toBeNull()
  })

  it('says a failed load failed instead of rendering an empty console', async () => {
    apiMock.get.mockRejectedValue(new ApiError(503, 'quote sources unavailable', 'quote_unavailable'))
    renderPage()

    expect(await screen.findByText('common.loadFailed')).toBeTruthy()
    expect(screen.getByText('quote sources unavailable')).toBeTruthy()
    // Not a page of defaults with a Save button that would write them back.
    expect(screen.queryByText('quoteAdmin.whyNoUrl')).toBeNull()
    expect(screen.queryByRole('button', { name: 'common.save' })).toBeNull()

    // And it offers the way back.
    const user = userEvent.setup()
    apiMock.get.mockResolvedValue(structuredClone(state))
    await user.click(screen.getByRole('button', { name: /common\.retry/ }))
    expect(await screen.findByText('sina')).toBeTruthy()
  })
})
