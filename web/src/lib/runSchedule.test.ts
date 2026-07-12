import { describe, it, expect } from 'vitest'
import dayjs from 'dayjs'
import { schedulePayload, scheduleError, presetSummary, type RunSchedule } from './runSchedule'
import type { RunPreset } from '../api/types'

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
