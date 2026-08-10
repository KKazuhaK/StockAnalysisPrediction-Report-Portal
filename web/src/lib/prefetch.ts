// Warming a GET before the click that needs it.
//
// The portal fetches on open: the run dialog asks for its workflow list when it opens, a settings
// page for its configuration when it mounts. That is correct — it is also why a slow link spends
// the first second of every one of those looking at a spinner, when the shell had been sitting
// idle for a minute beforehand doing nothing.
//
// So: fetch it during that idle time instead, and let the opener read what is already there.
// Three rules keep this a head start rather than a second, quietly-wrong source of truth:
//
//   - a warm entry has an age, and the caller says how old it may be. Past that it is ignored.
//   - the caller still issues its own request; the warm entry only decides whether it waits for
//     the answer or renders while it arrives.
//   - nothing is warmed on a connection the user asked us not to spend, or for a page that is
//     not on screen.
//
// Failures are not cached: a warm attempt that fails simply leaves nothing, and the opener loads
// the way it always did.

type Entry = { at: number; data: unknown }

const cache = new Map<string, Entry>()
const inFlight = new Map<string, Promise<void>>()

// A session that hovers its way through the whole admin menu should not accumulate for ever.
const MAX_ENTRIES = 32

type NetworkInformation = { saveData?: boolean; effectiveType?: string }

// Whether it is reasonable to spend a request on something nobody has asked for yet.
function speculationAllowed(): boolean {
  const conn = (navigator as { connection?: NetworkInformation }).connection
  if (conn?.saveData) return false
  if ((conn?.effectiveType ?? '').includes('2g')) return false
  if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return false
  return true
}

/**
 * Fetch `url` now so a later reader finds it warm. Never throws, never reports: a prefetch that
 * fails costs nothing, because every caller can still load for itself.
 */
export function prefetch(url: string): Promise<void> {
  if (!speculationAllowed()) return Promise.resolve()
  const running = inFlight.get(url)
  if (running) return running

  const task = fetch(url, { credentials: 'same-origin', headers: { 'X-Prefetch': '1' } })
    .then(async (res) => {
      if (!res.ok) return
      const text = await res.text()
      if (!text) return
      if (cache.size >= MAX_ENTRIES) cache.clear()
      cache.set(url, { at: Date.now(), data: JSON.parse(text) })
    })
    .catch(() => {})
    .finally(() => {
      inFlight.delete(url)
    })

  inFlight.set(url, task)
  return task
}

/**
 * Store an answer that was fetched for real. A page that has just been read is the likeliest one
 * to be read again — going back to it is the most predictable navigation there is — so what was
 * loaded deliberately is worth keeping on the same terms as what was guessed.
 */
export function rememberPrefetched(url: string, data: unknown): void {
  if (cache.size >= MAX_ENTRIES) cache.clear()
  cache.set(url, { at: Date.now(), data })
}

/** The warm answer for `url` if one was stored within `maxAgeMs`, otherwise undefined. */
export function readPrefetched<T>(url: string, maxAgeMs: number): T | undefined {
  const hit = cache.get(url)
  if (!hit) return undefined
  if (Date.now() - hit.at > maxAgeMs) return undefined
  return hit.data as T
}

/** Drop warm entries — after a save, or between tests. */
export function forgetPrefetched(url?: string): void {
  if (url) cache.delete(url)
  else cache.clear()
}
