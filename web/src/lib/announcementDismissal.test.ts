import { describe, it, expect, beforeEach } from 'vitest'
import { announcementSig, dismissPopup, loadDismissals } from './announcementDismissal'
import type { Announcement } from '../api/types'

const MAP_KEY = 'report-portal.announce.dismissed.v1.alice'
const DAY = 86400000

function announcement(over: Partial<Announcement> = {}): Announcement {
  return {
    id: 1,
    level: 'warning',
    title: '维护通知',
    content: '今晚 22:00 开始维护。',
    popup: true,
    dismissible: false,
    scope: 'home',
    endsAt: '',
    ...over,
  }
}

describe('announcementSig', () => {
  // The legacy localStorage value was produced by exactly this hash over exactly this shape. That
  // is what makes the one-time migration a match rather than a guess, and it is why the signature
  // is computed in the browser instead of being handed down by the server.
  it('is stable, and covers only what the announcement says', () => {
    const a = announcement()
    expect(announcementSig(a)).toBe(announcementSig(announcement()))
    expect(announcementSig(announcement({ id: 99, popup: false, scope: 'app', endsAt: 'x' })))
      .toBe(announcementSig(a))
    expect(announcementSig(announcement({ content: '推迟到明晚。' }))).not.toBe(announcementSig(a))
    expect(announcementSig(announcement({ level: 'error' }))).not.toBe(announcementSig(a))
  })
})

describe('dismissal storage', () => {
  beforeEach(() => window.localStorage.clear())

  it('keeps dismissals per announcement and per reader', () => {
    const a = announcement({ id: 1 })
    const b = announcement({ id: 2, title: '另一条' })
    dismissPopup('alice', a)

    const alice = loadDismissals('alice', [a, b])
    expect(alice.popupDismissed(a)).toBe(true)
    expect(alice.popupDismissed(b)).toBe(false)
    expect(loadDismissals('bob', [a, b]).popupDismissed(a)).toBe(false)
  })

  it('re-notifies when the wording changes but not when anything else does', () => {
    const a = announcement()
    dismissPopup('alice', a)
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(true)

    // Reordered, rescheduled, retargeted, switched — same message, still silenced.
    const untouched = announcement({ scope: 'app', endsAt: '2026-09-01T00:00:00Z', dismissible: true })
    expect(loadDismissals('alice', [untouched]).popupDismissed(untouched)).toBe(true)

    const edited = announcement({ content: '维护推迟到明晚。' })
    expect(loadDismissals('alice', [edited]).popupDismissed(edited)).toBe(false)
  })

  // The tempting garbage-collection rule — "drop anything not in the current feed" — is wrong. The
  // feed filters by enabled, by display window and by audience, so "absent" has four meanings and
  // only one is "deleted". Switching an announcement off for a day would erase everyone's
  // dismissal and re-interrupt them all the next morning.
  it('does not forget a dismissal just because the announcement is not on offer right now', () => {
    const a = announcement()
    dismissPopup('alice', a)
    loadDismissals('alice', []) // e.g. the operator switched it off overnight
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(true)
  })

  it('drops entries older than the retention window', () => {
    const a = announcement()
    dismissPopup('alice', a, Date.now() - 200 * DAY)
    // Any write re-runs the trim, so a second, current dismissal evicts the ancient one.
    dismissPopup('alice', announcement({ id: 2 }))
    const stored = JSON.parse(window.localStorage.getItem(MAP_KEY) as string)
    expect(Object.keys(stored)).toEqual(['2'])
  })

  it('survives a corrupt or hostile stored value instead of throwing in the app shell', () => {
    const a = announcement()
    for (const junk of ['not json', '[]', '{"1":42}', 'null']) {
      window.localStorage.setItem(MAP_KEY, junk)
      expect(() => loadDismissals('alice', [a])).not.toThrow()
      expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(false)
    }
  })
})
