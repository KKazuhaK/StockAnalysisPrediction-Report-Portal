import { useEffect, useState } from 'react'

// How a new build reaches an open tab.
//
// The portal already had an answer for this: /api/version is polled, and when the server reports a
// different build a banner appears with a Refresh button. The user decides when — which is the only
// defensible policy, because the alternative throws away whatever they were in the middle of.
//
// The service worker had a second, contradictory answer. It called skipWaiting() on install and
// reloaded the page from controllerchange, so on a PWA-enabled portal a deploy could take the page
// out from under someone mid-sentence — a half-written assistant prompt, a run dialog, an unsaved
// settings form, a streaming answer.
//
// Worse, the two interacted badly. Activating immediately also purges the previous build's cache,
// and an open tab still running the old JS then asks for chunks that no longer exist — recoverable
// only through the one-shot reload in lazyRetry, which is a fallback, not a plan.
//
// So the service worker installs and WAITS. This hook reports that a build is ready, the banner
// says so alongside the /api/version signal, and applying it is what the user clicked.
//
// WHY ACCEPTING HAS TO FINISH THE HANDOVER. The two signals are independent, and accepting used to
// resolve only whichever one happened to be ready. /api/version notices a deploy within five
// minutes; the browser only re-fetches /sw.js on a navigation. So the usual order was: banner from
// the version poll, nothing waiting yet, plain reload — and that reload is a navigation, which
// fetches the new /sw.js, which installs and waits. The page was now running the NEW build's HTML
// under the OLD service worker, with a worker waiting beside it: the banner came straight back, and
// accepting it a second time reloaded a second time. Two prompts, two reloads, for one deploy.
// Accepting now asks the registration to update, waits for what that finds, and hands over — so one
// click is one reload and the page ends up controlled by the build it is running.

type Waiting = ServiceWorker | null

let waiting: Waiting = null
let ready = false
// Whether this tab asked for the handover. A tab that did not is not mid-reload, so control
// changing under it is news to report rather than something it already knows.
let initiated = false
const listeners = new Set<(ready: boolean) => void>()

// How long to wait for a freshly-fetched worker to finish installing, and then for it to take
// control. Both are bounded so the button is never dead: past either, reload regardless.
const INSTALL_WAIT_MS = 3000
const CONTROL_WAIT_MS = 2000

function announce(sw: Waiting, isReady = sw != null) {
  waiting = sw
  ready = isReady
  listeners.forEach((fn) => fn(ready))
}

// A tab that did not initiate the handover has just had its service worker replaced, which means
// the cache it was reading from is gone and the build it is running is no longer served. Saying so
// lets the user reload on their own terms; without it the first stale chunk reloads the page under
// them (lazyRetry), which is recovery, not an explanation.
function onControlChange() {
  if (initiated) return // this tab drove it and is already reloading
  announce(null, true) // nothing left to hand over — accepting is a plain reload
}

// One named handler, registered on every call: an identical (type, listener) pair is a no-op the
// second time, so this needs no flag of its own to stay idempotent.
function watchControlChange() {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return
  navigator.serviceWorker.addEventListener('controllerchange', onControlChange)
}

/** Watch a registration for a worker that has installed and is waiting to take over. */
export function trackSWUpdates(reg: ServiceWorkerRegistration): void {
  watchControlChange()
  if (reg.waiting) announce(reg.waiting)
  reg.addEventListener('updatefound', () => {
    const installing = reg.installing
    if (!installing) return
    installing.addEventListener('statechange', () => {
      // "installed" with a controller already present means an update, not a first install: the
      // page is running the old build and a new one is ready beside it.
      if (installing.state === 'installed' && navigator.serviceWorker.controller) announce(installing)
    })
  })
}

/** Forget any waiting worker — used when the service worker is unregistered. */
export function clearSWUpdate(): void {
  initiated = false
  announce(null, false)
}

// installedWorker resolves the worker a fresh update check produced, or null if it produced none
// within INSTALL_WAIT_MS. An install that is still running past that is not worth holding a click
// for — the plain reload below fetches the new HTML either way.
function installedWorker(reg: ServiceWorkerRegistration): Promise<Waiting> {
  if (reg.waiting) return Promise.resolve(reg.waiting)
  const installing = reg.installing
  if (!installing) return Promise.resolve(null)
  return new Promise<Waiting>((resolve) => {
    let settled = false
    const done = (sw: Waiting) => {
      if (settled) return
      settled = true
      resolve(sw)
    }
    installing.addEventListener('statechange', () => {
      if (installing.state === 'installed') done(installing)
      // A worker that fails to install leaves nothing to hand over; do not hold the click for it.
      if (installing.state === 'redundant') done(null)
    })
    window.setTimeout(() => done(null), INSTALL_WAIT_MS)
  })
}

// pendingWorker is the build to hand over to: the one already waiting, or whatever asking the
// registration to update turns up. Null means there is nothing to hand over — no service worker at
// all, or the server is serving the same build this tab is running.
async function pendingWorker(): Promise<Waiting> {
  if (waiting) return waiting
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null
  const reg = await navigator.serviceWorker.getRegistration('/sw.js').catch(() => null)
  if (!reg) return null
  if (reg.waiting) return reg.waiting
  // The version poll can notice a deploy long before the browser re-checks /sw.js on its own. Ask.
  await reg.update().catch(() => undefined)
  return await installedWorker(reg)
}

/**
 * Accept the new build: hand over to the service worker carrying it, then reload — once.
 *
 * Reloading before the handover would re-run the build we are trying to leave, and reloading
 * without one leaves the new HTML under the old worker, which is what produced a second prompt.
 * Where there is nothing to hand over — no service worker, or a signal that came from /api/version
 * on a portal with the PWA off — a plain reload is the whole of it.
 */
export async function applyUpdate(): Promise<void> {
  initiated = true
  const sw = await pendingWorker()
  if (!sw) {
    window.location.reload()
    return
  }
  let reloaded = false
  const onChange = () => {
    if (reloaded) return
    reloaded = true
    window.location.reload()
  }
  navigator.serviceWorker.addEventListener('controllerchange', onChange)
  sw.postMessage({ type: 'SKIP_WAITING' })
  // If the handover does not happen — an older browser, a worker that errored — reload anyway
  // rather than leaving a button that appears to do nothing.
  window.setTimeout(onChange, CONTROL_WAIT_MS)
}

/** Whether a new build is ready and the banner should say so. */
export function useSWUpdateReady(): boolean {
  const [isReady, setReady] = useState(ready)
  useEffect(() => {
    listeners.add(setReady)
    setReady(ready)
    return () => {
      listeners.delete(setReady)
    }
  }, [])
  return isReady
}
