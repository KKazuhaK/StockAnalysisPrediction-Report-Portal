import { describe, expect, it } from 'vitest'

import { auditDetail, headline, type ChangeMember, type DetailPart } from './auditDetail'

// The values this build has been taught — a closed vocabulary the server defines in Go. Anything
// outside it must still render, as an identifier, which is the case the tests below pin down.
const VOCAB = new Set([
  'audit.v.reason.bad_password',
  'audit.v.op.retry',
  'audit.v.surface.batch',
  'audit.v.audience.all',
  'audit.v.downgraded.true',
  'audit.v.notify.true',
  'audit.v.field.primary_group',
  'audit.v.field.role',
  'audit.v.visibility.owner',
  'audit.v.visibility.all',
])

// Stands in for the console's t(): echoes the key and shows what was interpolated. The second
// argument is i18next's default-value form — a STRING fallback for a key the bundle does not have,
// distinct from an object argument. Modelling that distinction is what lets a test tell "this value
// is in the server's vocabulary" from "this build has not been taught it yet", which is exactly the
// difference the enum rendering turns on.
const t = ((k: string, o?: unknown) => {
  if (o && typeof o === 'object')
    return `${k}(${Object.entries(o as Record<string, unknown>).map(([a, b]) => `${a}=${b}`).join(',')})`
  if (typeof o === 'string') return VOCAB.has(k) ? k : o
  return k
}) as never

const lead = (text: string): DetailPart => ({ kind: 'lead', text })
const phrase = (text: string): DetailPart => ({ kind: 'phrase', text })
const field = (key: string, value: string): DetailPart => ({ kind: 'field', key, value })
// A member is a name plus the KIND the store's `u:`/`g:` prefix carried. A bare string is the
// kindless case, which is what a value that was never a principal looks like.
const member = (name: string, type = ''): ChangeMember => ({ type, name })
const change = (
  key: string,
  from: string,
  to: string,
  added: Array<string | ChangeMember>,
  removed: Array<string | ChangeMember>,
): DetailPart => ({
  kind: 'change',
  key,
  from,
  to,
  added: added.map((m) => (typeof m === 'string' ? member(m) : m)),
  removed: removed.map((m) => (typeof m === 'string' ? member(m) : m)),
})

describe('auditDetail', () => {
  it('says what was read, not which fields the row happens to have', () => {
    const out = auditDetail('report.read', '{"date":"2026-08-10","symbol":"000909","title":"000909 重组舆情分析"}', t)
    // The title already opens with the symbol — repeating it would read as two different things.
    expect(out).toEqual([lead('000909 重组舆情分析'), lead('2026-08-10')])
  })

  it('keeps the symbol when the title does not carry it', () => {
    const out = auditDetail('report.read', '{"date":"2026-08-10","symbol":"600519","title":"内部纪要"}', t)
    expect(out).toEqual([lead('600519 内部纪要'), lead('2026-08-10')])
  })

  // A report a person wrote, edited or restored is the same subject as one they read: the answer to
  // "what did this line do" is the report itself, so those actions lead the same way.
  it('leads a hand-written report the same way as a read one', () => {
    const out = auditDetail('report.create', '{"symbol":"600519","title":"内部纪要","date":"2026-08-10","audience":"all"}', t)
    expect(out).toEqual([lead('600519 内部纪要'), lead('2026-08-10'), phrase('audit.v.audience.all')])
  })

  it('leads a run with what was run, and keeps its parameters as identifiers', () => {
    const raw = '{"target":"研报分析","inputs":"symbol=603587","rows":1,"priority":"30","retries":1,"target_id":5}'
    const out = auditDetail('run.submit', raw, t)
    expect(out[0]).toEqual(lead('audit.d.run(target=研报分析)'))
    // A run's parameters are the record, not the headline — see headline() below. They still carry
    // every value that was submitted, and they never vanish.
    expect(out).toContainEqual(field('inputs', 'symbol=603587'))
    expect(out).toContainEqual(field('priority', '30'))
    expect(out).toContainEqual(field('retries', '1'))
  })

  it('drops the fields that say nothing', () => {
    const raw = '{"target":"研报分析","run_at":"","downgraded":false,"target_id":5,"rows":1}'
    const out = auditDetail('run.submit', raw, t)
    // Empty strings, false flags, and an id whose name is already on the line are noise: they are
    // what made the old raw-JSON column unreadable.
    expect(out.map((p) => ('key' in p ? p.key : p.text))).toEqual(['audit.d.run(target=研报分析)'])
  })

  it('keeps the row count when a batch has more than one', () => {
    expect(auditDetail('run.submit', '{"target":"x","rows":42}', t)).toContainEqual(field('rows', '42'))
  })

  // The values the server defines in Go are a closed vocabulary, and they are the only ones worth
  // translating: a reader needs the refusal in words, not "bad_password". The name disappears into the
  // phrase, because the phrase is written to stand on its own.
  it('renders a value from a closed vocabulary as a phrase, without its field name', () => {
    expect(auditDetail('auth.login_failed', '{"reason":"bad_password"}', t)).toEqual([
      phrase('audit.v.reason.bad_password'),
    ])
    expect(auditDetail('run.change', '{"owner":"alice","op":"retry"}', t)).toEqual([
      field('owner', 'alice'),
      phrase('audit.v.op.retry'),
    ])
  })

  // ...but a value this build has not been taught is NOT dropped. It falls back to the identifier
  // rendering, so a detail written by a newer server still reads — seeing "reason=quantum_break" is
  // how somebody finds out the vocabulary grew.
  it('falls back to an identifier for a vocabulary value it has not been taught', () => {
    expect(auditDetail('auth.login_failed', '{"reason":"quantum_break"}', t)).toEqual([
      field('reason', 'quantum_break'),
    ])
  })

  it('turns a true flag into its sentence, and shows the flag when there is no sentence', () => {
    expect(auditDetail('run.submit', '{"target":"x","downgraded":true}', t)).toContainEqual(
      phrase('audit.v.downgraded.true'),
    )
    expect(auditDetail('run.submit', '{"target":"x","notify":true}', t)).toContainEqual(phrase('audit.v.notify.true'))
    // There is nothing to say about "weird=true", but the fact must not vanish either.
    expect(auditDetail('run.submit', '{"target":"x","weird":true}', t)).toContainEqual(field('weird', 'true'))
  })

  it('shows every other field as an identifier beside its value', () => {
    expect(auditDetail('token.create', '{"name":"dify","expires":"2026-12-01"}', t)).toEqual([
      field('name', 'dify'),
      field('expires', '2026-12-01'),
    ])
  })

  it('shows both sides of a grant change, including the empty one', () => {
    // "Nobody could see it before" is the whole point of half these lines, and String([]) is the
    // empty string — which would print as if the field had no value at all. Here it is the removed
    // set that is empty, which is what "everybody on this list gained access" looks like.
    expect(auditDetail('grant.change', '{"before":[],"after":["u:client@corp.example"]}', t)).toEqual([
      change('', '—', 'client@corp.example', [member('client@corp.example', 'user')], []),
    ])
  })

  // A grant is written in the store's `g:<ou id>` / `u:<name>` encoding. Neither half is something
  // to read: the prefix is wire format and the OU half is a number — "g:7 → g:9" is a change nobody
  // can act on. The ids are resolved the same way the actor column resolves one.
  it('says a grant in names rather than in the encoding the store keeps', () => {
    const raw = '{"before":["u:alice","g:7"],"after":["u:alice","g:9"]}'
    expect(auditDetail('grant.change', raw, t, { ouNames: { '7': '客户A', '9': '客户B' } })).toEqual([
      change('', 'alice、客户A', 'alice、客户B', [member('客户B', 'group')], [member('客户A', 'group')]),
    ])
    // An id with no name is still printed — a grant that silently lost a member would be worse
    // than an ugly one.
    expect(auditDetail('grant.change', '{"before":["g:7"],"after":[]}', t)).toEqual([
      change('', 'OU 7', '—', [], [member('OU 7', 'group')]),
    ])
  })

  // Read as two fields this line has to be diffed by eye, and the server stores them in a map — so
  // they arrive alphabetically and "after" comes first. A change says which members appeared and
  // which went, and the order it was written in stops mattering.
  it('reads a pair of fields as one change rather than two facts', () => {
    expect(auditDetail('grant.change', '{"before":["u:a","u:b"],"after":["u:b","u:c"]}', t)).toEqual([
      change('', 'a、b', 'b、c', [member('c', 'user')], [member('a', 'user')]),
    ])
    // A scalar pair has no membership to diff, so it reads as "was → is" — and it is ordered.
    expect(auditDetail('x', '{"from":3,"to":7}', t)).toEqual([change('', '3', '7', [], [])])
  })

  it('lets a sibling field name the change, so the pair does not need a label of its own', () => {
    // Its value names what changed, and that is a closed vocabulary too — "primary_group" is not
    // something a reader of a Chinese console can act on. The ids are OU ids and read as units.
    const raw = '{"field":"primary_group","from":0,"to":7}'
    expect(auditDetail('user.change', raw, t, { ouNames: { '7': '客户A' } })).toEqual([
      change('audit.v.field.primary_group', '—', '客户A', [], []),
    ])
  })

  // A report version's visibility is a value from a closed vocabulary, so it reads as the choice
  // somebody made rather than as the token the store keeps. A version being published for the first
  // time has no previous visibility at all, which is a dash rather than nothing.
  it('reads a vocabulary-valued pair as choices, and an absent side as a dash', () => {
    const raw =
      '{"before":[],"after":["u:a"],"visibility_before":"owner","visibility_after":"all"}'
    expect(auditDetail('grant.change', raw, t)).toEqual([
      change('', '—', 'a', [member('a', 'user')], []),
      change('visibility', 'audit.v.visibility.owner', 'audit.v.visibility.all', [], []),
    ])
    expect(auditDetail('grant.change', '{"visibility_before":"","visibility_after":"all"}', t)).toEqual([
      change('visibility', '—', 'audit.v.visibility.all', [], []),
    ])
    // A value this build does not know is shown as stored rather than swallowed.
    expect(auditDetail('grant.change', '{"visibility_before":"owner","visibility_after":"wat"}', t)).toEqual([
      change('visibility', 'audit.v.visibility.owner', 'wat', [], []),
    ])
  })

  // A pair whose two sides are equal says nothing, and printing it would ASSERT a change that did
  // not happen. A version save records its audience alongside its grants, so a line where only the
  // grants moved would otherwise carry "visibility grant" as if that were news.
  it('drops a pair that did not move', () => {
    const raw = '{"before":["u:a"],"after":["u:a","u:b"],"visibility_before":"grant","visibility_after":"grant"}'
    expect(auditDetail('grant.change', raw, t)).toEqual([change('', 'a', 'a、b', [member('b', 'user')], [])])
    // ...and a pair that was already empty on both sides goes with it.
    expect(auditDetail('grant.change', '{"before":[],"after":[]}', t)).toEqual([])
  })

  it('keeps a one-sided from/to as an ordinary field, rather than inventing a past', () => {
    // The writer recorded the new role and not the old one. "— → admin" would read as a change that
    // was never written down. The `field` still names it, as a phrase.
    expect(auditDetail('user.change', '{"field":"role","to":"admin"}', t)).toEqual([
      phrase('audit.v.field.role'),
      field('to', 'admin'),
    ])
  })

  // A job's window rule is copied into the row's detail as the resolver's JSON. Described the way
  // the picker describes it, the same window reads the same in both places.
  it('describes a stored window rule instead of printing it', () => {
    const rule = JSON.stringify({
      freq: 'daily',
      intervals: [{ start: { time: '09:00' }, stop: { time: '12:00' } }],
      on_overrun: 'next',
    })
    expect(auditDetail('run.submit', JSON.stringify({ target: 'x', preset: rule }), t)).toEqual([
      lead('audit.d.run(target=x)'),
      field('preset', 'run.freq.daily 09:00–12:00'),
    ])
  })

  it('reads any other JSON value as its shape, not as its bytes', () => {
    expect(auditDetail('x', '{"cfg":"{\\"a\\":1,\\"b\\":{\\"c\\":2}}"}', t)).toEqual([field('cfg', '{a: 1, b: …}')])
    // A truncated or malformed one is shown as stored: guessing at half an object is worse than
    // showing it, and the cell is clamped anyway.
    expect(auditDetail('x', '{"cfg":"{\\"a\\":1"}', t)).toEqual([field('cfg', '{"a":1')])
  })

  it('does not swallow a nested object it was never taught to read', () => {
    expect(auditDetail('x', '{"changes":{"role":"admin"}}', t)).toEqual([field('changes', '{role: admin}')])
  })

  it('falls back to the raw text for a detail that was never JSON', () => {
    expect(auditDetail('whatever', 'not json at all', t)).toEqual([lead('not json at all')])
    expect(auditDetail('whatever', '', t)).toEqual([])
  })
})

describe('headline', () => {
  // What the LIST shows. A row that says what happened shows just that: everything else a
  // submission carried — its inputs, its window, which surface it came from — is the record, and
  // the full-record button is what it is for.
  it('shows the sentence and the change, and nothing that hangs off them', () => {
    expect(
      headline([lead('Ran x'), field('priority', '30'), phrase('audit.v.surface.run'), change('', 'a', 'b', [], [])]),
    ).toEqual([lead('Ran x'), change('', 'a', 'b', [], [])])
  })

  // ...but a row with no such headline keeps everything it has. "wrong password" IS the fact a
  // refused sign-in is about, and hiding it would leave a row saying only that something was
  // refused.
  it('keeps a phrase-only row whole', () => {
    const parts = [phrase('audit.v.reason.bad_password'), field('provider', 'sso')]
    expect(headline(parts)).toEqual(parts)
  })

  it('has nothing to show for a row with no detail', () => {
    expect(headline([])).toEqual([])
  })
})
