import { describe, it, expect } from 'vitest'
import { resolveOU, ouPath } from './ouSettings'
import type { UserGroupRow } from '../../api/types'

const g = (o: Partial<UserGroupRow>): UserGroupRow =>
  ({ id: 1, name: 'X', members: 0, weight: null, ...o }) as UserGroupRow

// The Default group: ticketed urgent, two per period.
const DEF = g({ id: 1, name: 'Default', is_default: true, weight: 2, allow_urgent: true, urgent_unlimited: false })

describe('resolveOU — the urgent policy is one answer, not three flags', () => {
  // The reported bug: an OU inheriting weight 2 from Default while setting unlimited itself
  // rendered "2 tickets/period · inherited" AND "unlimited urgent" side by side. Both were true of
  // the stored flags; neither was a useful thing to tell an admin.
  it('unlimited beats an inherited ticket count', () => {
    const r = resolveOU(g({ id: 2, weight: null, urgent_unlimited: true }), DEF)
    expect(r.urgent.value).toBe('unlimited')
    expect(r.urgent.inherited).toBe(false) // this OU chose it
  })

  it('disabled beats everything, including a ticket count it would otherwise show', () => {
    const r = resolveOU(g({ id: 2, allow_urgent: false, weight: 5, urgent_unlimited: true }), DEF)
    expect(r.urgent.value).toBe('off')
  })

  it('an OU that sets nothing inherits the Default group, and says so', () => {
    const r = resolveOU(g({ id: 2 }), DEF)
    expect(r.urgent.value).toBe('ticket')
    expect(r.urgent.inherited).toBe(true)
    expect(r.weight).toEqual({ value: 2, inherited: true })
  })

  it('the Default group inherits from nobody — a null there is unset, not inherited', () => {
    const r = resolveOU(g({ id: 1, is_default: true, weight: null, max_queued: null }), DEF)
    expect(r.weight.inherited).toBe(false)
    expect(r.maxQueued).toEqual({ value: 0, inherited: false })
  })

  it('an own ticket count is not reported as inherited', () => {
    const r = resolveOU(g({ id: 2, weight: 7, allow_urgent: true, urgent_unlimited: false }), DEF)
    expect(r.urgent.value).toBe('ticket')
    expect(r.urgent.inherited).toBe(false)
    expect(r.weight).toEqual({ value: 7, inherited: false })
  })
})

describe('resolveOU — the other settings', () => {
  it('reports each as inherited or own, with the value either way', () => {
    const def = g({ id: 1, is_default: true, max_queued: 3, run_window: '9-18' })
    const own = resolveOU(g({ id: 2, max_queued: 9, run_window: '' }), def)
    expect(own.maxQueued).toEqual({ value: 9, inherited: false })
    // An explicit empty window is a real choice — "any hour" — not an absence.
    expect(own.runWindow).toEqual({ value: '', inherited: false })

    const inh = resolveOU(g({ id: 3 }), def)
    expect(inh.maxQueued).toEqual({ value: 3, inherited: true })
    expect(inh.runWindow).toEqual({ value: '9-18', inherited: true })
  })

  it('priority falls back to the SYSTEM default, not to the Default group', () => {
    const def = g({ id: 1, is_default: true, priority: '80' })
    expect(resolveOU(g({ id: 2 }), def).priority).toEqual({ value: null, inherited: true })
    expect(resolveOU(g({ id: 2, priority: '40' }), def).priority).toEqual({ value: 40, inherited: false })
  })

  it('survives having no Default group at all', () => {
    const r = resolveOU(g({ id: 2 }), undefined)
    expect(r.urgent.value).toBe('ticket')
    expect(r.weight.value).toBe(0)
  })
})

describe('ouPath', () => {
  const TREE = [g({ id: 1, name: 'Root' }), g({ id: 2, name: 'Clients', parent_id: 1 }), g({ id: 3, name: 'APAC', parent_id: 2 })]

  it('reads from the root down', () => {
    expect(ouPath(TREE, 3)).toEqual(['Root', 'Clients', 'APAC'])
    expect(ouPath(TREE, 1)).toEqual(['Root'])
  })

  it('terminates on a cycle rather than hanging the page', () => {
    expect(ouPath([g({ id: 1, name: 'A', parent_id: 2 }), g({ id: 2, name: 'B', parent_id: 1 })], 1)).toHaveLength(2)
  })
})
