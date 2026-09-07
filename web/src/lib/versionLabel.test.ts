import { describe, it, expect } from 'vitest'
import { versionLabel } from './versionLabel'

const t = (k: string) => (k === 'versions.default' ? '默认' : k)

// The default version is seeded with no label at all, and the server falls back to the internal
// identifier — so every ordinary report was filed under a literal "default" in the reading page's
// switcher and, once it existed, in the browse filter. That is the identifier leaking into the UI,
// not a name anybody chose.
describe('what a version is called on screen', () => {
  it('names the default version instead of showing its identifier', () => {
    expect(versionLabel('default', '', t)).toBe('默认')
    // The server sends the identifier as the label when there is none, so that shape counts too.
    expect(versionLabel('default', 'default', t)).toBe('默认')
  })

  it('leaves an admin-chosen label exactly as typed', () => {
    expect(versionLabel('default', '内部完整版', t)).toBe('内部完整版')
    expect(versionLabel('对外版', '对外版', t)).toBe('对外版')
    expect(versionLabel('manual', '人工', t)).toBe('人工')
  })

  it('falls back to the identifier for any other unlabelled version', () => {
    // Better than blank: an auto-registered version (resolveVersion registers an unknown name on
    // sight) has no label, and its identifier is the only thing anyone can recognise it by.
    expect(versionLabel('client-a', '', t)).toBe('client-a')
  })
})
