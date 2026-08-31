import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Alert, Button, Space, Modal, Typography, theme } from 'antd'
import type { AlertProps } from 'antd'
import {
  CheckCircleFilled,
  CloseCircleFilled,
  CloseOutlined,
  DownOutlined,
  ExclamationCircleFilled,
  InfoCircleFilled,
  UpOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router'
import { useAuth } from '../auth'
import { bandItems, inScope, stripItems, useAnnouncements } from '../announcements'
import {
  announcementSig,
  dismissBanner,
  dismissPopup,
  loadDismissals,
  markSeen,
  type DismissalState,
} from '../lib/announcementDismissal'
import type { Announcement, AnnouncementLevel } from '../api/types'

const ALERT_TYPES: Record<AnnouncementLevel, AlertProps['type']> = {
  notice: 'info',
  success: 'success',
  warning: 'warning',
  error: 'error',
}

export function announcementAlertType(level?: string): AlertProps['type'] {
  return ALERT_TYPES[level as AnnouncementLevel] || 'info'
}

const LEVEL_ICON: Record<AnnouncementLevel, typeof InfoCircleFilled> = {
  notice: InfoCircleFilled,
  success: CheckCircleFilled,
  warning: ExclamationCircleFilled,
  error: CloseCircleFilled,
}

// Severity order, so a band holding several announcements can be tinted by the loudest rather than
// by whichever the operator happened to drag to the top.
const LEVEL_RANK: Record<AnnouncementLevel, number> = { success: 0, notice: 1, warning: 2, error: 3 }

// antd token names per level, for the strip. The alert stack gets these from antd itself.
const LEVEL_TOKENS: Record<AnnouncementLevel, { bg: string; fg: string }> = {
  notice: { bg: 'colorInfoBg', fg: 'colorInfo' },
  success: { bg: 'colorSuccessBg', fg: 'colorSuccess' },
  warning: { bg: 'colorWarningBg', fg: 'colorWarning' },
  error: { bg: 'colorErrorBg', fg: 'colorError' },
}

// How many banners stack before the rest fold behind a counter. A phone has room for fewer, and an
// unbounded stack is worse than a truncated one: five warnings above the fold teaches the reader to
// scroll past the band entirely, which is the failure this whole feature exists to avoid.
const MAX_STACKED = 3
const MAX_STACKED_COMPACT = 2

/** One announcement rendered as the roomy banner. Shared with the admin preview. */
export function AnnouncementAlert({
  announcement,
  onClose,
  style,
}: {
  announcement: Pick<Announcement, 'level' | 'title' | 'content' | 'dismissible'>
  onClose?: () => void
  style?: CSSProperties
}) {
  const title = announcement.title.trim()
  const content = announcement.content.trim()
  const message = title ? <Typography.Text style={{ fontWeight: 700 }}>{title}</Typography.Text> : undefined
  const description = content ? (
    <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line', lineHeight: 1.5 }}>
      {content}
    </Typography.Text>
  ) : undefined
  return (
    <Alert
      className="rp-announcement"
      showIcon
      type={announcementAlertType(announcement.level)}
      message={message || description}
      description={message ? description : undefined}
      closable={announcement.dismissible ? { closeIcon: true } : undefined}
      onClose={onClose}
      style={{ borderRadius: 8, paddingBlock: 8, ...style }}
    />
  )
}

// Shared plumbing: the reader's dismissal state for whatever set of announcements is on offer, and
// the local record of what they have closed in this render. Both surfaces need exactly this, and
// they render disjoint sets, so each gets its own instance rather than a shared one.
function useReaderState(items: Announcement[]) {
  const { user } = useAuth()
  const [closed, setClosed] = useState<Record<number, boolean>>({})
  // The payload's identity: what is on offer AND what each one says. It is what the memo below
  // depends on, so a navigation alone never rebuilds this.
  const identity = items.map((a) => `${a.id}:${announcementSig(a)}`).join(',')
  const dismissals = useMemo<DismissalState>(
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `items`
    () => loadDismissals(user || '', items),
    [user, identity],
  )
  const open = items.filter((a) => !closed[a.id] && !dismissals.bannerDismissed(a))
  const close = (a: Announcement) => {
    dismissBanner(user || '', a)
    setClosed((prev) => ({ ...prev, [a.id]: true }))
  }
  return { open, close, dismissals, identity, user: user || '' }
}

/**
 * The home page's announcement band: home-scoped announcements, stacked, in the operator's order.
 */
export default function SiteAnnouncement({ style, compact = false }: { style?: CSSProperties; compact?: boolean }) {
  const { t } = useTranslation()
  const loc = useLocation()
  const { items, collapse } = useAnnouncements()
  const [expanded, setExpanded] = useState(false)
  const mine = useMemo(() => bandItems(items, loc.pathname), [items, loc.pathname])
  const { open, close } = useReaderState(mine)

  if (!open.length) return null
  const limit = compact ? MAX_STACKED_COMPACT : MAX_STACKED
  const shown = collapse && !expanded ? open.slice(0, limit) : open
  const hidden = open.length - shown.length

  return (
    <Space direction="vertical" size={8} style={{ width: '100%', ...style }}>
      {shown.map((a) => (
        <AnnouncementAlert key={a.id} announcement={a} onClose={() => close(a)} />
      ))}
      {hidden > 0 && (
        <Button type="link" size="small" style={{ padding: 0 }} onClick={() => setExpanded(true)}>
          {t('announcement.showMore', { count: hidden })}
        </Button>
      )}
    </Space>
  )
}

/**
 * The site-wide announcement strip: app-scoped announcements, on every page.
 *
 * Collapsed to one line by design. This band follows the reader everywhere, so it is chrome and has
 * to cost what chrome costs — three stacked alerts would be ~150px of every screen in the portal,
 * against ~36px here. Clicking it expands the full list in place rather than opening a popover
 * (360px of floating panel is unusable on a 375px phone) or a drawer (a scrim over the whole app to
 * read two sentences).
 */
export function AnnouncementStrip({ compact = false, maxWidth }: { compact?: boolean; maxWidth?: number | string }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { items, collapse } = useAnnouncements()
  const [expanded, setExpanded] = useState(false)
  const mine = useMemo(() => stripItems(items), [items])
  const { open, close } = useReaderState(mine)

  if (!open.length) return null
  const palette = token as unknown as Record<string, string>
  // The band is tinted by the loudest announcement in it, not by whichever happens to be first: a
  // stack whose one incident notice sits third should not read as a calm blue bar.
  const loudest = open.reduce((worst, a) => (LEVEL_RANK[a.level] > LEVEL_RANK[worst.level] ? a : worst), open[0])
  const tokens = LEVEL_TOKENS[loudest.level]
  // Folded is opt-in and off by default. It shows one line, and while folded it is the ONLY place
  // the lead appears — the previous version kept that line visible after expanding, so the first
  // announcement was drawn twice and the band looked broken.
  const folded = collapse && !expanded
  const shown = folded ? open.slice(0, 1) : open
  const hidden = open.length - shown.length

  return (
    <div className="rp-announce-strip" style={{ background: palette[tokens.bg] }}>
      <div
        style={{
          maxWidth,
          margin: '0 auto',
          padding: compact ? '8px 12px' : '8px 20px',
          display: 'flex',
          flexDirection: 'column',
          gap: 6,
        }}
      >
        {shown.map((a) => {
          const Icon = LEVEL_ICON[a.level]
          return (
            <div key={a.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
              <Icon
                style={{
                  color: palette[LEVEL_TOKENS[a.level].fg],
                  fontSize: 15,
                  flexShrink: 0,
                  marginTop: 4,
                }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                {a.title && (
                  <div
                    style={{
                      color: token.colorText,
                      fontSize: 14,
                      fontWeight: a.content ? 600 : 400,
                      ...(folded ? { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } : {}),
                    }}
                  >
                    {a.title}
                  </div>
                )}
                {a.content && !folded && (
                  <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line', lineHeight: 1.5 }}>
                    {a.content}
                  </Typography.Text>
                )}
                {!a.title && folded && (
                  <div
                    style={{
                      color: token.colorText,
                      fontSize: 14,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {a.content.split('\n')[0]}
                  </div>
                )}
              </div>
              {a.dismissible && (
                <Button
                  type="text"
                  size="small"
                  aria-label={t('announcement.close')}
                  icon={<CloseOutlined />}
                  onClick={() => close(a)}
                  style={{ flexShrink: 0 }}
                />
              )}
            </div>
          )
        })}

        {/* Only ever rendered when the site opted into folding: with the default there is nothing
            left over to reveal. */}
        {collapse && open.length > 1 && (
          <button
            type="button"
            aria-expanded={expanded}
            onClick={() => setExpanded((v) => !v)}
            style={{
              alignSelf: 'flex-start',
              marginLeft: 25,
              background: 'none',
              border: 0,
              padding: 0,
              cursor: 'pointer',
              color: token.colorTextSecondary,
              font: 'inherit',
              fontSize: 12,
              display: 'flex',
              alignItems: 'center',
              gap: 6,
            }}
          >
            {hidden > 0 ? t('announcement.showMore', { count: hidden }) : t('announcement.showLess')}
            {expanded ? <UpOutlined style={{ fontSize: 10 }} /> : <DownOutlined style={{ fontSize: 10 }} />}
          </button>
        )}
      </div>
    </div>
  )
}

/**
 * The one announcement popup.
 *
 * At most one per page load, the first eligible in the operator's order, over every announcement in
 * scope regardless of which surface draws its banner. Advancing to the next after it closes would
 * make a reader dismiss two modals in one interaction and would contradict what the admin page
 * says, so the admin list labels the popup switches that will not fire instead — an ignored setting
 * should be visible where it is set, not discovered later.
 */
export function AnnouncementPopup() {
  const { t } = useTranslation()
  const loc = useLocation()
  const { items } = useAnnouncements()
  const [popup, setPopup] = useState<Announcement | null>(null)
  const visible = useMemo(() => inScope(items, loc.pathname), [items, loc.pathname])
  const { dismissals, identity, user } = useReaderState(visible)
  // Keyed by id AND signature, not id alone: if the operator fixes a typo while a tab is open, the
  // corrected announcement is a different thing to have offered, and this tab should offer it.
  const fired = useRef(new Set<string>())

  useEffect(() => {
    const next = visible.find(
      (a) =>
        a.popup &&
        !fired.current.has(`${a.id}:${announcementSig(a)}`) &&
        !dismissals.popupDismissed(a) &&
        !dismissals.seenThisSession(a),
    )
    if (!next) return
    fired.current.add(`${next.id}:${announcementSig(next)}`)
    setPopup(next)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `visible`
  }, [identity, dismissals])

  // "Got it" stops the popup for this session; "don't show again" stops it for good. Both stop only
  // the POPUP, and only this one: the banner stays, so the notice is still readable and only the
  // interruption ends.
  const closePopup = (forGood: boolean) => {
    if (popup) {
      if (forGood) dismissPopup(user, popup)
      else markSeen(user, popup)
    }
    setPopup(null)
  }

  return (
    <Modal
      rootClassName="rp-announce-popup"
      open={!!popup}
      title={
        <Typography.Text style={{ fontWeight: 700 }}>{popup?.title || t('announcement.popupTitle')}</Typography.Text>
      }
      onCancel={() => closePopup(false)}
      footer={
        <Space style={{ width: '100%', justifyContent: 'flex-end' }} wrap>
          <Button onClick={() => closePopup(true)}>{t('announcement.dontShowAgain')}</Button>
          <Button type="primary" onClick={() => closePopup(false)}>
            {t('announcement.gotIt')}
          </Button>
        </Space>
      }
      destroyOnHidden
    >
      {popup?.content && (
        <Typography.Paragraph style={{ whiteSpace: 'pre-line', marginBottom: 0 }}>{popup.content}</Typography.Paragraph>
      )}
    </Modal>
  )
}
