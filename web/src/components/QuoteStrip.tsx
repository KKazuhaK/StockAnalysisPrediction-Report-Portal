import { Button, Skeleton, Tag, Tooltip, Typography, theme } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../api/client'
import type { QuoteResp, QuoteSnapshot } from '../api/types'
// The chart drawn directly below this strip prints the same 分 amounts. Two local formatters would
// agree on 33.35 and disagree on every seven-figure 成交额, so both components call one.
import { fenToYuan, groupDigits, signedFenToYuan } from '../lib/money'

// The price strip above a stock report (ADR 0028).
//
// It renders and nothing else: the page owns the request, the polling and the retry, because the
// same answer also feeds the candlestick chart below and two components fetching the same quote
// would double the load on a vendor endpoint that is neither ours nor rate-limit-generous.
//
// Everything here is a DECORATION on a reading page. The report is what the reader came for, so no
// state of this component — not loading, not a dead vendor — is allowed to occupy the viewport, to
// push the report down by more than its own line height, or to render a blocking overlay.

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
 * asOf arrives as RFC3339 with a +08:00 offset because it is the exchange's wall clock, and the
 * exchange is the only clock a session time means anything on. Handing it to dayjs or toLocale*
 * would re-express it in the reader's timezone, so a European reader would be told the market
 * closed at 09:00 — a true instant, and a false statement about the trading day.
 */
function vendorClock(asOf: string): string {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}(?::\d{2})?)/.exec(asOf)
  return m ? `${m[1]} ${m[2]}` : asOf
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

  const stats = [
    { key: 'open', label: t('quote.open'), value: fenToYuan(s.open) },
    { key: 'prevClose', label: t('quote.prevClose'), value: fenToYuan(s.prevClose) },
    { key: 'high', label: t('quote.high'), value: fenToYuan(s.high) },
    { key: 'low', label: t('quote.low'), value: fenToYuan(s.low) },
    // Volume is a share count and turnover is already whole 元 — neither is 分, so neither goes
    // through fenToYuan. The unit words come from the bundle because this component is rendered
    // under three languages and a hardcoded 股 is wrong in two of them. There is deliberately no
    // 万/亿 folding: the bundles carry no scale words, and English has no unit at 10^4 to fold to.
    { key: 'volume', label: t('quote.volume'), value: `${groupDigits(s.volume)} ${t('quote.shares')}` },
    { key: 'amount', label: t('quote.amount'), value: `${groupDigits(s.amount)} ${t('quote.yuan')}` },
  ]

  return (
    <section aria-label={t('quote.title')} data-testid="quote-strip" style={shell}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 4 }}>
        <Typography.Text strong>{data.name}</Typography.Text>
        <Typography.Text type="secondary">{data.symbol}</Typography.Text>
        {/* The session and cache tags stay colourless on purpose. Two lines below, green is a
            FALLING price; a green "交易中" pill beside a red number would be read as part of the
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
        </Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('quote.source')} {t(`quote.source.${data.source}`)}
        </Typography.Text>
      </div>
    </section>
  )
}
