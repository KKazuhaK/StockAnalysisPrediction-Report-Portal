import { describe, it, expect } from 'vitest'
import { countryFlag, countryName, formatRegion } from './geo'

describe('geo labels', () => {
  it('turns an ISO code into a flag', () => {
    expect(countryFlag('CN')).toBe('🇨🇳')
    expect(countryFlag('us')).toBe('🇺🇸')
  })

  // A bad code must render as nothing, not as two stray letter-glyphs.
  it('refuses anything that is not two letters', () => {
    for (const bad of ['', 'C', 'CHN', '12', undefined]) {
      expect(countryFlag(bad)).toBe('')
    }
  })

  it('prefers the database’s name and localizes the code when there is none', () => {
    expect(countryName({ country: 'China', country_code: 'CN' })).toBe('China')
    // A country-level database often carries only the code, and "CN" is a worse label
    // than the reader's own word for it.
    expect(countryName({ country_code: 'JP' })).toBe('Japan')
    expect(countryName({})).toBe('')
  })

  it('builds country · region · city', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China', region: 'Guangdong', city: 'Shenzhen' })).toBe(
      '🇨🇳 China · Guangdong · Shenzhen',
    )
  })

  // Free databases report both for a municipality; "Shanghai · Shanghai" reads as a bug.
  it('drops a city that repeats its region', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China', region: 'Shanghai', city: 'Shanghai' })).toBe(
      '🇨🇳 China · Shanghai',
    )
  })

  it('renders a country-only result, and nothing at all for an unknown address', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China' })).toBe('🇨🇳 China')
    expect(formatRegion({})).toBe('')
    expect(formatRegion(undefined)).toBe('')
  })
})
