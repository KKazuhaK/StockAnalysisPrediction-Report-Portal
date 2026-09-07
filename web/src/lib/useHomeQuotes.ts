import { useEffect, useMemo, useState } from 'react'
import { api, qs } from '../api/client'

// The home feed's live prices: ONE batch request for the symbols currently on screen (ADR 0028's
// fetch-on-read, widened to a page's worth of codes by GET /api/quotes).
//
// Everything here is built around a single rule: THE FEED MUST NEVER WAIT ON A VENDOR. The home
// page is the busiest surface in the portal and a price on a card is a decoration on it, so this
// hook is deliberately incapable of reporting a failure. It returns a map, it starts empty, and a
// dead vendor, a refused request, a switched-off feature and a response this build cannot read all
// converge on the same observable state as "the answer has not arrived yet": an empty map, which
// every card renders as no price at all. There is no error out-parameter for the page to render,
// because there is no honest thing to render — a banner saying a third-party quote feed is down,
// over somebody's report list, is noise about a decoration.
//
// One request, not one per card, and not one per page view of the same symbols: the page passes a
// fresh array on every render (data.groups is replaced by the 60s home poll even when nothing
// changed), so the effect keys on a STRING derived from that array rather than on the array
// itself. Same symbols, same key, no second request.

/**
 * What a card prints. Three fields, and all three come from the vendor.
 *
 * `last` and `change` are integer 分 — the schema has no REAL column and a float64 never touches a
 * price anywhere in this system (ADR 0028 §10), so the division by 100 happens once, in money.ts,
 * at the moment the number becomes text. `changePct` is the vendor's own STRING and is rendered
 * verbatim: recomputing it from a price and a previous close prints a double-digit crash on every
 * ex-rights morning, which is the whole argument of ADR 0028 §4. The previous close is not even
 * carried here, so nothing downstream is able to try.
 */
export interface CardQuote {
  symbol: string
  /** Last traded price, in 分. */
  last: number
  /** Signed move against the vendor's previous close, in 分. The colour's sign comes from HERE. */
  change: number
  /** The vendor's own percentage string, e.g. "0.12" — printed with a '%' and never re-derived. */
  changePct: string
}

/**
 * The most symbols one request may name.
 *
 * The server caps the batch and REFUSES beyond it rather than truncating silently, which is the
 * right server behaviour and the wrong thing to walk into: one over-sized request would cost every
 * card on the page its price, not just the tail. Asking for the first 50 instead keeps the rest, and
 * a card with no quote already renders exactly as it does today.
 *
 * This is defence in depth and not a live hazard, which is worth saying plainly rather than
 * inventing a scenario: the feed's page size is NOT free-form. `apiHome` allowlists it to 15, 30 or
 * 50 and falls back to 30 for anything else (internal/app/server.go), so `?size=200` yields thirty
 * groups and the SPA cannot reach this cap today. It guards the day that allowlist grows a larger
 * option, which is a one-line change nobody would think to make here.
 *
 * 50 is the server's `quoteBatchMax` (quote_api.go). The server counts the symbols it was SENT and
 * this counts them after de-duplication, so a page under this cap is always under the server's one;
 * a build that raised the number here alone would go back to earning a refusal for the whole page,
 * which is why HomePage.test.tsx pins the count a page of 60 codes actually asks for.
 */
const HOME_QUOTES_MAX = 50

// One shared empty map for every no-answer outcome. setState bails out when the next value is
// Object.is-equal to the current one, so a failed batch on a page that already had no prices
// re-renders nothing at all.
const NO_QUOTES: ReadonlyMap<string, CardQuote> = new Map()

/**
 * One entry of the batch, or null when this build cannot read it.
 *
 * The endpoint promises ONE per-symbol shape: `apiQuotes` maps every entry through `quoteCardOf`,
 * which flattens the price fields onto a `QuoteCard` precisely so nothing on the home page can
 * start depending on a field that endpoint is not promising to serve. So this reads that shape and
 * nothing else — the symbol arrives as the KEY of a keyed body, and is taken from the entry only
 * when it carries one. Anything past that — a missing price, a number where a string belongs, a
 * per-symbol failure the server encoded as an object of its own — returns null and that symbol
 * simply has no quote. This is the decoration's contract with the page: unreadable and absent are
 * the same thing, and neither throws during render.
 */
function readOne(key: string, v: unknown): CardQuote | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  const symbol = typeof o.symbol === 'string' && o.symbol ? o.symbol : key
  const { last, change, changePct } = o
  if (!symbol) return null
  if (typeof last !== 'number' || typeof change !== 'number' || typeof changePct !== 'string') return null
  return { symbol, last, change, changePct }
}

/**
 * The batch body as a symbol → quote map.
 *
 * The single place that decides what counts as a quote, and the reason a body of the wrong shape
 * leaves the cards alone instead of taking the feed down with a read off undefined. An array is
 * refused rather than walked: `Object.entries` would key its elements by their INDEX, so a body of
 * the wrong shape would quietly enter the map under the symbol "0" instead of being ignored.
 */
function readHomeQuotes(body: unknown): Map<string, CardQuote> {
  const out = new Map<string, CardQuote>()
  if (!body || typeof body !== 'object') return out
  const quotes = (body as Record<string, unknown>).quotes ?? body
  if (!quotes || typeof quotes !== 'object' || Array.isArray(quotes)) return out
  for (const [k, v] of Object.entries(quotes as Record<string, unknown>)) {
    const one = readOne(k, v)
    if (one) out.set(one.symbol, one)
  }
  return out
}

/**
 * The `symbols=` value for one page of cards: de-duplicated, in first-appearance order, capped.
 *
 * De-duplication is not a micro-optimisation. The same code legitimately appears on several cards
 * at once — a stock with reports on three dates is three groups in the feed — and naming it three
 * times would send a third-party vendor a list that describes the FEED rather than the set of
 * instruments, for no extra information in return.
 */
function homeQuoteKey(symbols: readonly string[]): string {
  const seen = new Set<string>()
  for (const s of symbols) {
    if (s) seen.add(s)
    if (seen.size >= HOME_QUOTES_MAX) break
  }
  return [...seen].join(',')
}

/**
 * Live prices for the symbols on screen. Never throws, never blocks, never reports a failure.
 *
 * Pass whatever the current page renders — a fresh array on every render is fine, because the
 * request is keyed by the symbols themselves rather than by the array's identity.
 */
export function useHomeQuotes(symbols: readonly string[]): ReadonlyMap<string, CardQuote> {
  const key = useMemo(() => homeQuoteKey(symbols), [symbols])
  const [quotes, setQuotes] = useState<ReadonlyMap<string, CardQuote>>(NO_QUOTES)

  useEffect(() => {
    // No symbols on screen (an empty feed, or a page of thematic reports that name no code) means
    // no request at all — not an empty one, which the server answers 400 `quote_bad_symbol` for
    // (quote_api.go): a page with nothing to ask about would otherwise spend a round trip earning a
    // refusal on every view. The previous map is left where it is rather than cleared: nothing on
    // this page can look a symbol up in it, and clearing would only cost a render.
    if (!key) return
    // The same cancelled flag StockPage uses for its quote, and for the same reason, one page up:
    // a filter change or a page step supersedes the request in flight, and without this the older
    // answer can land LAST and paint prices belonging to a card list that is no longer on screen.
    let cancelled = false
    api
      .get(`/api/quotes${qs({ symbols: key })}`)
      .then((body) => {
        if (!cancelled) setQuotes(readHomeQuotes(body))
      })
      .catch(() => {
        // Deliberately the whole handler. A vendor outage, a 503 from the gate, a refusal because
        // an operator switched the feature off, an aborted navigation — the cards are identical in
        // every one of those cases, which is the promise this feature was allowed to ship on.
        if (!cancelled) setQuotes(NO_QUOTES)
      })
    return () => {
      cancelled = true
    }
  }, [key])

  return quotes
}
