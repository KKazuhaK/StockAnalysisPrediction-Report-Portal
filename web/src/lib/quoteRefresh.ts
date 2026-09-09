import { ApiError } from '../api/client'

export interface QuoteRefreshAdvice {
  refreshAfterSecs?: number
  refreshAt?: string
}

export const QUOTE_UNKNOWN_RETRY_SECS = 120

export function readQuoteRefreshAdvice(value: unknown): QuoteRefreshAdvice | null {
  if (!value || typeof value !== 'object') return null
  const body = value as Record<string, unknown>
  const after = body.refreshAfterSecs
  if (typeof after === 'number' && Number.isFinite(after) && after > 0) {
    return { refreshAfterSecs: after }
  }
  const at = body.refreshAt
  if (typeof at === 'string' && at && Number.isFinite(Date.parse(at))) {
    return { refreshAt: at }
  }
  return null
}

export function quoteRefreshDelayMs(advice: QuoteRefreshAdvice | null, nowMs = Date.now()): number | null {
  if (!advice) return null
  if (typeof advice.refreshAfterSecs === 'number' && Number.isFinite(advice.refreshAfterSecs) && advice.refreshAfterSecs > 0) {
    return Math.max(1, Math.ceil(advice.refreshAfterSecs * 1000))
  }
  if (advice.refreshAt) {
    const at = Date.parse(advice.refreshAt)
    if (Number.isFinite(at)) return Math.max(1, at - nowMs)
  }
  return null
}

// A structured server outage carries the same advice as a successful quote. A browser-level
// network failure has no response body, so it gets the conservative unknown-state retry. Permanent
// request refusals do not retry themselves.
export function quoteErrorRefreshAdvice(error: unknown): QuoteRefreshAdvice | null {
  if (error instanceof ApiError) {
    const advised = readQuoteRefreshAdvice(error.data)
    if (advised) return advised
    if (error.code && error.code !== 'quote_unavailable') return null
  }
  return { refreshAfterSecs: QUOTE_UNKNOWN_RETRY_SECS }
}
