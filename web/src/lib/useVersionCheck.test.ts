import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { buildKey, useVersionCheck } from './useVersionCheck'

const get = vi.fn()
vi.mock('../api/client', () => ({ api: { get: (...a: unknown[]) => get(...a) } }))

const build = (over: Partial<{ version: string; commit: string; buildDate: string }> = {}) => ({
  version: 'v0.2.11',
  commit: 'aaaaaaa',
  buildDate: '2026-07-13T00:00:00Z',
  ...over,
})

describe('buildKey', () => {
  it('combines version, commit and buildDate into a per-binary identity', () => {
    expect(buildKey(build())).toBe('v0.2.11@aaaaaaa@2026-07-13T00:00:00Z')
    // any field changing yields a different key (a rebuilt same-tag binary still differs)
    expect(buildKey(build({ commit: 'bbbbbbb' }))).not.toBe(buildKey(build()))
    expect(buildKey(build({ buildDate: '2026-07-14T00:00:00Z' }))).not.toBe(buildKey(build()))
  })
})

describe('useVersionCheck', () => {
  beforeEach(() => get.mockReset())

  it('stays false while the server keeps reporting the same build', async () => {
    get.mockResolvedValue(build())
    const { result } = renderHook(() => useVersionCheck(5))
    // let several poll cycles run against an unchanged build
    await new Promise((r) => setTimeout(r, 40))
    expect(result.current).toBe(false)
    expect(get.mock.calls.length).toBeGreaterThan(1) // it really did poll more than once
  })

  it('flips to true once the server reports a new build', async () => {
    get.mockResolvedValueOnce(build()).mockResolvedValue(build({ commit: 'bbbbbbb' }))
    const { result } = renderHook(() => useVersionCheck(5))
    await waitFor(() => expect(result.current).toBe(true))
  })

  it('never fabricates an update prompt when a fetch yields no usable build', async () => {
    // A failed request is swallowed to null by the hook's .catch, landing in the same "no
    // usable info" branch as an empty response — so a null resolve exercises that guard
    // deterministically (a rejected-promise mock trips the runner's unhandled-rejection
    // tracker even when the hook catches it). The guard must never flip the flag.
    get.mockResolvedValue(null)
    const { result } = renderHook(() => useVersionCheck(5))
    await new Promise((r) => setTimeout(r, 40))
    expect(get.mock.calls.length).toBeGreaterThan(1) // it kept polling, unbothered
    expect(result.current).toBe(false)
  })
})
