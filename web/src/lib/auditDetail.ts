import type { TFunction } from 'i18next'

// Turning an audit row's detail into the sentence somebody came to the log to read.
//
// The stored detail is JSON and stays JSON: it is what the filter and any later export work
// against, and a log that recorded prose could not be queried. But the console was printing it
// verbatim, so the answer to "who read what" arrived as
// {"date":"2026-08-10","symbol":"000909","title":"000909 重组舆情分析"} — every field the writer
// happened to store, in the order a serialiser chose, punctuation and all.
//
// So the row is rendered, not dumped. Each action names the fields that make its sentence; whatever
// is left over is appended as key=value, because a field this file has not been taught about must
// still be visible — a renderer that quietly drops what it does not recognise is worse than the
// JSON it replaced.

type Detail = Record<string, unknown>

// Actions whose subject is a report: the line opens with what was read, written or deleted, and
// those fields carry no label — a title does not need to be told it is a title.
const REPORT_ACTIONS = new Set(['report.read', 'report.ingest', 'report.delete'])

// Fields that carry no information when they hold these values, and which are what made the raw
// column unreadable: an unset schedule, a flag that did not fire, the numeric id of something the
// line already names in words, and a row count of one on a single-row run.
function informative(key: string, value: unknown, d: Detail): boolean {
  if (value === '' || value === null || value === undefined) return false
  if (value === false) return false
  if (key === 'target_id' && d.target) return false
  if (key === 'rows' && value === 1) return false
  return true
}

// "000909 重组舆情分析" already opens with the symbol, so prefixing it again would read as two
// different things. Only prefix when the title does not carry it.
function what(symbol: string, title: string): string {
  if (!title) return symbol
  if (!symbol || title.startsWith(symbol)) return title
  return `${symbol} ${title}`
}

export function auditDetail(action: string, raw: string, t: TFunction): string {
  const text = (raw ?? '').trim()
  if (!text) return ''
  let d: Detail
  try {
    const parsed: unknown = JSON.parse(text)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return text
    d = parsed as Detail
  } catch {
    return text // a detail that was never JSON is already prose
  }

  const parts: string[] = []
  const used = new Set<string>()
  // The lead is built from the parts that are actually there rather than from one template with a
  // slot per field: a report with no date would otherwise render its separator around nothing.
  const lead = (field: string, text: string) => {
    used.add(field)
    if (text) parts.push(text)
  }

  if (REPORT_ACTIONS.has(action)) {
    used.add('symbol')
    lead('title', what(String(d.symbol ?? ''), String(d.title ?? '')))
    lead('date', String(d.date ?? ''))
  } else if (action === 'run.submit' && d.target) {
    lead('target', t('audit.d.run', { target: String(d.target) }))
    // The inputs are already key=value pairs; labelling them "inputs=" would put two '=' on one
    // phrase and say nothing the pairs do not.
    lead('inputs', String(d.inputs ?? ''))
  }

  for (const [k, v] of Object.entries(d)) {
    if (used.has(k) || !informative(k, v, d)) continue
    // A field name gets a label where one exists and stays itself where it does not, so a detail
    // written by a newer server still reads.
    const label = t(`audit.f.${k}`, k)
    parts.push(v === true ? label : `${label}=${value(v)}`)
  }
  return parts.join(' · ')
}

// A grant change carries lists — "who could see this before, and who can now" — and an empty one
// is the whole point of half of those lines. String([]) is the empty string, which would print the
// field as if it had no value at all, so an empty list says so with a dash.
function value(v: unknown): string {
  if (Array.isArray(v)) return v.length ? v.map((x) => String(x)).join(', ') : '—'
  if (v && typeof v === 'object') return JSON.stringify(v) // not understood, but not swallowed
  return String(v)
}
