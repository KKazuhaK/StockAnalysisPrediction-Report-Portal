import { describe, it, expect } from 'vitest'
import { auditTime } from './auditTime'

// The stamp is UTC; the question is what the reader should see. The panel timezone is the one the
// business's days are counted in, and the reader's browser zone is often a different one.

const AT = '2026-08-03T14:30:00Z'

describe('auditTime', () => {
  it('renders in the panel timezone, not the reader’s', () => {
    // 14:30 UTC is 22:30 in Shanghai. A reader in New York must still see the business time first,
    // because that is the clock the rest of the portal counts days in.
    expect(auditTime(AT, 'Asia/Shanghai', 'America/New_York').text).toBe('2026-08-03 22:30:00')
  })

  it('adds the reader’s own time only when the two zones differ', () => {
    const away = auditTime(AT, 'Asia/Shanghai', 'America/New_York')
    expect(away.local).toBe('2026-08-03 10:30:00')

    // Same zone: one rendering. Repeating it would double the width of every row for the usual case.
    expect(auditTime(AT, 'Asia/Shanghai', 'Asia/Shanghai').local).toBeUndefined()
  })

  it('falls back to the reader’s zone when the portal has not set one', () => {
    const r = auditTime(AT, '', 'America/New_York')
    expect(r.text).toBe('2026-08-03 10:30:00')
    expect(r.local).toBeUndefined() // there is only one zone in play, so no parenthetical
  })

  // Rows written before v0.4.15 are the host's local wall clock with no zone recorded. Converting
  // them would mean inventing the offset nobody wrote down; showing them shifted would be worse
  // than showing them plain, because it would look authoritative.
  it('shows a pre-UTC row as stored, and says it is one', () => {
    const r = auditTime('2026-08-03 14:30:00', 'Asia/Shanghai', 'America/New_York')
    expect(r.text).toBe('2026-08-03 14:30:00')
    expect(r.legacy).toBe(true)
    expect(r.local).toBeUndefined()
  })

  it('does not crash on an empty or unparseable stamp', () => {
    expect(auditTime('', 'Asia/Shanghai').text).toBe('')
    expect(auditTime('not-a-time', 'Asia/Shanghai').text).toBe('not-a-time')
  })
})
