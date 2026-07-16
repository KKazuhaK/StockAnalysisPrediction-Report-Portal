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
// The host re-posts this whenever the theme changes so an app can follow the host's
// light/dark and colours live, not just on first load (ADR 0003 phase 2).
export const THEME_MESSAGE = 'rp:theme'

export interface ApiRequestMsg {
  type: typeof API_MESSAGE
  reqId: string | number
  method?: string
  path: string
  body?: unknown
}

// ThemePayload is handed to an app on init and on every theme change so it can match
// the host without any API access.
export interface ThemePayload {
  dark: boolean
  colorPrimary: string
  colorBg: string
  colorText: string
  colorBorder: string
  colorBgLayout: string
  borderRadius: number
}

export type Validated =
  | { ok: true; method: string; path: string; body?: unknown }
  | { ok: false; error: string }

// Methods an app may use to write. The /api/v1 write surface is POST (ingest) and
// DELETE (delete report) only; PATCH/PUT are never exposed.
const WRITE_METHODS = new Set(['POST', 'DELETE'])

// validateApiRequest gates a message from an app before the host calls the API.
// Reads (GET) need the `query` scope; writes (POST/DELETE under /api/v1/) need the
// `ingest` scope (ADR 0003 phase 2). Any other method, any path outside /api/v1/,
// or a missing scope is refused here — a second, authoritative check happens
// server-side via the token's scope.
export function validateApiRequest(msg: unknown, scopes: string[]): Validated {
  if (!msg || typeof msg !== 'object') return { ok: false, error: 'not a message' }
  const m = msg as Record<string, unknown>
  if (m.type !== API_MESSAGE) return { ok: false, error: 'unknown message type' }
  const method = String(m.method || 'GET').toUpperCase()
  if (method === 'GET') {
    if (!scopes.includes('query')) return { ok: false, error: 'app lacks the query scope' }
  } else if (WRITE_METHODS.has(method)) {
    if (!scopes.includes('ingest')) return { ok: false, error: 'app lacks the ingest scope' }
  } else {
    return { ok: false, error: `method ${method} not permitted` }
  }
  if (typeof m.path !== 'string') return { ok: false, error: 'path must be a string' }
  const rawPath = m.path
  // Must be a same-origin API path, not an absolute/protocol-relative URL, and
  // must not try to climb out of the v1 namespace. URL parsing is required here:
  // browsers normalize encoded dot segments before fetch, so a raw string check alone
  // would accept /api/v1/%2e%2e/admin and then request /api/admin.
  if (!rawPath.startsWith('/api/v1/') || /\s/.test(rawPath)) {
    return { ok: false, error: 'path is malformed' }
  }
  let decoded = rawPath
  try {
    // Reject double-encoded traversal too. Two passes cover the browser/server decoding
    // boundary without accepting an ambiguous path that different stacks parse differently.
    for (let i = 0; i < 2; i += 1) decoded = decodeURIComponent(decoded)
  } catch {
    return { ok: false, error: 'path is malformed' }
  }
  if (decoded.includes('..') || decoded.includes('//') || decoded.includes('\\')) {
    return { ok: false, error: 'path is malformed' }
  }
  let url: URL
  try {
    url = new URL(rawPath, 'https://app-bridge.invalid')
  } catch {
    return { ok: false, error: 'path is malformed' }
  }
  if (url.origin !== 'https://app-bridge.invalid' || !url.pathname.startsWith('/api/v1/') || url.hash) {
    return { ok: false, error: 'path must be under /api/v1/' }
  }
  return { ok: true, method, path: url.pathname + url.search, body: m.body }
}

// hasReqId reports whether a message carries a usable request id (so the host can
// correlate its reply). Kept separate so a malformed reqId doesn't reject an
// otherwise-valid request outright — the host just drops it.
export function reqIdOf(msg: unknown): string | number | null {
  if (!msg || typeof msg !== 'object') return null
  const id = (msg as Record<string, unknown>).reqId
  return typeof id === 'string' || typeof id === 'number' ? id : null
}
