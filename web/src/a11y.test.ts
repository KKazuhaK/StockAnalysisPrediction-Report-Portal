import { describe, expect, it } from 'vitest'

// Two accessibility properties that are invisible in review and only show themselves to someone
// using a keyboard or a screen reader — which is to say, to nobody who reviews the diff.
//
// Both are guarded by scanning the source rather than by testing each site, because the failure mode
// is a NEW one being added, not an old one regressing. A per-component test cannot fail for a
// component that does not exist yet; this can.
//
// Vite's raw glob rather than node:fs — the web tsconfig carries no node types, and this is how the
// bundler already reads files (see antdStaticApi.test.ts).
const sources = import.meta.glob('./**/*.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p))

function lineOf(src: string, index: number): number {
  return src.slice(0, index).split('\n').length
}

describe('controls have an accessible name', () => {
  // A self-closing <Button> has no children, so an icon is the whole of it. A screen reader then
  // announces "button" and nothing else: delete and edit are indistinguishable, and one of them is
  // not recoverable.
  it('every icon-only Button carries one', () => {
    const pattern = /<Button\b((?:[^<>]|\{[^{}]*\}|\{\{[^{}]*\}\})*?)\/>/g
    const offenders: string[] = []
    for (const [path, src] of production) {
      for (const m of src.matchAll(pattern)) {
        const attrs = m[1]
        if (!attrs.includes('icon=')) continue
        if (attrs.includes('aria-label') || attrs.includes('title=')) continue
        offenders.push(`${path}:${lineOf(src, m.index ?? 0)}`)
      }
    }
    expect(offenders, 'add aria-label={t(...)} — an icon alone announces as "button"').toEqual([])
  })

  // A Card with onClick is a <div>: no focus, no role, and Enter does nothing. `clickable()` is the
  // one place that fixes all four, so the rule is that such a Card goes through it.
  it('every clickable Card goes through clickable()', () => {
    const pattern = /<Card\b((?:[^<>]|\{[^{}]*\}|\{\{[^{}]*\}\})*?)>/g
    const offenders: string[] = []
    for (const [path, src] of production) {
      for (const m of src.matchAll(pattern)) {
        const attrs = m[1]
        if (!/\bonClick=/.test(attrs)) continue
        if (attrs.includes('clickable(')) continue
        offenders.push(`${path}:${lineOf(src, m.index ?? 0)}`)
      }
    }
    expect(offenders, 'spread {...clickable(open, name)} instead of onClick — a div is not a control').toEqual([])
  })

  // A scan that silently matches nothing is the failure it exists to prevent: if the regexes stop
  // matching (a formatter change, a component rename), both assertions above pass for ever while
  // finding nothing at all.
  it('actually finds the components it is scanning', () => {
    const buttons = production.reduce((n, [, s]) => n + [...s.matchAll(/<Button\b/g)].length, 0)
    const cards = production.reduce((n, [, s]) => n + [...s.matchAll(/<Card\b/g)].length, 0)
    expect(buttons).toBeGreaterThan(50)
    expect(cards).toBeGreaterThan(10)
    expect(production.length).toBeGreaterThan(40)
  })
})
