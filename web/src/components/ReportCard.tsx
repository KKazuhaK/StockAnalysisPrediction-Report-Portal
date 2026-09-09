import { Button, Card, Space, Tag, Typography, theme } from 'antd'
import { CalendarOutlined, FileTextOutlined, RightOutlined } from '@ant-design/icons'
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { Group } from '../api/types'
import { formatReportDateTime, isInstant } from '../lib/datetime'
import { clickable } from '../lib/clickable'
import type { CardQuote } from '../lib/useHomeQuotes'
// The same formatter the price strip and the candlestick chart use. A local `(fen / 100).toFixed(2)`
// here would be a second money format in the portal, agreeing with the strip on 33.35 and disagreeing
// with it on every seven-figure amount.
import { fenToYuan, signedFenToYuan } from '../lib/money'
import { FavoriteButton } from './FavoriteButton'

// How tall the price line is, in CSS pixels, whether or not there is a price in it.
//
// The point of the constant is the EMPTY case. A card that grows when its quote lands moves every
// card below it, and antd stretches the cards in a row to the tallest one — so one late answer would
// re-flow the whole grid a second or two after the reader started reading it. The line is therefore
// reserved at render time, from the report list alone, and the quote drops into a hole that is
// already the right size. Every span in it carries an explicit line-height for the same reason the
// height is a constant: antd's body line-height of 1.5714 makes a 16px number a 25px line box, which
// a 22px hole would clip — so the numbers are set at 1.2 and the hole is sized for that, rather than
// the hole being sized for whatever line-height the theme happens to ship.
const QUOTE_LINE_H = 22
const PREVIEW_REPORT_LIMIT = 3
const PREVIEW_HOVER_DELAY_SECONDS = 0.7

export default function ReportCard({
  g,
  kindColors,
  quote,
}: {
  g: Group
  kindColors?: Record<string, string>
  /** Absent until the page's batch lands, and absent forever when it fails — see useHomeQuotes. */
  quote?: CardQuote
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const isNew = g.src === 'new'
  const [previewOpen, setPreviewOpen] = useState(false)
  const [baseHeight, setBaseHeight] = useState(0)
  const shellRef = useRef<HTMLDivElement>(null)
  const previewOpenTimer = useRef<number | null>(null)
  const previewCloseTimer = useRef<number | null>(null)

  useEffect(
    () => () => {
      if (previewOpenTimer.current !== null) window.clearTimeout(previewOpenTimer.current)
      if (previewCloseTimer.current !== null) window.clearTimeout(previewCloseTimer.current)
    },
    [],
  )

  const clearPreviewTimers = () => {
    if (previewOpenTimer.current !== null) {
      window.clearTimeout(previewOpenTimer.current)
      previewOpenTimer.current = null
    }
    if (previewCloseTimer.current !== null) {
      window.clearTimeout(previewCloseTimer.current)
      previewCloseTimer.current = null
    }
  }

  // Capture the card's resting height before taking it out of the grid flow. The shell keeps that
  // exact height while the card grows over its neighbours, so opening the preview never shifts a
  // row the reader may already be scanning.
  const openPreview = () => {
    clearPreviewTimers()
    const height = shellRef.current?.getBoundingClientRect().height || 0
    if (height > 0) setBaseHeight(height)
    setPreviewOpen(true)
  }
  const queuePreview = () => {
    clearPreviewTimers()
    previewOpenTimer.current = window.setTimeout(openPreview, PREVIEW_HOVER_DELAY_SECONDS * 1000)
  }
  const closePreview = (delay = 120) => {
    clearPreviewTimers()
    previewCloseTimer.current = window.setTimeout(() => {
      setPreviewOpen(false)
      previewCloseTimer.current = null
    }, delay)
  }

  // A-SHARE COLOUR CONVENTION: 红涨绿跌 — RED is up, GREEN is down, the reverse of every US and
  // European chart. Identical to QuoteStrip's, deliberately, because the same reader crosses from
  // this grid to that strip in one click and a colour that changed meaning on the way would be read
  // as a price that changed direction. The sign comes from `change`, an integer, and never from
  // changePct: that is a vendor string which may carry a '+', a '-', a full-width sign or none.
  const direction = quote ? Math.sign(Math.trunc(quote.change)) : 0
  const moveColour = direction > 0 ? token.colorError : direction < 0 ? token.colorSuccess : token.colorText

  const open = () => {
    if (isNew && g.symbol) navigate(`/stock/${encodeURIComponent(g.symbol)}?date=${encodeURIComponent(g.date)}`)
    else navigate(`/run/${encodeURIComponent(g.key)}`)
  }

  // Prefer the (as-of) company name, then the code; for thematic reports with
  // neither, show the original document title instead of a bare "报告".
  const displayName = g.name || g.symbol || g.title || t('home.reports')
  const kinds = (g.kinds?.length ? g.kinds : [g.kind]).filter(Boolean)
  const visibleKinds = kinds.slice(0, 3)
  const hiddenKinds = kinds.slice(3)
  const members = g.members || []
  const previewMembers = members.slice(0, PREVIEW_REPORT_LIMIT)
  const hiddenPreviewCount = Math.max(0, Math.max(g.n, members.length) - previewMembers.length)

  const preview = (
    <div
      className="rp-report-card-preview"
      data-testid="report-card-preview"
      aria-label={t('card.quickPreview')}
    >
      <div className="rp-report-card-preview__title">
        <Typography.Text strong>{t('card.quickPreview')}</Typography.Text>
      </div>

      {previewMembers.length > 0 && (
        <div className="rp-report-card-preview__reports">
          {previewMembers.map((member) => {
            const label = member.rtype || member.kind
            const title = member.title || label
            return (
              <div className="rp-report-card-preview__report" key={member.id}>
                {label && (
                  <Tag color={kindColors?.[member.kind] || 'default'} title={label}>
                    {label}
                  </Tag>
                )}
                <Typography.Text ellipsis={{ tooltip: title }}>{title}</Typography.Text>
              </div>
            )
          })}
          {hiddenPreviewCount > 0 && (
            <Tag
              className="rp-report-card-preview__more"
              title={members
                .slice(PREVIEW_REPORT_LIMIT)
                .map((member) => member.title || member.rtype)
                .filter(Boolean)
                .join(', ')}
            >
              +{hiddenPreviewCount}
            </Tag>
          )}
        </div>
      )}

      <div className="rp-report-card-preview__footer">
        <Space size={4}>
          {g.market && g.symbol && (
            <FavoriteButton market={g.market} symbol={g.symbol} showLabel size="small" />
          )}
          <Button
            type="primary"
            size="small"
            aria-label={t('queue.viewReport')}
            onClick={(event) => {
              event.stopPropagation()
              open()
            }}
          >
            {t('queue.viewReport')} <RightOutlined />
          </Button>
        </Space>
      </div>
    </div>
  )

  return (
    <div
      ref={shellRef}
      className={`rp-report-card-shell${previewOpen ? ' rp-report-card-shell--expanded' : ''}`}
      style={previewOpen && baseHeight > 0 ? { height: baseHeight } : undefined}
      onMouseEnter={queuePreview}
      onMouseLeave={() => closePreview()}
      onFocusCapture={openPreview}
      onBlurCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) closePreview(0)
      }}
      onKeyDownCapture={(event) => {
        if (event.key !== 'Escape' || !previewOpen) return
        event.preventDefault()
        event.stopPropagation()
        clearPreviewTimers()
        setPreviewOpen(false)
      }}
    >
      <Card
        hoverable
        size="small"
        className={`rp-report-card${previewOpen ? ' rp-report-card--expanded' : ''}`}
        {...clickable(open, displayName)}
        styles={{ body: { padding: 16 } }}
        style={{ height: '100%' }}
      >
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Space style={{ justifyContent: 'space-between', width: '100%' }} align="start">
            <div style={{ minWidth: 0, flex: 1 }}>
              <Typography.Paragraph
                strong
                style={{ fontSize: 16, marginBottom: 0 }}
                ellipsis={{ rows: 2, tooltip: displayName }}
              >
                {displayName}
              </Typography.Paragraph>
              {g.curName && g.curName !== g.name && (
                <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block' }}>
                  {t('card.now')}: {g.curName}
                </Typography.Text>
              )}
            </div>
            {g.symbol && (
              <div className="rp-card-symbol-actions">
                <Typography.Text
                  style={{ fontSize: 15, fontWeight: 500, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' }}
                >
                  {g.symbol}
                </Typography.Text>
                {g.market && <FavoriteButton market={g.market} symbol={g.symbol} className="rp-card-favorite" />}
              </div>
            )}
          </Space>

          {/* The live price, and the hole it lands in.
              Reserved for every card that names a code and for no other: a thematic report has no
              symbol, can never be quoted, and would only be paying 22 blank pixels for a line it will
              never fill. No currency label, unlike the strip: reports are A-share only (ADR 0030 §4),
              so every price that can reach this grid is CNY and there is no second kind of money here
              to mis-compare it against. A halted stock is shown as whatever the vendor published for
              it — this line has no room for the 停牌 tag the strip carries, and inventing one from a
              price alone is not something a card can honestly do. */}
          {!!g.symbol && (
            <div
              data-testid="card-quote-line"
              style={{ height: QUOTE_LINE_H, display: 'flex', alignItems: 'baseline', gap: 8, overflow: 'hidden' }}
            >
              {quote && (
                <>
                  <span
                    data-testid="card-quote"
                    style={{ color: moveColour, fontSize: 16, fontWeight: 600, lineHeight: 1.2, fontVariantNumeric: 'tabular-nums' }}
                  >
                    {fenToYuan(quote.last)}
                  </span>
                  <span style={{ color: moveColour, fontSize: 13, lineHeight: 1.2, fontVariantNumeric: 'tabular-nums' }}>
                    {signedFenToYuan(quote.change)}
                  </span>
                  {/* Verbatim, with a '%' and nothing else — not parsed, not reformatted, not derived
                      from last and prevClose (which is not even carried this far). ADR 0028 §4: on an
                      ex-rights morning the exchange restates the previous close and the vendor's
                      percentage is computed against that, so our own arithmetic would print a
                      double-digit crash on a stock that opened flat. */}
                  <span style={{ color: moveColour, fontSize: 13, lineHeight: 1.2, fontVariantNumeric: 'tabular-nums' }}>
                    {quote.changePct}%
                  </span>
                </>
              )}
            </div>
          )}

          <Space size={[6, 6]} wrap>
            {visibleKinds.map((k) => (
              <Tag key={k} color={kindColors?.[k] || 'default'} style={{ marginInlineEnd: 0 }}>
                {k}
              </Tag>
            ))}
            {hiddenKinds.length > 0 && (
              <Tag style={{ marginInlineEnd: 0 }} title={hiddenKinds.join(', ')}>
                +{hiddenKinds.length}
              </Tag>
            )}
            {!isNew && <Tag>{t('src.old')}</Tag>}
          </Space>

          <Space style={{ justifyContent: 'space-between', width: '100%' }}>
            <Typography.Text type="secondary" style={{ fontSize: 13 }}>
              <CalendarOutlined /> {isInstant(g.time) ? formatReportDateTime(g.time) : g.date}
            </Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 13 }}>
              <FileTextOutlined /> {g.n} {t('card.reports')}
            </Typography.Text>
          </Space>
          {previewOpen && preview}
        </Space>
      </Card>
    </div>
  )
}
