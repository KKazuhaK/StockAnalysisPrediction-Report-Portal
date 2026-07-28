// Lightweight fetch wrapper: same-origin cookie session, JSON encode/decode, throws ApiError(status, message) on error.

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
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
    throw new ApiError(res.status, msg)
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
    throw new ApiError(res.status, msg)
  }
  return data as T
}

export const api = {
  get: <T = any>(url: string) => request<T>('GET', url),
  post: <T = any>(url: string, body?: unknown) => request<T>('POST', url, body ?? {}),
  put: <T = any>(url: string, body?: unknown) => request<T>('PUT', url, body ?? {}),
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
