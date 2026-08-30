import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Alert, Button, Space, Modal, Typography } from 'antd'
import type { AlertProps } from 'antd'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router'
import { useAuth } from '../auth'
import { inScope, useAnnouncements } from '../announcements'
import {
  announcementSig,
  dismissBanner,
  dismissPopup,
  loadDismissals,
  markSeen,
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

// How many banners stack before the rest fold behind a counter. A phone has room for fewer, and
// an unbounded stack is worse than a truncated one: five warnings above the fold teaches the
// reader to scroll past the band entirely, which is the failure this whole feature exists to avoid.
const MAX_STACKED = 3
const MAX_STACKED_COMPACT = 2

export function AnnouncementBody({ content }: { content: string }) {
  return (
    <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line', lineHeight: 1.5 }}>
      {content}
    </Typography.Text>
  )
}

/** One announcement rendered as the banner the reader sees. Shared with the admin preview. */
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
  const description = content ? <AnnouncementBody content={content} /> : undefined
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

/**
 * The reader-facing announcement band: every announcement addressed to this reader that belongs on
 * this route, plus at most one popup.
 *
 * Popups: one per page load, the first eligible in the operator's order. Advancing to the next one
 * after it is closed would contradict the hint the admin page shows and would make the reader
 * dismiss two modals in one interaction, so the admin list labels the popup switches that will not
 * fire instead — an ignored toggle should be visible where it is set, not discovered later.
 */
export default function SiteAnnouncement({ style, compact = false }: { style?: CSSProperties; compact?: boolean }) {
  const { t } = useTranslation()
  const loc = useLocation()
  const { user } = useAuth()
  const { items } = useAnnouncements()
  const [expanded, setExpanded] = useState(false)
  const [closed, setClosed] = useState<Record<number, boolean>>({})
  const [popup, setPopup] = useState<Announcement | null>(null)
  // Keyed by id AND signature, not id alone: if the operator fixes a typo while a tab is open,
  // the corrected announcement is a different thing to have offered, and this tab should offer it.
  const fired = useRef(new Set<string>())

  const visible = useMemo(() => inScope(items, loc.pathname), [items, loc.pathname])
  // The payload's identity, so the effects below re-run when what is on offer actually changes —
  // and NOT on every navigation. The previous version depended on the router's location key, so a
  // popup re-fired every time the reader came back to the page it was scoped to.
  const identity = visible.map((a) => `${a.id}:${announcementSig(a)}`).join(',')

  const dismissals = useMemo(
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `visible`
    () => loadDismissals(user || '', visible),
    [user, identity],
  )

  useEffect(() => {
    const next = visible.find(
      (a) => a.popup && !fired.current.has(`${a.id}:${announcementSig(a)}`) &&
        !dismissals.popupDismissed(a) && !dismissals.seenThisSession(a),
    )
    if (!next) return
    fired.current.add(`${next.id}:${announcementSig(next)}`)
    setPopup(next)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `visible`
  }, [identity, dismissals])

  const banners = visible.filter((a) => !closed[a.id] && !dismissals.bannerDismissed(a))
  const limit = compact ? MAX_STACKED_COMPACT : MAX_STACKED
  const shown = expanded ? banners : banners.slice(0, limit)
  const hidden = banners.length - shown.length

  const closeBanner = (a: Announcement) => {
    dismissBanner(user || '', a)
    setClosed((prev) => ({ ...prev, [a.id]: true }))
  }

  // "Got it" stops the popup for this session; "don't show again" stops it for good. Both stop
  // only the POPUP, and only this one: the banner stays, so the notice is still readable and only
  // the interruption ends.
  const closePopup = (forGood: boolean) => {
    if (popup) {
      if (forGood) dismissPopup(user || '', popup)
      else markSeen(user || '', popup)
    }
    setPopup(null)
  }

  if (!banners.length && !popup) return null

  return (
    <>
      {shown.length > 0 && (
        <Space direction="vertical" size={8} style={{ width: '100%', ...style }}>
          {shown.map((a) => (
            <AnnouncementAlert key={a.id} announcement={a} onClose={() => closeBanner(a)} />
          ))}
          {hidden > 0 && (
            <Button type="link" size="small" style={{ padding: 0 }} onClick={() => setExpanded(true)}>
              {t('announcement.showMore', { count: hidden })}
            </Button>
          )}
        </Space>
      )}
      <Modal
        rootClassName="rp-announce-popup"
        open={!!popup}
        title={
          <Typography.Text style={{ fontWeight: 700 }}>
            {popup?.title || t('announcement.popupTitle')}
          </Typography.Text>
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
          <Typography.Paragraph style={{ whiteSpace: 'pre-line', marginBottom: 0 }}>
            {popup.content}
          </Typography.Paragraph>
        )}
      </Modal>
    </>
  )
}
