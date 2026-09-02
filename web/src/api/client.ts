// Lightweight fetch wrapper: same-origin cookie session, JSON encode/decode, throws ApiError(status, message) on error.

export class ApiError extends Error {
  status: number
  /**
   * A stable machine-readable reason from the server, when it sent one. The `message` cannot be
   * localized — the portal speaks three languages and the server writes the response before the
   * browser's choice is known — so anything user-facing should render the CODE through t() and
   * fall back to the message. See jsonErrorCode in internal/app/apiui.go.
   */
  code?: string
  /**
   * The parsed response body, when the server sent JSON. A refusal sometimes has to carry more than
   * a reason — a collision answers with the id of the row you collided with, so the UI can offer to
   * open it — and squeezing that into `message` gives the caller a string it cannot act on.
   */
  data?: unknown
  constructor(status: number, message: string, code?: string, data?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.data = data
    this.name = 'ApiError'
  }
}

/**
 * errText turns a thrown error into something worth showing a user: the translation of its server
 * code when there is one, otherwise the server's own message, otherwise a generic line.
 */
export function errText(e: unknown, t: (k: string, o?: Record<string, unknown>) => string, fallbackKey = 'common.error'): string {
  if (e instanceof ApiError) {
    if (e.code) {
      const key = `err.${e.code}`
      const translated = t(key)
      if (translated !== key) return translated // i18next echoes the key when it has no string
    }
    if (e.message) return e.message
  }
  return t(fallbackKey)
}

// The session can end while the page is open — it simply runs out, an admin revokes it, or the
// account is disabled — and every request after that is a 401. Left to each caller, that is a page
// that renders nothing and says nothing: the app still believes somebody is signed in, so it stays
// where it is with no data and no way back to the login form.
//
// So one place learns it instead. The gate every session-backed endpoint goes through answers with
// the `session_expired` code (requireUserJSON), and only that code fires this — a wrong password at
// the login form and a failed step-up are 401s too, and neither means the session is gone.
let sessionLost: (() => void) | null = null

export function onSessionLost(fn: (() => void) | null) {
  sessionLost = fn
}

// SESSION_GONE is the server's word for it, not an inference from the status code.
export const SESSION_GONE = 'session_expired'

export function noteSessionLost(status: number, code?: string) {
  if (status === 401 && code === SESSION_GONE) sessionLost?.()
}

async function request<T>(method: string, url: string, body?: unknown, extraHeaders?: Record<string, string>): Promise<T> {
  const headers: Record<string, string> = { ...(extraHeaders ?? {}) }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(url, {
    method,
    headers: Object.keys(headers).length ? headers : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg = (data && typeof data === 'object' && data.error) || res.statusText || 'request failed'
    const code = data && typeof data === 'object' && typeof data.code === 'string' ? data.code : undefined
    noteSessionLost(res.status, code)
    throw new ApiError(res.status, msg, code, data)
  }
  return data as T
}

async function requestForm<T>(method: string, url: string, body: FormData): Promise<T> {
  const res = await fetch(url, { method, body, credentials: 'same-origin' })
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg = (data && typeof data === 'object' && data.error) || res.statusText || 'request failed'
    const code = data && typeof data === 'object' && typeof data.code === 'string' ? data.code : undefined
    noteSessionLost(res.status, code)
    throw new ApiError(res.status, msg, code, data)
  }
  return data as T
}

export const api = {
  get: <T = any>(url: string) => request<T>('GET', url),
  post: <T = any>(url: string, body?: unknown) => request<T>('POST', url, body ?? {}),
  put: <T = any>(url: string, body?: unknown) => request<T>('PUT', url, body ?? {}),
  // PATCH for a partial update — a review records a verdict on one field of one row, and PUT would
  // claim to be replacing the whole thing.
  patch: <T = any>(url: string, body?: unknown) => request<T>('PATCH', url, body ?? {}),
  del: <T = any>(url: string) => request<T>('DELETE', url),
  // Step-up: a credential change re-proves a factor inside the session. The proof travels in a
  // header — never the query string, which lands in proxy logs, browser history and the Referer of
  // every subresource.
  stepUp: <T = any>(method: 'POST' | 'DELETE', url: string, proof: string, body?: unknown) =>
    request<T>(method, url, method === 'POST' ? (body ?? {}) : body, { 'X-Step-Up-Proof': proof }),
  upload: <T = any>(url: string, body: FormData) => requestForm<T>('POST', url, body),
}

// qs builds a query string from a filter object (skipping empty values).
export function qs(params: Record<string, string | number | undefined | null>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}
