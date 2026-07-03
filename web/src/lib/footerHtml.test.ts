import { describe, expect, it } from 'vitest'
import { sanitizeFooterHtml } from './footerHtml'

describe('sanitizeFooterHtml', () => {
  it('keeps lightweight filing-link HTML and adds safe rel on blank targets', () => {
    const html = sanitizeFooterHtml(
      '<strong>备案</strong> <a href="https://beian.miit.gov.cn/" target="_blank">沪ICP备xxxxxxxx号</a><br><small>ok</small>',
    )

    expect(html).toContain('<strong>备案</strong>')
    expect(html).toContain('<a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener noreferrer">')
    expect(html).toContain('<br>')
    expect(html).toContain('<small>ok</small>')
  })

  it('removes scripts, event handlers, unsafe links, and unsupported markup', () => {
    const html = sanitizeFooterHtml(
      '<script>alert(1)</script><img src=x onerror=alert(2)><a href="javascript:alert(3)" onclick="x()">bad</a><span style="color:red">text</span>',
    )

    expect(html).not.toContain('script')
    expect(html).not.toContain('alert')
    expect(html).not.toContain('img')
    expect(html).not.toContain('onclick')
    expect(html).not.toContain('style=')
    expect(html).toContain('<a>bad</a>')
    expect(html).toContain('<span>text</span>')
  })
})
