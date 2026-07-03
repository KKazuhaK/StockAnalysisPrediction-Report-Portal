// The host↔app postMessage bridge (see docs/adr/0003-downloadable-apps.md).
//
// A sandboxed iframe app cannot reach the portal API directly. Instead it posts a
// message; the trusted host validates it against the app's granted scopes and, if
// allowed, performs the /api/v1 call with a short-lived scoped token the iframe
// never sees. This module holds the *pure* validation logic so it is unit-testable
// in isolation from the DOM.

export const API_MESSAGE = 'rp:api'
export const API_RESULT = 'rp:api:result'
export const INIT_MESSAGE = 'rp:init'

export interface ApiRequestMsg {
  type: typeof API_MESSAGE
  reqId: string | number
  method?: string
  path: string
}

export type Validated =
  | { ok: true; method: string; path: string }
  | { ok: false; error: string }

// validateApiRequest gates a message from an app before the host calls the API.
// Phase 1 grants only read access: GET under /api/v1/ with the query scope. Any
// write method, any path outside /api/v1/, or a missing scope is refused here — a
// second, authoritative check happens server-side via the token's scope.
export function validateApiRequest(msg: unknown, scopes: string[]): Validated {
  if (!msg || typeof msg !== 'object') return { ok: false, error: 'not a message' }
  const m = msg as Record<string, unknown>
  if (m.type !== API_MESSAGE) return { ok: false, error: 'unknown message type' }
  const method = String(m.method || 'GET').toUpperCase()
  if (method !== 'GET') return { ok: false, error: `method ${method} not permitted` }
  if (!scopes.includes('query')) return { ok: false, error: 'app lacks the query scope' }
  if (typeof m.path !== 'string') return { ok: false, error: 'path must be a string' }
  const path = m.path
  // Must be a same-origin API path, not an absolute/protocol-relative URL, and
  // must not try to climb out of the v1 namespace.
  if (!path.startsWith('/api/v1/')) return { ok: false, error: 'path must be under /api/v1/' }
  if (path.includes('..') || path.includes('//') || /\s/.test(path)) {
    return { ok: false, error: 'path is malformed' }
  }
  return { ok: true, method, path }
}

// hasReqId reports whether a message carries a usable request id (so the host can
// correlate its reply). Kept separate so a malformed reqId doesn't reject an
// otherwise-valid request outright — the host just drops it.
export function reqIdOf(msg: unknown): string | number | null {
  if (!msg || typeof msg !== 'object') return null
  const id = (msg as Record<string, unknown>).reqId
  return typeof id === 'string' || typeof id === 'number' ? id : null
}
