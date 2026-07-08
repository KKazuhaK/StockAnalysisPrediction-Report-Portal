import { describe, it, expect } from 'vitest'
import dayjs from 'dayjs'
import { schedulePayload, scheduleError, type RunSchedule } from './runSchedule'

const base: RunSchedule = { mode: 'now', runAt: null, idle: false, urgent: false }

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
