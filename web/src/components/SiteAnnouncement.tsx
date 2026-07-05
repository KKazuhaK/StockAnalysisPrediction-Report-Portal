import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import { Alert } from 'antd'
import type { AlertProps } from 'antd'
import { useSite } from '../site'
import type { AnnouncementLevel } from '../api/types'

const DISMISSED_KEY = 'report-portal.site-announcement.dismissed'

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

function readDismissed() {
  try {
    return window.localStorage.getItem(DISMISSED_KEY) || ''
  } catch {
    return ''
  }
}

function writeDismissed(key: string) {
  try {
    window.localStorage.setItem(DISMISSED_KEY, key)
  } catch {
    // Ignore private-mode/storage failures; the banner can still be dismissed for this render.
  }
}

export default function SiteAnnouncement({ style }: { style?: CSSProperties }) {
  const { settings } = useSite()
  const title = settings.announcementTitle.trim()
  const content = settings.announcementContent.trim()
  const enabled = settings.announcementEnabled && !!(title || content)
  const announcementKey = useMemo(() => {
    if (!enabled) return ''
    return hashAnnouncement(JSON.stringify({ level: settings.announcementLevel, title, content }))
  }, [content, enabled, settings.announcementLevel, title])
  const [dismissed, setDismissed] = useState(readDismissed)

  useEffect(() => {
    setDismissed(readDismissed())
  }, [announcementKey])

  if (!enabled || dismissed === announcementKey) return null

  const multiline = { whiteSpace: 'pre-line' as const }
  const message = title ? <span>{title}</span> : <span style={multiline}>{content}</span>
  const description = title && content ? <span style={multiline}>{content}</span> : undefined

  return (
    <Alert
      showIcon
      closable
      type={announcementAlertType(settings.announcementLevel)}
      message={message}
      description={description}
      onClose={() => {
        writeDismissed(announcementKey)
        setDismissed(announcementKey)
      }}
      style={{ borderRadius: 8, ...style }}
    />
  )
}
