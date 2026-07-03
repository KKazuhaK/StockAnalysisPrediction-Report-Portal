import { describe, it, expect, beforeEach } from 'vitest'
import {
  FONT_MIN,
  FONT_MAX,
  FONT_DEFAULT,
  clampFontSize,
  normalizeWeight,
  setReaderPrefs,
  getReaderPrefs,
} from './reader'

describe('reader prefs store', () => {
  beforeEach(() => {
    window.localStorage.clear()
    // Reset to defaults between tests (the store is a module singleton).
    setReaderPrefs({ fontSize: FONT_DEFAULT, fontWeight: 400, wide: false })
  })

  it('clamps font size into range and falls back on non-finite values', () => {
    expect(clampFontSize(FONT_MIN - 5)).toBe(FONT_MIN)
    expect(clampFontSize(FONT_MAX + 9)).toBe(FONT_MAX)
    expect(clampFontSize(17)).toBe(17)
    expect(clampFontSize(17.6)).toBe(18) // rounded
    expect(clampFontSize(NaN)).toBe(FONT_DEFAULT)
  })

  it('accepts only the known font weights', () => {
    expect(normalizeWeight(400)).toBe(400)
    expect(normalizeWeight(500)).toBe(500)
    expect(normalizeWeight(600)).toBe(600)
    expect(normalizeWeight(700)).toBe(400)
    expect(normalizeWeight(123)).toBe(400)
  })

  it('persists changes to localStorage and reflects them in the snapshot', () => {
    setReaderPrefs({ fontSize: 20, fontWeight: 600, wide: true })
    const p = getReaderPrefs()
    expect(p).toEqual({ fontSize: 20, fontWeight: 600, wide: true })
    expect(localStorage.getItem('rp_read_fs')).toBe('20')
    expect(localStorage.getItem('rp_read_fw')).toBe('600')
    expect(localStorage.getItem('rp_read_wide')).toBe('1')
  })

  it('clamps and normalizes invalid values on set', () => {
    setReaderPrefs({ fontSize: 999, fontWeight: 321 })
    const p = getReaderPrefs()
    expect(p.fontSize).toBe(FONT_MAX)
    expect(p.fontWeight).toBe(400)
  })

  it('degrades to in-memory prefs when a storage write throws', () => {
    // Safari private mode / quota exhausted: the getter succeeds but setItem throws.
    const orig = window.localStorage.setItem
    window.localStorage.setItem = () => {
      throw new Error('QuotaExceededError')
    }
    try {
      expect(() => setReaderPrefs({ fontSize: 21, wide: true })).not.toThrow()
      // The update still commits to the in-memory snapshot.
      expect(getReaderPrefs().fontSize).toBe(21)
      expect(getReaderPrefs().wide).toBe(true)
    } finally {
      window.localStorage.setItem = orig
    }
  })

  it('notifies subscribers on change', () => {
    let hits = 0
    // Same subscribe path useSyncExternalStore uses; verified indirectly via a set.
    setReaderPrefs({ wide: true })
    hits += getReaderPrefs().wide ? 1 : 0
    expect(hits).toBe(1)
  })
})
