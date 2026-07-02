import dayjs from 'dayjs'

// A report's `time` is a UTC RFC3339 instant (e.g. "2026-07-03T06:14:22Z"), stamped
// by the portal at ingest. It is rendered in the VIEWER's local timezone. Legacy rows
// carry no real instant — they are empty, a date-only "YYYY-MM-DD", or an old
// space-separated local string with no zone — so there is no clock to show for them.

// isInstant reports whether t is a real UTC instant we can safely localize (has a
// date + a 'T' separator, i.e. RFC3339), vs a legacy date-only / zoneless value.
export function isInstant(t?: string | null): boolean {
  return !!t && t.length > 10 && t.includes('T')
}

// formatReportTime renders the local clock time of an instant ("HH:mm", or
// "YYYY-MM-DD HH:mm:ss" with seconds). Returns '' for legacy/date-only values.
export function formatReportTime(t?: string | null, withSeconds = false): string {
  if (!isInstant(t)) return ''
  const d = dayjs(t)
  if (!d.isValid()) return ''
  return d.format(withSeconds ? 'YYYY-MM-DD HH:mm:ss' : 'HH:mm')
}

// formatReportDateTime renders the full local date+time of an instant, for tooltips.
// Returns '' for legacy/date-only values.
export function formatReportDateTime(t?: string | null): string {
  if (!isInstant(t)) return ''
  const d = dayjs(t)
  return d.isValid() ? d.format('YYYY-MM-DD HH:mm:ss') : ''
}
