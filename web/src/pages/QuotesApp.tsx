import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button, Card, Empty, Input, Result, Segmented, Skeleton, Space, Tag, Typography } from 'antd'
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, qs, errText, ApiError } from '../api/client'
import type { QuoteResp } from '../api/types'
import QuoteStrip from '../components/QuoteStrip'
import PriceChart, { type ChartInterval } from '../components/PriceChart'
import { NO_ITEM_TOOLTIP } from '../lib/segmented'

// The Quotes app (/apps/quotes) — the one place in this portal where a chart filling the screen is
// the CORRECT behaviour.
//
// It exists because the reading page's chart was doing this uninvited. PriceChart used to be a
// 720x300 viewBox scaled by width:100%/height:auto, so its height was a function of its width
// (aspect 0.417): a 1900px column rendered it 792px tall and pushed the report — the thing the
// reader came for — off the screen. The fix was to give the chart a height in CSS pixels, and the
// reframe was to move deep data viewing out of the reading page entirely. The strip beside a report
// stays compact; everything a reader wants to do WITH a price lives here, where nothing is being
// buried because the price is the whole page.
//
// WHAT THIS PAGE OWNS, AND WHAT IT DOES NOT. It owns the request — which symbol, which window, and
// which of two answers is allowed to land — and the frame around the picture. It renders no price
// itself: every number on screen comes from QuoteStrip and PriceChart, the same two components the
// reading page uses, because a second implementation of "what a snapshot looks like" is how the two
// surfaces drift apart while both keep passing their own tests (ADR 0030, Consequences).
//
// Scope: quote VIEWING is multi-market, reports are not. See the note under the chart.

// The windows /api/quote accepts (internal/app/quote_api.go), spelled out rather than derived from
// a response — same reason StockPage spells them out: the switcher has to render before the first
// fetch lands, and a control that appears only after data arrives is a control that looks broken
// for exactly as long as the network is slow.
//
// Shortest first, which puts the two INTRADAY windows on the left. They are not merely shorter
// daily windows: 分时 is about 400 one-minute points and 5日 about 330 five-minute ones (both
// measured against the vendor), so the chart draws them as a line rather than as candles two pixels
// wide. The four on the right are the daily candles this app shipped with and are unchanged.
const QUOTE_RANGES = ['1d', '5d', '1m', '3m', '6m', '1y'] as const
type QuoteRange = (typeof QUOTE_RANGES)[number]
const DEFAULT_RANGE: QuoteRange = '3m'

// Which of those windows are minute-resolution.
//
// The chart is TOLD what kind of series it was handed and never sniffs it: an intraday bar's `d` is
// a full timestamp and a daily bar's is a date, so a regex on bars[0] looks like a free answer and
// is silently wrong in both directions — PriceChart's own doc says so, and this is the caller it is
// talking about. The range is where the answer actually lives, because the range is what was asked
// for.
const INTRADAY_RANGES = new Set<string>(['1d', '5d'])

function intervalFor(r: QuoteRange): ChartInterval {
  return INTRADAY_RANGES.has(r) ? 'intraday' : 'daily'
}

// The markets the endpoint answers for (frozen quote v2 contract). The badge is rendered through
// t() as `quote.market.<m>`, and i18next echoes a key it has no string for, so a market this build
// has never heard of gets NO badge rather than the literal text `quote.market.xx` on the page. Same
// rule PriceChart applies to the currency code, for the same reason.
const MARKET_KEYS = new Set(['sh', 'sz', 'bj', 'hk', 'us'])

// The sources this build can name, under the same rule and for the same reason — `quote.source.<s>`
// also goes through t(). The set is the bundles' own list rather than the response type's, because
// which sources are compiled in is an operator's choice that moves faster than this file does: a
// source added to the server before its label exists here prints nothing, not `quote.source.acme`.
const SOURCE_KEYS = new Set(['tencent', 'sina', 'yahoo'])

// Where a report can exist at all. The `stocks` table, the ingest path and the search index are
// A-share only and this change did not widen them — what widened is quote viewing. An INDEX is
// excluded even on an A-share exchange: sh000001 is a market-wide number, not something anybody
// writes a company report about.
const A_SHARE_MARKETS = new Set(['sh', 'sz', 'bj'])

// Chart height, in CSS pixels, derived from the VIEWPORT HEIGHT — never from the container width,
// which is the arithmetic that buried the report on the reading page. `min(60vh, 520)` with a floor
// keeps a laptop's chart generous without letting a tall monitor turn it into a page nobody can see
// the bottom of, and the floor keeps a short window (a phone in landscape, a half-height browser)
// from collapsing it into a strip too thin for two panels and a date axis.
const CHART_VH = 0.6
const CHART_MAX_H = 520
const CHART_MIN_H = 320

function chartHeightPx(viewportH: number): number {
  // jsdom, a detached document and an obscure browser can all hand back 0 or a non-finite number,
  // and NaN would travel all the way into the SVG's viewBox. The floor is what catches it.
  const h = Number.isFinite(viewportH) ? viewportH : 0
  return Math.max(CHART_MIN_H, Math.min(Math.round(h * CHART_VH), CHART_MAX_H))
}

function viewportHeight(): number {
  return typeof window === 'undefined' ? 0 : window.innerHeight
}

// quote_bad_symbol is a REFUSAL — the code is not one of the shapes the endpoint's grammar accepts,
// so the identical request is refused identically for as long as the page is open. Offering a retry
// there sells a permanent refusal as a bad minute. Every other failure (quote_unavailable, and any
// transport error that named no code at all) is worth one more try.
const BAD_SYMBOL = 'quote_bad_symbol'

/**
 * The answer on screen and THE WINDOW THAT PRODUCED IT, held as one value because they are one
 * fact.
 *
 * A range switch deliberately keeps the previous window's bars up for the whole round trip (see the
 * effect below), so between the click on 分时 and the vendor's answer the chart is holding DAILY
 * bars while `range` already reads `1d`. Taking the chart's interval from `range` there would draw
 * three months of candles as a 分时 line under a clock axis — precisely the mislabelling PriceChart
 * refuses to guess its way into, reintroduced from the caller's side, and visible for exactly as
 * long as the vendor is slow. The interval travels with the bars, so it can only ever describe the
 * bars it arrived with.
 */
interface Answer {
  resp: QuoteResp
  range: QuoteRange
}

export default function QuotesApp() {
  const { t } = useTranslation()
  const [sp, setSp] = useSearchParams()

  // The symbol and the range live in the URL, not in state: a chart that cannot be linked is a
  // chart that cannot be sent to the person you are arguing with about it, and one that forgets
  // itself on reload looks like it did not work.
  const symbol = (sp.get('symbol') || '').trim()
  const rangeParam = sp.get('range') || ''
  const range: QuoteRange = (QUOTE_RANGES as readonly string[]).includes(rangeParam)
    ? (rangeParam as QuoteRange)
    : DEFAULT_RANGE

  const [draft, setDraft] = useState(symbol)
  const [answer, setAnswer] = useState<Answer | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  // Bumped by the retry button: the request is a pure function of (symbol, range), so re-running it
  // needs a dependency that means nothing except "again".
  const [nonce, setNonce] = useState(0)
  const [chartH, setChartH] = useState(() => chartHeightPx(viewportHeight()))
  // Which symbol the answer currently on screen is about. Not derived from answer.resp.symbol: the
  // server echoes its own CANONICAL code, so "sh600519" in the box comes back as "600519", and
  // comparing against the echo would read every range switch on a prefixed code as a symbol switch
  // and blank the card for the round trip. What the effect needs to know is whether the URL's
  // symbol changed, so the URL's spelling is what is remembered.
  const shownSymbol = useRef('')

  // Browser back/forward, and a link opened into an already-mounted page, both change the URL
  // without touching the box. Without this the input keeps showing whatever was last typed while
  // the chart below it shows something else.
  useEffect(() => {
    setDraft(symbol)
  }, [symbol])

  useEffect(() => {
    const onResize = () => setChartH(chartHeightPx(viewportHeight()))
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  // The cancelled flag is the whole race guard, and it is not decoration. Typing 600519, then AAPL
  // a second later, leaves two requests in flight against two different vendor paths that answer at
  // whatever speeds they answer at; without this the slower FIRST answer lands last and the page
  // ends up showing a Shanghai price under a US ticker in the search box — a real number, for the
  // wrong instrument, with nothing on screen admitting it. Switching the range does the same thing
  // more quietly, since both answers are for the same symbol and only the bars differ.
  useEffect(() => {
    if (!symbol) {
      // Clearing the box has to clear the answer with it. A chart left standing under the "type a
      // code" empty state is a price the page is no longer claiming to be showing.
      setAnswer(null)
      setError(null)
      setLoading(false)
      shownSymbol.current = ''
      return
    }
    let cancelled = false
    // A SYMBOL switch drops the previous instrument at the START of the request; a RANGE switch
    // keeps it. The two cases are opposite and this effect is the only place that can tell them
    // apart, since `symbol` and `range` change it identically.
    //
    // Keeping the old answer through a range switch is right: it is the same instrument, only a
    // different window of it, and blanking the card would make every range click flash. Keeping it
    // through a SYMBOL switch is the same bug the .catch below already refuses — the card's header
    // and market badge are the page's own, not the strip's, so typing AAPL over a loaded 600519
    // leaves 贵州茅台 and an `sh` badge standing over an in-flight US request for as long as the
    // vendor takes. Nothing on screen admits it, and a slow vendor makes it seconds.
    if (shownSymbol.current !== symbol) setAnswer(null)
    setLoading(true)
    setError(null)
    api
      .get<QuoteResp>(`/api/quote/${encodeURIComponent(symbol)}${qs({ range })}`)
      .then((d) => {
        if (cancelled) return
        shownSymbol.current = symbol
        // `range` here is the one this request was BUILT from, out of the effect's own closure, not
        // whatever the URL says by the time the answer lands. They are the same value for the
        // answer that is allowed through — a range switch re-runs the effect and the cancelled flag
        // drops the older answer — and pairing them at the point of arrival is what keeps that true
        // without anyone having to re-check it.
        setAnswer({ resp: d, range })
        setError(null)
      })
      .catch((e) => {
        if (cancelled) return
        // The previous symbol's numbers are dropped rather than left under an error banner: the
        // strip prints a name and a code, so keeping them would attribute one instrument's price
        // to another one's request.
        shownSymbol.current = ''
        setAnswer(null)
        setError(e)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [symbol, range, nonce])

  // Both controls write the whole query string, because setSearchParams REPLACES it: building it in
  // one place is what stops "switch the range, lose the symbol" from being one forgotten line away.
  const go = (next: { symbol: string; range: QuoteRange }) => {
    const params: Record<string, string> = {}
    if (next.symbol) params.symbol = next.symbol
    // The default range stays OUT of the URL, so a link somebody pastes into a chat carries the code
    // and nothing else; an explicit choice is the one worth writing down.
    if (next.range !== DEFAULT_RANGE) params.range = next.range
    setSp(params)
  }

  // Enter is the only gesture that changes the page. Searching as you type would put a request
  // behind every keystroke of "600519" — against a vendor endpoint that is neither ours nor
  // rate-limit-generous — and five of those six are for codes the reader never meant to ask about.
  const submit = (raw: string) => go({ symbol: raw.trim(), range })

  // No client-side symbol regex on purpose. The grammar (6 digits / 5 digits / 1-6 letters / an
  // explicit `<mkt>:<code>`) is enforced server-side because that is where the URL to the vendor is
  // built, and a second copy here would be a second place for it to drift — a browser that refuses
  // what the server accepts is a bug nobody can see from the server logs. The cost of being wrong
  // is one round trip that comes back as quote_bad_symbol, which this page already renders.
  const searchBox = (
    <Input
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onPressEnter={(e) => submit((e.target as HTMLInputElement).value)}
      prefix={<SearchOutlined />}
      placeholder={t('quote.searchPlaceholder')}
      // Codes are not prose: a phone that capitalizes them and a browser that underlines AAPL in
      // red are both wrong about what this field holds.
      autoCapitalize="off"
      autoCorrect="off"
      spellCheck={false}
      style={{ maxWidth: 420 }}
    />
  )

  const rangeSwitcher = (
    <Segmented
      size="small"
      value={range}
      onChange={(v) => go({ symbol, range: v as QuoteRange })}
      options={QUOTE_RANGES.map((r) => ({ label: t(`quote.range.${r}`), value: r, title: NO_ITEM_TOOLTIP }))}
    />
  )

  let body: ReactNode
  if (!symbol) {
    body = <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('quote.noSymbol')} />
  } else if (error) {
    const code = error instanceof ApiError ? error.code : undefined
    body = (
      <Result
        // A refusal of what was typed is a warning; a source that could not answer is a failure of
        // ours, not of the reader's, and gets the louder mark because it comes with an action.
        status={code === BAD_SYMBOL ? 'warning' : 'error'}
        title={errText(error, t, 'quote.unavailable')}
        // The code that was refused, verbatim. "Bad symbol" without it leaves the reader guessing
        // which of the things they typed the server could not read.
        subTitle={symbol}
        extra={
          code === BAD_SYMBOL ? undefined : (
            <Button icon={<ReloadOutlined />} onClick={() => setNonce((n) => n + 1)}>
              {t('quote.retry')}
            </Button>
          )
        }
      />
    )
  } else {
    const data = answer?.resp ?? null
    const marketKey = data && MARKET_KEYS.has(data.market) ? `quote.market.${data.market}` : ''
    const isIndex = data?.kind === 'index'
    // Whether the rest of the portal has anything to say about this instrument.
    const hasReports = !!data && A_SHARE_MARKETS.has(data.market) && !isIndex
    // WHICH SOURCE DREW THE CHART, and only when it is not the one that priced the snapshot.
    //
    // The strip prints one source line and it is the SNAPSHOT's. Sources now declare what they can
    // serve per interval rather than per market, so one answer can legitimately carry a snapshot
    // from one vendor and a series from another — the whole point of the arrangement is that a
    // market whose dailies one source cannot serve gets them from one that can. Letting the strip's
    // single line stand for both would then attribute a chart to a vendor that never drew it, which
    // is a false statement about provenance on a page whose entire job is provenance. Said only on
    // the difference, because repeating "Tencent" twice on the ordinary answer is noise.
    const barsSource =
      data && data.bars.length > 0 && data.barsSource !== data.source && SOURCE_KEYS.has(data.barsSource)
        ? data.barsSource
        : ''
    // A five-digit HK code and a six-digit A-share code look alike at a glance, and 00700 vs 000700
    // is one keystroke. The badges are how a reader confirms they are looking at what they meant to
    // look at before drawing a conclusion from the line.
    const header = (
      <Space size={8} wrap>
        <Typography.Text strong>{data?.name || symbol}</Typography.Text>
        {marketKey && <Tag data-testid="quote-market">{t(marketKey)}</Tag>}
        {isIndex && <Tag data-testid="quote-index">{t('quote.kind.index')}</Tag>}
      </Space>
    )
    body = (
      <Card size="small" title={header} styles={{ body: { paddingTop: 12 } }}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          {answer ? (
            // Everything a person came here for that is not the line itself — open, previous close,
            // high, low, volume, turnover, the market and currency badges, the vendor's own clock
            // with the zone it belongs to, and which source answered. All of it is the strip's,
            // unchanged from the reading page: this app renders a bigger chart, not a different
            // set of facts.
            <QuoteStrip data={answer.resp} loading={loading} />
          ) : (
            // The skeleton stands in for the strip's own two rows rather than for a spinner, so the
            // page does not jump at the moment the answer lands.
            <Skeleton active title={false} paragraph={{ rows: 2 }} />
          )}

          {/* THE RANGE STRIP BELONGS TO THE CHART, not to the card's header, which is where it used
              to sit. With 分时 and 5日 added there are six windows, and six of them squeezed into a
              header beside an instrument's name and two badges wraps into a second line on a laptop
              and collides on a phone. Directly above the picture they change is also where every
              market app puts them, and it is what makes the row below read as a chart rather than
              as the output of the form at the top of the page.

              Rendered whether or not an answer has landed: a switcher that appears only with the
              data is a control that looks broken for exactly as long as the vendor is slow, which
              is the same reason QUOTE_RANGES is a literal instead of something read off a
              response. */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 8,
              flexWrap: 'wrap',
            }}
          >
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('quote.trend')}
            </Typography.Text>
            {rangeSwitcher}
          </div>

          {answer ? (
            <PriceChart
              bars={answer.resp.bars}
              loading={loading}
              unavailable={answer.resp.barsUnavailable}
              height={chartH}
              currency={answer.resp.currency}
              // From the answer's own range, never from the URL's: see Answer above.
              interval={intervalFor(answer.range)}
            />
          ) : (
            // The skeleton reserves the chart's real height rather than a token two lines, so the
            // page does not jump by half a screen at the moment the answer lands.
            <Skeleton.Node active style={{ width: '100%', height: chartH }} />
          )}

          {barsSource && (
            <Typography.Text type="secondary" data-testid="quote-bars-source" style={{ fontSize: 12 }}>
              {t('quote.source')} {t(`quote.source.${barsSource}`)}
            </Typography.Text>
          )}

          {answer && !hasReports && (
            // Said plainly and said quietly. This portal analyses A-shares; the quote endpoint is
            // wider than that on purpose, and a US ticker or an index rendering here with no word
            // about it implies a library of reports behind it that does not exist. The key is the
            // existing err.no_reports_for_symbol — its three translations already say exactly
            // this, and inventing a fourth string for the same sentence is how one language ends
            // up rendering a raw dotted path.
            <Typography.Text type="secondary" data-testid="quote-no-reports" style={{ fontSize: 12 }}>
              {t('err.no_reports_for_symbol')}
            </Typography.Text>
          )}
        </Space>
      </Card>
    )
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        {t('nav.quotes')}
      </Typography.Title>
      {searchBox}
      {body}
    </Space>
  )
}
