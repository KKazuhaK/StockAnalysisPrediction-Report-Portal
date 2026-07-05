import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import { Alert, Button, Checkbox, Modal, Space, Typography } from 'antd'
import type { AlertProps } from 'antd'
import { useTranslation } from 'react-i18next'
import { useSite } from '../site'
import type { AnnouncementLevel } from '../api/types'

const DISMISSED_KEY = 'report-portal.site-announcement.dismissed'
const POPUP_DISMISSED_KEY = 'report-portal.site-announcement.popup.dismissed'
const POPUP_SEEN_KEY = 'report-portal.site-announcement.popup.seen'

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

function readSession(key: string) {
  try {
    return window.sessionStorage.getItem(key) || ''
  } catch {
    return ''
  }
}

function writeSession(key: string, value: string) {
  try {
    window.sessionStorage.setItem(key, value)
  } catch {
    // Ignore private-mode/storage failures; the popup can still close for this render.
  }
}

export default function SiteAnnouncement({ style }: { style?: CSSProperties }) {
  const { t } = useTranslation()
  const { settings } = useSite()
  const title = settings.announcementTitle.trim()
  const content = settings.announcementContent.trim()
  const enabled = settings.announcementEnabled && !!(title || content)
  const announcementKey = useMemo(() => {
    if (!enabled) return ''
    return hashAnnouncement(JSON.stringify({ level: settings.announcementLevel, title, content }))
  }, [content, enabled, settings.announcementLevel, title])
  const [dismissed, setDismissed] = useState(() => readStorage(DISMISSED_KEY))
  const [popupDismissed, setPopupDismissed] = useState(() => readStorage(POPUP_DISMISSED_KEY))
  const [popupSeen, setPopupSeen] = useState(() => readSession(POPUP_SEEN_KEY))
  const [popupOpen, setPopupOpen] = useState(false)
  const [popupDontShowAgain, setPopupDontShowAgain] = useState(false)

  useEffect(() => {
    setDismissed(readStorage(DISMISSED_KEY))
    const storedPopup = readStorage(POPUP_DISMISSED_KEY)
    const seenPopup = readSession(POPUP_SEEN_KEY)
    setPopupDismissed(storedPopup)
    setPopupSeen(seenPopup)
    setPopupDontShowAgain(false)
    setPopupOpen(enabled && settings.announcementPopup && storedPopup !== announcementKey && seenPopup !== announcementKey)
  }, [announcementKey, enabled, settings.announcementPopup])

  if (!enabled) return null

  const closeBanner = () => {
    writeStorage(DISMISSED_KEY, announcementKey)
    setDismissed(announcementKey)
  }
  const closePopup = () => {
    if (popupDontShowAgain) {
      writeStorage(POPUP_DISMISSED_KEY, announcementKey)
      setPopupDismissed(announcementKey)
    }
    writeSession(POPUP_SEEN_KEY, announcementKey)
    setPopupSeen(announcementKey)
    setPopupOpen(false)
  }
  const message = (
    <span style={{ display: 'inline-flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap', lineHeight: 1.35 }}>
      {title && <Typography.Text strong>{title}</Typography.Text>}
      {content && (
        <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line' }}>
          {content}
        </Typography.Text>
      )}
    </span>
  )
  const showBanner = dismissed !== announcementKey

  return (
    <>
      {showBanner && (
        <Alert
          showIcon
          closable
          type={announcementAlertType(settings.announcementLevel)}
          message={message}
          onClose={closeBanner}
          style={{ borderRadius: 8, paddingBlock: 8, ...style }}
        />
      )}
      <Modal
        open={popupOpen && popupDismissed !== announcementKey && popupSeen !== announcementKey}
        title={title || t('announcement.popupTitle')}
        onCancel={closePopup}
        footer={
          <Space style={{ width: '100%', justifyContent: 'space-between' }} wrap>
            <Checkbox checked={popupDontShowAgain} onChange={(e) => setPopupDontShowAgain(e.target.checked)}>
              {t('announcement.dontShowAgain')}
            </Checkbox>
            <Button type="primary" onClick={closePopup}>
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
