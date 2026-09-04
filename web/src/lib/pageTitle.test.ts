import { describe, it, expect } from 'vitest'
import { pageTitle, routeTitle } from './pageTitle'

const t = (k: string) => k

describe('what the browser tab says', () => {
  it('names the page, with the site as the second half', () => {
    expect(pageTitle(routeTitle('/queue', t), '智研平台')).toBe('nav.queue · 智研平台')
    expect(pageTitle(routeTitle('/manage/users', t), '智研平台')).toBe('nav.users · 智研平台')
    expect(pageTitle(routeTitle('/apps/batch', t), '智研平台')).toBe('nav.batch · 智研平台')
  })

  it('leaves the home page and the signed-out pages as the site name alone', () => {
    for (const p of ['/', '/login', '/register', '/forgot', '/reset', '/verify']) {
      expect(pageTitle(routeTitle(p, t), '智研平台')).toBe('智研平台')
    }
  })

  it('names a reader page by what it is showing, which is what tells two open reports apart', () => {
    expect(routeTitle('/stock/600519', t)).toBe('600519')
    expect(routeTitle('/run/600519%7C2026-09-04', t)).toBe('600519|2026-09-04')
    // A hand-typed URL can carry a malformed escape; a title is never worth an exception.
    expect(routeTitle('/stock/%E4%B8', t)).toBe('%E4%B8')
  })

  it('falls back to the section rather than going blank on an unknown manage tab', () => {
    expect(routeTitle('/manage/something-added-later', t)).toBe('nav.manage')
    expect(routeTitle('/manage', t)).toBe('nav.manage')
  })

  it('ignores a trailing slash, which the router treats as the same route', () => {
    expect(routeTitle('/queue/', t)).toBe('nav.queue')
    expect(routeTitle('/', t)).toBe('')
  })

  it('distinguishes writing a report from editing one', () => {
    expect(routeTitle('/report/new', t)).toBe('reportEditor.titleNew')
    expect(routeTitle('/report/42/edit', t)).toBe('reportEditor.titleEdit')
  })
})
