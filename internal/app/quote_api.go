package app

// quote_api.go —— GET /api/quote/{symbol}, the SPA's only door to the vendors.
//
// It sits behind requireUserJSON for the same reason GET /api/stock/{symbol} does. The quotes
// themselves are public information, but the endpoint is not: it makes this server issue outbound
// requests on the caller's behalf, and an unauthenticated one is a free open relay pointed at
// Tencent from our address, with our IP being the one that gets rate-limited or blocked.

import (
	"net/http"
	"strconv"
	"time"
)

// The four ranges the UI offers, in trading days. A month is about 21 trading days and a year about
// 243, so these are the round numbers nearest to 1/3/6/12 months of sessions — the series is drawn,
// not counted, and asking for exactly 243 would buy nothing.
const (
	quoteRange1M = 22
	quoteRange3M = 66
	quoteRange6M = 125
	quoteRange1Y = 250

	// quoteRangeDefault is what an absent or unrecognised range= falls back to. Three months is the
	// span the chart is designed around, and falling back is deliberately silent: a range the SPA
	// does not know about is a stale bookmark or an older bundle, and refusing to draw a chart over
	// a query-string typo would be a worse answer than drawing the default one.
	quoteRangeDefault = "3m"
)

// quoteRangeBars maps range= to a bar count.
//
// The map is the whole allowlist: the number interpolated into a vendor URL is one of these four
// constants and never anything derived from the query string, so no caller can ask us to pull ten
// thousand bars from Tencent by editing a URL. quoteBarCount clamps it again downstream, which is
// belt and braces on purpose — this map is the thing an edit could widen by accident.
var quoteRangeBars = map[string]int{
	"1m": quoteRange1M,
	"3m": quoteRange3M,
	"6m": quoteRange6M,
	"1y": quoteRange1Y,
}

func quoteBarsForRange(rangeKey string) int {
	if n, ok := quoteRangeBars[rangeKey]; ok {
		return quoteBarCount(n)
	}
	return quoteBarCount(quoteRangeBars[quoteRangeDefault])
}

// apiQuote answers one stock's live quote plus its daily history. GET /api/quote/{symbol}?range=3m
//
// The symbol may be six digits (A-share or index, prefix inferred as it always has been), five
// digits (Hong Kong), one to six letters (US), or an explicit "<market>:<code>" — which is the only
// way to reach the codes whose shape lies, 000001 being 上证指数 on Shanghai and 平安银行 on
// Shenzhen. REPORTS remain A-share only; what widened here is quote VIEWING.
func (s *Server) apiQuote(w http.ResponseWriter, r *http.Request, _ string) {
	target, err := quoteResolve(r.PathValue("symbol"))
	if err != nil {
		// quoteResolve is the URL-construction boundary — it is what refuses a code with a NUL in it
		// before anything concatenates it into a vendor URL — so the browser's 400 is that same
		// judgement rather than a second rule written here that could drift from it. quote_bad_symbol
		// and not a bare 400: the SPA renders err.quote_bad_symbol through t(), and a server-side
		// Chinese message would be the wrong language for two of the three locales this portal ships.
		jsonErrorCode(w, http.StatusBadRequest, "quote_bad_symbol", "股票代码无效")
		return
	}
	// The CANONICAL code from here on, never what the user typed: it is the cache key, the string the
	// vendor echoes back and what the response reports, and "aapl" and "AAPL" have to be one answer
	// rather than two cache entries and two upstream calls.
	market, code := target.Market.id, target.Code
	bars := quoteBarsForRange(r.URL.Query().Get("range"))

	// The operator's source order and TTLs, read per request so a save on the 行情 panel takes
	// effect on the next page view rather than at the next restart.
	resp, cached, ttl, err := s.quotes.fetchUnder(r.Context(), s.quoteConfigLoad(), market, code, bars)
	if err != nil {
		// Every source failed — transport, a non-2xx, or the drift gate, which counts as a failure
		// and not a warning. 503 rather than 502 or 500: nothing is wrong with the request and
		// nothing is wrong with this server, the answer is simply not available right now, and that
		// is the one thing worth telling the browser because it is the one that means "retry".
		jsonErrorCode(w, http.StatusServiceUnavailable, "quote_unavailable", "行情源暂时不可用，请稍后再试")
		return
	}

	// A COPY. The value behind resp is shared with every other request holding the same cache entry,
	// so stamping Cached on it directly would race with a concurrent reader and would also leave a
	// cached=true entry behind for whoever fetched it first. The copy is shallow and that is fine:
	// Bars is never written after the parser returns it.
	out := *resp
	out.Cached = cached
	out.Bars = quoteVisibleBars(market, resp.Bars)
	if !quoteMarketHasDailyHistory(market) {
		// Two markets have no daily series worth drawing, and what arrives from them is worse than
		// nothing because it renders like data: Beijing answers "day":[] on Tencent and a
		// sixteen-month-stale series on Sina, and the US answers a sixty-bar request with two rows
		// fifteen years apart. The promise this endpoint makes is that such a response NEVER carries
		// bars, whatever a vendor decides to start returning, and that the UI is told why in a form
		// it can translate. Asked of the market table rather than of "bj", so the promise covers
		// both markets and any third one measured into the table later.
		out.BarsSource = ""
		out.BarsUnavailable = quoteBarsMarketUnsupported
	}

	// Cache-Control carries what is LEFT of the server's own TTL, never the full one. A browser told
	// max-age=300 by an entry the server will drop in four seconds would sit on a stale price for
	// almost five minutes longer than anything here intended. private, because the response went
	// through a session cookie and has no business in a shared proxy's store.
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(quoteMaxAgeSeconds(ttl)))
	writeJSON(w, &out)
}

// quoteVisibleBars is the never-null guarantee the TypeScript contract depends on: bars is
// QuoteBar[], and a JSON null there is a runtime error in the chart rather than an empty chart. It
// is also where a market with no trustworthy history loses the bars a vendor sent anyway.
func quoteVisibleBars(market string, bars []QuoteBar) []QuoteBar {
	if bars == nil || !quoteMarketHasDailyHistory(market) {
		return []QuoteBar{}
	}
	return bars
}

// quoteMaxAgeSeconds floors a remaining TTL at one second. Zero would tell the browser to revalidate
// immediately, which turns the last moment of every cache entry's life into a stampede — the exact
// thing the header is here to prevent.
func quoteMaxAgeSeconds(ttl time.Duration) int {
	if seconds := int(ttl / time.Second); seconds > 0 {
		return seconds
	}
	return 1
}
