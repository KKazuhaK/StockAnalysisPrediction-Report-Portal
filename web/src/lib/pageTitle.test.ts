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

// A route added later must not silently get an unnamed tab — which is what the whole feature is
// about, and the easiest thing in it to forget. This reads the router itself rather than a list
// kept beside it, so the check cannot drift out of date without failing.
const appSource = Object.values(
  import.meta.glob('../App.tsx', { query: '?raw', import: 'default', eager: true }),
)[0] as string

// Routes that are deliberately just the site name: home, and the pages you see before signing in
// where the site's name is the only useful thing to say.
const BARE = new Set(['/', '/login', '/register', '/forgot', '/reset', '/verify'])
// Never rendered: a back-compat redirect to /manage/site.
const REDIRECT_ONLY = new Set(['settings'])

describe('every route in the router has a tab title', () => {
  const declared = [...appSource.matchAll(/path="([^"]+)"/g)].map((m) => m[1])

  it('finds the routes at all', () => {
    // A scan that silently matches nothing would pass the assertion below for ever.
    expect(declared.length).toBeGreaterThan(25)
    expect(declared).toContain('/queue')
    expect(declared).toContain('users')
  })

  it('names each of them', () => {
    const unnamed: string[] = []
    for (const raw of declared) {
      if (raw === '*' || REDIRECT_ONLY.has(raw)) continue
      // Relative routes in this router are all children of /manage.
      const path = raw.startsWith('/') ? raw : `/manage/${raw}`
      if (BARE.has(path)) continue
      // A concrete pathname: params never reach routeTitle as their placeholder.
      const concrete = path.replace(/:[^/]+/g, 'x')
      if (!routeTitle(concrete, t)) unnamed.push(raw)
    }
    expect(unnamed, 'add these to pageTitle.ts, or to BARE if the site name alone is right').toEqual([])
  })
})
