import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import { Alert, Button, Space, Modal, Typography } from 'antd'
import type { AlertProps } from 'antd'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'
import { useSite } from '../site'
import type { AnnouncementLevel } from '../api/types'

const POPUP_DISMISSED_KEY = 'report-portal.site-announcement.popup.dismissed'

const ALERT_TYPES: Record<AnnouncementLevel, AlertProps['type']> = {
  notice: 'info',
  success: 'success',
  warning: 'warning',
  error: 'error',
}

export function announcementAlertType(level?: string): AlertProps['type'] {
  return ALERT_TYPES[level as AnnouncementLevel] || 'info'
}

function hashAnnouncement(raw: string): string {
  let hash = 2166136261
  for (let i = 0; i < raw.length; i += 1) {
    hash ^= raw.charCodeAt(i)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function readStorage(key: string) {
  try {
    return window.localStorage.getItem(key) || ''
  } catch {
    return ''
  }
}

function writeStorage(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value)
  } catch {
    // Ignore private-mode/storage failures; the announcement can still be dismissed for this render.
  }
}

export default function SiteAnnouncement({ style }: { style?: CSSProperties }) {
  const { t } = useTranslation()
  const loc = useLocation()
  const { settings } = useSite()
  const title = settings.announcementTitle.trim()
  const content = settings.announcementContent.trim()
  const enabled = settings.announcementEnabled && !!(title || content)
  const announcementKey = useMemo(() => {
    if (!enabled) return ''
    return hashAnnouncement(JSON.stringify({ level: settings.announcementLevel, title, content }))
  }, [content, enabled, settings.announcementLevel, title])
  const [popupDismissed, setPopupDismissed] = useState(() => readStorage(POPUP_DISMISSED_KEY))
  const [popupOpen, setPopupOpen] = useState(false)

  useEffect(() => {
    const storedPopup = readStorage(POPUP_DISMISSED_KEY)
    setPopupDismissed(storedPopup)
    setPopupOpen(loc.pathname === '/' && enabled && settings.announcementPopup && storedPopup !== announcementKey)
  }, [announcementKey, enabled, loc.key, loc.pathname, settings.announcementPopup])

  if (!enabled) return null

  const closePopup = (dontShowAgain = false) => {
    if (dontShowAgain) {
      writeStorage(POPUP_DISMISSED_KEY, announcementKey)
      setPopupDismissed(announcementKey)
    }
    setPopupOpen(false)
  }
  const message = title ? <Typography.Text style={{ fontWeight: 700 }}>{title}</Typography.Text> : undefined
  const description = content ? (
    <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line', lineHeight: 1.5 }}>
      {content}
    </Typography.Text>
  ) : undefined

  return (
    <>
      <Alert
        className="rp-announcement"
        showIcon
        type={announcementAlertType(settings.announcementLevel)}
        message={message || description}
        description={message ? description : undefined}
        style={{ borderRadius: 8, paddingBlock: 8, ...style }}
      />
      <Modal
        open={popupOpen && popupDismissed !== announcementKey}
        title={<Typography.Text style={{ fontWeight: 700 }}>{title || t('announcement.popupTitle')}</Typography.Text>}
        onCancel={() => closePopup(false)}
        footer={
          <Space style={{ width: '100%', justifyContent: 'flex-end' }} wrap>
            <Button onClick={() => closePopup(true)}>
              {t('announcement.dontShowAgain')}
            </Button>
            <Button type="primary" onClick={() => closePopup(false)}>
              {t('announcement.gotIt')}
            </Button>
          </Space>
        }
        destroyOnHidden
      >
        {content && <Typography.Paragraph style={{ whiteSpace: 'pre-line', marginBottom: 0 }}>{content}</Typography.Paragraph>}
      </Modal>
    </>
  )
}
