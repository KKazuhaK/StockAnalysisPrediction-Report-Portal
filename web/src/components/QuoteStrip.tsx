import { Button, Skeleton, Tag, Tooltip, Typography, theme } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../api/client'
import type { QuoteResp, QuoteSnapshot } from '../api/types'
// The chart that renders with this strip prints the same 分 amounts. Two local formatters would
// agree on 33.35 and disagree on every seven-figure 成交额, so both components call one.
import { fenToYuan, groupDigits, signedFenToYuan } from '../lib/money'

// The price strip above a stock report (ADR 0028), and the same strip at the top of the Quotes app.
//
// It renders and nothing else: the page owns the request, the polling and the retry, because the
// same answer also feeds the candlestick chart and two components fetching the same quote would
// double the load on a vendor endpoint that is neither ours nor rate-limit-generous.
//
// Everything here is a DECORATION on a reading page. The report is what the reader came for, so no
// state of this component — not loading, not a dead vendor — is allowed to occupy the viewport, to
// push the report down by more than its own line height, or to render a blocking overlay. On that
// page the chart is now collapsed by default and this strip is the whole of the quote panel, which
// is the other reason it stays short.

interface Props {
  data: QuoteResp | null
  loading: boolean
  /**
   * Whatever the page's fetch threw. Its ApiError code is read, not merely its presence: the two
   * codes this endpoint answers with mean opposite things to a reader, and one of them must not be
   * offered a retry.
   */
  error?: unknown
  onRetry?: () => void
}

// The two codes /api/quote/{symbol} answers with (ADR 0028). quote_bad_symbol is a REFUSAL: the
// code is not six digits or carries an unknown market prefix, so the identical request is refused
// identically for as long as the page is open. quote_unavailable is an OUTAGE, and it is the only
// one of the two a retry can do anything about.
const BAD_SYMBOL = 'quote_bad_symbol'
const UNAVAILABLE = 'quote_unavailable'

// The markets and the currencies the frozen quote contract defines, as an allowlist rather than a
// straight interpolation into t(). Both are rendered as `quote.market.<m>` / `quote.currency.<c>`,
// and i18next ECHOES a key it has no string for — so a market or a currency this build has never
// heard of would print the literal text `quote.market.xx` beside somebody's research. No badge is
// the honest answer there; the same rule PriceChart applies to its axis unit.
const MARKET_KEYS = new Set(['sh', 'sz', 'bj', 'hk', 'us'])
const CURRENCY_KEYS = new Set(['CNY', 'HKD', 'USD'])

// The zones the frozen contract can send (the quoteMarkets table in internal/app/quote.go), each
// mapped to a label a person reads. "America/New_York" is a database key, not a label: printed
// beside a timestamp it is noise, and noise beside a time is skipped.
//
// The labels are deliberately NOT all the same shape, because a label that is wrong half the year
// is worse than none. China and Hong Kong have kept a fixed +08:00 since 1991 and 1979, so an
// offset is true for them in every month. New York moves between UTC-5 and UTC-4 twice a year, so
// a baked-in offset would be a false statement for one of the two halves; ET is the label the US
// exchanges themselves print on a close, and it is true all year.
const ZONE_LABELS: Record<string, string> = {
  'Asia/Shanghai': 'UTC+8',
  'Asia/Hong_Kong': 'UTC+8',
  'America/New_York': 'ET',
}

/**
 * The contract's error code carried by a thrown ApiError, or '' for anything else.
 *
 * Only the two codes above are recognised, because the code is rendered through t() as `err.<code>`
 * and i18next echoes a key it has no string for. Passing an arbitrary server code straight through
 * would print the literal `err.something` on the page, and locales.test.ts compares the bundles only
 * against each other — nothing checks that a key a component asks for exists at all.
 */
function quoteErrorCode(e: unknown): string {
  const code = e instanceof ApiError ? e.code : undefined
  return code === BAD_SYMBOL || code === UNAVAILABLE ? code : ''
}

/**
 * True when the session has no price range to report — a suspended (停牌) stock, or one that never
 * opened. The captured bj830799 fixture is this with volume zero as well.
 *
 * The predicate is deliberately the SAME condition the Go drift gate uses to skip its
 * low <= last <= high check: high == 0 || low == 0. Also demanding a zero volume would open a gap
 * between the two — a body with high 0, low 0 and a non-zero volume passes the gate and then fails
 * this test, and falls through to the ordinary render as 最高 0.00 / 最低 0.00 / +0.00 / 0.00%.
 * That is the picture of a stock that traded all day and closed unchanged: a far more reassuring
 * claim than the truth, and one no vendor made.
 */
export function isSuspended(s: QuoteSnapshot): boolean {
  return s.high === 0 || s.low === 0
}

/**
 * The vendor's timestamp shown on the vendor's own clock.
 *
 * asOf arrives as RFC3339 carrying the EXCHANGE's own offset — +08:00 for Shanghai, Shenzhen,
 * Beijing and Hong Kong, US/Eastern for a US listing (QuoteResp.tz names the zone) — because the
 * exchange is the only clock a session time means anything on. Handing it to dayjs or toLocale*
 * would re-express it in the reader's timezone, so a European reader would be told the Shanghai
 * market closed at 09:00 — a true instant, and a false statement about the trading day. Slicing
 * the literal digits keeps whatever wall clock the vendor stamped, whatever its offset — and
 * zoneLabel below is what says whose clock that is, since the digits alone cannot.
 */
function vendorClock(asOf: string): string {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}(?::\d{2})?)/.exec(asOf)
  return m ? `${m[1]} ${m[2]}` : asOf
}

/**
 * The label for the clock vendorClock's digits belong to, or the raw zone when this build has no
 * label for it.
 *
 * Keeping the exchange's wall clock is only half the job: 2026-09-04 16:00:01 and
 * 2026-09-04 16:14:58 are the same string to a reader, and one of them is a New York close from
 * last night while the other is this afternoon in Shanghai. Rendered in the same furniture with no
 * zone anywhere, a reader in China reads that New York close as twelve hours fresher than it is —
 * thirteen in January, when New York has moved off summer time and Shanghai, which keeps none, has
 * not. The zone is what makes the digits mean something, which is what QuoteResp.tz was added to
 * carry.
 *
 * An unmapped zone falls back to the RAW string, unlike the market and currency badges above, which
 * render nothing. The asymmetry is the cost of being wrong: an unrecognised market costs the reader
 * a badge, while a timestamp with no zone at all is the exact failure this function exists to
 * prevent — read as local, silently, and half a day out. "Australia/Sydney" is ugly; unlabelled is
 * dangerous.
 */
function zoneLabel(tz: string): string {
  if (!tz) return ''
  return ZONE_LABELS[tz] ?? tz
}

export default function QuoteStrip({ data, loading, error, onRetry }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()

  // Padding and a hairline, deliberately no fixed height and no minHeight: whatever this renders,
  // the report below it moves by the difference between two short rows and never by a screenful.
  const shell = {
    border: `1px solid ${token.colorBorderSecondary}`,
    borderRadius: token.borderRadiusLG,
    padding: '10px 14px',
    background: token.colorFillQuaternary,
  }

  // A quote already on screen outranks both the spinner and the failure. A poll that has not landed
  // yet, or one that just failed, leaves the previous numbers up next to their own 行情时间, so the
  // reader sees how stale the quote is instead of watching the strip blink empty every refresh.
  // `data && !data.snapshot` is not a shape the Go handler can send — Snapshot is a value struct, so
  // it is always encoded — but this strip's whole contract is that it decorates the reading page and
  // never blocks it, and reading a field off an absent object would throw during render and take the
  // report down with it. Treating an unrecognised body as "no quote" keeps that promise against a
  // proxy, a future field rename, or a stubbed fetch in a test.
  if (!data || !data.snapshot) {
    if (loading) {
      return (
        <div data-testid="quote-strip-loading" style={{ ...shell, display: 'flex', alignItems: 'center', gap: 16 }}>
          <Skeleton.Input active size="small" style={{ width: 120 }} />
          <Skeleton.Input active size="small" style={{ width: 200 }} />
        </div>
      )
    }
    if (error) {
      const code = quoteErrorCode(error)
      return (
        <div
          data-testid="quote-strip-error"
          role="status"
          style={{ ...shell, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}
        >
          {/* The server's own code when it sent one of the two the contract defines; the generic
              line only for a throw that named no reason — a transport failure or an abort — where
              "temporarily unavailable" is the honest description. */}
          <Typography.Text type="secondary">{code ? t(`err.${code}`) : t('quote.unavailable')}</Typography.Text>
          {/* A real button, not a clickable span with a reload glyph: the retry has to be reachable
              by keyboard and has to announce itself as something other than "button". And it is
              withheld from an invalid symbol, because that request is guaranteed to be refused
              again: a button that cannot succeed sells a permanent refusal as a bad minute. */}
          {onRetry && code !== BAD_SYMBOL && (
            <Button size="small" type="link" icon={<ReloadOutlined />} onClick={onRetry} style={{ padding: 0 }}>
              {t('quote.retry')}
            </Button>
          )}
        </div>
      )
    }
    // Nothing asked for, nothing failed — the page has not requested a quote for this report, and
    // an empty framed box would claim it tried.
    return null
  }

  const s = data.snapshot
  const suspended = isSuspended(s)

  // A-SHARE COLOUR CONVENTION: 红涨绿跌 — RED is UP and GREEN is DOWN, the reverse of US and
  // European markets. These are antd's semantic tokens rather than literals so both themes get a
  // shade already tuned for contrast against their own background; only the mapping onto direction
  // is inverted. The sign comes from snapshot.change, an integer, because changePct is a vendor
  // string that may carry a leading '-', a '+', or neither.
  const direction = suspended ? 0 : Math.sign(Math.trunc(s.change))
  const moveColour = direction > 0 ? token.colorError : direction < 0 ? token.colorSuccess : token.colorText

  const sessionLabel =
    s.session === 'open' ? t('quote.session.open') : s.session === 'close' ? t('quote.session.close') : ''

  // WHICH EXCHANGE AND WHICH MONEY. Neither is decoration now that one endpoint answers for
  // Shanghai, Shenzhen, Beijing, Hong Kong and US listings: 00700 and 000700 are one keystroke
  // apart and both parse, and a 231.40 USD price shown as a bare number beside a 33.35 CNY one
  // reads as the same kind of quantity — which is exactly how a reader mis-compares them. An index
  // is called out because it is not a company: no report in this portal is about one.
  const marketLabel = MARKET_KEYS.has(data.market) ? t(`quote.market.${data.market}`) : ''
  const currencyLabel = CURRENCY_KEYS.has(data.currency) ? t(`quote.currency.${data.currency}`) : ''
  // WHICH CLOCK. The other half of the same sentence: the strip may not print a bare number, and it
  // may not print a bare instant either.
  const zone = zoneLabel(data.tz)

  const stats = [
    { key: 'open', label: t('quote.open'), value: fenToYuan(s.open) },
    { key: 'prevClose', label: t('quote.prevClose'), value: fenToYuan(s.prevClose) },
    { key: 'high', label: t('quote.high'), value: fenToYuan(s.high) },
    { key: 'low', label: t('quote.low'), value: fenToYuan(s.low) },
    // Volume is a share count and turnover is already whole currency units — neither is 分, so
    // neither goes through fenToYuan. The unit words come from the bundle because this component is
    // rendered under three languages and a hardcoded 股 is wrong in two of them. There is
    // deliberately no 万/亿 folding: the bundles carry no scale words, and English has no unit at
    // 10^4 to fold to.
    { key: 'volume', label: t('quote.volume'), value: `${groupDigits(s.volume)} ${t('quote.shares')}` },
    // The turnover's unit follows the payload's currency: labelling a US turnover 元 is the same
    // false statement as printing the price bare. quote.yuan is the fallback for a body carrying no
    // currency at all, which is the pre-multi-market shape of this response, and that one was 元.
    {
      key: 'amount',
      label: t('quote.amount'),
      value: `${groupDigits(s.amount)} ${currencyLabel || t('quote.yuan')}`,
    },
  ]

  return (
    <section aria-label={t('quote.title')} data-testid="quote-strip" style={shell}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 4 }}>
        <Typography.Text strong>{data.name}</Typography.Text>
        <Typography.Text type="secondary">{data.symbol}</Typography.Text>
        {/* Strip-scoped test ids: a page embedding this strip may badge the market in its own
            header too, and two elements answering one data-testid make every getByTestId in a test
            that mounts both ambiguous. */}
        {marketLabel && <Tag data-testid="quote-strip-market">{marketLabel}</Tag>}
        {data.kind === 'index' && <Tag data-testid="quote-strip-index">{t('quote.kind.index')}</Tag>}
        {/* The market, session and cache tags stay colourless on purpose. Two lines below, green is
            a FALLING price; a green "交易中" pill beside a red number would be read as part of the
            same colour language and contradict it. */}
        {sessionLabel && <Tag data-testid="quote-session">{sessionLabel}</Tag>}
        {data.cached && <Tag data-testid="quote-cached">{t('quote.cached')}</Tag>}
        {!data.adjusted && (
          <Tooltip title={t('quote.unadjustedHint')}>
            {/* Focusable so the explanation is reachable without a pointer: antd opens the tooltip
                on focus as well as on hover. */}
            <Tag data-testid="quote-unadjusted" tabIndex={0} style={{ cursor: 'help' }}>
              {t('quote.unadjusted')}
            </Tag>
          </Tooltip>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap' }}>
        <span
          data-testid="quote-last"
          style={{ color: moveColour, fontSize: 26, fontWeight: 700, lineHeight: 1.2, fontVariantNumeric: 'tabular-nums' }}
        >
          {fenToYuan(s.last)}
        </span>
        {/* Beside the number, not up in the tag row: the unit belongs to the price the way the
            decimals do, and a reader comparing two quotes reads the number first. */}
        {currencyLabel && (
          <span data-testid="quote-strip-currency" style={{ color: token.colorTextSecondary, fontSize: 13 }}>
            {currencyLabel}
          </span>
        )}
        {suspended ? (
          <Tag data-testid="quote-suspended" color="default">
            {t('quote.suspended')}
          </Tag>
        ) : (
          <>
            <span data-testid="quote-change" style={{ color: moveColour, fontSize: 15, fontWeight: 600 }}>
              {signedFenToYuan(s.change)}
            </span>
            {/* changePct is printed EXACTLY as the vendor sent it, with a '%' and nothing else.
                It is never recomputed from last and prevClose: on an ex-rights day the previous
                close is the pre-dividend price and the last price is not, so the arithmetic gives
                a double-digit crash that never happened — and the vendor, which knows the
                adjustment factor, already published the right number. Reformatting it is the same
                bug wearing a smaller hat: parsing to a float and re-printing loses the vendor's
                own precision and can round 9.995 into a different move than the one on the ticker. */}
            <span data-testid="quote-change-pct" style={{ color: moveColour, fontSize: 15, fontWeight: 600 }}>
              {s.changePct}%
            </span>
          </>
        )}
      </div>

      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', marginTop: 6 }}>
        {stats.map((it) => (
          <Typography.Text key={it.key} type="secondary" data-testid={`quote-stat-${it.key}`} style={{ fontSize: 12 }}>
            {it.label} <span style={{ color: token.colorText }}>{it.value}</span>
          </Typography.Text>
        ))}
      </div>

      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', marginTop: 2 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('quote.asOf')} {vendorClock(s.asOf)}
          {/* Beside the digits, never instead of them, and never applied TO them: converting the
              instant into the reader's own zone is the thing vendorClock refuses to do, because a
              European reader would then be told the Shanghai market closed at 09:00. The label
              names whose clock the digits are on and changes nothing about them. */}
          {zone ? (
            <span data-testid="quote-strip-tz" style={{ marginLeft: 4 }}>
              {zone}
            </span>
          ) : null}
        </Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('quote.source')} {t(`quote.source.${data.source}`)}
        </Typography.Text>
      </div>
    </section>
  )
}
