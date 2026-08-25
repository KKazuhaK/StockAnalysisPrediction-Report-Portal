import type { Dayjs } from 'dayjs'
import type { RunFreq, RunMode, RunPreset, RunPresetAnchor, RunPresetInterval, RunPresetsResp } from '../api/types'

// The run-time + priority choice shared by the single-run modal and the batch console
// (docs/adr/0014-idle-lane-and-preset-windows.md). mode picks WHEN (now / a preset low-peak
// window / an explicit 定时 time); idle and urgent are the mutually-exclusive priority lanes
// (idle only meaningful in "now" mode). Kept as one small serializable shape so both consumers
// build the /api/admin/batch/jobs body the same way.
export interface RunSchedule {
  mode: RunMode
  runAt: Dayjs | null
  presetId?: number
  idle: boolean
  urgent: boolean
}

export const emptySchedule: RunSchedule = { mode: 'now', runAt: null, idle: false, urgent: false }

// schedulePayload maps the schedule to the create-job fields. priority is 'urgent' | 'idle' | ''
// (urgent wins if both are somehow set; the caller may substitute an admin base number for the ''
// case). preset_id 0 / run_at '' mean "not that mode".
export function schedulePayload(s: RunSchedule): { priority: string; run_at: string; preset_id: number } {
  // Urgent wins; idle is only meaningful in immediate mode (a preset/定时 run already fixes "when"),
  // so a stale idle flag from an admin default can't leak into a scheduled submit.
  const priority = s.urgent ? 'urgent' : s.idle && s.mode === 'now' ? 'idle' : ''
  const run_at = s.mode === 'scheduled' && s.runAt ? s.runAt.format('YYYY-MM-DD HH:mm:ss') : ''
  const preset_id = s.mode === 'preset' ? s.presetId ?? 0 : 0
  return { priority, run_at, preset_id }
}

// ---------------------------------------------------------------------------
// Admin-set run-form defaults
// ---------------------------------------------------------------------------
// What the run forms open on before the user touches anything (docs/adr/0014 §4, edited on
// Manage → 运行默认). Every field is a suggestion the user can change, and every one of them may be
// "none" — an unconfigured portal sends 0 / false throughout, which is exactly the empty form the
// run dialog has always opened with.
export interface RunFormDefaults {
  targetId: number // pre-selected workflow; 0 = none
  mode: RunMode
  presetId?: number // pre-picked preset window; undefined = none
  idle: boolean
  retries: number
  notify: boolean
  // Not a default the user overrides — it decides what the form *shows*: whether a preset window's
  // whole rule is printed next to its name, or left to the info button beside the picker. It rides
  // the same response, so it is read in the same place.
  showPresetRule: boolean
}

export const noRunDefaults: RunFormDefaults = {
  targetId: 0,
  mode: 'now',
  idle: false,
  retries: 0,
  notify: false,
  showPresetRule: false,
}

// readRunDefaults resolves the wire response into what a form can actually open on, dropping a
// default the form could not offer: "preset" mode with no enabled window (the preset button is
// hidden then, so nothing could select it) falls back to immediate, and a default window that is
// disabled or gone is no default at all. Shared so the single-run dialog and the batch console
// cannot disagree about what the admin configured.
export function readRunDefaults(r: RunPresetsResp): RunFormDefaults {
  const enabled = (r.presets || []).filter((p) => p.enabled)
  const mode = r.default_mode === 'preset' && enabled.length === 0 ? 'now' : r.default_mode || 'now'
  return {
    targetId: r.default_target_id || 0,
    mode,
    presetId: enabled.some((p) => p.id === r.default_preset_id) ? r.default_preset_id : undefined,
    idle: !!r.default_idle,
    retries: r.default_retries ?? 0,
    notify: !!r.default_notify,
    showPresetRule: !!r.show_preset_rule,
  }
}

// scheduleFromDefaults seeds a fresh schedule from the defaults — used both on open and on the
// reset after a submit, so a submitted form returns to the state it opened in rather than to blank.
export function scheduleFromDefaults(d: RunFormDefaults): RunSchedule {
  return { ...emptySchedule, mode: d.mode, presetId: d.presetId, idle: d.idle }
}

// scheduleError returns an i18n key when the schedule is incomplete (a mode that needs a value has
// none), else '' — the consumers show it and block submit.
export function scheduleError(s: RunSchedule): '' | 'run.pickTime' | 'run.pickPreset' {
  if (s.mode === 'scheduled' && !s.runAt) return 'run.pickTime'
  if (s.mode === 'preset' && !s.presetId) return 'run.pickPreset'
  return ''
}

type TFunc = (key: string, opts?: Record<string, unknown>) => string

// ---------------------------------------------------------------------------
// Weekly windows, as the editor works with them
// ---------------------------------------------------------------------------
// A weekly preset stores one interval per occurrence, each carrying its own start and stop
// weekday — so "Mon, Wed and Fri, 09:00–12:00" is three near-identical intervals the admin had to
// type three times. The editor works in *rows* instead: one time range applied to a set of
// weekdays, expanded to the stored intervals on save and grouped back on load. Nothing changes on
// the wire, in the table, or in the resolver — a row is only how the form spells a group of
// intervals that share a time range (docs/adr/0014 §5, 2026-08-24 amendment).
export interface WeeklyRow {
  days: number[] // 0=Sun..6=Sat, the stored convention
  start: string // "HH:mm"
  stop: string // "HH:mm"
}

// WEEK_ORDER lists the weekdays the way a week is read rather than the way they are numbered, so
// a picker and a summary both run Monday → Sunday while the values stay 0=Sun..6=Sat.
export const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]

const byWeekOrder = (a: number, b: number) => WEEK_ORDER.indexOf(a) - WEEK_ORDER.indexOf(b)

// minutesOfDay turns "HH:mm" into minutes since midnight; anything unparseable reads as 00:00,
// which is how the resolver's own parse failure degrades.
function minutesOfDay(s: string): number {
  const [h, m] = (s || '').split(':')
  const hh = Number(h)
  const mm = Number(m)
  return Number.isFinite(hh) && Number.isFinite(mm) ? hh * 60 + mm : 0
}

// wrapsMidnight reports whether a window runs into the following day — a stop at or before its
// start, which the backend resolver rolls forward by one period. Equal times are a full 24 hours.
export function wrapsMidnight(start: string, stop: string): boolean {
  return minutesOfDay(stop) <= minutesOfDay(start)
}

// parseWeeklyRows groups stored weekly intervals back into editor rows: the windows sharing a time
// range become one row listing their weekdays. null means the preset says something a row cannot —
// a window spanning whole days ("Mon 09:00 → Wed 18:00") — and the editor keeps the per-edge anchor
// fields for it rather than quietly rewriting the rule.
export function parseWeeklyRows(intervals: RunPresetInterval[]): WeeklyRow[] | null {
  const rows: WeeklyRow[] = []
  const byRange = new Map<string, WeeklyRow>()
  for (const iv of intervals) {
    const day = iv.start.weekday ?? 0
    const start = iv.start.time || '00:00'
    const stop = iv.stop.time || '00:00'
    const expected = wrapsMidnight(start, stop) ? (day + 1) % 7 : day
    if ((iv.stop.weekday ?? 0) !== expected) return null
    const key = `${start}-${stop}`
    const row = byRange.get(key)
    if (row) {
      if (!row.days.includes(day)) row.days.push(day)
      continue
    }
    const fresh: WeeklyRow = { days: [day], start, stop }
    byRange.set(key, fresh)
    rows.push(fresh)
  }
  for (const r of rows) r.days.sort(byWeekOrder)
  return rows
}

// weeklyIntervals expands editor rows back into the stored one-interval-per-weekday form. A window
// whose stop is at or before its start lands on the following weekday, so 22:00–02:00 is the four
// hours it reads as and not the six days a same-weekday stop would resolve to. A row with no
// weekday picked contributes nothing.
export function weeklyIntervals(rows: WeeklyRow[]): RunPresetInterval[] {
  const out: RunPresetInterval[] = []
  for (const r of rows) {
    const wrap = wrapsMidnight(r.start, r.stop)
    for (const day of [...r.days].sort(byWeekOrder)) {
      out.push({
        start: { weekday: day, time: r.start },
        stop: { weekday: wrap ? (day + 1) % 7 : day, time: r.stop },
      })
    }
  }
  return out
}

// ---------------------------------------------------------------------------
// Human-readable windows
// ---------------------------------------------------------------------------

// anchorSummary renders one window edge for a human ("Mon 09:00", "day 5 09:00", "2/29 09:00",
// or just "00:30" for daily), using only the fields that freq uses.
export function anchorSummary(a: RunPresetAnchor, freq: RunFreq, t: TFunc): string {
  const time = a.time || '00:00'
  switch (freq) {
    case 'weekly':
      return `${t('run.weekday.' + (a.weekday ?? 0))} ${time}`
    case 'monthly':
      return `${t('run.dayOfMonth', { d: a.day ?? 1 })} ${time}`
    case 'yearly':
      return `${a.month ?? 1}/${a.day ?? 1} ${time}`
    default:
      return time
  }
}

// timeRange renders "09:00–12:00", marking a window that runs past midnight so 22:00–02:00 does not
// read as a window that ends before it starts.
const timeRange = (start: string, stop: string, t: TFunc) =>
  `${start}–${wrapsMidnight(start, stop) ? `${t('preset.nextDay')} ` : ''}${stop}`

// presetWindows renders one line per sub-window — what the picker joins into a summary and what the
// rule popover lists. Daily and weekly presets are described the way they are configured: a weekly
// preset names its days once per time range ("Mon/Wed 09:00–12:00") rather than once per edge. A
// weekly window that spans whole days has no row form, so it keeps the per-edge wording.
export function presetWindows(p: RunPreset, t: TFunc): string[] {
  const intervals = p.intervals || []
  if (p.freq === 'weekly') {
    const rows = parseWeeklyRows(intervals)
    if (rows) return rows.map((r) => `${r.days.map((d) => t('run.weekday.' + d)).join('/')} ${timeRange(r.start, r.stop, t)}`)
  }
  if (p.freq === 'daily') return intervals.map((iv) => timeRange(iv.start.time || '00:00', iv.stop.time || '00:00', t))
  return intervals.map((iv) => `${anchorSummary(iv.start, p.freq, t)}–${anchorSummary(iv.stop, p.freq, t)}`)
}

// presetSummary is the compact "每天 09:00–12:00、14:00–18:00"-style description shown in the
// picker + editor: the frequency followed by each sub-window, joined by a separator. An inverted
// preset (run OUTSIDE the windows) prefixes the windows with an "except" marker so the same list
// reads as time to avoid, e.g. "每天 避开 09:00–12:00".
export function presetSummary(p: RunPreset, t: TFunc): string {
  const parts = presetWindows(p, t)
  const windows = p.invert ? `${t('preset.summaryExcept')} ${parts.join('、')}` : parts.join('、')
  return `${t('run.freq.' + p.freq)} ${windows}`
}
