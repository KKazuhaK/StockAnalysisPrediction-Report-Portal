import { Tag } from 'antd'
import type { TFunction } from 'i18next'

// Shared presentation for run/queue views (docs/adr/0007-run-analysis-and-scheduling.md).

export const PRIORITY_LEVELS = ['urgent', 'normal', 'other'] as const
export const PRIORITY_COLOR: Record<string, string> = { urgent: 'red', normal: 'blue', other: 'default' }
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

export function priorityTag(t: TFunction, p?: string) {
  const level = p || 'normal'
  return <Tag color={PRIORITY_COLOR[level] || 'default'}>{t(`batch.priority.${level}`)}</Tag>
}

export function priorityOptions(t: TFunction) {
  return PRIORITY_LEVELS.map((p) => ({ value: p, label: t(`batch.priority.${p}`) }))
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
