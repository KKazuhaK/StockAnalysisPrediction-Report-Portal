import { describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'

import { INPUT_ENTRY_MAX, INPUT_TOTAL_MAX, INPUT_VALUE_MAX, InputsPreview, fmtInputs, summarizeInputs } from './batchUi'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const json = (o: Record<string, string>) => JSON.stringify(o)
const long = (n: number, c = 'x') => c.repeat(n)

describe('summarizeInputs — level 1: each value is clamped on its own', () => {
  it('clamps a runaway value but keeps every later entry whole', () => {
    // The real case: an agent run whose `query` is a multi-thousand-character prompt. It
    // must not swallow the entries after it — symbol=301539 is the one people look for.
    const { entries, hidden } = summarizeInputs(json({ query: long(4000), symbol: '301539' }))
    expect(entries).toEqual([`query=${long(INPUT_VALUE_MAX)}…`, 'symbol=301539'])
    expect(hidden).toBe(0)
  })

  it('leaves a value that fits untouched', () => {
    expect(summarizeInputs(json({ symbol: '301539', date: '2026-08-10' })).entries).toEqual([
      'symbol=301539',
      'date=2026-08-10',
    ])
  })

  it('collapses newlines so a multi-line prompt stays one line', () => {
    expect(summarizeInputs(json({ query: 'a\n\nb  c' })).entries).toEqual(['query=a b c'])
  })
})

describe('summarizeInputs — level 2: the list as a whole is clamped', () => {
  it('keeps at most INPUT_ENTRY_MAX entries and reports the rest as hidden', () => {
    const many = Object.fromEntries(Array.from({ length: INPUT_ENTRY_MAX + 5 }, (_, i) => [`k${i}`, 'v']))
    const { entries, hidden } = summarizeInputs(json(many))
    expect(entries).toHaveLength(INPUT_ENTRY_MAX)
    expect(hidden).toBe(5)
  })

  it('stops once the kept entries have spent the total budget', () => {
    // Every value is under the per-value cap, so only the whole-list budget can stop this.
    const fat = Object.fromEntries(Array.from({ length: 9 }, (_, i) => [`k${i}`, long(INPUT_VALUE_MAX - 10)]))
    const { entries, hidden } = summarizeInputs(json(fat))
    expect(entries.length).toBeLessThan(9)
    expect(hidden).toBe(9 - entries.length)
    expect(entries.slice(0, -1).join('').length).toBeLessThanOrEqual(INPUT_TOTAL_MAX)
  })

  it('always keeps the first entry, however long it is', () => {
    const { entries, hidden } = summarizeInputs(json({ query: long(9000) }), { totalMax: 10 })
    expect(entries).toHaveLength(1)
    expect(hidden).toBe(0)
  })

  it('honours explicit caps', () => {
    const { entries, hidden } = summarizeInputs(json({ a: 'abcdef', b: 'b', c: 'c' }), { valueMax: 3, entryMax: 2 })
    expect(entries).toEqual(['a=abc…', 'b=b'])
    expect(hidden).toBe(1)
  })
})

describe('summarizeInputs — edge cases', () => {
  it('returns nothing for empty or blank inputs', () => {
    expect(summarizeInputs(undefined)).toEqual({ entries: [], hidden: 0 })
    expect(summarizeInputs('')).toEqual({ entries: [], hidden: 0 })
    expect(summarizeInputs(json({}))).toEqual({ entries: [], hidden: 0 })
  })

  it('drops empty values, as the full formatter does', () => {
    expect(summarizeInputs(json({ symbol: '301539', note: '' })).entries).toEqual(['symbol=301539'])
  })

  it('clamps a body that is not JSON instead of dumping it whole', () => {
    const { entries } = summarizeInputs(long(4000))
    expect(entries).toEqual([`${long(INPUT_VALUE_MAX)}…`])
  })
})

describe('fmtInputs stays lossless', () => {
  it('is untouched by the preview caps — search and the detail modal read the full text', () => {
    const full = fmtInputs(json({ query: long(4000), symbol: '301539' }))
    expect(full).toBe(`query=${long(4000)}  symbol=301539`)
  })
})

describe('InputsPreview', () => {
  it('renders the clamped entries, not the raw inputs', () => {
    const { container } = render(<InputsPreview inputs={json({ query: long(4000), symbol: '301539' })} />)
    expect(container.textContent).toContain('symbol=301539')
    expect(container.textContent).not.toContain(long(INPUT_VALUE_MAX + 1))
  })

  it('reports how many entries it dropped', () => {
    const many = Object.fromEntries(Array.from({ length: INPUT_ENTRY_MAX + 3 }, (_, i) => [`k${i}`, 'v']))
    const { container } = render(<InputsPreview inputs={json(many)} />)
    expect(container.textContent).toMatch(/batch\.inputsMore/)
  })

  it('renders nothing without inputs', () => {
    const { container } = render(<InputsPreview inputs="" />)
    expect(container.textContent).toBe('')
  })
})
