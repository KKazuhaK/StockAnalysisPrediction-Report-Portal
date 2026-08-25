import { describe, it, expect } from 'vitest'
import dayjs from 'dayjs'
import {
  schedulePayload,
  scheduleError,
  presetSummary,
  presetWindows,
  parseWeeklyRows,
  weeklyIntervals,
  readRunDefaults,
  type RunSchedule,
} from './runSchedule'
import type { RunPreset, RunPresetInterval, RunPresetsResp } from '../api/types'

const base: RunSchedule = { mode: 'now', runAt: null, idle: false, urgent: false }

// A stub translator that echoes its key, so the summary's shape is asserted without a locale file.
const tk = (k: string) => k
const dailyPreset = (invert: boolean): RunPreset => ({
  id: 1,
  label: 'x',
  freq: 'daily',
  intervals: [{ start: { time: '09:00' }, stop: { time: '12:00' } }],
  on_overrun: 'next',
  enabled: true,
  invert,
  ord: 0,
})

describe('schedulePayload', () => {
  it('immediate + idle → priority idle, no run_at/preset', () => {
    expect(schedulePayload({ ...base, idle: true })).toEqual({ priority: 'idle', run_at: '', preset_id: 0 })
  })
  it('urgent wins over idle', () => {
    expect(schedulePayload({ ...base, idle: true, urgent: true }).priority).toBe('urgent')
  })
  it('preset mode → preset_id, empty priority/run_at', () => {
    expect(schedulePayload({ ...base, mode: 'preset', presetId: 7 })).toEqual({ priority: '', run_at: '', preset_id: 7 })
  })
  it('scheduled mode → formatted run_at, no preset', () => {
    const at = dayjs('2026-07-09 08:30:00')
    expect(schedulePayload({ ...base, mode: 'scheduled', runAt: at })).toEqual({
      priority: '',
      run_at: '2026-07-09 08:30:00',
      preset_id: 0,
    })
  })
  it('idle applies only in immediate mode (a stale idle flag never leaks into a scheduled submit)', () => {
    expect(schedulePayload({ ...base, mode: 'scheduled', runAt: dayjs('2026-01-01 00:00:00'), idle: true }).priority).toBe('')
    expect(schedulePayload({ ...base, mode: 'preset', presetId: 3, idle: true }).priority).toBe('')
  })
})

describe('scheduleError', () => {
  it('scheduled without a time is incomplete', () => {
    expect(scheduleError({ ...base, mode: 'scheduled' })).toBe('run.pickTime')
  })
  it('preset without a selection is incomplete', () => {
    expect(scheduleError({ ...base, mode: 'preset' })).toBe('run.pickPreset')
  })
  it('immediate is always complete', () => {
    expect(scheduleError(base)).toBe('')
  })
  it('a chosen preset / time is complete', () => {
    expect(scheduleError({ ...base, mode: 'preset', presetId: 3 })).toBe('')
    expect(scheduleError({ ...base, mode: 'scheduled', runAt: dayjs() })).toBe('')
  })
})

describe('presetSummary', () => {
  it('a normal preset lists its run windows', () => {
    expect(presetSummary(dailyPreset(false), tk)).toBe('run.freq.daily 09:00–12:00')
  })
  it('an inverted preset marks the windows as time to avoid', () => {
    expect(presetSummary(dailyPreset(true), tk)).toBe('run.freq.daily preset.summaryExcept 09:00–12:00')
  })
})

// ---------------------------------------------------------------------------
// Weekly rows — the editor's spelling of a group of intervals (ADR 0014 §5 amendment)
// ---------------------------------------------------------------------------
const weekly = (intervals: RunPresetInterval[]): RunPreset => ({
  id: 2,
  label: 'w',
  freq: 'weekly',
  intervals,
  on_overrun: 'next',
  enabled: true,
  invert: false,
  ord: 0,
})

describe('parseWeeklyRows', () => {
  it('groups the weekdays that share a time range into one row', () => {
    expect(
      parseWeeklyRows([
        { start: { weekday: 1, time: '09:00' }, stop: { weekday: 1, time: '12:00' } },
        { start: { weekday: 3, time: '09:00' }, stop: { weekday: 3, time: '12:00' } },
        { start: { weekday: 5, time: '09:00' }, stop: { weekday: 5, time: '12:00' } },
        { start: { weekday: 6, time: '14:00' }, stop: { weekday: 6, time: '18:00' } },
      ]),
    ).toEqual([
      { days: [1, 3, 5], start: '09:00', stop: '12:00' },
      { days: [6], start: '14:00', stop: '18:00' },
    ])
  })

  it('reads a window that runs past midnight as one row on the day it starts', () => {
    expect(parseWeeklyRows([{ start: { weekday: 5, time: '22:00' }, stop: { weekday: 6, time: '02:00' } }])).toEqual([
      { days: [5], start: '22:00', stop: '02:00' },
    ])
  })

  it('orders a row’s days Monday-first, whatever order they were stored in', () => {
    expect(
      parseWeeklyRows([
        { start: { weekday: 0, time: '09:00' }, stop: { weekday: 0, time: '12:00' } },
        { start: { weekday: 2, time: '09:00' }, stop: { weekday: 2, time: '12:00' } },
      ])?.[0].days,
    ).toEqual([2, 0])
  })

  it('refuses a window spanning whole days, so the editor keeps the anchor fields for it', () => {
    expect(parseWeeklyRows([{ start: { weekday: 1, time: '09:00' }, stop: { weekday: 3, time: '18:00' } }])).toBeNull()
  })

  it('has no rows for a preset with no windows', () => {
    expect(parseWeeklyRows([])).toEqual([])
  })
})

describe('weeklyIntervals', () => {
  it('expands one row into one interval per weekday', () => {
    expect(weeklyIntervals([{ days: [1, 3], start: '09:00', stop: '12:00' }])).toEqual([
      { start: { weekday: 1, time: '09:00' }, stop: { weekday: 1, time: '12:00' } },
      { start: { weekday: 3, time: '09:00' }, stop: { weekday: 3, time: '12:00' } },
    ])
  })

  it('lands a past-midnight row on the next weekday, wrapping Saturday round to Sunday', () => {
    expect(weeklyIntervals([{ days: [6], start: '22:00', stop: '02:00' }])).toEqual([
      { start: { weekday: 6, time: '22:00' }, stop: { weekday: 0, time: '02:00' } },
    ])
  })

  it('treats an equal start and stop as a full 24 hours, not a full week', () => {
    expect(weeklyIntervals([{ days: [6], start: '00:00', stop: '00:00' }])[0].stop.weekday).toBe(0)
  })

  it('contributes nothing for a row with no weekday picked', () => {
    expect(weeklyIntervals([{ days: [], start: '09:00', stop: '12:00' }])).toEqual([])
  })

  it('round-trips rows through the stored intervals', () => {
    const rows = [
      { days: [1, 3, 5], start: '09:00', stop: '12:00' },
      { days: [6, 0], start: '22:00', stop: '02:00' },
    ]
    expect(parseWeeklyRows(weeklyIntervals(rows))).toEqual(rows)
  })
})

describe('presetWindows', () => {
  it('names a weekly preset’s days once per time range instead of once per edge', () => {
    expect(
      presetSummary(
        weekly([
          { start: { weekday: 1, time: '09:00' }, stop: { weekday: 1, time: '12:00' } },
          { start: { weekday: 3, time: '09:00' }, stop: { weekday: 3, time: '12:00' } },
        ]),
        tk,
      ),
    ).toBe('run.freq.weekly run.weekday.1/run.weekday.3 09:00–12:00')
  })

  it('falls back to the per-edge wording for a window that spans whole days', () => {
    expect(presetSummary(weekly([{ start: { weekday: 1, time: '09:00' }, stop: { weekday: 3, time: '18:00' } }]), tk)).toBe(
      'run.freq.weekly run.weekday.1 09:00–run.weekday.3 18:00',
    )
  })

  it('marks a window that runs past midnight', () => {
    const p = dailyPreset(false)
    p.intervals = [{ start: { time: '22:00' }, stop: { time: '02:00' } }]
    expect(presetSummary(p, tk)).toBe('run.freq.daily 22:00–preset.nextDay 02:00')
  })

  it('lists each window as its own line for the rule popover', () => {
    const p = dailyPreset(true)
    p.intervals = [
      { start: { time: '09:00' }, stop: { time: '12:00' } },
      { start: { time: '14:00' }, stop: { time: '18:00' } },
    ]
    expect(presetWindows(p, tk)).toEqual(['09:00–12:00', '14:00–18:00'])
  })
})

describe('readRunDefaults', () => {
  const resp = (extra: Partial<RunPresetsResp> = {}): RunPresetsResp => ({
    presets: [],
    default_mode: 'now',
    default_idle: false,
    ...extra,
  })

  it('carries the admin’s choice of whether the run form prints the whole rule', () => {
    expect(readRunDefaults(resp({ show_preset_rule: true })).showPresetRule).toBe(true)
  })

  it('leaves the rule out when the portal has never been configured', () => {
    expect(readRunDefaults(resp()).showPresetRule).toBe(false)
  })
})
