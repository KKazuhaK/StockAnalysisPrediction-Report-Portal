import { lazy, type ComponentType } from 'react'

// The go:embed dist is wiped clean on every build (see web/scripts/clean-dist.mjs),
// so an old hashed chunk filename no longer exists once a new version is deployed.
// A browser tab left open across a deploy can navigate to a route it hasn't lazily
// loaded yet and get a 404 for that chunk — this key guards a one-time reload to
// recover instead of leaving a broken Suspense boundary.
export const RELOAD_FLAG = 'rp_chunk_reload_attempted'

// Wraps a dynamic import() so that a failed chunk load triggers exactly one full
// page reload (which re-fetches index.html and its current hashed asset names).
// A second failure without an intervening success (e.g. genuinely offline) is not
// retried again — it surfaces as a real error instead of reload-looping forever.
export function withChunkReload<T>(factory: () => Promise<T>): () => Promise<T> {
  return async () => {
    try {
      const mod = await factory()
      sessionStorage.removeItem(RELOAD_FLAG) // a later failure is a fresh event, not a loop
      return mod
    } catch (err) {
      if (!sessionStorage.getItem(RELOAD_FLAG)) {
        sessionStorage.setItem(RELOAD_FLAG, '1')
        window.location.reload()
        return new Promise<T>(() => {}) // navigating away — never resolve or reject
      }
      throw err
    }
  }
}

// React.lazy() with the stale-chunk-reload behavior above.
export function lazyRetry<T extends ComponentType<any>>(factory: () => Promise<{ default: T }>) {
  return lazy(withChunkReload(factory))
}
