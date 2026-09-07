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

  // A plain DOM element with onClick is the same defect as the Card, wearing a different tag: no
  // focus, no role, and Enter does nothing. This rule found six the Card rule did not — a captcha
  // refresh on the LOGIN page, and three that are primary navigation (the reader's date timeline,
  // the review queue's link to a report, the chat's conversation list).
  //
  // antd components are excluded: they manage their own semantics. This is about raw tags.
  it('every clickable plain element is a real control', () => {
    const pattern = /<(div|span|li|td|tr|p|img|a|section|article)\b((?:[^<>]|\{[^{}]*\}|\{\{[^{}]*\}\})*?)>/g
    const offenders: string[] = []
    for (const [path, src] of production) {
      for (const m of src.matchAll(pattern)) {
        const attrs = m[2]
        if (!/\bonClick=/.test(attrs)) continue
        // href makes an <a> focusable and Enter-activatable by itself; role+tabIndex (what
        // clickable() spreads) does the same for anything else.
        if (/\bhref=/.test(attrs) || (attrs.includes('role=') && attrs.includes('tabIndex'))) continue
        offenders.push(`${path}:${lineOf(src, m.index ?? 0)}`)
      }
    }
    expect(offenders, 'spread {...clickable(fn)} — a bare onClick is unreachable without a mouse').toEqual([])
  })

  // Two antd components render markup that is NOT keyboard-operable, which the capitalised-tag
  // exemption above would otherwise wave through. Both were verified by rendering them, and
  // antdKeyboardBehaviour below pins that verification so an antd upgrade that fixes either one
  // makes a test fail rather than leaving a rule here that is quietly obsolete.
  //
  //   Typography.Link  — an <a> with no href: focusable (antd sets tabIndex=0), but Enter does
  //                      nothing, because an anchor's activation behaviour requires an href.
  //   List.Item        — rendered with tabIndex=-1, so it is not reachable by Tab at all.
  const UNSAFE_ANTD = ['Typography.Link', 'List.Item']

  it('every clickable antd component that is not keyboard-operable goes through clickable()', () => {
    const offenders: string[] = []
    for (const [path, src] of production) {
      for (const tag of UNSAFE_ANTD) {
        const pattern = new RegExp(`<${tag.replace('.', '\\.')}\\b((?:[^<>]|\\{[^{}]*\\}|\\{\\{[^{}]*\\}\\})*?)>`, 'g')
        for (const m of src.matchAll(pattern)) {
          const attrs = m[1]
          if (!/\bonClick=/.test(attrs)) continue
          if (/\bhref=/.test(attrs) || attrs.includes('clickable(')) continue
          offenders.push(`${path}:${lineOf(src, m.index ?? 0)} (${tag})`)
        }
      }
    }
    expect(offenders, 'spread {...clickable(fn)} — antd does not make these operable by itself').toEqual([])
  })

  // A scan that silently matches nothing is the failure it exists to prevent: if the regexes stop
  // matching (a formatter change, a component rename), both assertions above pass for ever while
  // finding nothing at all.
  it('actually finds the components it is scanning', () => {
    const buttons = production.reduce((n, [, s]) => n + [...s.matchAll(/<Button\b/g)].length, 0)
    const cards = production.reduce((n, [, s]) => n + [...s.matchAll(/<Card\b/g)].length, 0)
    const clicks = production.reduce((n, [, s]) => n + [...s.matchAll(/onClick=/g)].length, 0)
    expect(buttons).toBeGreaterThan(50)
    expect(cards).toBeGreaterThan(10)
    expect(clicks).toBeGreaterThan(100)
    expect(production.length).toBeGreaterThan(40)
  })
})
