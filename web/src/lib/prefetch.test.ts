import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { forgetPrefetched, prefetch, readPrefetched } from './prefetch'

const conn = (v: unknown) => Object.defineProperty(navigator, 'connection', { value: v, configurable: true })

describe('prefetch', () => {
  beforeEach(() => {
    forgetPrefetched()
    conn(undefined)
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, text: () => Promise.resolve('{"a":1}') } as unknown as Response)))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('makes a later read of the same url instant', async () => {
    await prefetch('/api/x')
    expect(readPrefetched('/api/x', 60_000)).toEqual({ a: 1 })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not fire the same request twice while one is in flight', async () => {
    const first = prefetch('/api/x')
    const second = prefetch('/api/x')
    await Promise.all([first, second])
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('serves nothing once the entry is older than the caller allows', async () => {
    vi.useFakeTimers()
    await prefetch('/api/x')
    expect(readPrefetched('/api/x', 1000)).toEqual({ a: 1 })
    vi.advanceTimersByTime(1500)
    // Warm data is a head start, never a substitute for asking: past its age it is ignored and
    // the caller loads normally.
    expect(readPrefetched('/api/x', 1000)).toBeUndefined()
  })

  it('stays off a connection the user asked us not to spend', async () => {
    conn({ saveData: true })
    await prefetch('/api/x')
    expect(fetch).not.toHaveBeenCalled()

    conn({ effectiveType: 'slow-2g' })
    await prefetch('/api/y')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('keeps a failure out of the cache instead of caching the error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 500, text: () => Promise.resolve('') } as unknown as Response)))
    await prefetch('/api/x')
    expect(readPrefetched('/api/x', 60_000)).toBeUndefined()
  })
})
