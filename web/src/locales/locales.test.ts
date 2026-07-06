import { describe, it, expect } from 'vitest'
import { LANGS, BASE_LANG, findLang, detectLang, normalizeSaved } from './index'

// Auto-detect maps the browser's ordered language preferences onto a supported code,
// resolving Simplified vs Traditional by script (Hans/Hant) or region (CN/SG vs TW/HK/MO),
// and falls back to the base language when nothing matches.
describe('detectLang', () => {
  const cases: Array<[string[], string]> = [
    [['zh-CN'], 'zh-CN'],
    [['zh-Hans'], 'zh-CN'],
    [['zh-Hans-CN'], 'zh-CN'],
    [['zh-SG'], 'zh-CN'],
    [['zh'], 'zh-CN'],
    [['zh-TW'], 'zh-TW'],
    [['zh-Hant'], 'zh-TW'],
    [['zh-Hant-HK'], 'zh-TW'],
    [['zh-HK'], 'zh-TW'],
    [['zh-MO'], 'zh-TW'],
    // explicit Simplified script must win over a Traditional region
    [['zh-Hans-HK'], 'zh-CN'],
    [['zh-Hans-MO'], 'zh-CN'],
    [['zh-Hans-TW'], 'zh-CN'],
    [['en-US'], 'en-US'],
    [['en'], 'en-US'],
    [['EN-GB'], 'en-US'], // case-insensitive
    [['fr', 'en-US'], 'en-US'], // skip unsupported, take the next preference
    [['ja'], 'zh-CN'], // nothing supported → base
    [[], 'zh-CN'], // empty → base
    [['zh-TW', 'en'], 'zh-TW'], // honor order: first match wins
  ]
  for (const [prefs, want] of cases) {
    it(`${JSON.stringify(prefs)} → ${want}`, () => {
      expect(detectLang(prefs)).toBe(want)
    })
  }
})

describe('normalizeSaved', () => {
  it('keeps supported locales and migrates legacy short codes', () => {
    expect(normalizeSaved('zh-CN')).toBe('zh-CN')
    expect(normalizeSaved('zh-TW')).toBe('zh-TW')
    expect(normalizeSaved('en-US')).toBe('en-US')
    expect(normalizeSaved('zh')).toBe('zh-CN')
    expect(normalizeSaved('en')).toBe('en-US')
  })

  it('falls back to the base language for unknown or missing values', () => {
    expect(normalizeSaved(null)).toBe(BASE_LANG)
    expect(normalizeSaved('en-GB')).toBe(BASE_LANG)
    expect(normalizeSaved('fr')).toBe(BASE_LANG)
  })
})

// Structural guarantee for "many languages": every registered language must load a bundle
// whose string keys exactly match the base language (no missing / extra / empty), and must
// carry its antd + dayjs locale. Adding a language with drifted keys fails here.
describe('locale bundles', () => {
  it('registers the base language', () => {
    expect(findLang(BASE_LANG)).toBeTruthy()
  })

  it('every language is complete and key-aligned with the base', async () => {
    const base = (await findLang(BASE_LANG)!.load()).default
    const baseKeys = Object.keys(base.translation).sort()
    expect(baseKeys.length).toBeGreaterThan(0)

    for (const lang of LANGS) {
      const b = (await lang.load()).default
      expect(Object.keys(b.translation).sort(), `${lang.code} keys`).toEqual(baseKeys)
      for (const [k, v] of Object.entries(b.translation)) {
        expect(typeof v === 'string' && v.length > 0, `${lang.code} ${k} non-empty`).toBe(true)
      }
      expect(b.antd, `${lang.code} antd pack`).toBeTruthy()
      expect(b.dayjs, `${lang.code} dayjs id`).toBeTruthy()
    }
  })
})
