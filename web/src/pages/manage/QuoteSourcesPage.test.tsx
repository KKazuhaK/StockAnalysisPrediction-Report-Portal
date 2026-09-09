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
//
// Yahoo is the third, and it is the one that ships OFF, so its position is 0 — and it is placed at
// the HEAD of the array on purpose. 0 sorts before 1, so a page that seats rows by position alone
// puts a source that is not in the failover chain at the top of it, and then writes that seat back
// as the order the moment somebody enables it.
const state = {
  sources: [
    {
      source: 'yahoo',
      enabled: false,
      position: 0,
      // Everything except Beijing, and — the point of the column beside it — only the US DAILY.
      // Yahoo is where a US chart comes from; it is not where an A-share daily comes from.
      markets: ['sh', 'sz', 'hk', 'us'],
      daily: ['us'],
      intraday: ['sh', 'sz', 'hk', 'us'],
      intraday5d: ['sh', 'sz', 'hk', 'us'],
      lastSuccess: '0001-01-01T00:00:00Z',
      lastError: '',
      lastErrorAt: '0001-01-01T00:00:00Z',
      consecutiveFailures: 0,
    },
    {
      source: 'sina',
      enabled: true,
      position: 1,
      markets: ['sh', 'sz', 'bj'],
      daily: ['sh', 'sz'],
      // A source that declares no intraday at all: the case the capability column has to render as
      // a stated "none" rather than as an empty cell that could equally be a missing field.
      intraday: [],
      intraday5d: [],
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
      // Prices five markets, draws three: Beijing answers "day":[] and a sixty-bar US request comes
      // back with two rows fifteen years apart (ADR 0030 §4). markets and daily DISAGREE here, and
      // that disagreement is the reason both columns exist.
      markets: ['sh', 'sz', 'bj', 'hk', 'us'],
      daily: ['sh', 'sz', 'hk'],
      // DELIBERATELY ASYMMETRIC, and the only row here that is not what the live server sends: the
      // real Tencent declares both windows for the same three markets, and so does Yahoo for the
      // same four, so a page that rendered `intraday` under BOTH headings would agree with every
      // truthful fixture. That is exactly the state the server was in — one union under a 分时
      // heading — when the panel advertised 5日 in three markets nothing served. A vendor whose
      // five-day endpoint covers fewer markets than its one-day one is a legitimate wire shape, and
      // it is the only one that can prove this page reads two fields rather than one twice.
      intraday: ['sh', 'sz', 'hk'],
      intraday5d: ['sh', 'sz'],
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
  ttlIntradaySecs: 60,
  ttlOpenFloor: 5,
  ttlClosedFloor: 30,
  ttlIntradayFloor: 15,
  homeCards: true,
  autoRefresh: true,
}

// The row a source's name appears in. Every capability assertion below is scoped through this: the
// page now prints market tags in two columns for three sources, so a bare getAllByText('quote.
// market.us') counts six things and pins none of them to the source that claims it.
function sourceRow(name: string): HTMLElement {
  const row = screen.getAllByRole('row').find((r) => within(r).queryByText(name))
  if (!row) throw new Error(`no row for source ${name}`)
  return row
}

// The market ids a given cell of that row lists, in order.
function tagsIn(row: HTMLElement, testid: string): string[] {
  return within(within(row).getByTestId(testid))
    .queryAllByText(/^quote\.market\./)
    .map((el) => el.textContent ?? '')
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

    // Markets are per source, and read off that source's own row: sina serves Beijing and neither
    // Hong Kong nor the US, tencent serves all five.
    expect(tagsIn(sourceRow('sina'), 'quote-source-markets')).toEqual(['quote.market.sh', 'quote.market.sz', 'quote.market.bj'])
    expect(tagsIn(sourceRow('tencent'), 'quote-source-markets')).toContain('quote.market.us')

    // The failing source: its consecutive-failure count (nothing else on this page renders a 4) and
    // the vendor's own error sentence, verbatim.
    expect(screen.getByText('4')).toBeTruthy()
    expect(screen.getByText('sina: snapshot column order check failed')).toBeTruthy()

    // Never succeeded: the zero instant is "never", not the year 1 — and no green OK beside it,
    // since zero failures on a source nobody has called is not evidence of health. Two of them
    // here: sina has only ever failed, and yahoo has never been switched on.
    expect(within(sourceRow('sina')).getByText('quoteAdmin.never')).toBeTruthy()
    expect(within(sourceRow('yahoo')).getByText('quoteAdmin.never')).toBeTruthy()
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

  it('prints what each source can serve, per interval, and not only which markets it knows', async () => {
    renderPage()
    await screen.findByText('sina')

    // The load-bearing pair. Tencent's markets column names the US — it does serve the US PRICE —
    // and its daily column does not, because a sixty-bar request for usAAPL answers with two rows
    // fifteen years apart. An operator reading the markets column alone would conclude that leaving
    // tencent on top is enough for a US chart; it is the reason yahoo exists in this build.
    expect(tagsIn(sourceRow('tencent'), 'quote-source-markets')).toContain('quote.market.us')
    expect(tagsIn(sourceRow('tencent'), 'quote-source-daily')).toEqual([
      'quote.market.sh',
      'quote.market.sz',
      'quote.market.hk',
    ])

    // And the mirror of it: yahoo's daily grant is the US ALONE, which is what makes dragging it
    // above tencent a decision about US charts rather than about every quote in the portal.
    expect(tagsIn(sourceRow('yahoo'), 'quote-source-daily')).toEqual(['quote.market.us'])
    expect(tagsIn(sourceRow('yahoo'), 'quote-source-intraday')).toEqual([
      'quote.market.sh',
      'quote.market.sz',
      'quote.market.hk',
      'quote.market.us',
    ])

    // A source that declares no intraday says so, for BOTH windows. An empty cell would be
    // indistinguishable from a build whose server never sent the field.
    expect(tagsIn(sourceRow('sina'), 'quote-source-intraday')).toEqual([])
    expect(within(sourceRow('sina')).getByTestId('quote-source-intraday').textContent).toContain('—')
    expect(tagsIn(sourceRow('sina'), 'quote-source-intraday5d')).toEqual([])
    expect(within(sourceRow('sina')).getByTestId('quote-source-intraday5d').textContent).toContain('—')
    expect(tagsIn(sourceRow('sina'), 'quote-source-daily')).toEqual(['quote.market.sh', 'quote.market.sz'])

    // The two intraday windows are two rows reading two fields, and tencent's fixture is the one
    // that can tell them apart. A page that rendered `intraday` under both headings — which is what
    // the SERVER used to send, one union under a 分时 label — would print sh, sz and hk twice here.
    expect(tagsIn(sourceRow('tencent'), 'quote-source-intraday')).toEqual([
      'quote.market.sh',
      'quote.market.sz',
      'quote.market.hk',
    ])
    expect(tagsIn(sourceRow('tencent'), 'quote-source-intraday5d')).toEqual([
      'quote.market.sh',
      'quote.market.sz',
    ])
    expect(tagsIn(sourceRow('yahoo'), 'quote-source-intraday5d')).toEqual([
      'quote.market.sh',
      'quote.market.sz',
      'quote.market.hk',
      'quote.market.us',
    ])

    // All three intervals are named on every row, so a reader knows which list they are looking at.
    expect(screen.getAllByText('quoteAdmin.daily')).toHaveLength(3)
    expect(screen.getAllByText('quoteAdmin.intraday')).toHaveLength(3)
    expect(screen.getAllByText('quote.range.5d')).toHaveLength(3)
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toContain('quoteAdmin.capabilities')
  })

  it('shows a source that ships disabled, with its notice, and lets it be switched on — at the END of the chain', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    // Visible at all: a panel that listed only the enabled sources would give an operator no way to
    // turn on the one thing this release added.
    const yahoo = screen.getByRole('switch', { name: 'yahoo quoteAdmin.enabled' })
    expect(yahoo.getAttribute('aria-checked')).toBe('false')

    // Seated LAST despite arriving first in the array with position 0. Sorting by position alone
    // puts every disabled source above the primary — and since the order written back is the row
    // order, enabling it there would hand an undocumented endpoint every A-share request.
    const rows = screen.getAllByRole('row')
    expect(within(rows[1]).getByText('sina')).toBeTruthy()
    expect(within(rows[3]).getByText('yahoo')).toBeTruthy()
    // Off, so no place in the chain at all: 主源 and 备源 describe the enabled sources only.
    expect(within(rows[3]).queryByText('quoteAdmin.primary')).toBeNull()
    expect(within(rows[3]).queryByText('quoteAdmin.fallback')).toBeNull()

    // The notice is RENDERED and not behind a hover: it is what the operator is deciding with —
    // undocumented endpoint, unlicensed use, their call, and the thing enabling it buys. Asserted on
    // the text, so hiding it in a tooltip's title attribute would fail here.
    //
    // It sits ABOVE the table rather than inside the row it is about, and that is deliberate: three
    // sentences under the source column's width wrapped to five lines and set the height of every
    // other row. So it carries the vendor's name instead of relying on adjacency, which is what the
    // assertion below checks — an unnamed banner at the card's width would not say WHICH source it
    // is about.
    const notices = screen.getAllByTestId('quote-source-notice')
    expect(notices).toHaveLength(1)
    expect(notices[0].textContent).toContain('quoteAdmin.yahooNotice')
    expect(notices[0].textContent).toContain('yahoo')
    // Still one per vendor that has something to declare, and not a banner every disabled row
    // inherits: tencent and sina are both in this fixture and neither adds one.
    expect(within(sourceRow('yahoo')).queryByTestId('quote-source-notice')).toBeNull()
    expect(within(sourceRow('tencent')).queryByTestId('quote-source-notice')).toBeNull()

    await user.click(yahoo)
    expect(screen.getByRole('switch', { name: 'yahoo quoteAdmin.enabled' }).getAttribute('aria-checked')).toBe('true')
    await waitFor(() => expect(within(sourceRow('yahoo')).getByText('quoteAdmin.fallback')).toBeTruthy())

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith(
        '/api/admin/quote',
        expect.objectContaining({ order: 'sina,tencent,yahoo' }),
      ),
    )
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
    // that has never answered is itself the finding (sina and the never-enabled yahoo, here).
    expect(screen.queryByText(/0001-01-01/)).toBeNull()
    expect(screen.getAllByText('quoteAdmin.never')).toHaveLength(2)
  })

  it('orders the rows by the server position, not by the order the array arrived in', async () => {
    // snapshotHealth walks a Go map and sorts it by source NAME, so the array can arrive in an
    // order that has nothing to do with the failover order it is describing. On this page the
    // ORDER IS THE SETTING: the first row is labelled 主源 and is written back as the first entry
    // of the order string, so a page that trusted the array would rename the primary source and
    // then save that rename.
    const shuffled = structuredClone(state)
    // tencent (position 2) first, then sina (1), then the disabled yahoo (0).
    shuffled.sources = [shuffled.sources[2], shuffled.sources[1], shuffled.sources[0]]
    apiMock.get.mockResolvedValue(shuffled)

    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    const rows = screen.getAllByRole('row')
    expect(within(rows[1]).getByText('sina')).toBeTruthy()
    expect(within(rows[1]).getByText('quoteAdmin.primary')).toBeTruthy()
    expect(within(rows[2]).getByText('tencent')).toBeTruthy()
    expect(within(rows[3]).getByText('yahoo')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ order: 'sina,tencent' })),
    )
  })

  it('saves the order read off the rows, all three TTLs and the home-card switch', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    // The WHOLE payload, not objectContaining: one Save writes every setting on the page, so a
    // field that stops being sent is a control the operator can move and cannot save — and the page
    // would still say "saved", because the request succeeded without it.
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', {
        order: 'sina,tencent',
        ttlOpenSecs: 30,
        ttlClosedSecs: 300,
        ttlIntradaySecs: 60,
        homeCards: true,
        autoRefresh: true,
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
    delete partial.ttlIntradaySecs
    delete partial.ttlOpenFloor
    delete partial.ttlClosedFloor
    delete partial.ttlIntradayFloor
    delete partial.homeCards
    delete partial.autoRefresh
    apiMock.get.mockResolvedValue(partial)

    renderPage()
    await screen.findByText('sina')
    expect((screen.getByLabelText('quoteAdmin.ttlOpen') as HTMLInputElement).value).toBe('0')
    expect((screen.getByLabelText('quoteAdmin.ttlClosed') as HTMLInputElement).value).toBe('0')
    expect((screen.getByLabelText('quoteAdmin.ttlIntraday') as HTMLInputElement).value).toBe('0')
    expect(screen.getAllByText('quoteAdmin.ttlHint ≥ 0')).toHaveLength(3)
    expect(screen.queryByText(/undefined/)).toBeNull()

    // And the switch that decides whether this portal hands a list of codes to a vendor reads OFF
    // when the answer did not say. `?? true` on the shipped default would put the page's own guess
    // under a control whose whole purpose is that somebody chose — and the next Save would post it
    // back as though they had.
    expect(screen.getByRole('switch', { name: 'quoteAdmin.homeCards' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByRole('switch', { name: 'quoteAdmin.autoRefresh' }).getAttribute('aria-checked')).toBe('false')
  })

  it('does not write back switches the answer never carried', async () => {
    // The shape a rolling deploy serves for a few minutes: an older server behind this bundle,
    // answering everything except the newest key. The admin came to change a TTL.
    const partial = structuredClone(state) as Record<string, unknown>
    delete partial.homeCards
    delete partial.autoRefresh
    apiMock.get.mockResolvedValue(partial)

    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')
    // Reading OFF is right and is pinned above; WRITING that OFF back is not. It would switch a
    // default-ON disclosure off in the name of an admin who never saw a decision to make, under a
    // green "saved" — the TTLs beside it survive the same round trip only because the server
    // clamps them, and this key has no clamp. The whole payload, so an unexpected key fails here.
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', {
        order: 'sina,tencent',
        ttlOpenSecs: 30,
        ttlClosedSecs: 300,
        ttlIntradaySecs: 60,
      }),
    )
    expect(Object.keys(apiMock.post.mock.calls[0][1] as object)).not.toContain('homeCards')
    expect(Object.keys(apiMock.post.mock.calls[0][1] as object)).not.toContain('autoRefresh')
  })

  it('writes the home-card switch the admin moved, even on a body that never carried it', async () => {
    // The other half: omitting the key must not turn the control into one an operator can move and
    // cannot save. Moving the switch IS the answer the body was missing.
    const partial = structuredClone(state) as Record<string, unknown>
    delete partial.homeCards
    apiMock.get.mockResolvedValue(partial)

    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    await user.click(screen.getByRole('switch', { name: 'quoteAdmin.homeCards' }))
    expect(screen.getByRole('switch', { name: 'quoteAdmin.homeCards' }).getAttribute('aria-checked')).toBe('true')

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ homeCards: true })),
    )
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

  it('saves the intraday TTL against its own floor, which is not the open one', async () => {
    const user = userEvent.setup()
    renderPage()
    const box = (await screen.findByLabelText('quoteAdmin.ttlIntraday')) as HTMLInputElement
    expect(box.value).toBe('60')
    // Three floors, three different numbers, each beside its own field — and the intraday one (15)
    // is neither of the others (5 and 30), so a hint wired to the wrong state variable shows up
    // here rather than in production, where a minute series cached for the window that suits a
    // daily chart is a chart claiming a resolution it does not have.
    expect(screen.getByText('quoteAdmin.ttlHint ≥ 15')).toBeTruthy()
    expect(screen.getByText('quoteAdmin.ttlHint ≥ 5')).toBeTruthy()
    expect(screen.getByText('quoteAdmin.ttlHint ≥ 30')).toBeTruthy()

    // 3 seconds is below the 15-second floor; the server takes it and answers with what it runs.
    apiMock.post.mockResolvedValue({ ...structuredClone(state), ttlIntradaySecs: 15 })
    await user.clear(box)
    await user.type(box, '3')
    await user.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ ttlIntradaySecs: 3 })),
    )
    // The clamp read-back, per field: leaving the typed 3 on screen under a green "saved" is the
    // page reporting a cache window the portal is not running.
    await waitFor(() => expect((screen.getByLabelText('quoteAdmin.ttlIntraday') as HTMLInputElement).value).toBe('15'))
    // And the other two are not disturbed by the field that was edited.
    expect((screen.getByLabelText('quoteAdmin.ttlOpen') as HTMLInputElement).value).toBe('30')
  })

  it('saves the home-card switch, and states the disclosure in the open', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    // The sentence is on the page, not in a tooltip: showing prices on the home cards sends the
    // codes currently on screen to a third-party vendor on every home page view, which is the whole
    // reason the feature has a switch instead of only a default.
    expect(screen.getByText('quoteAdmin.homeCardsHint')).toBeTruthy()

    const sw = screen.getByRole('switch', { name: 'quoteAdmin.homeCards' })
    expect(sw.getAttribute('aria-checked')).toBe('true')
    await user.click(sw)
    expect(screen.getByRole('switch', { name: 'quoteAdmin.homeCards' }).getAttribute('aria-checked')).toBe('false')

    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ homeCards: false })),
    )

    // Read back from the ANSWER, like the clamped TTLs beside it. This fixture's server replies
    // that the switch is still on — a stale echo, a refusal the handler turned into a no-op — and
    // what the page must then show is the server's state, not the click that did not take.
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'quoteAdmin.homeCards' }).getAttribute('aria-checked')).toBe('true'),
    )
  })

  it('saves the automatic-refresh switch and explains that it applies to visible pages', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('sina')

    expect(screen.getByText('quoteAdmin.autoRefreshHint')).toBeTruthy()
    const sw = screen.getByRole('switch', { name: 'quoteAdmin.autoRefresh' })
    expect(sw.getAttribute('aria-checked')).toBe('true')
    await user.click(sw)
    await user.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/quote', expect.objectContaining({ autoRefresh: false })),
    )
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
