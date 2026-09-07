import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router'
import QuotesApp from './QuotesApp'
import { ApiError } from '../api/client'
import en from '../locales/en-US.json'

// The quotes app is the page that took the full-screen chart off the reading page, and everything
// worth pinning here is about what it does with a REQUEST rather than about how a candle is drawn:
// which symbol it asks for, which answer it is allowed to believe, and what it says when there is
// no answer at all. The chart and the strip have their own suites.

// Every request is parked instead of resolving, so a test decides when — and in what ORDER — the
// answers arrive. Nothing else can express "the older answer landed last", which is the failure
// this page's cancelled flag exists for.
interface Pending {
  url: string
  resolve: (v: unknown) => void
  reject: (e: unknown) => void
}
const pending: Pending[] = []

// Only api.get is replaced. ApiError and errText are the real ones: the page decides whether to
// offer a retry from `instanceof ApiError` and its `code`, and a hand-written stand-in for either
// would let the page's actual branch rot while this file kept passing.
vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      get: (url: string) =>
        new Promise((resolve, reject) => {
          pending.push({ url, resolve, reject })
        }),
    },
  }
})

// The REAL English bundle, echoing the key when it has no string — not `t = (k) => k`. errText
// decides whether a server code has a translation by checking whether t() handed the key straight
// back, so an identity t makes every code look untranslated and the page prints the server's raw
// English message instead of the localized line these tests are about.
const dict = en as Record<string, string>
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => (en as Record<string, string>)[k] ?? k }),
}))

// The strip and the chart belong to another file in this change and carry their own tests. Standing
// in for them keeps this suite about the page's job, and lets it read the props that ARE the job:
// the payload handed to the strip, and the pixel height handed to the chart.
interface ChartProps {
  bars: unknown[]
  loading?: boolean
  unavailable?: string
  height?: number
  currency?: string
}
const chartProps: ChartProps[] = []
vi.mock('../components/QuoteStrip', () => ({
  default: ({ data }: { data: { symbol: string; name: string } | null }) => (
    <div data-testid="strip">{data ? `strip:${data.symbol}:${data.name}` : 'strip:none'}</div>
  ),
}))
vi.mock('../components/PriceChart', () => ({
  default: (p: ChartProps) => {
    chartProps.push(p)
    return <div data-testid="chart">{`chart:${p.bars.length}:${p.currency ?? ''}`}</div>
  },
}))

const lastChart = () => chartProps[chartProps.length - 1]

// One body shape for every market, because there is only one: the endpoint answers the same
// QuoteResp for Shanghai, Hong Kong and New York, and only market / kind / currency differ. Prices
// are integer minor units throughout (ADR 0028 §10).
function quote(over: Record<string, unknown>) {
  return {
    symbol: '600519',
    name: 'x',
    market: 'sh',
    kind: 'stock',
    currency: 'CNY',
    tz: 'Asia/Shanghai',
    source: 'tencent',
    snapshot: {
      last: 3335,
      prevClose: 3331,
      open: 3385,
      high: 3405,
      low: 3307,
      change: 4,
      changePct: '0.12',
      volume: 176934100,
      amount: 5939060556,
      asOf: '2026-09-04T16:14:58+08:00',
      session: 'close',
    },
    bars: [
      { d: '2026-09-03', o: 3325, h: 3366, l: 3295, c: 3331, v: 169589800 },
      { d: '2026-09-04', o: 3385, h: 3405, l: 3307, c: 3335, v: 176934100 },
    ],
    barsSource: 'tencent',
    barsUnavailable: '',
    adjusted: false,
    cached: false,
    ...over,
  }
}

const MAOTAI = quote({ symbol: '600519', name: '贵州茅台', market: 'sh', currency: 'CNY' })
const TENCENT = quote({ symbol: '00700', name: '腾讯控股', market: 'hk', currency: 'HKD', tz: 'Asia/Hong_Kong' })
const APPLE = quote({ symbol: 'AAPL', name: 'Apple Inc', market: 'us', currency: 'USD', tz: 'America/New_York' })
const SSE_INDEX = quote({ symbol: '000001', name: '上证指数', market: 'sh', kind: 'index', currency: 'CNY' })

function Loc() {
  const l = useLocation()
  return <div data-testid="loc">{l.search}</div>
}

// Browser back, which is the ONLY way the URL's symbol changes without the search box being
// touched. Nothing else in this suite can drive that case: typing and mounting both set the draft
// themselves, so the effect that syncs the box to the URL never does observable work under them.
function Nav() {
  const nav = useNavigate()
  return (
    <button type="button" onClick={() => nav(-1)}>
      nav:back
    </button>
  )
}

function renderApp(path = '/apps/quotes') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="/apps/quotes"
          element={
            <>
              <QuotesApp />
              <Loc />
              <Nav />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

const box = () => screen.getByPlaceholderText(dict['quote.searchPlaceholder'])

function setViewport(height: number, width: number) {
  Object.defineProperty(window, 'innerHeight', { value: height, configurable: true })
  Object.defineProperty(window, 'innerWidth', { value: width, configurable: true })
}

beforeEach(() => {
  pending.length = 0
  chartProps.length = 0
})
afterEach(() => {
  // jsdom's own defaults, so a viewport a height test moved does not follow the suite around.
  setViewport(768, 1024)
})

describe('QuotesApp', () => {
  it('shows the empty state and asks the server for nothing until a code is typed', () => {
    renderApp()
    expect(screen.getByText(dict['quote.noSymbol'])).toBeTruthy()
    // An empty box must not become `/api/quote/` — a request whose only possible answer is a
    // refusal, sent on every visit to the app.
    expect(pending).toHaveLength(0)
    expect(screen.queryByTestId('chart')).toBeNull()
  })

  const CASES = [
    { what: 'an A-share code', typed: '600519', resp: MAOTAI, market: 'quote.market.sh', index: false, reports: true },
    { what: 'a Hong Kong code', typed: '00700', resp: TENCENT, market: 'quote.market.hk', index: false, reports: false },
    { what: 'a US ticker', typed: 'AAPL', resp: APPLE, market: 'quote.market.us', index: false, reports: false },
    { what: 'an index', typed: '000001', resp: SSE_INDEX, market: 'quote.market.sh', index: true, reports: false },
  ]

  for (const c of CASES) {
    it(`fetches and renders ${c.what}`, async () => {
      renderApp()
      await userEvent.type(box(), `${c.typed}{Enter}`)
      await waitFor(() => expect(pending).toHaveLength(1))
      expect(pending[0].url).toBe(`/api/quote/${c.typed}?range=3m`)
      await act(async () => {
        pending[0].resolve(c.resp)
      })

      // The strip got the answer, not a placeholder: the name only exists in the payload.
      expect(await screen.findByText(`strip:${c.resp.symbol}:${c.resp.name}`)).toBeTruthy()
      expect(lastChart().bars).toHaveLength(2)
      expect(lastChart().currency).toBe(c.resp.currency)

      // 00700 and 000700 are one keystroke apart and neither looks like the other market's code,
      // so the badge is the only thing on screen that says which exchange answered.
      expect(screen.getByTestId('quote-market').textContent).toBe(dict[c.market])
      expect(Boolean(screen.queryByTestId('quote-index'))).toBe(c.index)

      // The portal analyses A-shares and nothing else. A US ticker, a Hong Kong code and an index
      // all have to say so; a Shanghai stock must NOT, because for it the promise is kept.
      const note = screen.queryByTestId('quote-no-reports')
      expect(Boolean(note)).toBe(!c.reports)
      if (note) expect(note.textContent).toBe(dict['err.no_reports_for_symbol'])
    })
  }

  it('puts the symbol in the URL and loads one that arrives in it, so a chart can be linked', async () => {
    renderApp('/apps/quotes?symbol=00700')
    await waitFor(() => expect(pending).toHaveLength(1))
    expect(pending[0].url).toBe('/api/quote/00700?range=3m')
    await act(async () => {
      pending[0].resolve(TENCENT)
    })
    // A link that reloads into the app has to refill the box too, or the page describes one symbol
    // while the control above it offers another.
    expect(await screen.findByDisplayValue('00700')).toBeTruthy()

    await userEvent.clear(box())
    await userEvent.type(box(), 'AAPL{Enter}')
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('?symbol=AAPL'))
  })

  it('re-requests the window when the range is switched, and records the choice in the URL', async () => {
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await screen.findByTestId('chart')

    await userEvent.click(screen.getByText(dict['quote.range.1y']))
    await waitFor(() => expect(pending).toHaveLength(2))
    expect(pending[1].url).toBe('/api/quote/600519?range=1y')
    expect(screen.getByTestId('loc').textContent).toBe('?symbol=600519&range=1y')
  })

  it('offers no retry for a symbol the server refused', async () => {
    renderApp()
    await userEvent.type(box(), 'NOTACODE{Enter}')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].reject(new ApiError(400, 'bad symbol', 'quote_bad_symbol'))
    })

    expect(await screen.findByText(dict['err.quote_bad_symbol'])).toBeTruthy()
    // The identical request is refused identically for as long as the page is open, so a retry
    // button here sells a permanent refusal as a bad minute.
    expect(screen.queryByText(dict['quote.retry'])).toBeNull()
    expect(screen.queryByTestId('chart')).toBeNull()
    // The refused code, so the reader can see WHICH of the things they typed came back wrong.
    expect(screen.getByText('NOTACODE')).toBeTruthy()
  })

  it('offers a retry for a source outage, and the button actually re-requests', async () => {
    renderApp()
    await userEvent.type(box(), '600519{Enter}')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].reject(new ApiError(502, 'upstream failed', 'quote_unavailable'))
    })

    expect(await screen.findByText(dict['err.quote_unavailable'])).toBeTruthy()
    await userEvent.click(screen.getByText(dict['quote.retry']))
    // A retry that changes no state issues no request: the URL is unchanged, so only a nonce can
    // make the effect run again.
    await waitFor(() => expect(pending).toHaveLength(2))
    expect(pending[1].url).toBe('/api/quote/600519?range=3m')
  })

  it('drops a superseded answer that lands after the newer one', async () => {
    renderApp()
    await userEvent.type(box(), '600519{Enter}')
    await waitFor(() => expect(pending).toHaveLength(1))
    await userEvent.clear(box())
    await userEvent.type(box(), 'AAPL{Enter}')
    await waitFor(() => expect(pending).toHaveLength(2))
    const [older, newer] = pending
    expect(older.url).toContain('600519')
    expect(newer.url).toContain('AAPL')

    // Out of order on purpose: the second request answers first, then the abandoned Shanghai
    // request finally comes back. Without the effect's cancelled flag the late answer wins simply
    // by being late, and the page shows a 贵州茅台 price with AAPL in the search box.
    await act(async () => {
      newer.resolve(APPLE)
    })
    expect(await screen.findByText('strip:AAPL:Apple Inc')).toBeTruthy()

    await act(async () => {
      older.resolve(MAOTAI)
    })
    expect(screen.getByTestId('strip').textContent).toBe('strip:AAPL:Apple Inc')
    expect(screen.getByTestId('quote-market').textContent).toBe(dict['quote.market.us'])
    expect(lastChart().currency).toBe('USD')
  })

  it('drops the previous instrument when the symbol changes, and keeps it when only the range does', async () => {
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    expect(await screen.findByText('strip:600519:贵州茅台')).toBeTruthy()

    // A RANGE switch is the same instrument through a different window, so the card stays up for
    // the whole round trip. Blanking it here would make every range click flash, and every fact on
    // screen — name, badge, price — is still true while the new window is fetched.
    await userEvent.click(screen.getByText(dict['quote.range.1y']))
    await waitFor(() => expect(pending).toHaveLength(2))
    expect(screen.getByTestId('strip').textContent).toBe('strip:600519:贵州茅台')
    expect(screen.getByTestId('quote-market').textContent).toBe(dict['quote.market.sh'])
    await act(async () => {
      pending[1].resolve(MAOTAI)
    })

    // A SYMBOL switch is a DIFFERENT instrument, and every one of those facts now belongs to the
    // old one. The strip prints its own data.symbol so it stays internally consistent, but the
    // card's header and market badge are the page's own: leaving them up puts 贵州茅台 and an `sh`
    // badge over an in-flight US request, for as long as the vendor takes to answer.
    await userEvent.clear(box())
    await userEvent.type(box(), 'AAPL{Enter}')
    await waitFor(() => expect(pending).toHaveLength(3))
    expect(pending[2].url).toBe('/api/quote/AAPL?range=1y')
    expect(screen.queryByTestId('strip')).toBeNull()
    expect(screen.queryByText('贵州茅台')).toBeNull()
    expect(screen.queryByTestId('quote-market')).toBeNull()
    // What the header falls back to is what was ASKED for, never what was showing.
    expect(screen.getByText('AAPL')).toBeTruthy()

    await act(async () => {
      pending[2].resolve(APPLE)
    })
    expect(await screen.findByText('strip:AAPL:Apple Inc')).toBeTruthy()
    expect(screen.getByTestId('quote-market').textContent).toBe(dict['quote.market.us'])
  })

  it('refills the search box when back or forward changes the symbol under it', async () => {
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    expect(await screen.findByDisplayValue('600519')).toBeTruthy()

    await userEvent.clear(box())
    await userEvent.type(box(), 'AAPL{Enter}')
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('?symbol=AAPL'))
    expect((box() as HTMLInputElement).value).toBe('AAPL')

    // Back moves the URL without touching the box. Without the sync the box keeps saying AAPL
    // while the card under it reloads 600519 — the page describing one instrument and offering
    // another, and the next Enter would re-request the one the reader just navigated away from.
    await userEvent.click(screen.getByText('nav:back'))
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('?symbol=600519'))
    await waitFor(() => expect((box() as HTMLInputElement).value).toBe('600519'))
  })

  it('falls back to the floor for a viewport height it cannot measure', async () => {
    // jsdom, a detached document and an obscure browser can all hand back 0 or a non-finite number.
    // NaN is the dangerous one: Math.round(NaN * 0.6) is NaN, Math.min and Math.max both propagate
    // it, and the height would reach the chart — and its SVG viewBox — as NaN rather than as a
    // short chart. A missing measurement gets the same answer as a tiny window.
    setViewport(Number.NaN, 1024)
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await screen.findByTestId('chart')
    expect(lastChart().height).toBe(320)
  })

  it('takes the chart height from the viewport height and never from its width', async () => {
    setViewport(600, 1200)
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await screen.findByTestId('chart')
    expect(lastChart().height).toBe(360) // min(60vh, 520)

    // THE regression this whole app exists to prevent. The chart used to be a fixed viewBox scaled
    // by width:100%/height:auto, so its height was its width times 0.417 and a wide monitor buried
    // the report under it. Widening the window must move nothing.
    setViewport(600, 3000)
    await act(async () => {
      window.dispatchEvent(new Event('resize'))
    })
    expect(lastChart().height).toBe(360)

    // A short window gets the floor rather than a chart too thin to hold two panels and a date axis.
    setViewport(300, 3000)
    await act(async () => {
      window.dispatchEvent(new Event('resize'))
    })
    expect(lastChart().height).toBe(320)
  })
})
