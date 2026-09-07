import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '../api/client'
import type { QuoteResp, QuoteSnapshot } from '../api/types'
import QuoteStrip from './QuoteStrip'

// t() returns the key, so every assertion names the key the contract froze rather than whichever
// of the three bundles happened to be loaded when the test ran.
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

// 紫金矿业 (sh601899) as the vendor actually served it on 2026-09-04, from
// internal/app/testdata/quote/tencent_fqkline_sh601899.json, converted the way the Go parser
// converts it: 元 to integer 分, 手 to 股 (x100), 万元 to 元 (x10000). Round invented numbers
// would have hidden the two things these tests exist for — a two-digit price whose 分 matter, and
// a nine-digit share count.
const SH: QuoteResp = {
  symbol: '601899',
  name: '紫金矿业',
  market: 'sh',
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
    amount: 5939060000,
    asOf: '2026-09-04T16:14:58+08:00',
    session: 'close',
  },
  bars: [],
  barsSource: 'tencent',
  barsUnavailable: '',
  adjusted: false,
  cached: false,
}

// 艾融软件 (bj830799) from tencent_fqkline_bj830799.json: a suspended stock, which every vendor
// reports as high / low / volume all zero with changePct "0.00".
const BJ: QuoteResp = {
  ...SH,
  symbol: '830799',
  name: '艾融软件',
  market: 'bj',
  snapshot: {
    last: 3428,
    prevClose: 3428,
    open: 3428,
    high: 0,
    low: 0,
    change: 0,
    changePct: '0.00',
    volume: 0,
    amount: 0,
    asOf: '2026-09-04T09:00:00+08:00',
    session: 'close',
  },
  barsUnavailable: 'market_unsupported',
}

function withSnapshot(base: QuoteResp, over: Partial<QuoteSnapshot>): QuoteResp {
  return { ...base, snapshot: { ...base.snapshot, ...over } }
}

// '#rrggbb' or 'rgb(...)' -> [r, g, b]. Reading the channels rather than comparing against a
// literal keeps the assertion about "red-ish" instead of about which shade of red antd ships in
// this major version or this theme.
function channels(colour: string): [number, number, number] {
  const hex = /^#([0-9a-f]{6})$/i.exec(colour.trim())
  if (hex) {
    const v = parseInt(hex[1], 16)
    return [(v >> 16) & 255, (v >> 8) & 255, v & 255]
  }
  const rgb = /rgba?\(([^)]+)\)/.exec(colour)
  if (!rgb) throw new Error(`unparseable colour: ${colour}`)
  const parts = rgb[1].split(',').map((p) => Number(p.trim()))
  return [parts[0], parts[1], parts[2]]
}

describe('QuoteStrip', () => {
  it('renders a positive change in the up token and a negative one in the down token', () => {
    const { unmount } = render(<QuoteStrip data={SH} loading={false} />)
    const [upR, upG] = channels(screen.getByTestId('quote-change').style.color)
    // 红涨绿跌: up is RED, so the red channel dominates. The reverse mapping is not a matter of
    // taste here — it would make the strip say the opposite of what happened.
    expect(upR).toBeGreaterThan(upG)
    unmount()

    render(<QuoteStrip data={withSnapshot(SH, { change: -371, changePct: '-10.00' })} loading={false} />)
    const [downR, downG] = channels(screen.getByTestId('quote-change').style.color)
    expect(downG).toBeGreaterThan(downR)
  })

  it('takes the direction from the integer 分 even when the vendor percentage contradicts it', () => {
    // change and changePct are deliberately in CONFLICT here. A fixture where they agree cannot
    // tell the two derivations apart, and the string is the one that can go wrong: the vendor may
    // send a percentage with no sign at all, or a full-width ０.12, or some later parser may
    // normalise it. Under 红涨绿跌 a direction read off that string paints a falling stock red and
    // tells the reader the opposite of what the market did.
    const { unmount } = render(
      <QuoteStrip data={withSnapshot(SH, { change: -371, changePct: '10.00' })} loading={false} />,
    )
    const fall = screen.getByTestId('quote-change')
    expect(fall.textContent).toBe('-3.71')
    const [downR, downG] = channels(fall.style.color)
    expect(downG).toBeGreaterThan(downR)
    // The vendor's own string still goes out verbatim, conflict and all: it is not this
    // component's job to correct the vendor, only to refuse to take its direction from it.
    expect(screen.getByTestId('quote-change-pct').textContent).toBe('10.00%')
    unmount()

    render(<QuoteStrip data={withSnapshot(SH, { change: 371, changePct: '-10.00' })} loading={false} />)
    const rise = screen.getByTestId('quote-change')
    expect(rise.textContent).toBe('+3.71')
    const [upR, upG] = channels(rise.style.color)
    expect(upR).toBeGreaterThan(upG)
  })

  it('renders changePct exactly as the vendor sent it', () => {
    const { unmount } = render(<QuoteStrip data={SH} loading={false} />)
    expect(screen.getByTestId('quote-change-pct').textContent).toBe('0.12%')
    unmount()

    const limitDown = withSnapshot(SH, { change: -371, changePct: '-10.00' })
    const down = render(<QuoteStrip data={limitDown} loading={false} />)
    expect(screen.getByTestId('quote-change-pct').textContent).toBe('-10.00%')
    down.unmount()

    // An ex-rights day: the previous close is the pre-dividend price and the last price is not, so
    // recomputing the move from the two prints a 50% crash that never happened. The vendor knows
    // the adjustment factor and already published the right number.
    render(<QuoteStrip data={withSnapshot(SH, { last: 1000, prevClose: 2000, change: 5, changePct: '0.53' })} loading={false} />)
    expect(screen.getByTestId('quote-change-pct').textContent).toBe('0.53%')
    expect(screen.queryByText(/-50/)).toBeNull()
  })

  it('formats 分 amounts to two decimals and keeps the separators on a large one', () => {
    const { unmount } = render(<QuoteStrip data={SH} loading={false} />)
    expect(screen.getByTestId('quote-last').textContent).toBe('33.35')
    expect(screen.getByTestId('quote-stat-open').textContent).toContain('33.85')
    unmount()

    const penny = render(<QuoteStrip data={withSnapshot(SH, { last: 7 })} loading={false} />)
    expect(screen.getByTestId('quote-last').textContent).toBe('0.07')
    penny.unmount()

    render(<QuoteStrip data={withSnapshot(SH, { last: 123456789 })} loading={false} />)
    expect(screen.getByTestId('quote-last').textContent).toBe('1,234,567.89')
  })

  it('groups the share count and the turnover and labels them from the bundle', () => {
    render(<QuoteStrip data={SH} loading={false} />)
    expect(screen.getByTestId('quote-stat-volume').textContent).toBe('quote.volume 176,934,100 quote.shares')
    expect(screen.getByTestId('quote-stat-amount').textContent).toBe('quote.amount 5,939,060,000 quote.yuan')
  })

  it('shows a suspended stock as suspended instead of as a flat quote', () => {
    render(<QuoteStrip data={BJ} loading={false} />)
    expect(screen.getByTestId('quote-suspended').textContent).toBe('quote.suspended')
    // The zeros a suspended stock reports must not be painted as a session that traded unchanged.
    expect(screen.queryByTestId('quote-change')).toBeNull()
    expect(screen.queryByTestId('quote-change-pct')).toBeNull()
    expect(screen.queryByText(/0\.00%/)).toBeNull()
  })

  it('shows a partially zeroed body as suspended, the same way the drift gate reads it', () => {
    // high and low zero with a NON-zero volume. The Go gate skips its low <= last <= high check on
    // high == 0 || low == 0, so this body is not rejected as a source failure and reaches the UI.
    // A predicate that also demanded a zero volume would fall through to the ordinary render here
    // and paint 最高 0.00 / 最低 0.00 / +0.00 / 0.00% — a stock that traded all day and closed
    // unchanged, which is not what any vendor said.
    const { unmount } = render(<QuoteStrip data={withSnapshot(BJ, { volume: 176934100 })} loading={false} />)
    expect(screen.getByTestId('quote-suspended').textContent).toBe('quote.suspended')
    expect(screen.queryByTestId('quote-change')).toBeNull()
    expect(screen.queryByTestId('quote-change-pct')).toBeNull()
    expect(screen.queryByText(/0\.00%/)).toBeNull()
    unmount()

    // Only one of the two zero is the same skip in the gate, and the same missing range here.
    render(<QuoteStrip data={withSnapshot(BJ, { high: 3428, volume: 176934100 })} loading={false} />)
    expect(screen.getByTestId('quote-suspended').textContent).toBe('quote.suspended')
    expect(screen.queryByTestId('quote-change')).toBeNull()
  })

  it('shows the vendor clock, the session and the source', () => {
    render(<QuoteStrip data={SH} loading={false} />)
    // The exchange's own wall clock, never re-expressed in the reader's timezone.
    expect(screen.getByText(/2026-09-04 16:14:58/)).toBeTruthy()
    expect(screen.getByTestId('quote-session').textContent).toBe('quote.session.close')
    expect(screen.getByText(/quote\.source\.tencent/)).toBeTruthy()
  })

  it('marks the numbers as unadjusted', () => {
    render(<QuoteStrip data={SH} loading={false} />)
    expect(screen.getByTestId('quote-unadjusted').textContent).toBe('quote.unadjusted')
  })

  it('falls back to the generic line for a throw that named no reason', async () => {
    const onRetry = vi.fn()
    // A transport failure or an abort carries no server code, and "temporarily unavailable" is the
    // honest description of it — unlike a 400, which knows exactly what is wrong.
    render(<QuoteStrip data={null} loading={false} error={new Error('boom')} onRetry={onRetry} />)
    expect(screen.getByText('quote.unavailable')).toBeTruthy()
    // A real button with an accessible name, not a clickable div.
    const retry = screen.getByRole('button', { name: /quote\.retry/ })
    await userEvent.click(retry)
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('calls an invalid symbol invalid, and offers no retry for a refusal that cannot change', () => {
    const onRetry = vi.fn()
    render(
      <QuoteStrip
        data={null}
        loading={false}
        error={new ApiError(400, 'invalid symbol', 'quote_bad_symbol')}
        onRetry={onRetry}
      />,
    )
    expect(screen.getByText('err.quote_bad_symbol')).toBeTruthy()
    // Not the transient line: that one tells the reader to come back later for a quote that is
    // never coming, and the button under it re-issues a request the server has already refused and
    // will refuse identically every time it is pressed.
    expect(screen.queryByText('quote.unavailable')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
    expect(onRetry).not.toHaveBeenCalled()
  })

  it('calls a dead source dead, and does offer a retry for it', async () => {
    const onRetry = vi.fn()
    render(
      <QuoteStrip
        data={null}
        loading={false}
        error={new ApiError(503, 'all sources failed', 'quote_unavailable')}
        onRetry={onRetry}
      />,
    )
    expect(screen.getByText('err.quote_unavailable')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: /quote\.retry/ }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('renders a compact skeleton while loading', () => {
    const { container } = render(<QuoteStrip data={null} loading={true} />)
    expect(screen.getByTestId('quote-strip-loading')).toBeTruthy()
    expect(container.querySelector('.ant-skeleton')).not.toBeNull()
    // A decoration on a reading page never gets a full-height spinner: the report below stays put.
    expect(container.querySelector('.ant-spin')).toBeNull()
  })

  it('renders nothing when there is no quote and nothing failed', () => {
    const { container } = render(<QuoteStrip data={null} loading={false} />)
    expect(container.firstChild).toBeNull()
  })

  it('keeps the last good quote on screen while a refresh is in flight', () => {
    render(<QuoteStrip data={SH} loading={true} />)
    expect(screen.getByTestId('quote-last').textContent).toBe('33.35')
    expect(screen.queryByTestId('quote-strip-loading')).toBeNull()
  })

  // The strip decorates the reading page; it must never be the reason the page is blank. A body the
  // component does not recognise used to throw during render, which React answers by unmounting the
  // whole subtree — so a malformed quote took the report with it. Found by wiring the page up: the
  // page's own test mock answered every URL with the report body.
  it('degrades instead of throwing when the body carries no snapshot', () => {
    const bad = { ...withSnapshot(SH, {}), snapshot: undefined } as unknown as QuoteResp
    expect(() => render(<QuoteStrip data={bad} loading={false} />)).not.toThrow()
  })
})
