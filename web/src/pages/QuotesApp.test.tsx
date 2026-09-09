import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router'
import QuotesApp, { quoteSearchParams } from './QuotesApp'
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
  /**
   * Declared by the page and never sniffed from the bars — a string here rather than the chart's
   * own union, so what these tests assert is the VALUE that arrived and not merely that something
   * type-checked. `undefined` is a real answer and a failing one: it is what a page that added the
   * intraday windows and forgot to say so hands over.
   */
  interval?: string
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
vi.mock('../components/FavoriteButton', () => ({
  FavoriteButton: ({ market, symbol }: { market: string; symbol: string }) => (
    <span data-testid="favorite-button">{`${market}${symbol}`}</span>
  ),
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
  vi.useRealTimers()
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

  it('refreshes the same request when a visible idle page reaches the advised delay', async () => {
    vi.useFakeTimers()
    renderApp('/apps/quotes?symbol=600519')
    await vi.advanceTimersByTimeAsync(0)
    expect(pending).toHaveLength(1)
    await act(async () => {
      pending[0].resolve({ ...MAOTAI, refreshAfterSecs: 2 })
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1999)
    })
    expect(pending).toHaveLength(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(pending).toHaveLength(2)
    expect(pending[1].url).toBe('/api/quote/600519?range=3m')
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
      expect(screen.getByTestId('favorite-button').textContent).toBe(`${c.resp.market}${c.resp.symbol}`)
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
  // 分时 (1d) and 5 日 (5d). These are not merely shorter windows: they are about 400 one-minute
  // points and about 330 five-minute ones against the 22-250 a daily range carries, which is why
  // the chart draws them as a line. Two independent things have to be right, and the second is the
  // one with no visible symptom — the window that is REQUESTED, and what the chart is TOLD it was
  // handed. A page that asks for 1d and lets the chart go on believing it has candles draws 400
  // two-pixel candles under a date axis and raises nothing.
  function intradayQuote(over: Record<string, unknown> = {}) {
    return quote({
      // The SAME instrument the daily fixture describes: a range switch changes the window and
      // nothing else, and a fixture that quietly changed the name too would let a page that blanked
      // the card on every range click pass this suite.
      symbol: '600519',
      name: '贵州茅台',
      // A full stamp where a daily bar carries a bare date. The chart is never allowed to read this
      // difference — the page declares the interval — and the fixture carries it so that a page
      // which stopped declaring it could not be rescued by the shape of its own data.
      bars: [
        { d: '2026-09-07 09:31', o: 3331, h: 3336, l: 3330, c: 3335, v: 12000 },
        { d: '2026-09-07 09:32', o: 3335, h: 3340, l: 3334, c: 3338, v: 9800 },
        { d: '2026-09-07 09:33', o: 3338, h: 3339, l: 3330, c: 3332, v: 8700 },
      ],
      ...over,
    })
  }

  for (const r of ['1d', '5d'] as const) {
    it(`asks for the ${r} window and tells the chart the series is intraday`, async () => {
      renderApp('/apps/quotes?symbol=600519')
      await waitFor(() => expect(pending).toHaveLength(1))
      await act(async () => {
        pending[0].resolve(MAOTAI)
      })
      await screen.findByTestId('chart')
      expect(lastChart().interval).toBe('daily')

      await userEvent.click(screen.getByText(dict[`quote.range.${r}`]))
      await waitFor(() => expect(pending).toHaveLength(2))
      expect(pending[1].url).toBe(`/api/quote/600519?range=${r}`)
      // An explicit window is worth writing into the URL; only the default 3m stays out of it.
      expect(screen.getByTestId('loc').textContent).toBe(`?symbol=600519&range=${r}`)

      await act(async () => {
        pending[1].resolve(intradayQuote())
      })
      await waitFor(() => expect(lastChart().interval).toBe('intraday'))
      expect(lastChart().bars).toHaveLength(3)
      // Still the same instrument. Changing the shape of the picture is not changing the subject.
      expect(screen.getByTestId('strip').textContent).toBe('strip:600519:贵州茅台')
    })
  }

  it('loads an intraday window that arrives in the URL, so a 分时 chart is linkable too', async () => {
    renderApp('/apps/quotes?symbol=600519&range=1d')
    await waitFor(() => expect(pending).toHaveLength(1))
    // A range the page does not know about falls back to 3m silently, so a 1d that reached the
    // vendor as 3m would look like nothing worse than a slightly wrong chart.
    expect(pending[0].url).toBe('/api/quote/600519?range=1d')
    await act(async () => {
      pending[0].resolve(intradayQuote())
    })
    await screen.findByTestId('chart')
    expect(lastChart().interval).toBe('intraday')
  })

  it('keeps the bars it still has labelled as what they are while the next window is in flight', async () => {
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await screen.findByTestId('chart')

    await userEvent.click(screen.getByText(dict['quote.range.1d']))
    await waitFor(() => expect(pending).toHaveLength(2))

    // The previous window stays up for the whole round trip — that is the property the symbol/range
    // split exists for — so what the chart is holding here is still three months of DAILY candles.
    // Taking the interval from the URL's range instead of from the answer's would tell it to draw
    // them as a 分时 line under a clock axis, for exactly as long as the vendor is slow: a chart
    // mislabelling what it is showing, which is the one failure a price chart must not have.
    expect(lastChart().bars).toHaveLength(2)
    expect(lastChart().bars[0]).toEqual(MAOTAI.bars[0])
    expect(lastChart().interval).toBe('daily')
    expect(screen.getByTestId('strip').textContent).toBe('strip:600519:贵州茅台')

    await act(async () => {
      pending[1].resolve(intradayQuote())
    })
    await waitFor(() => expect(lastChart().interval).toBe('intraday'))

    // And the mirror image: an intraday series left on screen under a daily range would be drawn as
    // three minute-bars' worth of candles labelled 六个月.
    await userEvent.click(screen.getByText(dict['quote.range.6m']))
    await waitFor(() => expect(pending).toHaveLength(3))
    expect(pending[2].url).toBe('/api/quote/600519?range=6m')
    expect(lastChart().bars).toHaveLength(3)
    expect(lastChart().interval).toBe('intraday')
    await act(async () => {
      pending[2].resolve(MAOTAI)
    })
    await waitFor(() => expect(lastChart().interval).toBe('daily'))
  })

  it('offers every window before the first answer lands', async () => {
    renderApp('/apps/quotes?symbol=600519')
    await waitFor(() => expect(pending).toHaveLength(1))
    // Nothing has answered yet, and the switcher is already there. A control that appears with the
    // data looks broken for exactly as long as the vendor is slow — and 分时 is the window a person
    // reaches for first when they came to watch a price move.
    expect(screen.queryByTestId('chart')).toBeNull()
    for (const r of ['1d', '5d', '1m', '3m', '6m', '1y']) {
      expect(screen.getByText(dict[`quote.range.${r}`])).toBeTruthy()
    }
  })

  // WHICH SOURCE DREW THE CHART. The strip prints one source line and it is the SNAPSHOT's; now
  // that a source declares what it can serve per interval rather than per market, one answer can
  // carry a snapshot from one vendor and a series from another, and letting that single line stand
  // for both attributes a chart to a vendor that never drew it.
  const BARS_SOURCES = [
    { what: 'a series drawn by another vendor', over: { barsSource: 'yahoo' }, named: 'quote.source.yahoo' },
    // The ordinary answer. Printing "Tencent" twice, six lines apart, is noise that trains a reader
    // to stop reading the line that matters in the case above.
    { what: 'one vendor for both halves', over: { barsSource: 'tencent' }, named: '' },
    // i18next echoes a key it has no string for, so this would print the literal
    // `quote.source.acme` under somebody's chart. Saying nothing is the honest answer, the same
    // rule the market badge follows.
    { what: 'a source this build has no name for', over: { barsSource: 'acme' }, named: '' },
  ]

  for (const c of BARS_SOURCES) {
    it(`names the source behind the chart: ${c.what}`, async () => {
      renderApp('/apps/quotes?symbol=600519')
      await waitFor(() => expect(pending).toHaveLength(1))
      await act(async () => {
        pending[0].resolve(quote({ source: 'tencent', ...c.over }))
      })
      await screen.findByTestId('chart')

      const line = screen.queryByTestId('quote-bars-source')
      if (!c.named) {
        expect(line).toBeNull()
        return
      }
      expect(line?.textContent).toContain(dict['quote.source'])
      expect(line?.textContent).toContain(dict[c.named])
    })
  }
})

// ---------- the reader's own date window ----------

describe('QuotesApp — the date window', () => {
  it('sends from and to instead of a range, and never both', async () => {
    renderApp('/apps/quotes?symbol=600519&from=2026-03-02&to=2026-04-10')
    await waitFor(() => expect(pending).toHaveLength(1))
    // The window REPLACES the range. A request carrying both would let the server's range key
    // truncate a window the reader chose — and the server refuses to honour both for that reason,
    // so a page that sent both would depend on which of them it happened to win.
    expect(pending[0].url).toContain('from=2026-03-02')
    expect(pending[0].url).toContain('to=2026-04-10')
    expect(pending[0].url).not.toContain('range=')
  })

  it('drops a range key already in the URL when a window is set beside it', async () => {
    renderApp('/apps/quotes?symbol=600519&range=1y&from=2026-03-02&to=2026-04-10')
    await waitFor(() => expect(pending).toHaveLength(1))
    expect(pending[0].url).not.toContain('range=')
    expect(pending[0].url).toContain('from=2026-03-02')
  })

  it('ignores a half-open or malformed window and asks for the range instead', async () => {
    // The server refuses these with 400. The page must not SEND them: a request it knows will be
    // refused costs a round trip and puts an error banner over a chart that could have been drawn.
    for (const q of ['from=2026-03-02', 'to=2026-04-10', 'from=2026-13-01&to=2026-13-02', 'from=2026-3-2&to=2026-04-10']) {
      pending.length = 0
      const { unmount } = renderApp(`/apps/quotes?symbol=600519&${q}`)
      await waitFor(() => expect(pending).toHaveLength(1))
      expect(pending[0].url, q).not.toContain('from=')
      expect(pending[0].url, q).not.toContain('to=')
      unmount()
    }
  })

  it('labels a windowed answer as daily whatever range key the URL carried', async () => {
    // The chart is TOLD its interval. A window is always daily bars, so a stale `range=1d` must not
    // make the page announce a minute series — that is the mislabelling the interval prop exists to
    // prevent, arriving from the caller's side.
    renderApp('/apps/quotes?symbol=600519&range=1d&from=2026-03-02&to=2026-04-10')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await waitFor(() => expect(screen.getByTestId('chart')).toBeTruthy())
    expect(lastChart().interval).toBe('daily')
  })

  it('a preset click clears the window, and the two never both sit in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/apps/quotes?symbol=600519&from=2026-03-02&to=2026-04-10')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await waitFor(() => expect(screen.getByTestId('chart')).toBeTruthy())

    await user.click(screen.getByText(dict['quote.range.1y']))
    await waitFor(() => {
      const search = screen.getByTestId('loc').textContent || ''
      expect(search).toContain('range=1y')
      expect(search).not.toContain('from=')
      expect(search).not.toContain('to=')
    })
    // And the request that went out is the preset's, not the window's.
    expect(pending[pending.length - 1].url).toContain('range=1y')
    expect(pending[pending.length - 1].url).not.toContain('from=')
  })

  it('does not re-request when nothing but an unrelated render happened', async () => {
    // The window is rebuilt from the URL on every render, so an object in the effect's dependency
    // list would re-hit the vendor on every keystroke in the search box. The key is a string for
    // exactly this reason, and typing is the cheapest way to prove it.
    const user = userEvent.setup()
    renderApp('/apps/quotes?symbol=600519&from=2026-03-02&to=2026-04-10')
    await waitFor(() => expect(pending).toHaveLength(1))
    await act(async () => {
      pending[0].resolve(MAOTAI)
    })
    await waitFor(() => expect(screen.getByTestId('chart')).toBeTruthy())
    await user.type(box(), 'AAPL')
    expect(pending).toHaveLength(1)
  })
})

// The link the page produces, asserted directly. These are rules about what somebody pastes into a
// chat and what it reopens as, and the only gesture that reaches them through the page is antd's
// RangePicker — not a control to hang a correctness claim on.
describe('quoteSearchParams', () => {
  it('carries a window OR a range, never both', () => {
    const win = { from: '2026-03-02', to: '2026-04-10' }
    expect(quoteSearchParams({ symbol: '600519', range: '1y', win })).toEqual({
      symbol: '600519',
      from: '2026-03-02',
      to: '2026-04-10',
    })
    // Even the default range is absent beside a window, so the two cannot disagree in a link.
    expect(quoteSearchParams({ symbol: '600519', range: '3m', win })).not.toHaveProperty('range')
  })

  it('keeps an explicit range and omits the default one', () => {
    expect(quoteSearchParams({ symbol: '600519', range: '1y', win: null })).toEqual({
      symbol: '600519',
      range: '1y',
    })
    // 3m is the default: a link to it carries the code and nothing else.
    expect(quoteSearchParams({ symbol: '600519', range: '3m', win: null })).toEqual({ symbol: '600519' })
  })

  it('omits an empty symbol rather than writing symbol=', () => {
    expect(quoteSearchParams({ symbol: '', range: '3m', win: null })).toEqual({})
  })
})
