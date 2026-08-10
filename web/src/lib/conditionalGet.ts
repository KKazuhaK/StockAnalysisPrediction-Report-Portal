import { ApiError } from '../api/client'

// A GET that can come back "nothing changed".
//
// The console polls the queue every few seconds for something that changes far less often than it
// asks. The server answers such a poll with 304 and an empty body (writeJSONIfChanged), and this is
// the other half of that arrangement: it remembers the ETag per URL, sends it back, and reports
// UNCHANGED so the caller can keep the object it already has — which is the real win, since an
// unchanged object means no parse, no setState and no re-render.
//
// This is deliberately not folded into api.get. /api/ is Cache-Control: no-store, so the browser's
// own cache does not participate; this revalidation is an explicit arrangement between one poller
// and one endpoint, and a caller that has not been written to handle UNCHANGED must not silently
// receive it.

export const UNCHANGED = Symbol('unchanged')

const tags = new Map<string, string>()

/** Forget the stored tags — after a mutation, or between tests. */
export function forgetTags(url?: string): void {
  if (url) tags.delete(url)
  else tags.clear()
}

export async function getIfChanged<T>(url: string): Promise<T | typeof UNCHANGED> {
  const known = tags.get(url)
  const res = await fetch(url, {
    credentials: 'same-origin',
    headers: known ? { 'If-None-Match': known } : undefined,
  })
  if (res.status === 304) return UNCHANGED

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    // A failed response must not leave a tag behind: the next poll has to ask properly.
    tags.delete(url)
    const body = data as { error?: string; code?: string } | null
    throw new ApiError(res.status, body?.error || res.statusText || 'request failed', body?.code)
  }
  const tag = res.headers.get('ETag')
  if (tag) tags.set(url, tag)
  else tags.delete(url)
  return data as T
}
