import dayjs from 'dayjs'
import utc from 'dayjs/plugin/utc'
import timezone from 'dayjs/plugin/timezone'

dayjs.extend(utc)
dayjs.extend(timezone)

// Rendering an audit stamp.
//
// The stamp is a UTC instant. The portal has a BUSINESS timezone — the one civil dates are decided
// in — and the reader has a browser one, and they are often not the same: an operator abroad
// reading a log about a business day in Shanghai. Showing one of them silently makes every row
// ambiguous, and showing both always makes every row twice as long for the usual case where they
// agree. So: the panel zone, plus the reader's own only when it differs.
//
// Rows written before v0.4.15 are the host's local wall clock with no zone recorded. There is no
// honest conversion for those — the offset was never written down — so they are shown as stored,
// and marked, rather than shifted by an offset that is a guess.

export interface AuditTime {
  /** The panel-timezone rendering, or the raw legacy string. */
  text: string
  /** The reader's own rendering, present only when their zone differs from the panel's. */
  local?: string
  /** True for a pre-v0.4.15 row: stored without a zone, so shown as-is. */
  legacy: boolean
}

/** guessZone is the reader's IANA zone, or '' when the browser will not say. */
export function guessZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
}

const FMT = 'YYYY-MM-DD HH:mm:ss'

export function auditTime(at: string, panelTz: string, viewerTz = guessZone()): AuditTime {
  if (!at) return { text: '', legacy: false }
  // No 'T' means a legacy local stamp: no zone was recorded, so there is nothing to convert.
  if (!at.includes('T')) return { text: at, legacy: true }

  const d = dayjs(at)
  if (!d.isValid()) return { text: at, legacy: true }

  // An unset panel timezone means "follow the system zone", which for the reader is their own —
  // so there is only one rendering and no parenthetical.
  const panel = panelTz || viewerTz
  const text = panel ? d.tz(panel).format(FMT) : d.format(FMT)
  if (!panel || !viewerTz || panel === viewerTz) return { text, legacy: false }
  return { text, local: d.tz(viewerTz).format(FMT), legacy: false }
}
