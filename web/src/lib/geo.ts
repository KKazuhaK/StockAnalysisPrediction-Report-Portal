import type { GeoLocation } from '../api/types'

// Rendering a resolved IP, the same way the sibling panel does it.
//
// "🇨🇳 China · Guangdong · Shenzhen" — country, then state/province, then city, each
// shown only when the database supplied it. Free databases are country-accurate and
// city-approximate, so the tail is a hint and the flag plus country is the fact.

/** countryFlag turns a 2-letter ISO code into its flag emoji (a regional-indicator
 *  pair). '' for anything that is not two letters, so a bad code renders as nothing
 *  rather than as two stray glyphs. */
export function countryFlag(cc?: string): string {
  if (!cc || !/^[A-Za-z]{2}$/.test(cc)) return ''
  const base = 0x1f1e6
  const up = cc.toUpperCase()
  return String.fromCodePoint(base + up.charCodeAt(0) - 65, base + up.charCodeAt(1) - 65)
}

/** countryName prefers the name the database gave, and otherwise localizes the ISO
 *  code into the reader's own language — a country-level database often carries only
 *  the code, and "CN" is a worse label than 中国 for a Chinese reader. */
export function countryName(g?: GeoLocation): string {
  if (!g) return ''
  if (g.country) return g.country
  if (!g.country_code) return ''
  try {
    const dn = new Intl.DisplayNames([navigator.language, 'en'], { type: 'region' })
    return dn.of(g.country_code.toUpperCase()) || g.country_code
  } catch {
    return g.country_code
  }
}

/** formatRegion builds the label. A city equal to its region is dropped, because free
 *  databases routinely report both for a municipality and "Shanghai · Shanghai" reads
 *  as a bug rather than as precision. */
export function formatRegion(g?: GeoLocation): string {
  if (!g || (!g.country_code && !g.country)) return ''
  const tail: string[] = []
  if (g.region) tail.push(g.region)
  if (g.city && g.city !== g.region) tail.push(g.city)
  const head = [countryFlag(g.country_code), countryName(g)].filter(Boolean).join(' ')
  return tail.length ? `${head} · ${tail.join(' · ')}` : head
}
