import type { TFunction } from 'i18next'

import { windowSnapshotSummary } from './runSchedule'

// Turning an audit row's detail into something somebody can read.
//
// The stored detail is JSON and stays JSON: it is what the filter and any later export work
// against, and a log that recorded prose could not be queried. But the console was printing it
// verbatim, so the answer to "who read what" arrived as
// {"date":"2026-08-10","symbol":"000909","title":"000909 重组舆情分析"} — every field the writer
// happened to store, in the order a serialiser chose, punctuation and all.
//
// It was then rendered as ONE string of `label=value` joined by "·", which forced every field
// through a translation table (audit.f.*) that had to grow with the server's detail schema. That is
// an OPEN set, and the table had already fallen behind: ~90 keys written, 16 translated, the rest
// showing English field names beside Chinese ones. Adding a language multiplied the treadmill by
// three, and nothing could ever enforce it.
//
// So a detail is rendered as PARTS, and a part is one of four things — which is the distinction a
// flat string cannot make:
//
//   - lead:   the sentence's subject: what was read, run or changed. Hand-written per action, and
//             bounded, so it is worth translating.
//   - phrase: a value from a CLOSED vocabulary the server defines in Go — a refusal reason, an
//             operation, where a run came from. A reader needs the refusal in their own language,
//             not "bad_password" — and the set is small and stable, which is what makes it worth
//             translating at all.
//   - change: a PAIR of fields that are one change, not two facts (before/after, from/to). Read
//             apart they have to be diffed by eye, and the server stores them in a map — so their
//             order is alphabetical and "after" comes first.
//   - field:  everything else. The field name is an IDENTIFIER, not prose. It is shown as-is and
//             visually separated from its value, so it needs no translation at all — a field the
//             server adds tomorrow is readable the day it ships, in every language.
//
// Nothing is ever dropped for being unrecognised. A phrase falls back to its identifier, a pair
// falls back to its two sides, and a value that cannot be summarised is shown as stored — so
// seeing "reason=quantum_break" is how somebody finds out the server's vocabulary grew.
//
// The LIST shows a headline of these parts and the full record shows all of them; headline() draws
// that line and says why.

/** One member of a grant change. */
export interface ChangeMember {
  /**
   * The kind of principal, as an `audit.t.*` key suffix — '' for a value that is not a principal.
   *
   * The store's `u:` / `g:` prefix is the ONLY thing that says which members are accounts and which
   * are units: an OU called "ext-demo" and an account called "ext-demo" are the same string once the
   * prefix is gone. So the kind is read off the prefix and travels with the member rather than being
   * thrown away with it.
   */
  type: string
  name: string
}

/** One piece of a detail line. */
export type DetailPart =
  | { kind: 'lead'; text: string }
  | { kind: 'phrase'; text: string }
  | { kind: 'field'; key: string; value: string }
  | {
      kind: 'change'
      key: string // the identifier naming the change, '' when the action already names it
      from: string
      to: string
      // What appeared and what went, when both sides were lists of comparable things. Empty when
      // there is nothing to diff — sides that are not lists read as "was → is" instead.
      added: ChangeMember[]
      removed: ChangeMember[]
    }

type Detail = Record<string, unknown>

/** What the renderer needs to know that the detail itself does not carry. */
export interface DetailContext {
  /**
   * OU ids resolved to names. The store keeps an OU as a bare number, so a change of primary OU
   * would read "0 → 7" — two numbers that name nothing. The console already resolves them for the
   * actor column; a detail that did not would disagree with the row it sits on.
   */
  ouNames?: Record<string, string>
}

// Actions whose subject is a report: the line opens with what was read, written or deleted, and
// those fields carry no label — a title does not need to be told it is a title.
const REPORT_ACTIONS = new Set([
  'report.read',
  'report.ingest',
  'report.create',
  'report.edit',
  'report.restore',
  'report.delete',
])

// Fields whose value comes from a closed vocabulary the server writes. Each value has a phrase
// under audit.v.<field>.<value>; a value this build has not been taught falls back to the
// identifier rendering rather than disappearing.
//
// `field` is here because it is not a field like the others: its VALUE names the attribute that
// changed ("role", "primary_group"), which is a closed set the server defines. A reader needs the
// unit's own name, not "primary_group".
const ENUM_FIELDS = new Set([
  'reason',
  'method',
  'factor',
  'op',
  'surface',
  'via',
  'kind',
  'audience',
  'field',
])

// Fields whose value is an OU ID rather than a name — the same problem as a principal, without the
// encoding that makes one recognisable.
const OU_ID_FIELDS = new Set(['parent', 'group_id', 'primary_group'])

// Pairs whose two sides are values from a closed vocabulary rather than free text. The pair's own
// label names the vocabulary, so the lookup is the same one an enum field gets, keyed by the label.
//
// A report version's visibility is the only one so far. Entra answers this case by giving the
// property a display name and leaving the value as stored; that works there because its audit log is
// English-only, and "owner" is a word its readers have. Here the same reasoning that translates a
// refusal reason translates this one.
const VOCAB_PAIRS = new Set(['visibility'])

// Field pairs that are one change. `label` names the change when the pair's own field names do not
// — "before"/"after" say nothing about WHAT changed, so they take the label from a sibling `field`
// when the writer supplied one, and otherwise go unlabelled and let the action carry it.
const PAIRS: Array<{ before: string; after: string; label: string }> = [
  { before: 'before', after: 'after', label: '' },
  { before: 'from', after: 'to', label: '' },
  { before: 'visibility_before', after: 'visibility_after', label: 'visibility' },
  { before: 'base_url_from', after: 'base_url_to', label: 'base_url' },
]

// The field whose VALUE names what a from/to pair changed ("role", "primary_group"). Consumed by
// the pair rather than printed beside it, so the change reads "primary_group 3 → 7".
const PAIR_NAME_FIELD = 'field'

// Between the members of one list — an enumeration comma rather than ", ", because the console is
// read in Chinese first and that is what a Chinese list is separated by. The value itself is data;
// only the punctuation between values is a display choice, and one character is not worth a locale.
export const listSeparator = '、'

// Fields that carry no information when they hold these values, and which are what made the raw
// column unreadable: an unset schedule, the numeric id of something the line already names in words,
// and a row count of one on a single-row run.
//
// A false boolean is NOT in this list. Whether its false says anything depends on the vocabulary —
// "enabled: false" is the sentence "this is now off" — so that call is made where the lookup is.
function informative(key: string, value: unknown, d: Detail): boolean {
  if (value === '' || value === null || value === undefined) return false
  if (key === 'target_id' && d.target) return false
  if (key === 'rows' && value === 1) return false
  return true
}

// Ops that name the MECHANISM rather than the change. "toggle" says a flag was flipped; the flag it
// flipped is the sentence, and printing both repeats the change in a vaguer word — which is how a
// row that turned an announcement off came to read "开关" and nothing else.
const SILENT_OPS = new Set(['toggle'])

// "000909 重组舆情分析" already opens with the symbol, so prefixing it again would read as two
// different things. Only prefix when the title does not carry it.
function what(symbol: string, title: string): string {
  if (!title) return symbol
  if (!symbol || title.startsWith(symbol)) return title
  return `${symbol} ${title}`
}

export function auditDetail(action: string, raw: string, t: TFunction, ctx?: DetailContext): DetailPart[] {
  const text = (raw ?? '').trim()
  if (!text) return []
  let d: Detail
  try {
    const parsed: unknown = JSON.parse(text)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return [{ kind: 'lead', text }]
    d = parsed as Detail
  } catch {
    return [{ kind: 'lead', text }] // a detail that was never JSON is already prose
  }

  const parts: DetailPart[] = []
  const used = new Set<string>()
  // The lead is built from the parts that are actually there rather than from one template with a
  // slot per field: a report with no date would otherwise render its separator around nothing.
  const lead = (field: string, s: string) => {
    used.add(field)
    if (s) parts.push({ kind: 'lead', text: s })
  }
  // An empty phrase means the vocabulary does not have this value, and the caller falls through to
  // the identifier rendering.
  const vocab = (field: string, v: unknown): boolean => {
    const s = t(`audit.v.${field}.${String(v)}`, '')
    if (!s) return false
    parts.push({ kind: 'phrase', text: s })
    return true
  }

  if (REPORT_ACTIONS.has(action)) {
    used.add('symbol')
    lead('title', what(String(d.symbol ?? ''), String(d.title ?? '')))
    lead('date', String(d.date ?? ''))
  } else if (action === 'run.submit' && d.target) {
    lead('target', t('audit.d.run', { target: String(d.target) }))
    // The inputs stay where everything else the run carried stays — see headline() below. A run's
    // parameters are what a reader almost never wants on the line, and a submission carries enough
    // of them (a query, a window, a target's own fields) to bury the one fact the row is about.
  }

  // Pairs come first: they are the substance of the change, and whatever incidental identifiers the
  // writer stored alongside them are what a reader skims past.
  for (const pair of PAIRS) {
    // Both sides or neither. A writer that recorded only the new value did not record a change, and
    // rendering "— → admin" would invent a past it never wrote down.
    if (!(pair.before in d) || !(pair.after in d)) continue
    const before = d[pair.before]
    const after = d[pair.after]
    if (!informative(pair.before, before, d) && !informative(pair.after, after, d)) continue
    used.add(pair.before)
    used.add(pair.after)
    const named = typeof d[PAIR_NAME_FIELD] === 'string' ? String(d[PAIR_NAME_FIELD]) : ''
    if (named) used.add(PAIR_NAME_FIELD)
    // A sibling `field` names what changed ("primary_group"). Its value is from a closed vocabulary
    // too, so it gets a phrase where one exists and stays itself where it does not.
    const label = pair.label || (named ? t(`audit.v.${PAIR_NAME_FIELD}.${named}`, named) : '')
    // An OU pair is two ids; a grant pair is two lists of principals. Either way the stored value is
    // an encoding rather than something to read — and the diff has to run on what is SHOWN, or the
    // two would disagree about what changed.
    const ouSide = OU_ID_FIELDS.has(named)
    const vocabSide = VOCAB_PAIRS.has(label)
    // One side of the change. A side that says nothing reads as "—" rather than as an empty gap —
    // a version being published for the first time has no previous visibility, and "visibility  →
    // all" is not a sentence.
    const side = (field: string, v: unknown): string => {
      if (!informative(field, v, d)) return '—'
      if (ouSide) return ouLabel(v, ctx)
      if (vocabSide) {
        const s = t(`audit.v.${label}.${String(v)}`, '')
        if (s) return s
      }
      return render(v, t, ctx)
    }
    const from = side(pair.before, before)
    const to = side(pair.after, after)
    const eb = ouSide ? null : members(before, ctx)
    const ea = ouSide ? null : members(after, ctx)
    // A diff needs comparable members on BOTH sides. Sides that are not lists still form a change —
    // it just has no membership to report, and reads as "was → is".
    const had = new Set((eb ?? []).map(identity))
    const has = new Set((ea ?? []).map(identity))
    const added = (ea ?? []).filter((m) => !had.has(identity(m)))
    const removed = (eb ?? []).filter((m) => !has.has(identity(m)))
    // A pair whose two sides are equal says nothing, and rendering it would ASSERT something that
    // did not happen: a version save records its audience alongside its grants, so a line where only
    // the grants moved would otherwise carry the audience as if it were news.
    if (from === to && added.length === 0 && removed.length === 0) continue
    parts.push({ kind: 'change', key: label, from, to, added, removed })
  }

  for (const [k, v] of Object.entries(d)) {
    if (used.has(k) || !informative(k, v, d)) continue
    if (k === 'op' && SILENT_OPS.has(String(v))) {
      used.add(k)
      continue
    }
    // A boolean is a two-valued vocabulary, and WHICH kind of flag it is decides how it reads.
    //
    // Taught on BOTH sides, it is a STATE of the object, and it keeps its key: "enabled 停用",
    // because 停用 alone does not say which of an object's several flags was turned off.
    //
    // Taught on its true side only, it is a STATEMENT about what somebody asked for, and the words
    // stand on their own — "加急未生效（无票）" needs no "downgraded" in front of it. Its false is the
    // default nobody asked about, so it goes unprinted.
    //
    // Taught on neither, the value states itself and a false is dropped for that same reason.
    if (typeof v === 'boolean') {
      const word = t(`audit.v.${k}.${v}`, '')
      if (word) {
        // Only a state is taught both sides, so a false that has a word is always a state.
        const isState = v === false || !!t(`audit.v.${k}.false`, '')
        parts.push(isState ? { kind: 'field', key: k, value: word } : { kind: 'phrase', text: word })
        continue
      }
      if (!v) continue
      parts.push({ kind: 'field', key: k, value: 'true' })
      continue
    }
    if (ENUM_FIELDS.has(k) && vocab(k, v)) continue
    parts.push({ kind: 'field', key: k, value: OU_ID_FIELDS.has(k) ? ouLabel(v, ctx) : render(v, t, ctx) })
  }
  return parts
}

// ouLabel renders an OU id as the unit it names. 0 is the store's "no OU", which is a real state —
// an account moved out of every unit, an OU reparented to the root.
function ouLabel(v: unknown, ctx?: DetailContext): string {
  const id = String(v ?? '')
  if (id === '' || id === '0') return '—'
  return ctx?.ouNames?.[id] ?? `OU ${id}`
}

// principalMember reads the store's `g:<ou id>` / `u:<name>` encoding the way a person would say it,
// and keeps the KIND — the prefix is the only thing that carries it, and an OU called "ext-demo" and
// an account called "ext-demo" are the same string once it is gone. A string that is not in the
// encoding is left alone; this is the format of one column, not a rule about strings.
function principalMember(s: string, ctx?: DetailContext): ChangeMember {
  if (s.startsWith('u:')) return { type: 'user', name: s.slice(2) }
  if (s.startsWith('g:')) return { type: 'group', name: ouLabel(s.slice(2), ctx) }
  return { type: '', name: s }
}

// The members of a list value, or null if it is not a list. A list of structures is rendered the way
// any other structured value is — a grant can carry one — and it reports no kind.
function members(v: unknown, ctx?: DetailContext): ChangeMember[] | null {
  if (!Array.isArray(v)) return null
  return v.map((x) =>
    x && typeof x === 'object' ? { type: '', name: compactSummary(x) } : principalMember(String(x), ctx),
  )
}

// identity is what a diff compares on. A name is NOT unique on its own — a unit and an account can
// share one — so the kind is part of it.
const identity = (m: ChangeMember) => `${m.type}:${m.name}`

// headline narrows a detail to what the LIST shows.
//
// A row that says WHAT happened — "Ran Deep Research", "read a report", "carol gained
// access" — shows just that. Everything else a submission carried (its inputs, its window, which
// surface it came from, whether it asked for urgent, ...) is the record, and the full-record button
// is what it is for: a page of rows holding every parameter of every submission reads as one wall
// of key=value and buries the one fact each row is about.
//
// A row with nothing but phrases has no such headline, and keeps them — "wrong password" IS the
// fact a refused sign-in is about, and hiding it would leave a row saying only that something was
// refused.
export function headline(parts: DetailPart[]): DetailPart[] {
  const head = parts.filter((p) => p.kind === 'lead' || p.kind === 'change')
  return head.length ? head : parts
}

// render turns one stored value into the text shown on the line.
function render(v: unknown, t: TFunction, ctx?: DetailContext): string {
  if (Array.isArray(v)) {
    const list = members(v, ctx)
    // An empty list is the point of half these lines: "nobody could see it before".
    return list && list.length ? list.map((m) => m.name).join(listSeparator) : '—'
  }
  if (v && typeof v === 'object') return compactSummary(v)
  if (typeof v === 'string') {
    // A stored window rule reads as the window; a serialised object reads as its shape. Neither is
    // dumped, and a string that is neither is already prose.
    return windowSnapshotSummary(v, t) ?? jsonSummary(v) ?? v
  }
  return String(v)
}

// A value that IS JSON — a serialised parameter, a nested config — reads as its shape rather than
// as its bytes. Only the top level survives and anything nested becomes the elision marker, which
// is the same rule the server applies when it summarises an oversized submitted input, so a stored
// summary and a live value read alike.
function jsonSummary(text: string): string | null {
  const t = text.trim()
  if (t.length < 2 || (t[0] !== '{' && t[0] !== '[')) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(t)
  } catch {
    return null // truncated or not JSON at all — shown as stored rather than guessed at
  }
  if (!parsed || typeof parsed !== 'object') return null
  return compactSummary(parsed)
}

const ELISION = '…'

function compactSummary(v: unknown): string {
  if (Array.isArray(v)) {
    if (v.some((x) => x !== null && typeof x === 'object')) return ELISION
    return v.length ? `[${v.map((x) => String(x)).join(', ')}]` : '[]'
  }
  if (v && typeof v === 'object') {
    const members = Object.entries(v as Record<string, unknown>).filter(
      ([, x]) => x !== null && x !== undefined && x !== '',
    )
    if (members.length === 0) return ELISION
    return `{${members.map(([k, x]) => `${k}: ${x !== null && typeof x === 'object' ? ELISION : String(x)}`).join(', ')}}`
  }
  return String(v)
}
