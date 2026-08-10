import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { applySWUpdate, clearSWUpdate, trackSWUpdates } from './swUpdate'

// A registration whose lifecycle a test can drive.
function fakeReg(waiting: unknown = null) {
  const handlers: Record<string, (() => void)[]> = {}
  return {
    waiting,
    installing: null as unknown,
    addEventListener: (ev: string, fn: () => void) => {
      ;(handlers[ev] ??= []).push(fn)
    },
    fire: (ev: string) => (handlers[ev] ?? []).forEach((fn) => fn()),
  }
}

describe('service-worker updates', () => {
  const posted: unknown[] = []
  let controllerChange: (() => void) | null = null

  beforeEach(() => {
    clearSWUpdate()
    posted.length = 0
    controllerChange = null
    vi.stubGlobal('navigator', {
      serviceWorker: {
        controller: {},
        addEventListener: (ev: string, fn: () => void) => {
          if (ev === 'controllerchange') controllerChange = fn
        },
      },
    })
    vi.stubGlobal('window', { location: { reload: vi.fn() }, setTimeout: vi.fn() })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('does nothing until a build is actually waiting', () => {
    // The banner can also be raised by the /api/version signal alone, with no service worker in
    // play; applying then has nothing to hand over to and must say so, not silently do nothing.
    expect(applySWUpdate()).toBe(false)
  })

  it('hands over to a waiting build and reloads only once it has taken control', () => {
    const waiting = { postMessage: (m: unknown) => posted.push(m) }
    trackSWUpdates(fakeReg(waiting) as unknown as ServiceWorkerRegistration)

    expect(applySWUpdate()).toBe(true)
    expect(posted).toEqual([{ type: 'SKIP_WAITING' }])
    // Reloading before the handover would simply re-run the build we are trying to leave.
    expect(window.location.reload).not.toHaveBeenCalled()

    controllerChange?.()
    expect(window.location.reload).toHaveBeenCalledTimes(1)

    // And exactly once, however many times control changes afterwards.
    controllerChange?.()
    expect(window.location.reload).toHaveBeenCalledTimes(1)
  })

  it('treats a first install as no update at all', () => {
    // No controller yet means this is the first service worker for this origin: there is no old
    // build to leave, so telling the user a new version is available would be nonsense.
    vi.stubGlobal('navigator', { serviceWorker: { controller: null, addEventListener: () => {} } })
    const reg = fakeReg()
    const installing = { state: 'installed', addEventListener: (_: string, fn: () => void) => fn() }
    reg.installing = installing
    trackSWUpdates(reg as unknown as ServiceWorkerRegistration)
    reg.fire('updatefound')
    expect(applySWUpdate()).toBe(false)
  })
})
