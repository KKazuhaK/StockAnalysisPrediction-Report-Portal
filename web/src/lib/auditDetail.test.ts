import { describe, expect, it } from 'vitest'

import { auditDetail } from './auditDetail'

// Stands in for the console's t(): echoes the key and shows what was interpolated, so a test can
// see which sentence was chosen without depending on any one language's wording. A STRING second
// argument is i18next's default-value form, not interpolation — modelled here because the field
// labels use it.
const t = ((k: string, o?: unknown) =>
  o && typeof o === 'object'
    ? `${k}(${Object.entries(o as Record<string, unknown>).map(([a, b]) => `${a}=${b}`).join(',')})`
    : k) as never

describe('auditDetail', () => {
  it('says what was read, not which fields the row happens to have', () => {
    const out = auditDetail('report.read', '{"date":"2026-08-10","symbol":"000909","title":"000909 重组舆情分析"}', t)
    // The title already opens with the symbol — repeating it would read as two different things.
    expect(out).toBe('000909 重组舆情分析 · 2026-08-10')
  })

  it('keeps the symbol when the title does not carry it', () => {
    const out = auditDetail('report.read', '{"date":"2026-08-10","symbol":"600519","title":"内部纪要"}', t)
    expect(out).toBe('600519 内部纪要 · 2026-08-10')
  })

  it('leads a run with what was run and what went in', () => {
    const raw = '{"target":"研报分析","inputs":"symbol=603587","rows":1,"priority":"30","retries":1,"notify":true,"downgraded":false,"run_at":"","target_id":5}'
    const out = auditDetail('run.submit', raw, t)
    expect(out.startsWith('audit.d.run(target=研报分析) · symbol=603587')).toBe(true)
    // What is left over still shows — an unrecognised field must never vanish silently.
    expect(out).toContain('audit.f.priority=30')
    expect(out).toContain('audit.f.retries=1')
    // A true flag reads as the flag itself; there is nothing to say about "notify=true".
    expect(out).toContain('audit.f.notify')
    expect(out).not.toContain('notify=true')
  })

  it('drops the fields that say nothing', () => {
    const raw = '{"target":"研报分析","run_at":"","downgraded":false,"target_id":5,"rows":1}'
    const out = auditDetail('run.submit', raw, t)
    // Empty strings, false flags, and an id whose name is already on the line are noise: they are
    // what made the old raw-JSON column unreadable.
    expect(out).not.toContain('run_at')
    expect(out).not.toContain('downgraded')
    expect(out).not.toContain('target_id')
    expect(out).not.toContain('rows') // a single-row run says nothing by being single
  })

  it('keeps the row count when a batch has more than one', () => {
    expect(auditDetail('run.submit', '{"target":"x","rows":42}', t)).toContain('audit.f.rows=42')
  })

  it('shows both sides of a grant change, including the empty one', () => {
    // "Nobody could see it before" is the whole point of half these lines, and String([]) is the
    // empty string — which would print as if the field had no value at all.
    const out = auditDetail('grant.change', '{"before":[],"after":["u:client@corp.example"]}', t)
    expect(out).toBe('audit.f.before=— · audit.f.after=u:client@corp.example')
  })

  it('does not swallow a nested object it was never taught to read', () => {
    expect(auditDetail('x', '{"changes":{"role":"admin"}}', t)).toBe('audit.f.changes={"role":"admin"}')
  })

  it('falls back to key=value for an action it has no sentence for, and to the raw text for non-JSON', () => {
    expect(auditDetail('token.create', '{"name":"dify","scope":"query"}', t)).toBe('audit.f.name=dify · audit.f.scope=query')
    expect(auditDetail('whatever', 'not json at all', t)).toBe('not json at all')
    expect(auditDetail('whatever', '', t)).toBe('')
  })
})
