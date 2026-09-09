import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { ReactNode } from 'react'
import HomePage from './HomePage'
import type { Group, HomeResp } from '../api/types'

// The browse feed's version filter (ADR 0024) is how the reports people wrote by hand become a set
// you can ask for rather than something you find one at a time (ADR 0026). What is worth pinning is
// that it appears exactly when the server says there is more than one written form, that choosing
// one reaches the server, and that a filter already in the URL comes back selected — a filter that
// forgets itself on reload looks like it did not work.
//
// The second half of this file is about the live price on each card, and every test there exists to
// hold ONE line: the feed does not wait on a vendor. So the cards are asserted while the batch is
// still in flight, after it fails on a page that already had prices, after an answer that has been
// superseded, and for the bodies and page sizes the endpoint refuses or this build cannot read —
// states a test that only ever resolved the happy path would never visit.

type Deferred = { url: string; resolve: (v: unknown) => void; reject: (e: unknown) => void }

const state: { resp: Partial<HomeResp>; urls: string[]; quotes: Deferred[] } = { resp: {}, urls: [], quotes: [] }

vi.mock('../api/client', () => ({
  api: {
    get: (u: string) => {
      state.urls.push(u)
      // The quote batch is handed back UNRESOLVED so a test can hold it open. That is the only way
      // to assert what the page looks like while a vendor is being slow, which is the state this
      // feature is judged on.
      if (u.startsWith('/api/quotes')) {
        return new Promise((resolve, reject) => {
          state.quotes.push({ url: u, resolve, reject })
        })
      }
      // A COPY, not the object itself: the server answers a poll with an equal-but-new body, and
      // setState bails out of re-rendering when the next value is Object.is-equal to the current
      // one. Handing back the same reference would hide the very re-render the batch has to
      // survive without re-asking a vendor.
      return Promise.resolve({ ...state.resp })
    },
  },
  errText: (_e: unknown, t: (k: string) => string) => t('common.error'),
  qs: (p: Record<string, string>) => {
    const q = new URLSearchParams(Object.entries(p).filter(([, v]) => v !== '')).toString()
    return q ? `?${q}` : ''
  },
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ can: () => true }) }))
vi.mock('../site', () => ({ useSite: () => ({ title: 'Portal' }), SiteLogo: () => null }))
vi.mock('../components/Omnibox', () => ({
  default: ({ suffix }: { suffix?: ReactNode }) => <div data-testid="omnibox">{suffix}</div>,
}))
vi.mock('../favorites', () => ({
  useFavorites: () => ({
    items: [],
    loaded: true,
    loading: false,
    error: null,
    reordering: false,
    ensureLoaded: vi.fn(),
    isFavorite: () => false,
    isBusy: () => false,
    toggle: vi.fn(),
    reorder: vi.fn(),
  }),
}))
// ReportCard is deliberately NOT mocked: the price line, its formatting and its colour are the
// thing under test here, and a stub standing in for the card would assert only that a prop was
// passed to it.

const base: Partial<HomeResp> = {
  groups: [],
  newTotal: 0,
  oldTotal: 0,
  totalRuns: 0,
  page: 1,
  pages: 1,
  size: 30,
  types: [],
  kinds: [],
  versions: [],
  links: [],
  linkGroups: [],
  kindColors: {},
}

const twoVersions = [
  // The default version arrives with no label of its own — the server falls back to the identifier —
  // so the filter has to name it rather than showing "default" to every reader.
  { name: 'default', label: 'default' },
  { name: 'manual', label: '人工' },
]

// One card in the feed. `symbol: ''` is a thematic report: a real shape in this feed, and the one
// that can never be quoted.
function group(key: string, symbol: string): Group {
  return {
    key,
    symbol,
    market: symbol.startsWith('6') ? 'sh' : symbol ? 'sz' : undefined,
    name: symbol ? `N${symbol}` : `T${key}`,
    title: symbol ? undefined : `T${key}`,
    date: '2026-09-01',
    kind: 'k',
    kinds: ['k'],
    src: 'new',
    n: 1,
    members: [],
  }
}

// A batch answer, in the keyed-by-symbol shape the endpoint sends. `last` and `change` are 分.
function quoteBody(rows: Record<string, [number, number, string]>) {
  const quotes: Record<string, unknown> = {}
  for (const [input, [last, change, changePct]] of Object.entries(rows)) {
    const symbol = input.replace(/^(sh|sz|bj|hk|us)/, '')
    const market = input.slice(0, input.length - symbol.length) || (symbol.startsWith('6') ? 'sh' : 'sz')
    const key = `${market}${symbol}`
    quotes[key] = { symbol, market, name: `N${symbol}`, currency: 'CNY', kind: 'stock', last, change, changePct }
  }
  return { quotes }
}

// Settling a deferred inside act(): the resolution is what triggers the React update, so without
// this the assertion after it can read the pre-update DOM and report it as final.
async function settle(d: Deferred, body: unknown) {
  await act(async () => {
    d.resolve(body)
  })
}
async function fail(d: Deferred) {
  await act(async () => {
    d.reject(new Error('vendor down'))
  })
}

// style.color comes back as 'rgb(r, g, b)' from jsdom's cssom and as the literal hex from a
// simpler one, so both are read. The assertion built on it — red is up, green is down — is about
// the CHANNELS rather than about a token name, so it survives a theme change and still fails the
// day somebody "corrects" the A-share convention into the American one.
function rgb(c: string): { r: number; g: number; b: number } {
  const m = /^rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(c)
  if (m) return { r: +m[1], g: +m[2], b: +m[3] }
  const h = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(c)
  if (h) return { r: parseInt(h[1], 16), g: parseInt(h[2], 16), b: parseInt(h[3], 16) }
  throw new Error(`unreadable colour: ${c}`)
}

const quoteUrls = () => state.urls.filter((u) => u.startsWith('/api/quotes'))

function renderHome(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <HomePage />
    </MemoryRouter>,
  )
}

// The filters live in the popover opened from the main search field's trailing control.
async function openFilters() {
  await userEvent.click(await screen.findByText('home.advanced'))
}

beforeEach(() => {
  state.resp = { ...base }
  state.urls = []
  state.quotes = []
})

afterEach(() => {
  vi.useRealTimers()
})

describe('the advanced filters', () => {
  it('opens from inside the main search field without reserving separate width', async () => {
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))

    const trigger = await screen.findByRole('button', { name: 'home.advanced' })
    const searchRow = trigger.closest('.rp-home-search-row') as HTMLElement
    expect(searchRow).toBeTruthy()
    expect(within(screen.getByTestId('omnibox')).getByRole('button', { name: 'home.advanced' })).toBe(trigger)
    expect(document.querySelector('.ant-collapse')).toBeNull()

    await userEvent.click(trigger)
    expect(screen.getAllByText('home.category').length).toBeGreaterThan(0)
  })

  it('shows how many advanced conditions remain active while the popover is closed', async () => {
    renderHome('/?kind=k&date_from=2026-09-01&date_to=2026-09-09')
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))

    const trigger = await screen.findByRole('button', { name: 'home.advanced (2)' })
    expect(trigger.closest('.ant-badge')?.textContent).toContain('2')
  })
})

describe('the version filter', () => {
  it('is absent while there is only one written form', async () => {
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()
    // The other filters are there; this one is not, because every setting of it would mean the same.
    // getAllByText: each filter's key renders twice, as the label and as the placeholder.
    expect(screen.getAllByText('home.category').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('home.version')).toHaveLength(0)
  })

  it('offers the written forms the server says are visible, by their labels', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()
    expect((await screen.findAllByText('home.version')).length).toBeGreaterThan(0)

    // The options are the server's labels, not the internal names: "人工" is what an author reads.
    await userEvent.click(document.querySelector('#version') as HTMLElement)
    expect(await screen.findByTitle('人工')).toBeTruthy()
    expect(screen.getByTitle('versions.default')).toBeTruthy()
  })

  it('asks the server for the chosen version', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()

    const select = document.querySelector('#version') as HTMLElement
    expect(select).toBeTruthy()
    await userEvent.click(select)
    await userEvent.click(await screen.findByTitle('人工'))
    await userEvent.click(screen.getByText('home.search'))

    await waitFor(() => expect(state.urls.some((u) => u.includes('version=manual'))).toBe(true))
  })

  it('comes back selected when the URL already carries it', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome('/?version=manual')
    // The very first request already carries it: the URL is the source of truth, not the form.
    await waitFor(() => expect(state.urls[0]).toContain('version=manual'))
    await openFilters()
    // And the control shows it, so the reader can see which filter is on and clear it.
    expect(await screen.findByTitle('人工')).toBeTruthy()
  })
})

describe('the home content mode', () => {
  it('shows favorites as a content view without sending the view flag to the report API', async () => {
    renderHome('/?view=favorites')

    expect(await screen.findByText('favorite.empty')).toBeTruthy()
    expect(screen.queryByText('home.advanced')).toBeNull()
    await waitFor(() => expect(state.urls.some((url) => url.startsWith('/api/home'))).toBe(true))
    expect(state.urls.find((url) => url.startsWith('/api/home'))).not.toContain('view=')
  })
})

describe('the live price on a card', () => {
  it('does not hold the feed back: the cards are readable while the batch is still in flight', async () => {
    state.resp = { ...base, groups: [group('a', '600519'), group('b', '')], totalRuns: 2 }
    renderHome()

    // The reports are on screen and the quote request has NOT answered — the deferred is still in
    // state.quotes, unsettled, for the whole of this test.
    expect(await screen.findByText('N600519')).toBeTruthy()
    expect(screen.getByText('Tb')).toBeTruthy()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // The hole the price will drop into is already the size it will be when full, so nothing below
    // the card moves when the answer lands. It is reserved only for the card that names a code.
    const lines = screen.getAllByTestId('card-quote-line')
    expect(lines).toHaveLength(1)
    expect(lines[0].style.height).toBe('22px')
    expect(lines[0].textContent).toBe('')
    expect(screen.queryByTestId('card-quote')).toBeNull()
  })

  it('asks once for a page of cards, naming each symbol once however many cards carry it', async () => {
    // Four cards, three of which name a code and two of those the SAME code — a stock with reports
    // on two dates is two groups in this feed.
    state.resp = {
      ...base,
      groups: [group('a', '600519'), group('b', '600519'), group('c', '000001'), group('d', '')],
      totalRuns: 4,
    }
    renderHome()

    await waitFor(() => expect(state.quotes).toHaveLength(1))
    expect(decodeURIComponent(state.quotes[0].url)).toContain('symbols=sh:600519,sz:000001')

    await settle(state.quotes[0], quoteBody({ sh600519: [3335, 12, '0.12'] }))
    await waitFor(() => expect(screen.getAllByTestId('card-quote')).toHaveLength(2))

    // One request for four cards, and still one after the answer landed: a card does not fetch its
    // own price, and the arrival of prices does not start a second round.
    expect(quoteUrls()).toHaveLength(1)
  })

  it('prints the vendor’s own numbers: 分 formatted once, and the percentage verbatim', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 1 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // The percentage is pinned to a value NOTHING here could have derived. The previous close never
    // reaches the browser, so the only recomputations available to a card are 12/3335 (0.36%) and
    // 12/3323 (0.36%) — neither of which is 0.12. If 0.12% renders, it was carried verbatim, which
    // is the ex-rights rule of ADR 0028 §4 holding on this surface too.
    await settle(state.quotes[0], quoteBody({ sh600519: [3335, 12, '0.12'] }))

    const line = await screen.findByTestId('card-quote-line')
    expect(line.textContent).toBe('33.35+0.120.12%')
    expect(line.style.height).toBe('22px')
  })

  it('colours a rise red and a fall green, the A-share way round', async () => {
    state.resp = { ...base, groups: [group('a', '600519'), group('b', '000001')], totalRuns: 2 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))
    await settle(state.quotes[0], quoteBody({ '600519': [3335, 12, '0.12'], '000001': [1180, -25, '-2.07'] }))

    const [up, down] = await screen.findAllByTestId('card-quote')
    const rose = rgb(up.style.color)
    const fell = rgb(down.style.color)
    expect(rose.r).toBeGreaterThan(rose.g)
    expect(fell.g).toBeGreaterThan(fell.r)
  })

  it('leaves the cards exactly as they are when the batch fails, and says nothing about it', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 1 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    await fail(state.quotes[0])

    // The report is still there, the reserved line is still the same height and still empty, and
    // there is no banner, no retry and no status message anywhere: a dead quote vendor is not news
    // on a page about reports.
    expect(screen.getByText('N600519')).toBeTruthy()
    const line = screen.getByTestId('card-quote-line')
    expect(line.textContent).toBe('')
    expect(line.style.height).toBe('22px')
    expect(screen.queryByTestId('card-quote')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByText('common.loadFailedContent')).toBeNull()
    expect(screen.queryByText('quote.unavailable')).toBeNull()
    expect(screen.queryByText('common.error')).toBeNull()
  })

  it('renders a card the answer said nothing about exactly as it was', async () => {
    state.resp = { ...base, groups: [group('a', '600519'), group('b', '000001')], totalRuns: 2 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // A per-symbol batch answers per symbol: one code was served and the other was not.
    await settle(state.quotes[0], quoteBody({ '600519': [3335, 12, '0.12'] }))

    await waitFor(() => expect(screen.getAllByTestId('card-quote')).toHaveLength(1))
    expect(screen.getByText('N000001')).toBeTruthy()
    const lines = screen.getAllByTestId('card-quote-line')
    expect(lines).toHaveLength(2)
    expect(lines[0].textContent).toBe('33.35+0.120.12%')
    expect(lines[1].textContent).toBe('')
    expect(lines[1].style.height).toBe(lines[0].style.height)
  })

  it('ignores an answer whose page has already been left', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 60 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // Page two, which happens to carry the same stock — so the stale answer below is not merely
    // about a card that has gone: it names a symbol that is still on screen and would paint.
    state.resp = { ...base, groups: [group('b', '600519'), group('c', '000001')], totalRuns: 60, page: 2 }
    await userEvent.click(screen.getByTitle('2'))
    await waitFor(() => expect(screen.getByText('N000001')).toBeTruthy())
    await waitFor(() => expect(state.quotes).toHaveLength(2))

    // The first request answers last, as a slow one does.
    await settle(state.quotes[0], quoteBody({ '600519': [11111, 999, '9.99'] }))
    expect(screen.queryByTestId('card-quote')).toBeNull()

    // ...and the request that belongs to what is on screen is the one that paints.
    await settle(state.quotes[1], quoteBody({ '600519': [3335, 12, '0.12'] }))
    const line = await screen.findByTestId('card-quote')
    expect(line.textContent).toBe('33.35')
    expect(screen.queryByText('111.11')).toBeNull()
  })

  it('does not re-ask the vendor when the feed silently refreshes the same cards', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 1 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))
    await settle(state.quotes[0], quoteBody({ '600519': [3335, 12, '0.12'] }))
    const homeCalls = () => state.urls.filter((u) => u.startsWith('/api/home')).length
    const before = homeCalls()

    // The home feed refetches itself silently every 60s and the moment the tab becomes visible
    // (startVisiblePoll), replacing `data` even when the cards are identical. That is a re-render
    // of every card with a brand-new symbol array, and it must NOT be a second list of symbols
    // sent to a third-party vendor.
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await waitFor(() => expect(homeCalls()).toBeGreaterThan(before))

    expect(quoteUrls()).toHaveLength(1)
    expect(screen.getByTestId('card-quote').textContent).toBe('33.35')
  })

  it('refreshes an idle visible page from the batch advice without multiplying the request', async () => {
    vi.useFakeTimers()
    state.resp = { ...base, groups: [group('a', '600519'), group('b', '000001')], totalRuns: 2 }
    renderHome()
    await vi.advanceTimersByTimeAsync(0)
    expect(state.quotes).toHaveLength(1)
    await settle(state.quotes[0], {
      ...quoteBody({ '600519': [3335, 12, '0.12'], '000001': [1180, -25, '-2.07'] }),
      refreshAfterSecs: 2,
    })

    await vi.advanceTimersByTimeAsync(1999)
    expect(state.quotes).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(state.quotes).toHaveLength(2)
    expect(quoteUrls()).toHaveLength(2)
  })

  it('does not let a superseded failure erase the prices that are on screen', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 60 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    state.resp = { ...base, groups: [group('b', '600519'), group('c', '000001')], totalRuns: 60, page: 2 }
    await userEvent.click(screen.getByTitle('2'))
    await waitFor(() => expect(state.quotes).toHaveLength(2))
    await settle(state.quotes[1], quoteBody({ '600519': [3335, 12, '0.12'] }))
    expect(await screen.findByTestId('card-quote')).toBeTruthy()

    // The abandoned request now gives up — and it is abandoned, so its failure is not news about
    // the prices currently on the page. The clearing branch is guarded by the same flag the
    // painting branch is, and this is the half a test that only ever resolves would miss.
    await fail(state.quotes[0])
    expect(screen.getByTestId('card-quote').textContent).toBe('33.35')
  })

  it('drops the previous page’s prices when the batch for the page now on screen fails', async () => {
    // The case the failure handler exists for, and the one a first-batch failure cannot reach: the
    // map is only ever non-empty by the time a SECOND page asks. Page one answers…
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 60 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))
    await settle(state.quotes[0], quoteBody({ '600519': [11111, 999, '9.99'] }))
    expect((await screen.findByTestId('card-quote')).textContent).toBe('111.11')

    // …the reader steps to page two, which carries the same stock — the ordinary case, since a
    // stock with reports on three dates is three groups in this feed.
    state.resp = { ...base, groups: [group('b', '600519'), group('c', '000001')], totalRuns: 60, page: 2 }
    await userEvent.click(screen.getByTitle('2'))
    await waitFor(() => expect(screen.getByText('N000001')).toBeTruthy())
    await waitFor(() => expect(state.quotes).toHaveLength(2))

    // …and page two's batch fails. Page one's price is not an answer about page two: without the
    // clear, 111.11 is still in the map, 600519 is still on screen, and this card would print a
    // price nobody asked the vendor for — indistinguishable, to a reader, from a live one.
    await fail(state.quotes[1])
    expect(screen.queryByTestId('card-quote')).toBeNull()
    expect(screen.queryByText('111.11')).toBeNull()
    const lines = screen.getAllByTestId('card-quote-line')
    expect(lines).toHaveLength(2)
    expect(lines.map((l) => l.textContent)).toEqual(['', ''])
    // And still nothing said about it, exactly as when the first batch fails.
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('names at most 50 symbols, so an over-sized page loses its tail and not its prices', async () => {
    // ?size= is an editable query parameter and the server REFUSES a batch naming more than 50
    // (quote_api.go's quoteBatchMax, counted before de-duplication). Asking for 51 would not cost
    // this page its 51st price — it would cost it all 60, as one 400.
    const many = Array.from({ length: 60 }, (_, i) => group(`g${i}`, String(600000 + i)))
    state.resp = { ...base, groups: many, totalRuns: 60 }
    renderHome()

    await waitFor(() => expect(state.quotes).toHaveLength(1))
    const asked = decodeURIComponent(state.quotes[0].url).replace('/api/quotes?symbols=', '').split(',')
    expect(asked).toHaveLength(50)
    // The first 50 in first-appearance order: the tail is dropped, not a sample of the middle.
    expect(asked[0]).toBe('sh:600000')
    expect(asked[49]).toBe('sh:600049')
    expect(asked).not.toContain('sh:600050')
    expect(asked).not.toContain('sh:600059')

    // The 50 that were asked about still get their prices; the tail renders exactly as an unquoted
    // card already does.
    await settle(state.quotes[0], quoteBody({ '600000': [3335, 12, '0.12'], '600049': [1180, -25, '-2.07'] }))
    await waitFor(() => expect(screen.getAllByTestId('card-quote')).toHaveLength(2))
    expect(quoteUrls()).toHaveLength(1)
  })

  it('asks nothing at all for a page whose reports name no code', async () => {
    // A feed of purely thematic reports. An empty `symbols=` is not a smaller question, it is a
    // 400 (quote_bad_symbol): the page would spend a round trip on every view to be refused.
    state.resp = { ...base, groups: [group('a', ''), group('b', '')], totalRuns: 2 }
    renderHome()

    expect(await screen.findByText('Ta')).toBeTruthy()
    expect(screen.getByText('Tb')).toBeTruthy()
    // The home request went out, so the effect that would have asked has certainly run by now.
    await waitFor(() => expect(state.urls.some((u) => u.startsWith('/api/home'))).toBe(true))
    expect(quoteUrls()).toHaveLength(0)
    expect(state.quotes).toHaveLength(0)
    // And no card reserved a line it can never fill.
    expect(screen.queryAllByTestId('card-quote-line')).toHaveLength(0)
  })

  it('ignores a batch entry it cannot read, and paints the ones it can', async () => {
    state.resp = {
      ...base,
      groups: [group('a', '600519'), group('b', '000001'), group('c', '000002')],
      totalRuns: 3,
    }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // 分 are integers and the percentage is the vendor's own STRING. An entry that swaps those —
    // an older build's shape, a proxy that stringified a body, a per-symbol failure encoded as an
    // object — is not a price this page can print, and printing it anyway means "33.35" formatted
    // out of a string and a NaN in the money column. Unreadable and absent are the same thing.
    await settle(state.quotes[0], {
      quotes: {
        sh600519: { symbol: '600519', market: 'sh', name: 'N600519', currency: 'CNY', kind: 'stock', last: 3335, change: 12, changePct: '0.12' },
        sz000001: { symbol: '000001', market: 'sz', name: 'N000001', currency: 'CNY', kind: 'stock', last: '1180', change: -25, changePct: '-2.07' },
        sz000002: { symbol: '000002', market: 'sz', name: 'N000002', currency: 'CNY', kind: 'stock', last: 1180, change: -25, changePct: -2.07 },
      },
    })

    await waitFor(() => expect(screen.getAllByTestId('card-quote')).toHaveLength(1))
    expect(screen.getByTestId('card-quote').textContent).toBe('33.35')
    const lines = screen.getAllByTestId('card-quote-line')
    expect(lines.map((l) => l.textContent)).toEqual(['33.35+0.120.12%', '', ''])
    expect(screen.queryByText(/NaN/)).toBeNull()
  })

  it('leaves the cards alone for a body of a shape this build cannot read', async () => {
    state.resp = { ...base, groups: [group('a', '600519')], totalRuns: 1 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // A LIST where the endpoint promises a map keyed by symbol, carrying an otherwise perfect
    // entry. Read positionally it would enter the map under "0" — or, if the element's own symbol
    // were trusted, under a shape /api/quotes has never promised to send. Neither is a quote this
    // page is entitled to print, and neither may throw during render.
    await settle(state.quotes[0], { quotes: [{ symbol: '600519', last: 3335, change: 12, changePct: '0.12' }] })

    expect(screen.getByText('N600519')).toBeTruthy()
    expect(screen.queryByTestId('card-quote')).toBeNull()
    expect(screen.getByTestId('card-quote-line').textContent).toBe('')
  })

  it('still holds the reserved line when the feature is switched off — not the grid from before it', async () => {
    state.resp = { ...base, groups: [group('a', '600519'), group('b', '')], totalRuns: 2 }
    renderHome()
    await waitFor(() => expect(state.quotes).toHaveLength(1))

    // What an operator who turned the home cards off on 管理 → 行情源 gets: the endpoint answers
    // `enabled:false` with an empty map rather than a refusal (quote_api.go).
    await settle(state.quotes[0], { enabled: false, quotes: {} })

    // The cost of the feature being off is stated here rather than in a comment claiming there is
    // none: every card that names a code keeps its 22 blank pixels, permanently. That is the trade
    // the reservation buys — the alternative is a grid that re-flows under a reader whenever a
    // late answer lands — and a build that "restores the old grid" by making the line conditional
    // fails here rather than in front of somebody mid-sentence.
    expect(screen.queryByTestId('card-quote')).toBeNull()
    const lines = screen.getAllByTestId('card-quote-line')
    expect(lines).toHaveLength(1)
    expect(lines[0].style.height).toBe('22px')
    expect(lines[0].textContent).toBe('')
    // And the thematic card, which can never be quoted, pays nothing.
    expect(screen.getByText('Tb')).toBeTruthy()
  })
})

describe('the report categories on a card', () => {
  it('shows at most three categories and summarizes the remainder', async () => {
    const g = group('a', '')
    g.kinds = ['kind-one', 'kind-two', 'kind-three', 'kind-four', 'kind-five']
    state.resp = { ...base, groups: [g], totalRuns: 1 }
    renderHome()

    expect(await screen.findByText('kind-one')).toBeTruthy()
    expect(screen.getByText('kind-two')).toBeTruthy()
    expect(screen.getByText('kind-three')).toBeTruthy()
    expect(screen.queryByText('kind-four')).toBeNull()
    expect(screen.queryByText('kind-five')).toBeNull()
    expect(screen.getByText('+2')).toBeTruthy()
  })
})
