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
// So the service worker now installs and WAITS. This hook reports that a build is ready, the banner
// says so alongside the /api/version signal, and applying it is what the user clicked: tell the
// waiting worker to take over, then reload once it has.

type Waiting = ServiceWorker | null

let waiting: Waiting = null
const listeners = new Set<(ready: boolean) => void>()

function announce(sw: Waiting) {
  waiting = sw
  listeners.forEach((fn) => fn(sw != null))
}

/** Watch a registration for a worker that has installed and is waiting to take over. */
export function trackSWUpdates(reg: ServiceWorkerRegistration): void {
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
  announce(null)
}

/**
 * Hand over to the waiting build and reload once it has taken control. Reloading before it does
 * would simply re-run the old build. Returns false when there is nothing waiting, so the caller can
 * fall back to a plain reload (the /api/version signal can fire without a service worker at all).
 */
export function applySWUpdate(): boolean {
  if (!waiting) return false
  let reloaded = false
  const onChange = () => {
    if (reloaded) return
    reloaded = true
    window.location.reload()
  }
  navigator.serviceWorker.addEventListener('controllerchange', onChange)
  waiting.postMessage({ type: 'SKIP_WAITING' })
  // If the handover does not happen — an older browser, a worker that errored — reload anyway
  // rather than leaving a button that appears to do nothing.
  window.setTimeout(onChange, 2000)
  return true
}

/** Whether a new build has installed and is waiting for the user to accept it. */
export function useSWUpdateReady(): boolean {
  const [ready, setReady] = useState(waiting != null)
  useEffect(() => {
    listeners.add(setReady)
    return () => {
      listeners.delete(setReady)
    }
  }, [])
  return ready
}
