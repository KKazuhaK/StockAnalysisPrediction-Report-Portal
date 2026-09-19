import { describe, it, expect } from 'vitest'
import { productVersionLabel } from './productVersionLabel'

describe('how the installed version reads', () => {
  it('drops the tag prefix, which belongs to git rather than to the number', () => {
    expect(productVersionLabel('v2026.38.1')).toBe('2026.38.1')
    expect(productVersionLabel('v2026.38.10')).toBe('2026.38.10')
    // A legacy tag is still a version number; the trim is about the prefix, not about CalVer.
    expect(productVersionLabel('v0.4.72')).toBe('0.4.72')
  })

  it('leaves a diagnostic build alone', () => {
    // "dev" and "ci" are what a local or CI build reports. There is nothing to trim, and treating a
    // leading v as a version prefix would turn "vnext" into "next".
    expect(productVersionLabel('dev')).toBe('dev')
    expect(productVersionLabel('ci')).toBe('ci')
    expect(productVersionLabel('unknown')).toBe('unknown')
    expect(productVersionLabel('vnext')).toBe('vnext')
    expect(productVersionLabel('')).toBe('')
  })
})
