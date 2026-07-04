import { Tag } from 'antd'
import type { TFunction } from 'i18next'

// Shared presentation for run/queue views. Priority is a Slurm-style number now, not a
// tier: a run stores "urgent" (加急) or a base number 0..100 (docs/adr/0008-multifactor-priority.md).

export const BASE_MAX = 100
export const JOB_STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  running: 'processing',
  cancelling: 'warning',
  cancelled: 'default',
  finished: 'success',
}

export function statusTag(t: TFunction, s: string) {
  return <Tag color={JOB_STATUS_COLOR[s] || 'default'}>{t(`batch.status.${s}`)}</Tag>
}

// isUrgent reports whether a stored priority is the 加急 escalation.
export function isUrgent(p?: string) {
  return p === 'urgent'
}

// priorityNum reads a stored priority as its base number (mirrors the backend's
// parsePriority): the legacy tier names map (normal→50, other→20), a number is clamped.
export function priorityNum(p?: string): number {
  if (p == null || p === '' || p === 'normal') return 50
  if (p === 'other') return 20
  const n = Number(p)
  if (!Number.isFinite(n)) return 50
  return Math.min(BASE_MAX, Math.max(0, Math.round(n)))
}

// baseTagColor tints a base number so higher priorities read warmer at a glance.
function baseTagColor(n: number): string {
  if (n >= 67) return 'volcano'
  if (n >= 34) return 'blue'
  return 'default'
}

// priorityTag renders a run's priority: a red 加急 tag, else the base number tinted by
// magnitude.
export function priorityTag(t: TFunction, p?: string) {
  if (isUrgent(p)) return <Tag color="red">{t('batch.priority.urgent')}</Tag>
  const n = priorityNum(p)
  return <Tag color={baseTagColor(n)}>{n}</Tag>
}

// fmtInputs renders a run's first-row inputs JSON as "key=value  key=value",
// dropping empty values (e.g. an unfilled optional field).
export function fmtInputs(s?: string) {
  if (!s) return ''
  try {
    const o = JSON.parse(s) as Record<string, string>
    return Object.entries(o)
      .filter(([, v]) => v !== '' && v != null)
      .map(([k, v]) => `${k}=${v}`)
      .join('  ')
  } catch {
    return s
  }
}

export function isTerminal(status: string) {
  return status === 'finished' || status === 'cancelled'
}
