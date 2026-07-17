import { describe, expect, it } from 'vitest'

import { policyAllows, surfaceSupportsMode, visibleOn } from './batchUi'
import { ALL_SURFACES } from '../api/types'
import type { BatchTarget, Surface } from '../api/types'

const target = (over: Partial<BatchTarget> = {}): BatchTarget => ({
  id: 1,
  plugin_slug: 'dify',
  name: 't',
  created_at: '',
  mode: 'workflow',
  ...over,
})

describe('capability (what the app can do)', () => {
  it('keeps agent apps out of report-producing surfaces', () => {
    // An agent-chat app holds a conversation and never posts a report, so no admin setting
    // can make it serve 运行分析.
    expect(surfaceSupportsMode('run', 'agent-chat')).toBe(false)
    expect(surfaceSupportsMode('batch', 'agent-chat')).toBe(false)
    expect(surfaceSupportsMode('chat', 'agent-chat')).toBe(true)
  })

  it('keeps workflow apps out of the assistant', () => {
    expect(surfaceSupportsMode('chat', 'workflow')).toBe(false)
    expect(surfaceSupportsMode('run', 'workflow')).toBe(true)
  })

  it('treats a missing mode as a workflow', () => {
    expect(surfaceSupportsMode('run', undefined)).toBe(true)
    expect(surfaceSupportsMode('chat', undefined)).toBe(false)
  })
})

describe('policy (what the admin wants)', () => {
  it('an unset target is allowed everywhere', () => {
    expect(ALL_SURFACES.every((s) => policyAllows(target(), s))).toBe(true)
    expect(ALL_SURFACES.every((s) => policyAllows(target({ surfaces: [] }), s))).toBe(true)
  })

  it('honours an explicit allow-list', () => {
    const tg = target({ surfaces: ['recurring'] })
    expect(policyAllows(tg, 'recurring')).toBe(true)
    expect(policyAllows(tg, 'run')).toBe(false)
    expect(policyAllows(tg, 'batch')).toBe(false)
  })

  it('fails OPEN on an unknown shape, never closed', () => {
    // A version skew that dropped the field must not make every target vanish from every
    // surface. Hiding everything looks like data loss; showing everything looks like the
    // old behaviour, which it is.
    expect(policyAllows(target({ surfaces: undefined }), 'run')).toBe(true)
  })
})

describe('visibleOn = capability AND policy', () => {
  it('the user case: a workflow restricted to scheduled runs only', () => {
    const scheduled = target({ id: 2, name: 'scheduled only', surfaces: ['recurring'] })
    const normal = target({ id: 3, name: 'everywhere' })
    const all = [scheduled, normal]
    expect(visibleOn(all, 'recurring').map((t) => t.id)).toEqual([2, 3])
    expect(visibleOn(all, 'run').map((t) => t.id)).toEqual([3])
    expect(visibleOn(all, 'batch').map((t) => t.id)).toEqual([3])
  })

  it('policy cannot grant what capability forbids', () => {
    // Ticking 运行分析 on an agent app is an invalid combination; the modal disables it,
    // but a stored one from any other route must still not surface.
    const agent = target({ mode: 'agent-chat', surfaces: ALL_SURFACES as Surface[] })
    expect(visibleOn([agent], 'run')).toEqual([])
    expect(visibleOn([agent], 'chat')).toEqual([agent])
  })

  it('capability cannot grant what policy forbids', () => {
    const wf = target({ mode: 'workflow', surfaces: ['batch'] })
    expect(visibleOn([wf], 'run')).toEqual([])
    expect(visibleOn([wf], 'batch')).toEqual([wf])
  })
})
