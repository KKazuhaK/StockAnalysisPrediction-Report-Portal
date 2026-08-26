import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { applyUpdate, clearSWUpdate, trackSWUpdates } from './swUpdate'

// A registration whose lifecycle a test can drive.
function fakeReg(waiting: unknown = null) {
  const handlers: Record<string, (() => void)[]> = {}
  return {
    waiting,
    installing: null as unknown,
    update: vi.fn(() => Promise.resolve()),
    addEventListener: (ev: string, fn: () => void) => {
      ;(handlers[ev] ??= []).push(fn)
    },
    fire: (ev: string) => (handlers[ev] ?? []).forEach((fn) => fn()),
  }
}

describe('service-worker updates', () => {
  const posted: unknown[] = []
  let controllerChange: (() => void) | null = null
  let timers: (() => void)[] = []
  let registration: ReturnType<typeof fakeReg> | null

  const stubNavigator = (controller: unknown = {}) =>
    vi.stubGlobal('navigator', {
      serviceWorker: {
        controller,
        getRegistration: () => Promise.resolve(registration),
        addEventListener: (ev: string, fn: () => void) => {
          if (ev === 'controllerchange') controllerChange = fn
        },
      },
    })

  beforeEach(() => {
    clearSWUpdate()
    posted.length = 0
    controllerChange = null
    timers = []
    registration = null
    stubNavigator()
    vi.stubGlobal('window', {
      location: { reload: vi.fn() },
      // Held rather than run, so a test decides whether the bounded waits elapse.
      setTimeout: (fn: () => void) => timers.push(fn),
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  const waitingWorker = () => ({ postMessage: (m: unknown) => posted.push(m) })

  it('hands over to a waiting build and reloads only once it has taken control', async () => {
    trackSWUpdates(fakeReg(waitingWorker()) as unknown as ServiceWorkerRegistration)

    await applyUpdate()
    expect(posted).toEqual([{ type: 'SKIP_WAITING' }])
    // Reloading before the handover would simply re-run the build we are trying to leave.
    expect(window.location.reload).not.toHaveBeenCalled()

    controllerChange?.()
    expect(window.location.reload).toHaveBeenCalledTimes(1)

    // And exactly once, however many times control changes afterwards.
    controllerChange?.()
    expect(window.location.reload).toHaveBeenCalledTimes(1)
  })

  // The regression this file exists for. /api/version notices a deploy within five minutes; the
  // browser only re-checks /sw.js on a navigation. Accepting used to reload with nothing waiting,
  // and that reload fetched the new worker — leaving the new HTML under the old worker with an
  // update waiting beside it, so the banner came straight back and the user reloaded twice.
  it('asks the registration for an update when nothing is waiting yet, and hands over to what it finds', async () => {
    const sw = waitingWorker()
    const reg = fakeReg()
    reg.update = vi.fn(() => {
      reg.waiting = sw // the check found the new build
      return Promise.resolve()
    })
    registration = reg

    await applyUpdate()
    expect(reg.update).toHaveBeenCalledTimes(1)
    expect(posted).toEqual([{ type: 'SKIP_WAITING' }])
    expect(window.location.reload).not.toHaveBeenCalled()

    controllerChange?.()
    expect(window.location.reload).toHaveBeenCalledTimes(1)
  })

  it('waits for a worker that is still installing when the check returns', async () => {
    let statechange: (() => void) | null = null
    const sw = {
      postMessage: (m: unknown) => posted.push(m),
      state: 'installing',
      addEventListener: (_: string, fn: () => void) => {
        statechange = fn
      },
    }
    const reg = fakeReg()
    reg.update = vi.fn(() => {
      reg.installing = sw
      return Promise.resolve()
    })
    registration = reg

    const applied = applyUpdate()
    await vi.waitFor(() => expect(statechange).not.toBeNull())
    sw.state = 'installed'
    statechange!()
    await applied

    expect(posted).toEqual([{ type: 'SKIP_WAITING' }])
  })

  it('reloads plainly when the portal has no service worker at all', async () => {
    vi.stubGlobal('navigator', {})
    await applyUpdate()
    expect(window.location.reload).toHaveBeenCalledTimes(1)
    expect(posted).toEqual([])
  })

  it('reloads plainly when the update check turns nothing up', async () => {
    registration = fakeReg()
    await applyUpdate()
    expect(registration.update).toHaveBeenCalledTimes(1)
    expect(window.location.reload).toHaveBeenCalledTimes(1)
    expect(posted).toEqual([])
  })

  it('reloads anyway if control never changes, so the button is never dead', async () => {
    trackSWUpdates(fakeReg(waitingWorker()) as unknown as ServiceWorkerRegistration)
    await applyUpdate()
    expect(window.location.reload).not.toHaveBeenCalled()

    timers.forEach((fn) => fn()) // the handover never happened
    expect(window.location.reload).toHaveBeenCalledTimes(1)
  })

  // The other half of the reported symptom. When one tab accepts, the new worker claims every
  // other tab: their cache is purged and the build they are running is no longer served, so the
  // first stale chunk reloads them out of nowhere (lazyRetry). Control changing under a tab that
  // did not ask for it is news to report — and there is nothing left for that tab to hand over to,
  // because the worker it would have handed over to is already the one in charge.
  it('reports the update in a tab that control changed under, and accepts it with a plain reload', async () => {
    trackSWUpdates(fakeReg(waitingWorker()) as unknown as ServiceWorkerRegistration)
    registration = fakeReg() // nothing waiting any more: it took over

    controllerChange?.() // another tab accepted

    await applyUpdate()
    expect(posted).toEqual([]) // handing over to the worker already in charge would do nothing
    expect(window.location.reload).toHaveBeenCalledTimes(1)
  })

  it('treats a first install as no update at all', () => {
    // No controller yet means this is the first service worker for this origin: there is no old
    // build to leave, so telling the user a new version is available would be nonsense.
    stubNavigator(null)
    const reg = fakeReg()
    const installing = { state: 'installed', addEventListener: (_: string, fn: () => void) => fn() }
    reg.installing = installing
    trackSWUpdates(reg as unknown as ServiceWorkerRegistration)
    reg.fire('updatefound')
    // Nothing was announced, so there is nothing to hand over and no banner to accept.
    expect(posted).toEqual([])
  })
})
