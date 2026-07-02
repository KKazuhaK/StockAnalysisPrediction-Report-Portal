import { describe, it, expect } from 'vitest'
import { GithubOutlined, LinkOutlined } from '@ant-design/icons'
import { linkIconComponent, LINK_ICON_MAP, LINK_ICON_OPTIONS } from './linkIcons'

describe('linkIconComponent', () => {
  it('maps a known icon name to its component', () => {
    expect(linkIconComponent('github')).toBe(GithubOutlined)
  })

  it('falls back to the default link glyph for empty/unknown names', () => {
    expect(linkIconComponent('')).toBe(LinkOutlined)
    expect(linkIconComponent(undefined)).toBe(LinkOutlined)
    expect(linkIconComponent('does-not-exist')).toBe(LinkOutlined)
  })
})

describe('link icon options', () => {
  it('exposes one option per mapped icon, each with a string value', () => {
    expect(LINK_ICON_OPTIONS.length).toBe(Object.keys(LINK_ICON_MAP).length)
    expect(LINK_ICON_OPTIONS.every((o) => typeof o.value === 'string')).toBe(true)
  })

  it('includes the default "link" icon', () => {
    expect(LINK_ICON_MAP.link).toBe(LinkOutlined)
  })
})
