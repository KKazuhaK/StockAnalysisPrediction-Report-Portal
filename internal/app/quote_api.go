package app

// quote_api.go —— GET /api/quote/{symbol} and GET /api/quotes, the SPA's only doors to the vendors.
//
// Both sit behind requireUserJSON for the same reason GET /api/stock/{symbol} does. The quotes
// themselves are public information, but the endpoints are not: they make this server issue outbound
// requests on the caller's behalf, and an unauthenticated one is a free open relay pointed at
// Tencent from our address, with our IP being the one that gets rate-limited or blocked.

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The six ranges the UI offers. The four daily ones are in trading days: a month is about 21 trading
// days and a year about 243, so these are the round numbers nearest to 1/3/6/12 months of sessions —
// the series is drawn, not counted, and asking for exactly 243 would buy nothing.
const (
	quoteRange1M = 22
	quoteRange3M = 66
	quoteRange6M = 125
	quoteRange1Y = 250

	// The two intraday ranges are in POINTS rather than sessions, and both are 400 because 400 is
	// what a session of them measured: Yahoo answered 391 points for AAPL at 1d/1m and 400 for
	// 0700.HK, 331 for 600519.SS at 5d/5m, and Tencent's 分时 endpoint answered 267 for sh600519 and
	// 332 for hk00700. A ceiling above every measured answer trims nothing off any of them, which is
	// what it is for: the number is a REQUEST for points, and the surplus is trimmed from the front
	// of whatever the vendor actually sent.
	//
	// They are two constants with the same value on purpose. Making one an alias of the other would
	// tie two independent measurements together, and the next re-measurement would move both.
	quoteRange1D = 400
	// Five sessions at the 5-minute bucket both sources answer this label with. Measured against the
	// committed fixtures rather than reasoned from session lengths: Yahoo sends 331 points, and
	// Tencent's five days bucket to 280 on Shanghai and Shenzhen and 340 on Hong Kong. The cap has
	// to clear the LARGEST of those or a Hong Kong week silently loses its first morning off the
	// front of the trim and is drawn as four and a half days under a 5日 label.
	quoteRange5D = 500

	// quoteRangeDefault is what an absent or unrecognised range= falls back to. Three months is the
	// span the chart is designed around, and falling back is deliberately silent: a range the SPA
	// does not know about is a stale bookmark or an older bundle, and refusing to draw a chart over
	// a query-string typo would be a worse answer than drawing the default one.
	quoteRangeDefault = "3m"
)

// quoteRangeSpec is what one range= value asks for: how many points, and at what resolution.
//
// The interval is part of the RANGE rather than of the market now, and that is the whole of stage 2.
// It used to be derivable from the market alone because every range was a daily one; 分时 and 5日
// are the same market and the same code asking a different question, and a source that can answer
// one of them cannot necessarily answer the other (quote_cache.go's declarations).
type quoteRangeSpec struct {
	bars int
	// interval is the resolution this range ASKS FOR, and every row names one — the four daily ranges
	// included. They used to leave it empty and let the market decide, which read as caution and was
	// the bug that made stage 1 unreachable: quoteMarket.history is a fact about what fqkline and
	// Sina's kline answer, so a US 3个月 request asked for a snapshot even on a deployment with a
	// source that serves US dailies enabled, and turning Yahoo on changed nothing the reader could see.
	//
	// What a range asks for is a property of the RANGE. Whether this deployment can serve it is a
	// different question with a different owner — servableInterval, one line later in apiQuote, which
	// degrades to a snapshot exactly when no enabled source declares the pair. That keeps Beijing and
	// an unconfigured portal's US behaving as they do today and lets the US chart appear the moment an
	// operator enables a source for it, with no second statement of the same fact to fall out of step.
	interval quoteInterval
}

// quoteRanges maps range= to what to ask for.
//
// The map is the whole allowlist: the number interpolated into a vendor URL is one of these six
// constants and never anything derived from the query string, so no caller can ask us to pull ten
// thousand bars from Tencent by editing a URL. quoteBarCount clamps it again downstream, which is
// belt and braces on purpose — this map is the thing an edit could widen by accident.
var quoteRanges = map[string]quoteRangeSpec{
	"1d": {bars: quoteRange1D, interval: quoteIntervalIntraday},
	"5d": {bars: quoteRange5D, interval: quoteIntervalIntraday5D},
	"1m": {bars: quoteRange1M, interval: quoteIntervalDaily},
	"3m": {bars: quoteRange3M, interval: quoteIntervalDaily},
	"6m": {bars: quoteRange6M, interval: quoteIntervalDaily},
	"1y": {bars: quoteRange1Y, interval: quoteIntervalDaily},
}

// quoteRangeFor is the one place a query string becomes a request, so that the bar count and the
// interval cannot fall back differently. An unknown range used to pick the default bar count while
// anything reading the interval separately would have had to repeat the same fallback — and a
// mismatch there is a 分时 number of points fetched as a daily series.
func quoteRangeFor(rangeKey string) quoteRangeSpec {
	spec, ok := quoteRanges[rangeKey]
	if !ok {
		spec = quoteRanges[quoteRangeDefault]
	}
	spec.bars = quoteBarCount(spec.bars)
	return spec
}

// quoteBarsForRange is the bar count alone, for callers that only need that half.
func quoteBarsForRange(rangeKey string) int { return quoteRangeFor(rangeKey).bars }

// apiQuote answers one stock's live quote plus its series. GET /api/quote/{symbol}?range=3m
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
	spec := quoteRangeFor(r.URL.Query().Get("range"))

	// The reader's own window, when they picked one. It REPLACES the range rather than narrowing it:
	// from= and to= say which sessions, so the range key's bar count and interval have nothing left
	// to contribute, and honouring both would let a 近1月 button silently truncate a year-wide window
	// the reader chose.
	//
	// Refused rather than ignored, and rather than clamped. A malformed or absurd window that this
	// endpoint quietly turned into the default range would draw the DEFAULT window under the
	// reader's own dates, which is the one outcome a chart must never produce.
	win, err := quoteParseWindow(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "quote_bad_range", err.Error())
		return
	}
	if win.bounded() {
		spec = quoteRangeSpec{bars: quoteMaxBars, interval: quoteIntervalDailyRange}
	}

	// The operator's source order and TTLs, read per request so a save on the 行情 panel takes
	// effect on the next page view rather than at the next restart.
	cfg := s.quoteConfigLoad()
	// What was ASKED FOR, then what can actually be SERVED. Both halves matter and they fail in
	// opposite directions.
	//
	// Asking for what the RANGE means is what makes a capability reachable: 3个月 on a US ticker asks
	// for a daily series, so the day an operator enables a source that declares (us, daily) the chart
	// appears, and on a portal that has not it degrades below. Reading the interval off the market
	// instead — which is what this used to do — meant the US asked for a price whoever was enabled,
	// and stage 1 was a panel column an operator could switch on for no effect.
	//
	// Narrowing it to what an enabled source declares is what keeps that from becoming a 503: 分时 with
	// nothing but Tencent enabled is a Beijing code or a US ticker, and 3个月 on the US is the same
	// shape of gap. Answering "the quote sources are unreachable, please retry" about a gap no amount
	// of retrying closes is a worse answer than the price plus an explicit empty chart.
	iv := s.quotes.servableInterval(cfg, target, spec.interval)

	resp, cached, ttl, err := s.quotes.fetchUnder(r.Context(), cfg, market, code, spec.bars, iv, win)
	if err != nil {
		// Every source failed — transport, a non-2xx, or the drift gate, which counts as a failure
		// and not a warning. 503 rather than 502 or 500: nothing is wrong with the request and
		// nothing is wrong with this server, the answer is simply not available right now, and that
		// is the one thing worth telling the browser because it is the one that means "retry".
		s.writeQuoteUnavailable(w, target, iv, cfg)
		return
	}

	// A COPY. The value behind resp is shared with every other request holding the same cache entry,
	// so stamping Cached on it directly would race with a concurrent reader and would also leave a
	// cached=true entry behind for whoever fetched it first. The copy is shallow and that is fine:
	// Bars is never written after the parser returns it.
	out := *resp
	out.Cached = cached
	out.Bars = quoteVisibleBars(iv, resp.Bars)
	advice := quoteRefreshAdviceFor(s.quotes.clockNow(), s.quoteAutoRefresh(), cfg, target, iv, ttl, resp)
	out.RefreshAfterSecs = advice.RefreshAfterSecs
	out.RefreshAt = advice.RefreshAt
	if iv == quoteIntervalSnapshot {
		// A snapshot request NEVER carries bars, whatever a vendor decides to start returning.
		//
		// Every range in the table asks for a series, so arriving here is always a DEGRADATION —
		// the reader wanted a chart and this deployment cannot draw one. Neither reason below is
		// worth retrying, which is what separates both from source_failed, but only one of them is
		// a fact about the market:
		//
		//   - a DAILY range on a market whose daily series is not worth drawing. Beijing answers
		//     "day":[] on Tencent and a sixteen-month-stale series on Sina; the US answers a
		//     sixty-bar request with two rows fifteen years apart. A standing gap, true whatever an
		//     operator enables, and market_unsupported says so.
		//   - anything else: a WINDOW no enabled source declares. The market has the data and this
		//     deployment has no source for it. Saying "this market has no historical data" there was
		//     wrong in the one way that costs somebody an afternoon — it is the sentence an operator
		//     reads after enabling a source FOR that window, and it did not change.
		out.BarsSource = ""
		if !spec.interval.intraday() && !quoteMarketHasDailyHistory(target.Market.id) {
			out.BarsUnavailable = quoteBarsMarketUnsupported
		} else {
			out.BarsUnavailable = quoteBarsIntervalUnsupported
		}
	}

	// Cache-Control carries what is LEFT of the server's own TTL, never the full one. A browser told
	// max-age=300 by an entry the server will drop in four seconds would sit on a stale price for
	// almost five minutes longer than anything here intended. private, because the response went
	// through a session cookie and has no business in a shared proxy's store.
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(quoteMaxAgeSeconds(ttl)))
	writeJSON(w, &out)
}

func (s *Server) writeQuoteUnavailable(w http.ResponseWriter, target quoteTarget, iv quoteInterval, cfg quoteConfig) {
	now := s.quotes.clockNow()
	advice := quoteRefreshAdviceFor(now, s.quoteAutoRefresh(), cfg, target, iv, 0, nil)
	if due, ok := quoteAdviceDue(now, advice); ok {
		w.Header().Set("Retry-After", strconv.Itoa(quoteRefreshSeconds(due.Sub(now))))
	}
	body := map[string]any{
		"error": "行情源暂时不可用，请稍后再试",
		"code":  "quote_unavailable",
	}
	quoteAdviceMap(body, advice)
	writeJSONStatus(w, http.StatusServiceUnavailable, body)
}

// quoteVisibleBars is the never-null guarantee the TypeScript contract depends on: bars is
// QuoteBar[], and a JSON null there is a runtime error in the chart rather than an empty chart. It
// is also where a request that asked for no series at all loses the bars a vendor sent anyway.
func quoteVisibleBars(iv quoteInterval, bars []QuoteBar) []QuoteBar {
	if bars == nil || iv == quoteIntervalSnapshot {
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

// ---------- the batch, for a page of cards ----------

// quoteBatchMax is the most symbols one request may name, and a request naming more is REFUSED
// rather than truncated.
//
// Truncating is the tempting behaviour and it is the wrong one: the caller has no way to tell which
// half of its list was answered, so the tail of the page renders as cards whose prices never arrive
// and never will — indistinguishable from a vendor being slow, forever. A refusal is a thing the
// caller can see. The client caps its own list at the same fifty (web/src/lib/useHomeQuotes.ts) so
// that an over-sized page loses the tail's decoration instead of the whole page's.
//
// Fifty is the vendor's number rather than ours: it is comfortably inside what one q= URL carries,
// and it is more codes than a page of the feed has ever shown.
const quoteBatchMax = 50

// QuoteCard is one symbol as a home card needs it: a price, a move, and enough provenance to render
// it honestly. It is deliberately NOT a QuoteResp — fifty of those would carry fifty series the
// cards do not draw, and a `bars` field on a card is a field somebody eventually renders.
//
// The percentage is the vendor's own string, carried verbatim exactly as it is on the single
// endpoint; see QuoteSnapshot.ChangePct for the ex-rights argument that makes recomputing it a
// fabricated crash. The sign shown to a reader comes from Change, which is an integer.
type QuoteCard struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Market   string `json:"market"`
	Currency string `json:"currency"`
	Kind     string `json:"kind"`

	Last      int64  `json:"last"`
	Change    int64  `json:"change"`
	ChangePct string `json:"changePct"`

	AsOf    string `json:"asOf"`
	Session string `json:"session"`
	Source  string `json:"source"`
	Cached  bool   `json:"cached"`
}

// apiQuotes answers many symbols at once. GET /api/quotes?symbols=601899,000001,00700
//
// It exists for the home feed, which shows a page of report cards and wants a price on each. Three
// things it does differently from the single endpoint, and each has a page-shaped reason:
//
//   - ONE upstream call for the whole list (quote_cache.go's fetchBatchUnder), because fifty cards
//     must not be fifty conversations with a vendor from one address.
//   - PER-SYMBOL results. A code that fails — delisted, refused by the drift gate, unknown to the
//     vendor — is absent from `quotes` and named in `missing`, and the other forty-nine are
//     unaffected. A batch that failed as a unit would blank a whole page over one bad row.
//   - NO series. A card draws a number, not a chart.
//
// And one thing it does not do: it never fails the request over a vendor. A dead vendor answers with
// an empty `quotes` map and HTTP 200, because the feed renders identically either way and there is
// nothing a reader could do with an error about a decoration.
func (s *Server) apiQuotes(w http.ResponseWriter, r *http.Request, _ string) {
	out := map[string]any{"enabled": true}
	// The operator's switch, and it is checked BEFORE anything is parsed or fetched, because what it
	// governs is the outbound disclosure itself: with it off, opening the home page must send no
	// codes anywhere. Answering 200 with nothing rather than an error is deliberate — the feed's
	// hook cannot render a failure and must not (a banner about a third-party feed over somebody's
	// report list is noise), so "switched off" and "not arrived yet" are correctly the same thing to
	// it. An operator with curl sees the difference in `enabled`.
	if !s.quoteHomeCards() {
		out["enabled"] = false
		out["quotes"] = map[string]QuoteCard{}
		writeJSON(w, out)
		return
	}

	raw := strings.Split(r.URL.Query().Get("symbols"), ",")
	asked := make([]string, 0, len(raw))
	for _, field := range raw {
		if sym := strings.TrimSpace(field); sym != "" {
			asked = append(asked, sym)
		}
	}
	if len(asked) == 0 {
		jsonErrorCode(w, http.StatusBadRequest, "quote_bad_symbol", "symbols= 不能为空")
		return
	}
	if len(asked) > quoteBatchMax {
		// Refused, with the count named so a caller can see what it did. It reuses quote_bad_symbol
		// rather than introducing a code of its own, and that is a deliberate trade rather than
		// laziness: every jsonErrorCode needs an err.<code> string in all three locale bundles
		// (wired_settings_test.go fails the build otherwise), this change does not own those
		// bundles, and a refusal rendered in the server's own language to an English admin is worse
		// than a slightly wide code. The message says what actually happened.
		jsonErrorCode(w, http.StatusBadRequest, "quote_bad_symbol",
			"一次最多查询 "+strconv.Itoa(quoteBatchMax)+" 个代码")
		return
	}

	targets := make([]quoteTarget, 0, len(asked))
	missing := []string{}
	seen := map[string]bool{}
	for _, want := range asked {
		target, err := quoteResolve(want)
		if err != nil {
			// One bad symbol does not sink the batch. It is named in `missing` rather than silently
			// dropped, because a card with no price and no explanation is what this endpoint exists
			// to stop being the only observable outcome.
			missing = append(missing, want)
			continue
		}
		if seen[target.Symbol] {
			// The same code legitimately appears on several cards — a stock with reports on three
			// dates is three groups in the feed. This dedupe is about the ANSWER and not about the
			// vendor: fetchBatchUnder dedupes again before a URL is built, so the vendor sees one
			// mention whether or not this loop bothers, and it is that one — not this one — that
			// makes the call count a promise. What this shapes is `missing` below, which is built by
			// walking these targets and never reaches the cache — without it, a code on three cards
			// that nothing answers for is named three times in a list the page renders.
			continue
		}
		seen[target.Symbol] = true
		targets = append(targets, target)
	}

	quotes := map[string]QuoteCard{}
	cfg := s.quoteConfigLoad()
	now := s.quotes.clockNow()
	autoRefresh := s.quoteAutoRefresh()
	answered := s.quotes.fetchBatchUnder(r.Context(), cfg, targets)
	advices := make([]quoteRefreshAdvice, 0, len(targets))
	for _, target := range targets {
		entry, ok := answered[target.Symbol]
		if !ok || entry.resp == nil {
			// A symbol that RESOLVED and was not answered is named by its vendor symbol, while one
			// that never resolved is named by the caller's own spelling above. The two are different
			// facts and the spelling says which: "sh600519" is a code this portal understood and no
			// vendor answered about, and "12345678" is one it refused before asking anybody.
			missing = append(missing, target.Symbol)
			advices = append(advices, quoteRefreshAdviceFor(now, autoRefresh, cfg, target, quoteIntervalSnapshot, 0, nil))
			continue
		}
		// Keyed by the VENDOR SYMBOL and not by the code, because a code is not unique across
		// markets: 000001 is 平安银行 on Shenzhen and 上证指数 on Shanghai, and a map keyed by six
		// digits would keep whichever of the two came second. The `symbol` field inside carries the
		// code, which is what the feed looks a card up by.
		quotes[target.Symbol] = quoteCardOf(entry)
		advices = append(advices, quoteRefreshAdviceFor(now, autoRefresh, cfg, target, quoteIntervalSnapshot, entry.ttl, entry.resp))
	}
	out["quotes"] = quotes
	out["missing"] = missing
	quoteAdviceMap(out, quoteAggregateAdvice(now, advices))
	// no-store rather than a max-age: this body is fifty answers with fifty different remaining
	// lifetimes, and any single number here would be wrong for most of them. The server's own cache
	// is what keeps the vendor call count down; the browser re-asks and is served from it.
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, out)
}

// quoteCardOf narrows one cached answer to what a card renders. Reading the fields out one at a time
// rather than embedding the snapshot is the point: a card carries no bars, no open/high/low and no
// turnover, so nothing on the home page can start depending on a field this endpoint is not
// promising to serve.
func quoteCardOf(entry quoteBatchEntry) QuoteCard {
	resp := entry.resp
	return QuoteCard{
		Symbol:    resp.Symbol,
		Name:      resp.Name,
		Market:    resp.Market,
		Currency:  resp.Currency,
		Kind:      resp.Kind,
		Last:      resp.Snapshot.Last,
		Change:    resp.Snapshot.Change,
		ChangePct: resp.Snapshot.ChangePct,
		AsOf:      resp.Snapshot.AsOf,
		Session:   resp.Snapshot.Session,
		Source:    resp.Source,
		Cached:    entry.cached,
	}
}
