import { describe, it, expect, vi, beforeEach } from 'vitest'
import { withChunkReload, RELOAD_FLAG } from './lazyRetry'

// A stale route chunk (deleted from the server by a new deploy while this tab was
// already open — the dist is wiped clean on every build, see scripts/clean-dist.mjs)
// must trigger exactly one reload instead of leaving a broken Suspense boundary, and
// must never reload-loop if the failure persists (e.g. genuinely offline).
describe('withChunkReload', () => {
  let reload: ReturnType<typeof vi.fn>

  beforeEach(() => {
    sessionStorage.clear()
    reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
  })

  it('reloads once on the first failure and never resolves the original promise', async () => {
    const failing = vi.fn().mockRejectedValue(new Error('Failed to fetch dynamically imported module'))
    const wrapped = withChunkReload(failing)

    let settled = false
    wrapped().then(
      () => (settled = true),
      () => (settled = true),
    )
    await new Promise((r) => setTimeout(r, 0))

    expect(reload).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(RELOAD_FLAG)).toBe('1')
    expect(settled).toBe(false) // navigating away — must not resolve or reject
  })

  it('does not reload again on a second failure without an intervening success (no loop)', async () => {
    sessionStorage.setItem(RELOAD_FLAG, '1') // simulate: already reloaded once this session
    const failing = vi.fn().mockRejectedValue(new Error('network offline'))
    const wrapped = withChunkReload(failing)

    await expect(wrapped()).rejects.toThrow('network offline')
    expect(reload).not.toHaveBeenCalled()
  })

  it('clears the reload flag on success, so a later failure can trigger one more reload', async () => {
    sessionStorage.setItem(RELOAD_FLAG, '1')
    const ok = vi.fn().mockResolvedValue({ default: 'component' })
    await withChunkReload(ok)()
    expect(sessionStorage.getItem(RELOAD_FLAG)).toBeNull()
  })

  it('passes through a successful load unchanged', async () => {
    const mod = { default: 'component' }
    const ok = vi.fn().mockResolvedValue(mod)
    await expect(withChunkReload(ok)()).resolves.toBe(mod)
    expect(reload).not.toHaveBeenCalled()
  })
})
