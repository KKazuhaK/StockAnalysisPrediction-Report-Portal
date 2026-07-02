import { describe, it, expect } from 'vitest'
import { isInstant, formatReportTime, formatReportDateTime } from './datetime'

describe('report time formatting', () => {
  it('detects real instants vs legacy/date-only values', () => {
    expect(isInstant('2026-07-03T06:14:22Z')).toBe(true)
    expect(isInstant('2026-07-03')).toBe(false) // date-only
    expect(isInstant('2026-07-03 06:14:22')).toBe(false) // legacy local, zoneless, no 'T'
    expect(isInstant('')).toBe(false)
    expect(isInstant(undefined)).toBe(false)
    expect(isInstant(null)).toBe(false)
  })

  it('formats an instant to a local HH:mm clock', () => {
    expect(formatReportTime('2026-07-03T06:14:22Z')).toMatch(/^\d{2}:\d{2}$/)
    expect(formatReportTime('2026-07-03T06:14:22Z', true)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('renders the full local datetime for tooltips', () => {
    expect(formatReportDateTime('2026-07-03T06:14:22Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('returns empty string for legacy/date-only/invalid — nothing to localize', () => {
    expect(formatReportTime('2026-07-03')).toBe('')
    expect(formatReportTime('')).toBe('')
    expect(formatReportTime(undefined)).toBe('')
    expect(formatReportDateTime('2026-07-03')).toBe('')
    expect(formatReportDateTime('not-a-date')).toBe('')
  })

  it('localizes UTC to the viewer zone (a Z instant near midnight shifts by offset)', () => {
    // Two instants 8h apart render different local clocks regardless of the viewer zone.
    const a = formatReportTime('2026-07-03T00:00:00Z')
    const b = formatReportTime('2026-07-03T08:00:00Z')
    expect(a).not.toBe(b)
  })
})
