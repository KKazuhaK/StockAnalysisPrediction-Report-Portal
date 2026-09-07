package app

// quote_cache.go —— what stands between a page view and the vendors.
//
// A quote is the one thing in this portal that a browser asks for on every visit to every stock
// page — and, since the home feed grew cards, on every visit to the front door too — and it is
// served by free endpoints that owe us nothing. Three separate failure modes follow from that, and
// this file is one answer to each:
//
//   - repetition — ten people opening the same stock in the same minute is ten identical calls, so
//     there is an LRU in front of them, expiring on one of THREE TTLs: the interval decides first (a
//     one-minute chart may not be served out of a five-minute cache), and for everything else the
//     VENDOR's own session field selects between open and closed. An operator sets the length of all
//     three (quote_admin_api.go);
//   - the thundering herd — those ten arriving in the same MILLISECOND all miss the LRU together,
//     so a hand-rolled single-flight collapses them into one upstream call and hands the one answer
//     to all ten. The home feed's batch is single-flighted too, keyed by the SET of symbols it asks
//     about rather than by one;
//   - amplification — one caller walking five thousand codes would otherwise become five thousand
//     concurrent requests aimed at Tencent from this server's address, so a buffered channel caps
//     how many can be in flight at once no matter how many browsers are waiting. A page of home
//     cards avoids the fan-out entirely: fetchBatchUnder asks for the whole page in one request.
//
// The single-flight is a mutex, a map and a done channel — the same hand-rolled shape as the LRU
// in mermaid_pdf.go. golang.org/x/sync IS resolvable (go.mod carries v0.22.0 as an indirect
// dependency of something else), so this is not a case of the package being unavailable: promoting
// an indirect dependency to a direct one is still a dependency this repository would then own, for
// about forty lines it can write itself.

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// This cache is its OWN instance with its OWN bounds, and deliberately not the mermaidChartCache
	// next door. That one is keyed by user, source and theme and holds the rendered SVGs a report's
	// PDF export needs; it is process-wide at 512 entries and 32 MiB, and one entry per symbol per
	// range per refresh would walk somebody's warmed report charts straight out of it minutes before
	// they pressed Export. Two caches with different lifetimes and different eviction pressure do
	// not belong in one LRU however similar their code looks.
	//
	// Both bounds are live, which is the point of having two. A 1m entry is roughly 1.9 KB and a 1y
	// entry roughly 19 KB, so a portal whose users mostly look at short ranges is bounded by the
	// entry count (256 short entries is under 500 KB), while one that mostly asks for 1y hits the
	// byte ceiling at around 220 entries. A count alone would let the second case cost 5 MB; a byte
	// budget alone would let the first case hold tens of thousands of map keys.
	quoteCacheMaxEntries = 256
	quoteCacheMaxBytes   = 4 << 20

	// WHICH of the two SESSION TTLs applies is chosen from the vendor's own market-session field
	// rather than from a trading calendar of our own, and that part is not configurable. (The
	// interval is asked first and can take the choice away from both — see quoteTTLIntraday.) A hand-written calendar
	// is wrong on exactly the days it matters: the Spring Festival and National Day weeks move every
	// year, half-day sessions exist, an unscheduled closure is not on anybody's calendar in advance,
	// and the exchange keeps publishing its own late-afternoon batch (settlement figures, 龙虎榜)
	// well after 15:00 — so a calendar that says "closed at 15:00" pins a five-minute-stale snapshot
	// over data that is still moving. Tencent already answers with the state it believes the market
	// is in, and it is never wrong about its own feed.
	//
	// HOW LONG each one lasts is a load-versus-freshness trade that belongs to whoever runs the
	// portal, so these two are the shipped defaults behind quote_ttl_open_secs and
	// quote_ttl_closed_secs rather than the only answers. Nothing changes until an admin saves
	// something else: quoteConfigDefault reads exactly these.
	quoteTTLOpen = 30 * time.Second
	// Not "open" — closed, or a source that does not publish a session at all — takes the long TTL.
	// The unknown case is Sina, which carries no market-state field, and it is only ever reached
	// when Tencent has already failed: being gentle with the one vendor still answering is the right
	// bias precisely then, and the alternative (guess from this server's wall clock) is the
	// substitution the whole feature avoids, because a server whose zone has drifted would
	// confidently poll a closed market every thirty seconds forever.
	quoteTTLClosed = 5 * time.Minute
	// And the third, which is not chosen by the market's session at all but by the INTERVAL the
	// request asked for. A 分时 chart is a line of one-minute points, and serving it out of a
	// five-minute cache draws a chart whose last five bars are missing and whose last price is five
	// minutes old — on the one view in this portal whose entire subject is the last few minutes. The
	// session TTLs stay what they are for the snapshot and the daily series; this one applies
	// wherever quoteInterval.intraday() is true, on an open market and a closed one alike, because
	// what makes it right is the RESOLUTION of the answer and not the state of the exchange.
	quoteTTLIntraday = 60 * time.Second

	// The floors under those TTLs, and they are Go consts rather than more settings for the reason
	// ADR 0017 gives about the retention floors: a floor that is itself configurable is not a floor.
	// Each is applied on SAVE and again on READ, so a value written by an older build, by a restored
	// backup or by hand into meta cannot take the portal below them — the same discipline
	// cleanupConfigLoad applies to the retention days.
	//
	// The number they protect is not this portal's, it is the vendors'. Five seconds of open-market
	// TTL is already one call per symbol per five seconds per portal against free endpoints that owe
	// us nothing, and the address that gets rate-limited for it is ours.
	quoteTTLOpenFloor   = 5 * time.Second
	quoteTTLClosedFloor = 30 * time.Second
	// The intraday floor is fifteen seconds rather than the open market's five. The number it
	// protects is the same vendors', and a 分时 view is the one a reader leaves open: a chart that
	// re-fetches four times a minute per viewer per symbol is the traffic pattern that gets this
	// portal's address blocked, and no reader can see the difference between a fifteen-second-old
	// minute bar and a five-second-old one, because the bar itself only changes once a minute.
	quoteTTLIntradayFloor = 15 * time.Second

	// And the ceiling over all three, applied at the same two sites. It is ONE number where the
	// floors are three because the reason does not vary with the market state or the resolution: a cached price that outlives
	// the session it was fetched in is indistinguishable from a broken feed. The reading page shows
	// a number that will never change again, with only the 缓存 chip as a hint, and the only way out
	// is the clear-cache button or noticing the value in the form.
	//
	// The large-positive side is the one nothing else catches: 2000000000 typed into the closed TTL
	// (or carried in by a restored backup) is time.Duration(2e9)*time.Second = 2e18ns, comfortably
	// UNDER the int64 wrap and therefore a perfectly valid Duration — every symbol pinned in the LRU
	// for about sixty-three years.
	//
	// A day is deliberately the generous end of "no longer than a session": long enough that an
	// operator shedding vendor load across a weekend is not fighting it, short enough that a value
	// nobody meant expires by itself.
	quoteTTLCeiling = 24 * time.Hour

	// quoteUpstreamConcurrency is the ceiling on simultaneous vendor conversations for the whole
	// process. Four is chosen to be obviously polite rather than tuned: a page view needs one, and
	// anything that needs more than four at once is somebody enumerating the market, which is
	// exactly the traffic this portal must not relay.
	quoteUpstreamConcurrency = 4

	// quoteUpstreamTimeout is the whole failover's budget on the detached context the leader's load
	// runs under. Failover is two vendors at quoteFetchTimeout each plus however long it waits for a
	// slot on the ceiling above, and it exists because a load that no caller can cancel any more
	// still has to end: this is the deadline that replaces the one the leader's request carried.
	quoteUpstreamTimeout = 30 * time.Second

	// The size accounting is an ESTIMATE, and only has to be one: its job is to keep an unbounded
	// map from existing, not to predict the allocator. quoteBarBytes covers a QuoteBar's five int64s
	// plus its string header, and quoteRespBytes the surrounding struct, its map key and its list
	// element.
	quoteBarBytes  = 64
	quoteRespBytes = 256
)

// errQuoteNoSources is what load returns when it called nobody at all — an empty source list, an
// order that names none of this build's sources, or a (market, interval) pair none of the enabled
// ones has declared. It exists so the handler's 503 path is reached rather than a nil response
// escaping as a success.
var errQuoteNoSources = errors.New("quote: no vendor sources configured")

// quoteFetchFunc is the shape both vendor fetchers in quote.go already have.
type quoteFetchFunc func(ctx context.Context, market, code string, bars int) (*QuoteResp, error)

// quoteIntervalFetchFunc is the same fetch WITH the interval in hand, for a source whose answer
// depends on it. Both shapes exist because both facts are true: Tencent's and Sina's endpoints take
// a bar count and nothing else, so handing their fetchers an interval would be a parameter they
// could only ignore — while Yahoo's takes range and interval as two query parameters, and asking it
// for a daily window when the caller wanted minutes returns a well-formed WRONG answer rather than
// an error. A source sets one field or the other; quoteSource.call is the single place that picks.
type quoteIntervalFetchFunc func(ctx context.Context, market, code string, bars int, iv quoteInterval) (*QuoteResp, error)

// quoteBatchFetchFunc is MANY symbols in ONE upstream call, and it exists because one vendor can
// genuinely do that: `qt.gtimg.cn/q=sh601899,sz000001,…` answers a line per code in a single
// request, so a home page of fifty cards costs one conversation with Tencent rather than fifty.
//
// It answers snapshots only — that endpoint carries no series — and it is keyed by VENDOR SYMBOL
// rather than by code, because 000001 is a Shenzhen company and a Shanghai index and a map keyed by
// six digits would silently keep one of them.
//
// A source without one is not asked in bulk. There is no per-symbol emulation loop hidden behind
// this type: a fallback that answered a batch by making fifty calls would be indistinguishable from
// the amplification the whole cache exists to prevent, and it would arrive on exactly the day the
// primary is down.
type quoteBatchFetchFunc func(ctx context.Context, targets []quoteTarget) (map[string]*QuoteResp, error)

// quoteCapability is one GRANT in a source's declaration: the markets this clause names, at the
// intervals it names. A source declares a LIST of them and can serve a request when any single grant
// covers it, because the declarations that matter are not rectangles. Tencent's is "every market's
// price, and every market's daily series except two" — no one (markets × intervals) product says
// that, and flattening it into one would either hand Tencent a US series it answers with two rows
// fifteen years apart or take away the US price it answers correctly.
type quoteCapability struct {
	// markets is a PREDICATE over the market table rather than a list of ids wherever the fact it
	// answers already lives there: Sina's grant is the `sina` column quoteSinaTarget itself refuses
	// hk and us on, so the declaration cannot come to disagree with the parser that actually
	// decides. Where the fact is about the SOURCE and not about the market it is named here as ids
	// instead — see Tencent's daily grant below, which is the one case and carries the reason.
	//
	// nil means every market.
	markets func(*quoteMarket) bool
	// intervals is what this grant covers. Empty grants nothing, which is why a source declares
	// grants rather than two independent sets: a market list with no intervals beside it would be a
	// declaration that reads as "everything" and means "nothing".
	intervals []quoteInterval
}

// covers reports whether this one grant reaches a request.
func (g quoteCapability) covers(m *quoteMarket, iv quoteInterval) bool {
	// A market this build has no row for is not something a declaration can answer about, so the
	// interval decides alone — the same tolerance the market predicate had before capabilities
	// existed, and only reachable from a caller that skipped quoteResolve.
	if g.markets != nil && m != nil && !g.markets(m) {
		return false
	}
	for _, ok := range g.intervals {
		if ok == iv {
			return true
		}
	}
	return false
}

// quoteSource is one vendor, named so the health counters and QuoteResp.Source agree without a
// second table mapping one to the other.
type quoteSource struct {
	name  string
	fetch quoteFetchFunc
	// fetchAt is fetch for a source that needs the interval to answer at all — see
	// quoteIntervalFetchFunc. Exactly one of the two is set; call prefers this one.
	fetchAt quoteIntervalFetchFunc
	// fetchBatch is the many-symbols-one-call fetcher, or nil for a source that has no such
	// endpoint. It is a SEPARATE field rather than something derived from the two above because
	// batching is a property of the vendor's URL, not of its parser: Tencent has an endpoint that
	// takes a comma list and Sina and Yahoo do not, and no amount of looping here would change that.
	fetchBatch quoteBatchFetchFunc
	// optIn keeps a compiled-in source OUT of the shipped order (quoteShippedOrder) while leaving it
	// in every other respect: the panel lists it, quote_source_order accepts its name, and adding it
	// there is the whole of turning it on. It is not a second enable flag — the order remains the
	// only one, and this decides nothing except what an UNCONFIGURED portal starts with.
	//
	// Yahoo is why it exists. Its endpoint is undocumented and unlicensed, so whether a deployment
	// depends on it is the operator's decision rather than the build's, which is the same trade ADR
	// 0017's cleanup targets and the GeoIP refresh loop already ship on.
	optIn bool
	// caps is what this source DECLARES it can serve. It replaces a bare market predicate because
	// the resolver's question grew a second half (quoteInterval): a source that serves a market's
	// price and not its history is the ordinary case here, not an exception. The admin panel prints
	// what this declaration says, so a source cannot advertise a market its fetcher then refuses.
	//
	// An EMPTY declaration means "no restriction recorded", which is what a test wiring a bare stub
	// gets and what every such test relies on.
	caps []quoteCapability
	// knows answers the half of "may this source be asked" that caps cannot: whether this source has
	// a spelling for THIS CODE. nil means every code in the markets above.
	//
	// A grant is a predicate over the market table, and Yahoo's Hong Kong problem is not about the
	// market: canonicalCode accepts any five digits there, and only the ones that begin with the zero
	// Yahoo drops have a measured symbol (quoteYahooSymbol). Without this, hk 80737 resolved to a
	// source whose fetcher refused it before any HTTP request — and load then recorded that refusal,
	// a fact about OUR OWN code, against the vendor's health, and answered 503 for a gap no retry
	// closes. sourcesFor exists precisely so that a skip cannot reach the counters, and a skip it
	// cannot see is a skip it cannot keep out of them.
	//
	// It is the FETCHER'S OWN symbol function that answers, never a second rule written beside it:
	// two statements of "which codes can this vendor be asked about" is one way for the resolver and
	// the parser to come to disagree, which is the same argument the market predicates carry.
	knows func(quoteTarget) bool
}

// serves is the whole of "may this source be asked for this request": the declaration over the
// (market, interval) pair, and then the code. It is what the RESOLVER asks, because the resolver has
// a target in hand; the panel has only a market and asks covers below.
func (src quoteSource) serves(t quoteTarget, iv quoteInterval) bool {
	if !src.covers(t.Market, iv) {
		return false
	}
	// A target with no market row cannot be asked about by code either — only a caller that skipped
	// quoteResolve produces one, and covers has already decided that case on the interval alone.
	return src.knows == nil || src.knows(t)
}

// covers is "may this source be asked about this MARKET at this interval", which is the declaration
// alone, with the empty case spelled out: an undeclared source is asked for everything. It is the
// panel's question (marketIDsFor) and half of the resolver's.
func (src quoteSource) covers(m *quoteMarket, iv quoteInterval) bool {
	if len(src.caps) == 0 {
		return true
	}
	for _, g := range src.caps {
		if g.covers(m, iv) {
			return true
		}
	}
	return false
}

// call runs this source's fetcher for one request. It is the only place that knows a source may
// carry either shape of fetcher, so load stays one loop over one call — and a source that declared
// an interval-dependent capability cannot be invoked through the fetcher that would ignore it.
//
// A source with NEITHER fetcher set is an error rather than a nil dereference: the empty
// declaration that lets a test wire a bare stub means "no restriction recorded", and this is what
// keeps that convenience from reaching the failover loop as a panic in a handler goroutine.
func (src quoteSource) call(ctx context.Context, market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
	switch {
	case src.fetchAt != nil:
		return src.fetchAt(ctx, market, code, bars, iv)
	case src.fetch != nil:
		return src.fetch(ctx, market, code, bars)
	}
	return nil, fmt.Errorf("quote: source %q has no fetcher", src.name)
}

// callBatch runs this source's batch fetcher. A source without one answers with the sentinel rather
// than with a loop over call: see quoteBatchFetchFunc for why emulating a batch is worse than not
// having one.
func (src quoteSource) callBatch(ctx context.Context, targets []quoteTarget) (map[string]*QuoteResp, error) {
	if src.fetchBatch == nil {
		return nil, fmt.Errorf("%w: %s has no batch endpoint", errQuoteNoSources, src.name)
	}
	return src.fetchBatch(ctx, targets)
}

// marketIDs is the answer to "which markets can this source serve", asked of every row in the market
// table so that adding a market cannot leave a stale answer behind here.
//
// It is the UNION over the intervals, and it has to be: the panel has one markets column while a
// source's grants differ per interval, so the only honest reading of a single list is "at some
// interval". Tencent's row therefore still names the US, which is what it said before capabilities
// existed and is true of the price it serves there; that the US has no chart is stated where it
// always was, by the market table and by the handler that blanks the bars.
func (src quoteSource) marketIDs() []string { return src.marketIDsFor(quoteAllIntervals...) }

// marketIDsFor is the same question asked of a NAMED set of intervals, which is what the panel's
// capability column needs: "which markets does this source draw a chart for" is a different and
// narrower answer than "which markets can it say anything about", and the US is the case that proves
// it — Tencent appears in the markets column for a price it serves correctly and must not appear in
// the daily one for a series it answers with two rows fifteen years apart.
//
// Asked of every row in the market table rather than read off a list, so that adding a market cannot
// leave a stale answer behind here. A source is named for an interval set if ANY interval in it is
// covered, which is why the intraday column can be one column while two intraday intervals exist.
func (src quoteSource) marketIDsFor(ivs ...quoteInterval) []string {
	out := make([]string, 0, len(quoteMarkets))
	for id, m := range quoteMarkets {
		for _, iv := range ivs {
			if src.covers(m, iv) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// defaultQuoteSources is the shipped failover order and the shipped capability declaration. Tencent
// first because one call returns history, snapshot and session state together and therefore cannot
// contradict itself about which instant it is describing; Sina second because it costs two calls and
// publishes no percentage of its own.
//
// What the declarations buy is not documentation. The resolver skips a source that has not declared
// the (market, interval) pair in hand, so "the US daily series comes from somewhere other than
// Tencent" is true BY CONSTRUCTION rather than by an order an admin has to get right: a source added
// here that declares us-daily is the only one that CAN be asked for it, wherever the operator puts
// it in quote_source_order. That is also why there is no per-market source setting and must not be
// one — it would be a second, editable statement of the same fact, free to disagree with this one.
//
// It is also the list quote_source_order is validated against: a name no source here answers to is
// refused at the save rather than stored and silently skipped.
func defaultQuoteSources() []quoteSource {
	return []quoteSource{
		{
			name: quoteSourceTencent,
			// fetchAt rather than fetch, because this vendor now has TWO endpoints and only the
			// interval says which one a request means: fqkline for the daily series, minute/query
			// for 分时. Wiring it through the interval-blind fetcher would answer a minute request
			// with a daily series and no error — the exact failure the note below used to describe
			// as the reason the intraday grant did not yet exist.
			fetchAt: fetchTencentAt,
			// And a third endpoint, which is not an interval but a SHAPE: q= answers many symbols in
			// one request. It is what makes a page of home cards one upstream call.
			fetchBatch: fetchTencentBatch,
			caps: []quoteCapability{
				// The price, in every market: fqkline answers all five — every fixture in
				// testdata/quote/ came out of it — and its snapshot half is trusted in all five.
				{intervals: []quoteInterval{quoteIntervalSnapshot}},
				// The SERIES, in every market except the two where what comes back is not one:
				// Beijing answers "day":[] and the US answers a sixty-bar request with two rows
				// fifteen years apart. Both were measured against committed fixtures (ADR 0030 §4).
				//
				// Named as ids rather than read off quoteMarket.history, which happens to hold the
				// same two markets today. `history` is a statement about the MARKET as this portal
				// currently sources it, and it is the flag the handler blanks bars on; this is a
				// statement about TENCENT. The day the two diverge is the day a source that really
				// does serve US dailies is enabled — and reading the column here would silently hand
				// the US series back to Tencent at exactly that moment, which is the failure this
				// whole declaration exists to make impossible.
				{
					markets:   func(m *quoteMarket) bool { return m.id != "us" && m.id != "bj" },
					intervals: []quoteInterval{quoteIntervalDaily},
				},
				// ONE SESSION of minutes, in the three markets where a real one was measured. The
				// endpoint answers all five and two of the answers are not a series: bj830799 comes
				// back with ONE point (0930, volume 0 — the suspended stock the daily fixtures are
				// also captured from) and usAAPL with ONE point and an EMPTY trading date, which is
				// not a bar this parser can stamp at all. Both are in testdata/quote/ as the
				// measurement rather than as a claim.
				//
				// Beijing is the row to be honest about: the only bj body captured is a suspended
				// stock, so what is measured is "no series for THIS code", not "no series for the
				// market". It is left out on the same principle that keeps `.BJ` out of the Yahoo
				// symbol map — an undeclared market is a request that resolves to nobody, while a
				// wrongly declared one is a chart drawn from a single point.
				//
				// The SAME three markets for the FIVE-session window, from a second endpoint on the
				// same host — day/query, measured 2026-09-07 at 5 sessions each: 267 rows a day on
				// Shanghai and Shenzhen, 332 on Hong Kong.
				//
				// It is one grant and not two because the answer is the same shape at two widths,
				// and it is written out rather than folded into the row above so the two intervals
				// keep separate evidence: each names the endpoint it was measured against, and a
				// vendor that breaks one window does not silently take the other's claim with it.
				//
				// The US and Beijing are out of BOTH for reasons that are not the same sentence.
				// day/query answers usAAPL with {"code":-1,"msg":"param error"} — it does not serve
				// the market. It answers bj830799 with five full sessions dated APRIL 2025: the
				// suspended stock every Beijing fixture in this repo is captured from, so what is
				// measured is "no series for this code" and not "none for the market". Declaring
				// Beijing on that evidence would draw a chart of a year-old week under a 5日 label.
				{
					markets: func(m *quoteMarket) bool {
						return m.id == "sh" || m.id == "sz" || m.id == "hk"
					},
					intervals: []quoteInterval{quoteIntervalIntraday, quoteIntervalIntraday5D},
				},
			},
		},
		{
			name:  quoteSourceSina,
			fetch: fetchSinaQuote,
			// A-shares only. Sina's Hong Kong and US lines are `rt_hk00700` and `gb_aapl` — 19 and 36
			// fields in a different order — so pointing the A-share parser at either would not be a
			// second source, it would be a second shape read as the first. The predicate is the same
			// `sina` column quoteSinaTarget refuses those two markets on.
			caps: []quoteCapability{
				{
					markets:   func(m *quoteMarket) bool { return m.sina },
					intervals: []quoteInterval{quoteIntervalSnapshot},
				},
				// The series, only where fetchSinaQuote will actually go and fetch one: it asks
				// quoteMarketHasDailyHistory before touching the kline endpoint, because Sina
				// answers a Beijing code with a series that stops over a year ago instead of with an
				// error. Reading the same column here is what keeps the declaration and the fetcher
				// from drifting apart — the opposite call from Tencent's above, and for the opposite
				// reason: this fetcher consults the column at request time, that one never does.
				{
					markets:   func(m *quoteMarket) bool { return m.sina && m.history },
					intervals: []quoteInterval{quoteIntervalDaily},
				},
			},
		},
		// Yahoo: compiled in, listed in the panel, and NOT in quoteShippedOrder — see optIn, and see
		// quote_yahoo.go for what it declares and why the operator rather than the build decides
		// whether to depend on it. It is the only source that declares a US daily series.
		quoteYahooSource(),
	}
}

// quoteSourceNames is every source compiled into this build, in table order, INCLUDING the ones that
// ship disabled — it is the list a save is validated against, and refusing to name a source an
// operator is allowed to enable is how a compiled-in source becomes unreachable. What an unconfigured
// portal actually runs is quoteShippedOrder, which is a shorter list. Derived from
// defaultQuoteSources rather than written out again, so a source added there cannot be one the
// order setting refuses to name.
func quoteSourceNames() []string {
	srcs := defaultQuoteSources()
	out := make([]string, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, src.name)
	}
	return out
}

// quoteShippedOrder is the failover order an UNCONFIGURED portal runs: every compiled-in source
// except the opt-in ones. It is not the same list as quoteSourceNames and the difference is the
// point — a source that ships disabled is still a source the order setting may name, the panel must
// list and the resolver will use the moment an operator adds it.
//
// Derived from the same table for the same reason quoteSourceNames is: a source added there is off
// or on by its own declaration, not by a second list here that somebody has to remember to edit.
func quoteShippedOrder() []string {
	srcs := defaultQuoteSources()
	out := make([]string, 0, len(srcs))
	for _, src := range srcs {
		if src.optIn {
			continue
		}
		out = append(out, src.name)
	}
	return out
}

// ---------- the operator's three choices ----------

const (
	// A comma-separated source order. A source ABSENT from it is off — there is no separate
	// enable flag, because two ways to disable a source is one way for the two to disagree.
	setQuoteSourceOrder = "quote_source_order"
	// The two TTLs, in seconds, clamped into [quoteTTLOpenFloor / quoteTTLClosedFloor,
	// quoteTTLCeiling].
	setQuoteTTLOpenSecs   = "quote_ttl_open_secs"
	setQuoteTTLClosedSecs = "quote_ttl_closed_secs"
	// The third TTL, for a minute-resolution answer, clamped into
	// [quoteTTLIntradayFloor, quoteTTLCeiling].
	setQuoteTTLIntradaySecs = "quote_ttl_intraday_secs"

	// Whether the home feed asks for prices at all. Default ON, and it exists for one reason that
	// has nothing to do with load: turning it on sends the list of codes currently on a reader's
	// screen to a third-party vendor on EVERY home page view. The data is public and read-only and
	// the request is small, but it is an outward disclosure of what this portal is showing, and an
	// operator running an internal deployment may not want to make it. Everything else about quotes
	// happens when somebody opens a stock; this is the one that happens when somebody opens the
	// front door.
	setQuoteHomeCards = "home_quotes"
)

// quoteConfig is what an admin can change about the fetch path. Every field's zero value is refused
// by orDefaults below rather than obeyed: an empty order would mean "every source off" and a zero
// TTL would mean "expire on arrival", and neither is a thing anybody configured.
type quoteConfig struct {
	Order       []string // source names, in the order to try
	TTLOpen     time.Duration
	TTLClosed   time.Duration
	TTLIntraday time.Duration
}

// quoteConfigDefault is what the portal did before any of this was configurable, and it is what an
// unconfigured portal still does. The whole point of the three settings is that this function is
// the answer until somebody saves something else.
func quoteConfigDefault() quoteConfig {
	return quoteConfig{Order: quoteShippedOrder(), TTLOpen: quoteTTLOpen, TTLClosed: quoteTTLClosed,
		TTLIntraday: quoteTTLIntraday}
}

// orDefaults fills in whatever a caller left unset. It exists because quoteConfig travels as a
// value: a zero one reaching load would disable every source, which is the one failure mode that
// looks like "the vendors are down" rather than like a bug here.
func (cfg quoteConfig) orDefaults() quoteConfig {
	def := quoteConfigDefault()
	if len(cfg.Order) == 0 {
		cfg.Order = def.Order
	}
	if cfg.TTLOpen <= 0 {
		cfg.TTLOpen = def.TTLOpen
	}
	if cfg.TTLClosed <= 0 {
		cfg.TTLClosed = def.TTLClosed
	}
	if cfg.TTLIntraday <= 0 {
		cfg.TTLIntraday = def.TTLIntraday
	}
	return cfg
}

// ttlFor picks which of the THREE TTLs this response gets, and the interval is asked FIRST.
//
// The order matters and is not arbitrary. A 分时 response for an open market would otherwise take
// the thirty-second open-market TTL — which is nearly right — and one for a CLOSED market would take
// the five-minute one, which is exactly wrong for the reader who opens the chart at 09:31 while the
// vendor still reports the previous session as closed. The interval is a property of what was ASKED
// FOR and the session is a property of what came back; a one-minute series wants a one-minute cache
// either way. See quoteTTLOpen for why the session choice, where it still applies, is the vendor's
// field and never a calendar.
func (cfg quoteConfig) ttlFor(resp *QuoteResp, iv quoteInterval) time.Duration {
	cfg = cfg.orDefaults()
	if iv.intraday() {
		return cfg.TTLIntraday
	}
	if resp != nil && resp.Snapshot.Session == quoteSessionOpen {
		return cfg.TTLOpen
	}
	return cfg.TTLClosed
}

// quoteParseOrder splits a stored or submitted order into source names. It returns the KNOWN names,
// in the order given and without repeats, plus the first name this build has no source for.
//
// The two answers have different audiences, which is why they are separate return values rather than
// one error: a save refuses on `unknown` (an admin typing a vendor that does not exist should be
// told, not quietly given something else), while a READ drops the unknown name and keeps the rest —
// a portal restored from a backup written by a build with a third source must go on serving quotes
// from the two it does have, and falling back to the shipped default there would silently re-enable
// a source the operator had switched off.
func quoteParseOrder(raw string) (order []string, unknown string) {
	known := map[string]bool{}
	for _, name := range quoteSourceNames() {
		known[name] = true
	}
	seen := map[string]bool{}
	for _, field := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(field))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !known[name] {
			if unknown == "" {
				unknown = name
			}
			continue
		}
		order = append(order, name)
	}
	return order, unknown
}

// quoteConfigLoad reads the three settings and clamps on READ as well as on save, so a value that
// reached meta some other way — an older build, a hand edit, a restore — cannot put the portal
// below a floor or above the ceiling. Same shape and same reason as cleanupConfigLoad (ADR 0017).
func (s *Server) quoteConfigLoad() quoteConfig {
	cfg := quoteConfigDefault()
	// Total by construction, like settingInt: these getters sit on the request path and have to
	// answer for a Server with no store to ask. No configuration available reads as the shipped
	// behaviour, which is what the default is.
	if s.st == nil {
		return cfg
	}
	// An order with nothing recognisable left in it — blank, or naming only sources this build does
	// not have — keeps the shipped one. The alternative is a portal that answers every quote with a
	// 503 until somebody thinks to look in meta.
	if order, _ := quoteParseOrder(s.st.GetSetting(setQuoteSourceOrder, "")); len(order) > 0 {
		cfg.Order = order
	}
	// The fallback is cfg's own field, which quoteConfigDefault has just filled in — not the constant
	// again. Naming the constant here would be a second statement of the shipped default, free to
	// disagree with the first: a mutation that changed quoteConfigDefault's TTL left this path
	// serving the old one, and every test still passed.
	cfg.TTLOpen = quoteTTLSetting(s.st, setQuoteTTLOpenSecs, cfg.TTLOpen, quoteTTLOpenFloor)
	cfg.TTLClosed = quoteTTLSetting(s.st, setQuoteTTLClosedSecs, cfg.TTLClosed, quoteTTLClosedFloor)
	cfg.TTLIntraday = quoteTTLSetting(s.st, setQuoteTTLIntradaySecs, cfg.TTLIntraday, quoteTTLIntradayFloor)
	return cfg
}

// quoteHomeCards is the reader for setQuoteHomeCards. Default ON, per the ask — and read here rather
// than in the handler so that the batch endpoint and the admin panel are looking at one function
// instead of two copies of a default.
//
// It fails OPEN (to the shipped default) when there is no store to ask, exactly as quoteConfigLoad
// does: a Server with no store is a test fixture, not an operator who has switched something off.
func (s *Server) quoteHomeCards() bool {
	if s.st == nil {
		return true
	}
	return settingBool(s.st.GetSetting(setQuoteHomeCards, ""), true)
}

// quoteTTLSetting reads one TTL in seconds. A value outside the range is CLAMPED to the nearer end,
// not replaced by the default the way settingInt would: an operator who asked for a two-second TTL
// wants the shortest one they are allowed to have, and handing them the five-minute default instead
// is further from what they asked for than the floor is. Only an unreadable value — absent, blank,
// not a number — falls back to the default, because that is not a request at all.
func quoteTTLSetting(st *Store, key string, def, floor time.Duration) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(st.GetSetting(key, "")))
	if err != nil {
		return def
	}
	return quoteClampTTL(time.Duration(n)*time.Second, floor)
}

// quoteClampTTL applies the floor and the ceiling, and it is handed a Duration rather than a seconds
// count so that the multiply is already done when the comparison happens. A seconds count big enough
// to overflow int64 nanoseconds wraps, and a wrapped value can come out negative — an entry that
// expires before it is stored, turning every page view into a vendor call. Comparing the seconds
// first and multiplying afterwards would let exactly that through; this way it lands on the floor.
// A wrap that lands large-positive is not distinguishable here from a number somebody meant, which
// is the other half of what the ceiling is for: whatever the multiply produced, what comes out of
// this function is inside the range.
//
// The floor is a parameter and the ceiling is not, because the floor differs by market state — it
// protects the vendors, and open and closed cost them differently — while the ceiling protects the
// READER, who cannot tell a day-old cache from a dead feed either way.
func quoteClampTTL(d, floor time.Duration) time.Duration {
	if d < floor {
		return floor
	}
	if d > quoteTTLCeiling {
		return quoteTTLCeiling
	}
	return d
}

// QuoteSourceHealth is one vendor's recent record, for an operator asking the question a log cannot
// answer at a glance: is this source down, or did it merely blip? Consecutive failures is the field
// that separates the two, which is why it is a counter and not a boolean.
type QuoteSourceHealth struct {
	Source      string    `json:"source"`
	LastSuccess time.Time `json:"lastSuccess"`
	LastError   string    `json:"lastError"`
	LastErrorAt time.Time `json:"lastErrorAt"`
	Failures    int       `json:"consecutiveFailures"`
}

type quoteCacheEntry struct {
	key     string
	resp    *QuoteResp
	expires time.Time
	size    int
}

// quoteFlight is one in-flight upstream load. Everyone who arrives for the same key while it is
// running waits on done and shares its result, which is the whole of the single-flight.
type quoteFlight struct {
	done chan struct{}
	resp *QuoteResp
	err  error
	// many is the batch leader's result — the same collapse, for a flight whose key is a SET of
	// symbols rather than one. It shares the struct and the map because it shares endFlight and the
	// publish-then-close ordering that makes a late arrival start a new flight instead of joining an
	// answered one; a batch flight sets this and a single-symbol flight sets resp, and neither ever
	// reads the other's field because the key spaces cannot collide ("batch:" prefix).
	many map[string]*QuoteResp
}

type quoteCache struct {
	once sync.Once

	mu      sync.Mutex // guards entries, lru, bytes and health
	entries map[string]*list.Element
	lru     list.List
	bytes   int
	health  map[string]*QuoteSourceHealth

	flightMu sync.Mutex // guards flights; separate from mu so a waiter never blocks a cache read
	flights  map[string]*quoteFlight

	sem chan struct{} // the upstream concurrency ceiling

	// sources and now are seams, set before first use. A test substitutes a counting stub here
	// rather than reaching for a package-level var, so two tests running concurrently under -race
	// cannot overwrite each other's fetcher.
	sources []quoteSource
	now     func() time.Time
}

// init fills in whatever the zero value lacks. The cache is a value field on Server, exactly like
// mermaidChartCache, so there is no constructor to do this in; sync.Once keeps the check to an
// atomic load on every call after the first. A seam already assigned by a test survives, which is
// what lets a test set sources on a zero-value Server.
func (c *quoteCache) init() {
	c.once.Do(func() {
		c.entries = make(map[string]*list.Element)
		c.flights = make(map[string]*quoteFlight)
		c.health = make(map[string]*QuoteSourceHealth)
		c.sem = make(chan struct{}, quoteUpstreamConcurrency)
		if c.sources == nil {
			c.sources = defaultQuoteSources()
		}
		if c.now == nil {
			c.now = time.Now
		}
	})
}

// quoteCacheKey identifies one cached answer. The bar count is part of it because it is part of the
// answer: the 1y response is not the 3m response with more rows appended, it is a different vendor
// call, and keying on the symbol alone would serve whichever range happened to be fetched first.
//
// So is the INTERVAL, and it had to become part of it in the same change that let a request choose
// one: 400 one-minute points and 400 daily bars are the same market, the same code and the same bar
// count, and without this they would share a slot — so whichever of 分时 and 1年 was asked for first
// would be drawn under both labels for the rest of the TTL.
//
// The bar count is dropped for a SNAPSHOT answer, and that is not a shortcut: a snapshot has no
// series, so the number of bars that were asked for is not part of what came back. Keeping it would
// give the same price as many cache entries as there are ranges.
//
// It is NOT what lets a home card reuse a reading page's fetch — an earlier version of this comment
// claimed that and was wrong. Card-to-page warming is snapshotFor's prefix scan, which never looks
// at the key's components. What this line actually buys is narrower and still worth having: for a
// market whose reading page can only ever get a snapshot (us and bj, whose daily ranges degrade),
// the card's key and the page's key become the SAME key rather than two entries holding one price.
func quoteCacheKey(market, code string, bars int, iv quoteInterval) string {
	if iv == quoteIntervalSnapshot {
		bars = 0
	}
	return market + code + ":" + strconv.Itoa(bars) + ":" + string(iv)
}

// quoteEntrySize estimates one entry's footprint. See quoteBarBytes for why an estimate is enough.
func quoteEntrySize(resp *QuoteResp) int {
	if resp == nil {
		return quoteRespBytes
	}
	n := quoteRespBytes +
		len(resp.Symbol) + len(resp.Name) + len(resp.Market) + len(resp.Source) +
		len(resp.BarsSource) + len(resp.BarsUnavailable) +
		len(resp.Snapshot.ChangePct) + len(resp.Snapshot.AsOf) + len(resp.Snapshot.Session)
	for _, b := range resp.Bars {
		n += quoteBarBytes + len(b.Date)
	}
	return n
}

// get returns a live entry and HOW LONG it stays live. The remaining lifetime, not the full TTL, is
// what the handler puts in Cache-Control: a browser told max-age=300 by a response that the server
// itself will drop in four seconds would hold a stale price for nearly five minutes longer than the
// server ever intended to.
func (c *quoteCache) get(key string) (*QuoteResp, time.Duration, bool) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return nil, 0, false
	}
	entry := element.Value.(*quoteCacheEntry)
	left := entry.expires.Sub(c.now())
	if left <= 0 {
		// Dropped rather than merely reported as a miss, so an expired symbol nobody asks for again
		// stops occupying the byte budget the moment anyone notices it has expired.
		c.removeLocked(element)
		return nil, 0, false
	}
	c.lru.MoveToFront(element)
	return entry.resp, left, true
}

func (c *quoteCache) put(key string, resp *QuoteResp, ttl time.Duration) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	size := quoteEntrySize(resp)
	expires := c.now().Add(ttl)
	if old := c.entries[key]; old != nil {
		entry := old.Value.(*quoteCacheEntry)
		c.bytes -= entry.size
		entry.resp, entry.expires, entry.size = resp, expires, size
		c.bytes += size
		c.lru.MoveToFront(old)
	} else {
		entry := &quoteCacheEntry{key: key, resp: resp, expires: expires, size: size}
		c.entries[key] = c.lru.PushFront(entry)
		c.bytes += size
	}
	for len(c.entries) > quoteCacheMaxEntries || c.bytes > quoteCacheMaxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.removeLocked(oldest)
	}
}

// removeLocked drops one element; the caller holds mu.
func (c *quoteCache) removeLocked(element *list.Element) {
	entry := element.Value.(*quoteCacheEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.size
	c.lru.Remove(element)
}

// acquire takes a slot on the upstream ceiling, or gives up when the context it is handed is done.
// That context is the load's own — fetch detaches it from any single caller, so what bounds the
// wait here is quoteUpstreamTimeout and not one browser tab's lifetime. Waiting on it at all is
// what keeps a queue that outlives its own deadline from holding places in line behind the four
// conversations actually in progress.
func (c *quoteCache) acquire(ctx context.Context) (func(), error) {
	c.init()
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// noteSuccess and noteFailure keep the per-source counters the admin panel reads (quote_admin_api.go).
// They are counters rather than a boolean because the question an operator actually has is "is this
// source down, or did it blip", and only a streak can answer it.
func (c *quoteCache) noteSuccess(source string) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.healthLocked(source)
	h.LastSuccess = c.now()
	h.Failures = 0
}

func (c *quoteCache) noteFailure(source string, err error) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.healthLocked(source)
	h.LastErrorAt = c.now()
	if err != nil {
		h.LastError = err.Error()
	}
	h.Failures++
}

func (c *quoteCache) healthLocked(source string) *QuoteSourceHealth {
	h := c.health[source]
	if h == nil {
		h = &QuoteSourceHealth{Source: source}
		c.health[source] = h
	}
	return h
}

// snapshotHealth copies the counters out. Copies, not pointers: whatever reads this must not be able
// to hold a reference into state the fetch path keeps mutating under the lock.
func (c *quoteCache) snapshotHealth() []QuoteSourceHealth {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]QuoteSourceHealth, 0, len(c.health))
	for _, h := range c.health {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// sourcesFor is the resolver: the sources this build has, in cfg's order, that have DECLARED they
// can serve this request — the (market, interval) pair, and the code. A source absent from the order is off — that is the whole
// of the enable/disable mechanism, and it is why there is no second flag to contradict it — and a
// source present in the order but silent about the pair is skipped, which is how the order comes to
// mean "in what order among the sources that CAN" rather than "which one, right or wrong".
//
// Both kinds of skip are absences, not failures, and neither may reach the health counters:
//
//   - a name in the order this build has no source for. The save refuses such a name and the read
//     drops it (quoteParseOrder), so reaching here means the setting outlived the source it named,
//     and a missing vendor is not a vendor that answered badly;
//   - a source that never claimed the pair. Asking Sina for usAAPL returns "sina has no
//     A-share-shaped line for the us market" — a fact about our own code — and counting that as a
//     vendor failure would paint a permanent red streak on the fallback in the admin panel for
//     every US symbol somebody looks up while Tencent is having a bad afternoon;
//   - a source with no SPELLING for the code (quoteSource.knows). The same kind of fact and the same
//     reason it may not be counted: "this portal has no measured yahoo symbol for hk 80737" is a
//     sentence about our own table, and a vendor that was never asked cannot have failed.
//
// It takes a TARGET rather than a market because the last of those is about the code. Deciding all
// three HERE rather than inside the failover loop is what makes the guarantee structural: a source
// this function did not return is one load never has an error from — and one servableInterval counts
// as absent, so a request only that source could have served degrades to the price and an empty chart
// instead of a 503 that no retry can close.
func (c *quoteCache) sourcesFor(cfg quoteConfig, t quoteTarget, iv quoteInterval) []quoteSource {
	byName := make(map[string]quoteSource, len(c.sources))
	for _, src := range c.sources {
		byName[src.name] = src
	}
	out := make([]quoteSource, 0, len(cfg.Order))
	for _, name := range cfg.Order {
		src, ok := byName[name]
		if !ok || !src.serves(t, iv) {
			continue
		}
		out = append(out, src)
	}
	return out
}

// servableInterval degrades a request that no ENABLED source can answer to a snapshot, rather than
// letting it become a 503.
//
// The difference matters on one screen: 分时 on a market this deployment has no minute source for.
// Without this the reader loses the whole panel — the price, the name, the market badge — behind
// "the quote sources are unreachable, please try again", which is false (they are answering fine)
// and invites a retry that will never work. With it they get the price and a chart that says there
// is no series, which is what a Beijing or US chart has said since ADR 0028 §7 and is the shape the
// handler already knows how to render.
//
// It degrades to the SNAPSHOT and never to another series. Serving a daily chart to a 分时 request
// because that is what happens to be available would answer a different question under the label
// the reader picked, which is the one thing this feature is not allowed to do.
//
// Since the four daily ranges started asking for what they mean, this is also what answers the DAILY
// question for the two markets ADR 0030 §4 called chart-less: Beijing degrades because no source
// declares (bj, daily), and the US degrades until an operator enables one that does. That is the same
// sentence the market table used to hard-code, checked against the sources actually running.
func (c *quoteCache) servableInterval(cfg quoteConfig, t quoteTarget, iv quoteInterval) quoteInterval {
	if iv == quoteIntervalSnapshot || len(c.sourcesFor(cfg, t, iv)) > 0 {
		return iv
	}
	return quoteIntervalSnapshot
}

// load runs the failover, holding one slot on the upstream ceiling for the WHOLE sequence rather
// than re-taking it per vendor. Failover is one logical upstream operation, and releasing between
// Tencent and Sina would let a burst of failing symbols double the fan-out at exactly the moment the
// vendors are least able to absorb it.
//
// A drift-gate failure arrives here as an ordinary error from the fetcher, which is the point: the
// gate is not advisory, and a source that answered with a shape we cannot trust has failed as
// completely as one that did not answer at all.
//
// The interval is a parameter rather than something worked out from the market here, because it is
// half of what sourcesFor answers on and only the caller knows which half it is asking for: a US
// price and a US daily series are two different chains through the same source table.
func (c *quoteCache) load(ctx context.Context, cfg quoteConfig, market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
	// Re-resolved rather than passed in, because what the resolver needs is the whole target: the
	// market row AND the code, the second of which decides whether a source has a symbol for this
	// request at all. Every caller has one already — this is the same quoteTargetFor the handler ran
	// over the same canonical code — so a failure here means a caller skipped quoteResolve, and it is
	// an error rather than an empty chain so that it cannot read as "every vendor is down".
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, err
	}
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	var firstErr error
	// Every source below has declared this pair and has a spelling for this code; one that has not is
	// not called and above all is not blamed (sourcesFor).
	for _, src := range c.sourcesFor(cfg, target, iv) {
		resp, err := src.call(ctx, market, code, bars, iv)
		if err != nil {
			// A failure that arrives because THIS side stopped waiting is not the vendor's, and it
			// must not land in the health counters: those are what an operator reads to decide
			// whether a source is down. The detach below means an abandoned browser tab no longer
			// reaches here at all, so this is the belt to that braces: it still covers a caller that
			// hands in an already-cancelled context, and it keeps the counters honest if the detach is
			// ever removed — which is exactly how the bug it guards arrived. The context's own
			// state decides it rather than the error's text, because a vendor that blows through
			// this feature's own eight-second budget also fails with context.DeadlineExceeded and
			// that one IS the vendor's.
			if ctx.Err() == nil {
				c.noteFailure(src.name, err)
			}
			if firstErr == nil {
				// The FIRST error, not the last: the primary's reason is the one worth reporting,
				// and the fallback's failure is usually the less interesting "and that one too".
				firstErr = err
			}
			continue
		}
		c.noteSuccess(src.name)
		return resp, nil
	}
	if firstErr == nil {
		return nil, errQuoteNoSources
	}
	return nil, firstErr
}

// fetchUnder is the whole path a handler takes: cache, then single-flight, then load. It reports
// whether the answer cost an upstream call and how long the answer stays fresh.
//
// The config arrives as an argument rather than as a field on the cache because it is read from the
// store per request: an admin who shortens the TTL or reorders the sources gets that on the next
// page view, not at the next restart. What it does NOT reach back into is the entries already
// stored — those keep the expiry they were written with, which is what the panel's clear button is
// for.
//
// The returned *QuoteResp is SHARED with every other caller holding the same cache entry. Nothing
// downstream may mutate it; the handler copies the struct before stamping Cached on it.
func (c *quoteCache) fetchUnder(ctx context.Context, cfg quoteConfig, market, code string, bars int, iv quoteInterval) (*QuoteResp, bool, time.Duration, error) {
	c.init()
	cfg = cfg.orDefaults()
	key := quoteCacheKey(market, code, bars, iv)
	if resp, left, ok := c.get(key); ok {
		return resp, true, left, nil
	}

	c.flightMu.Lock()
	if fl := c.flights[key]; fl != nil {
		c.flightMu.Unlock()
		select {
		case <-fl.done:
		case <-ctx.Done():
			// This caller gave up; the leader carries on, because the other waiters still want the
			// answer and the upstream call is already paid for.
			return nil, false, 0, ctx.Err()
		}
		if fl.err != nil {
			return nil, false, 0, fl.err
		}
		// A follower is served the leader's answer, which is in the LRU by now, and reports it as
		// cached. The flag answers "did this response cost an upstream call", which is the question
		// both the 缓存 chip and Cache-Control actually need answered — a follower made none.
		return fl.resp, true, cfg.ttlFor(fl.resp, iv), nil
	}
	fl := &quoteFlight{done: make(chan struct{})}
	c.flights[key] = fl
	c.flightMu.Unlock()

	// Re-checked under the flight: a request that arrived while the PREVIOUS flight was between
	// storing its result and deleting its map entry would otherwise start a second identical call
	// against a cache that already has the answer.
	if resp, left, ok := c.get(key); ok {
		fl.resp = resp
		c.endFlight(key, fl)
		return resp, true, left, nil
	}

	// The upstream call runs on a context DETACHED from the leader's request. The leader is only
	// whichever tab happened to arrive first, and Go cancels a request's context the moment that tab
	// navigates away — which used to cancel the shared load, hand every follower still waiting a
	// context.Canceled that the handler turns into 503 quote_unavailable, and record a client
	// disconnect against the vendor's health as though the vendor had gone down. A FOLLOWER still
	// holds its own context and can still give up on the select above; the LEADER cannot, because it
	// calls load synchronously — so an abandoned leader's handler goroutine stays alive until the
	// load finishes or quoteUpstreamTimeout fires. That is the price of the detach, and it is
	// bounded by that timeout rather than by anything the client does.
	loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), quoteUpstreamTimeout)
	defer cancelLoad()

	ttl := time.Duration(0)
	// The interval is the CALLER's now rather than something worked out from the market here: the 1d
	// and 5d ranges let a request choose one, so the same market and the same code can be two
	// different questions. It travels into quoteCacheKey above for the same reason.
	fl.resp, fl.err = c.load(loadCtx, cfg, market, code, bars, iv)
	if fl.err == nil {
		ttl = cfg.ttlFor(fl.resp, iv)
		c.put(key, fl.resp, ttl)
	}
	resp, err := fl.resp, fl.err
	c.endFlight(key, fl)
	if err != nil {
		return nil, false, 0, err
	}
	return resp, false, ttl, nil
}

// ---------- many symbols, one call ----------

// quoteBatchEntry is one symbol's answer out of a batch: the SHARED response — nothing downstream
// may mutate it, exactly as with fetchUnder's — and whether it cost an upstream call.
type quoteBatchEntry struct {
	resp   *QuoteResp
	cached bool
}

// fetchBatchUnder answers many symbols at once, keyed by vendor symbol, and is the whole of what
// GET /api/quotes does with the vendors.
//
// Three properties, and each one is why a batch is not a loop over fetchUnder:
//
//   - ONE upstream call for N symbols. The loop would be N, aimed at one vendor from one address,
//     on the busiest page in the portal.
//   - the SAME cache. A card is served by any live entry for that symbol, whatever range or interval
//     put it there (snapshotFor), so a reader who has just had a stock page open costs nothing when
//     the home feed asks about the same code. The reverse — a card warming a reading page — cannot
//     work and is worth being precise about rather than claiming: a card's answer carries no series,
//     so it cannot answer a request for 66 daily bars, and pretending otherwise would serve a chart
//     with nothing in it.
//   - PER-SYMBOL failure. A symbol missing from the answer is missing from the map; it does not
//     take the other forty-nine with it. The vendor half of that promise is in parseTencentBatch,
//     which drops a line that fails the gate and keeps the rest.
//
// A total failure — no batching source enabled, transport down, an unreadable body — returns what
// the cache had and nothing else. There is no error out-parameter because there is nothing the home
// feed could do with one: the cards render without prices, which is exactly how they render today.
func (c *quoteCache) fetchBatchUnder(ctx context.Context, cfg quoteConfig, targets []quoteTarget) map[string]quoteBatchEntry {
	c.init()
	cfg = cfg.orDefaults()
	out := make(map[string]quoteBatchEntry, len(targets))
	misses := make([]quoteTarget, 0, len(targets))
	// Deduplicated HERE as well as by the caller, and the two are not the same guard wearing two
	// hats. The handler's shapes its `missing` list, which is built by walking the targets it
	// assembled and never reaches this far. This one is the VENDOR's: it keeps a repeat out of the
	// vendor's URL no matter who assembled the list, which is a promise about every caller this
	// method will ever have rather than about the one it has today.
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t.Symbol] {
			continue
		}
		seen[t.Symbol] = true
		if resp, ok := c.snapshotFor(t.Market.id, t.Code); ok {
			out[t.Symbol] = quoteBatchEntry{resp: resp, cached: true}
			continue
		}
		misses = append(misses, t)
	}
	if len(misses) == 0 {
		return out
	}
	for sym, resp := range c.loadBatch(ctx, cfg, misses) {
		out[sym] = quoteBatchEntry{resp: resp, cached: false}
	}
	return out
}

// loadBatch is the single-flighted upstream half. The flight key is the SET of symbols being asked
// for, so ten browsers opening the same home page in the same millisecond make one call between them
// — the same collapse fetchUnder does per symbol, keyed by the thing a batch actually is.
//
// Different pages produce different sets and therefore different flights, which is the honest limit
// of this: two overlapping-but-unequal sets are two calls. Making them one would need a scheduler
// that holds requests back to coalesce them, and a home page that waits on a queue is worse than a
// second request the vendor will not notice.
func (c *quoteCache) loadBatch(ctx context.Context, cfg quoteConfig, targets []quoteTarget) map[string]*QuoteResp {
	syms := make([]string, 0, len(targets))
	for _, t := range targets {
		syms = append(syms, t.Symbol)
	}
	sort.Strings(syms)
	key := "batch:" + strings.Join(syms, ",")

	c.flightMu.Lock()
	if fl := c.flights[key]; fl != nil {
		c.flightMu.Unlock()
		select {
		case <-fl.done:
			return fl.many
		case <-ctx.Done():
			return nil
		}
	}
	fl := &quoteFlight{done: make(chan struct{})}
	c.flights[key] = fl
	c.flightMu.Unlock()
	// Detached for the same reason fetchUnder's load is: the leader is whichever tab arrived first,
	// and its context dies the moment that tab navigates away — which would cancel the call every
	// other waiter is still holding out for, and record a client disconnect against the vendor's
	// health as though the vendor had gone down.
	loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), quoteUpstreamTimeout)
	defer cancelLoad()
	fl.many = c.loadBatchUncached(loadCtx, cfg, targets)
	many := fl.many
	c.endFlight(key, fl)
	return many
}

// loadBatchUncached walks the enabled sources for one that can answer in bulk, and stores what comes
// back under the same keys fetchUnder uses for a snapshot.
//
// A source is asked only about the symbols it has DECLARED it can serve a snapshot for — and only
// about codes it has a spelling for — so a batch spanning four markets cannot blame a vendor for the
// one market it never claimed. The same rule sourcesFor applies per request, applied here per symbol.
func (c *quoteCache) loadBatchUncached(ctx context.Context, cfg quoteConfig, targets []quoteTarget) map[string]*QuoteResp {
	byName := make(map[string]quoteSource, len(c.sources))
	for _, src := range c.sources {
		byName[src.name] = src
	}
	out := map[string]*QuoteResp{}
	left := targets
	for _, name := range cfg.Order {
		if len(left) == 0 {
			return out
		}
		src, ok := byName[name]
		if !ok || src.fetchBatch == nil {
			// Not a failure and not recorded as one: a source with no batch endpoint has not been
			// asked anything. See quoteBatchFetchFunc for why it is not emulated with a loop.
			continue
		}
		mine := make([]quoteTarget, 0, len(left))
		rest := make([]quoteTarget, 0, len(left))
		for _, t := range left {
			if src.serves(t, quoteIntervalSnapshot) {
				mine = append(mine, t)
			} else {
				rest = append(rest, t)
			}
		}
		if len(mine) == 0 {
			continue
		}
		// One slot on the upstream ceiling for the whole batch — it is one conversation, however many
		// symbols it names.
		release, err := c.acquire(ctx)
		if err != nil {
			return out
		}
		got, err := src.callBatch(ctx, mine)
		release()
		switch {
		case err != nil:
			// A cancelled context is this side giving up, not the vendor failing — the same rule
			// load applies, and for the same reason: these counters are what an operator reads to
			// decide whether a source is down.
			if ctx.Err() == nil {
				c.noteFailure(src.name, err)
			}
		case len(got) == 0:
			// The source ANSWERED and our own gate dropped every line it sent. That is neither of the
			// other two outcomes and it is recorded as neither.
			//
			// It is not a failure. A page whose only card is a suspended or delisted code is a batch
			// of one refused line, and marking the vendor failed for it made the consecutive-failure
			// streak an operator reads climb on every single page view while Tencent answered
			// perfectly — the exact opposite of what those counters are for, and a direct
			// contradiction of this endpoint's promise that a per-symbol failure costs only that
			// symbol. The vendor half of "it answered" is enforced where it can be: a body that
			// carried no line for ANY symbol asked about is an error from the parser (fetchTencentBatch),
			// so it arrives above as a failure and a source answering rubbish all day cannot look
			// healthy by falling in here.
			//
			// It is not a success either. Every line failing the gate is what a vendor whose array
			// shape has drifted looks like, and resetting the streak to zero on that would erase the
			// evidence of a real outage that was in progress. The refusals are named per symbol in the
			// log by the parser; the counters keep whatever the last real outcome was.
		default:
			c.noteSuccess(src.name)
		}
		for _, t := range mine {
			resp, ok := got[t.Symbol]
			if !ok || resp == nil {
				// This symbol alone; it falls through to the next batching source, and if there is
				// none it is simply absent from the answer.
				rest = append(rest, t)
				continue
			}
			out[t.Symbol] = resp
			// Stored under the SNAPSHOT key, which quoteCacheKey normalises the bar count out of —
			// so the next card, and the next reading page for a market with no series, are hits.
			c.put(quoteCacheKey(t.Market.id, t.Code, 0, quoteIntervalSnapshot), resp,
				cfg.ttlFor(resp, quoteIntervalSnapshot))
		}
		left = rest
	}
	return out
}

// snapshotFor returns a live cached answer for one symbol whatever it was fetched for — a card asks
// for a price and a price is in every response this cache holds, so a 3个月 chart fetched a moment
// ago for the same code answers a card without a second call.
//
// The FRESHEST is returned rather than whichever the map iteration reached first: entries for one
// symbol differ only in when they expire, Go randomises map order, and a batch that answered from a
// different entry on every request would make the 缓存 chip and the TTL beside it meaningless.
//
// It does not promote what it finds to the front of the LRU. A card is a decoration and should not
// be able to keep a reading page's chart alive at the expense of another reader's.
func (c *quoteCache) snapshotFor(market, code string) (*QuoteResp, bool) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := market + code + ":"
	now := c.now()
	var best *quoteCacheEntry
	for key, element := range c.entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		entry := element.Value.(*quoteCacheEntry)
		if !entry.expires.After(now) {
			continue
		}
		if best == nil || entry.expires.After(best.expires) {
			best = entry
		}
	}
	if best == nil {
		return nil, false
	}
	return best.resp, true
}

// endFlight publishes the leader's result to its waiters. The map entry goes first and the channel
// closes second, so a goroutine that misses the cache after this point starts a NEW flight instead
// of joining one that has already answered.
func (c *quoteCache) endFlight(key string, fl *quoteFlight) {
	c.flightMu.Lock()
	if c.flights[key] == fl {
		delete(c.flights, key)
	}
	c.flightMu.Unlock()
	close(fl.done)
}

// QuoteHealth reports each vendor's recent record — the counters the 管理 → 行情 panel reads
// (quote_admin_api.go).
func (s *Server) QuoteHealth() []QuoteSourceHealth { return s.quotes.snapshotHealth() }

// ---------- what the admin panel asks the cache ----------

// quoteCacheStats is the occupancy half of that panel, in BOTH units rather than one: the entry
// count is what binds when readers ask for short ranges and the byte budget is what binds when they
// ask for 1y, so a single number would be silent about whichever of quoteCacheMaxEntries and
// quoteCacheMaxBytes is actually doing the work today.
type quoteCacheStats struct {
	Entries int
	Bytes   int
}

func (c *quoteCache) stats() quoteCacheStats {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statsLocked()
}

// statsLocked is the same reading for a caller that already holds mu — clear needs it before it
// empties the map, and taking the lock twice there would let a fetch land in between and be
// reported as dropped when it was not.
func (c *quoteCache) statsLocked() quoteCacheStats {
	return quoteCacheStats{Entries: len(c.entries), Bytes: c.bytes}
}

// clear drops every entry and returns what it dropped. The count is not on the wire — the panel
// reads the fresh occupancy back instead — but it is what makes "the button emptied the cache" an
// assertion rather than an impression.
//
// The health counters SURVIVE it. They are the record of what the vendors have been doing, not
// cached data, and an operator who clears the cache to test a source would otherwise erase the
// evidence they were about to read. The in-flight single-flights survive too: a leader mid-call
// still stores its answer afterwards, which is correct — that answer was fetched fresh.
func (c *quoteCache) clear() quoteCacheStats {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := c.statsLocked()
	c.entries = make(map[string]*list.Element)
	c.lru.Init()
	c.bytes = 0
	return dropped
}

// quoteSourceInfo is one compiled-in source as the panel sees it, before the operator's order and
// the health counters are laid over it.
type quoteSourceInfo struct {
	Name string
	// Markets is the UNION over the intervals — every market this source can say anything about.
	Markets []string
	// Daily and Intraday are the same claim split by what it is a claim ABOUT, because the union
	// above is a wider statement than either and an operator moving a source up the order is
	// choosing among these rather than among names. An EMPTY list is a real answer here ("serves no
	// intraday"), and the panel renders it as a dash rather than as a blank cell, so that it cannot
	// be confused with a field the server did not send.
	Daily []string
	// The TWO intraday windows, separately. They used to be one union under a 分时 heading, and that
	// union is what let the panel advertise a window nothing served: Tencent declared the one-session
	// interval only, the column named its three markets, and an operator read that as "5日 works
	// here" while every 5日 request in every market degraded to a snapshot. A column whose two halves
	// can differ has to be able to say so.
	Intraday   []string
	Intraday5D []string
}

// describeSources reports the sources this cache actually holds, not the shipped list: a build whose
// source table has been swapped (a test) must describe what it will really call, or the panel is a
// picture of a different program.
func (c *quoteCache) describeSources() []quoteSourceInfo {
	c.init()
	out := make([]quoteSourceInfo, 0, len(c.sources))
	for _, src := range c.sources {
		out = append(out, quoteSourceInfo{
			Name:       src.name,
			Markets:    src.marketIDs(),
			Daily:      src.marketIDsFor(quoteIntervalDaily),
			Intraday:   src.marketIDsFor(quoteIntervalIntraday),
			Intraday5D: src.marketIDsFor(quoteIntervalIntraday5D),
		})
	}
	return out
}
