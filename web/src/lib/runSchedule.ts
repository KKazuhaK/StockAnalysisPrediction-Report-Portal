import type { Dayjs } from 'dayjs'
import type { RunFreq, RunMode, RunPreset, RunPresetAnchor } from '../api/types'

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

// scheduleError returns an i18n key when the schedule is incomplete (a mode that needs a value has
// none), else '' — the consumers show it and block submit.
export function scheduleError(s: RunSchedule): '' | 'run.pickTime' | 'run.pickPreset' {
  if (s.mode === 'scheduled' && !s.runAt) return 'run.pickTime'
  if (s.mode === 'preset' && !s.presetId) return 'run.pickPreset'
  return ''
}

type TFunc = (key: string, opts?: Record<string, unknown>) => string

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

// presetSummary is the compact "每天 00:30–08:30"-style description shown in the picker + editor.
export function presetSummary(p: RunPreset, t: TFunc): string {
  return `${t('run.freq.' + p.freq)} ${anchorSummary(p.start, p.freq, t)}–${anchorSummary(p.stop, p.freq, t)}`
}
