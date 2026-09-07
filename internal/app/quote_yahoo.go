package app

// quote_yahoo.go —— Yahoo Finance as a third quote source: compiled in, listed in 管理 → 行情源,
// and OFF.
//
// It is absent from quoteShippedOrder, so an unconfigured portal never calls it and nothing about
// today's behaviour changes. Why it ships that way is not caution about the parser — it is that the
// decision is not the build's to make. `query1.finance.yahoo.com/v8/finance/chart` is undocumented
// and unlicensed: there is no contract behind it, no changelog, and no terms this repository can
// point an operator at. Whether to make a deployment depend on it is an operator's call about their
// own deployment, and the repo has the precedent for exactly that shape — ADR 0017's cleanup targets
// and the GeoIP refresh loop both ship compiled-in and switched off. An operator turns it on by
// adding "yahoo" to quote_source_order; nothing else is needed and no second flag exists.
//
// What it buys, and it is the hole ADR 0030 §4 spends a paragraph on: `AAPL?range=6mo&interval=1d`
// answers with 128 daily bars, where Tencent answers a sixty-bar request for usAAPL with TWO rows
// fifteen years apart. A US chart is the thing this portal cannot draw today, and this is the only
// measured way to draw it.
//
// ---------- one call, and a different shape from either Chinese vendor ----------
//
// `meta` is the snapshot (price, day high/low, volume, currency, exchange zone), `timestamp[]` is
// epoch seconds and `indicators.quote[0].{open,high,low,close,volume}` are five PARALLEL arrays
// indexed by it. That is the opposite hazard from Tencent's: nothing here is positional, so an
// inserted column cannot rename a field — but the arrays can disagree in LENGTH with each other and
// with `timestamp[]`, and any element of any of them can be null. Neither failure is an error on the
// wire; both render.
//
// ---------- three traps, all measured 2026-09-07 against the fixtures beside this file ----------
//
//  1. `meta.chartPreviousClose` is the close before the REQUESTED RANGE, not yesterday's close. For
//     AAPL on the same afternoon it is 328.21 at range=1d, 319.70 at 5d and 262.52 at 6mo. Taking it
//     as prevClose on the six-month chart renders Apple at +21.9% on a day it fell 2.51%. It is
//     therefore NOT A FIELD OF yahooChartMeta — see the note there — and prevClose is derived from
//     the series itself; quoteYahooPrevClose carries the rule.
//  2. `00700.HK` answers HTTP 200 with a well-formed body describing a DIFFERENT INSTRUMENT: a
//     mutual fund on "YHD" with a null currency and a June 2019 timestamp. Hong Kong on Yahoo is
//     FOUR digits (`0700.HK`) while this portal's canonical HK code is five (`00700`), so the naive
//     mapping is the wrong one and it fails silently. quoteYahooSymbol is where the leading zero
//     goes, and the gate below is what stands behind it.
//  3. Prices are JSON floats — `328.30999755859375` is how the wire spells 328.31. Every number in
//     here is decoded as json.Number, which keeps the vendor's own literal, and parsed by quoteFen,
//     which is integer arithmetic end to end. No float64 touches a price, per ADR 0028 §10.
//
// ---------- the gate ----------
//
// Every source in this portal is behind a drift gate; this one's checks are not the same five,
// because the response is not the same shape. quote.go's check 3 — the vendor's own percentage
// against its own two prices — DOES still apply here and is run, but only because the prevClose it
// needs is derived (see quoteYahooPrevClose); it is the check that makes trap 1 impossible to
// reintroduce at runtime rather than merely covered by a test. What replaces the positional checks
// is written at parseYahooChart, in the order they run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	quoteSourceYahoo = "yahoo"

	// The two query parameters are Yahoo's own words: `range` is the window and `interval` is the
	// granularity. Both are chosen from quoteYahooWindow's table and never from anything a caller
	// typed. The SYMBOL, which is, lands in the PATH rather than in a query parameter — so it is
	// built by quoteYahooSymbol out of a canonical code and a literal suffix, and a "/" or a "?" can
	// no more reach it than it can reach a Tencent param=.
	quoteYahooChartURL = "https://query1.finance.yahoo.com/v8/finance/chart/%s?range=%s&interval=%s"

	// The tolerance between meta.regularMarketPrice and the last bar's close: ONE 分. They are the
	// same number on all five captured bodies, and the slack is for the float64 the vendor rendered
	// them from rather than for disagreement.
	quoteYahooSnapshotTolerance = 1

	// A one-minute series is 400 points a day, so the intraday windows below need more headroom than
	// quoteDefaultBars. Above this a request is served the five-day, five-minute window instead —
	// see quoteYahooWindow.
	quoteYahooIntradayDayPoints = 400
)

var (
	// errQuoteYahooCurrency is the check that catches trap 2 when the symbol echo does not: the fund
	// body carries `"currency": null`, and a market whose currency the vendor will not name is a
	// market this response is not about.
	errQuoteYahooCurrency = errors.New("quote: yahoo sent no currency, or not the one this market quotes")
	// errQuoteYahooSeries is a series that does not describe itself: parallel arrays of different
	// lengths, or one that is mostly holes.
	errQuoteYahooSeries = errors.New("quote: yahoo's timestamps and prices do not describe one series")
	// errQuoteYahooDrift is the snapshot disagreeing with the series in the SAME response. One call
	// returns both, so they cannot be two moments in time — if they differ, one of them is being read
	// out of the wrong place.
	errQuoteYahooDrift = errors.New("quote: yahoo's own snapshot price disagrees with its own last bar")
	// errQuoteYahooSymbolShape is a market this file has no MEASURED symbol for. It is a refusal and
	// not a guess, because guessing a symbol is exactly what trap 2 costs: a plausible spelling that
	// answers 200 about something else.
	errQuoteYahooSymbolShape = errors.New("quote: this portal has no measured yahoo symbol for that market")
	// errQuoteYahooInterval is an interval this fetcher has no window for. Unreachable through the
	// resolver, which only routes what the declaration below claims; it is here so that a caller who
	// skipped the resolver gets an error rather than a daily series labelled as something else.
	errQuoteYahooInterval = errors.New("quote: yahoo has no window for that interval")
)

// ---------- the wire shape ----------

type yahooChartEnvelope struct {
	Chart struct {
		Result []yahooChartResult `json:"result"`
		// Error is null on success and an object on failure — and the failures measured so far also
		// carry a non-2xx status, which vendorGet refuses before this file sees the body. It is
		// decoded anyway: a 200 with an error object would otherwise read as a result-less success.
		Error *yahooChartError `json:"error"`
	} `json:"chart"`
}

type yahooChartError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type yahooChartResult struct {
	Meta      yahooChartMeta `json:"meta"`
	Timestamp []int64        `json:"timestamp"`
	// Indicators carries `quote` (the unadjusted series) and `adjclose` (the back-adjusted closes).
	// ONLY quote is declared here, and that is ADR 0028's unadjusted rule rather than an omission: a
	// back-adjusted series rewrites its own history every time a dividend is paid, so a report scored
	// against it stops matching the price it was written about.
	Indicators struct {
		Quote []yahooQuoteArrays `json:"quote"`
	} `json:"indicators"`
}

// yahooChartMeta is the snapshot half. Every number is a json.Number so that the vendor's own
// decimal literal survives to quoteFen; decoding one as float64 and multiplying by a hundred is the
// bug ADR 0028 §10 exists to prevent.
//
// A null in any of these leaves the zero value — an empty json.Number, which quoteScaled reads as 0
// — so "the vendor did not send it" and "the vendor sent zero" are the same thing here. That is why
// the price is checked for emptiness explicitly before it is parsed rather than after.
type yahooChartMeta struct {
	Symbol   string `json:"symbol"`
	Currency string `json:"currency"`
	// The vendor's own name for the instrument. shortName is the one every captured body carries
	// ("Apple Inc.", "KWEICHOW MOUTAI", "TENCENT"); longName is present on some and is the fallback.
	// Both are romanised even for A-shares, where this portal's own stocks table holds the Chinese
	// name — a quote fetched from here therefore names 贵州茅台 in English, which is worth knowing
	// before enabling it as a fallback for a market the reading page shows names in.
	ShortName string `json:"shortName"`
	LongName  string `json:"longName"`
	// InstrumentType is "EQUITY" on all five real bodies and "MUTUALFUND" on the trap. It is read
	// only to answer QuoteResp.Kind, and only "EQUITY" is answered, because "INDEX" is a spelling
	// this file has never measured — see quoteYahooKind.
	InstrumentType string `json:"instrumentType"`
	// RegularMarketTime is the VENDOR's clock, epoch seconds, and is what AsOf is stamped from. Not
	// this server's clock: a feed that has stopped updating is only visible as stale if the time
	// shown is the feed's own.
	RegularMarketTime int64 `json:"regularMarketTime"`

	RegularMarketPrice   json.Number `json:"regularMarketPrice"`
	RegularMarketDayHigh json.Number `json:"regularMarketDayHigh"`
	RegularMarketDayLow  json.Number `json:"regularMarketDayLow"`
	RegularMarketVolume  json.Number `json:"regularMarketVolume"`
	// RegularMarketChangePercent is the vendor's own percentage, and ADR 0028's rule that the
	// percentage shown is always the vendor's own applies here as it does to Tencent. The v3 contract
	// said Yahoo publishes none; measured today it publishes this on all five real bodies, agreeing
	// with the derived prevClose to four parts in 10^5 across four markets — which is precisely what
	// makes quoteCheckIdentity worth running on this path.
	RegularMarketChangePercent json.Number `json:"regularMarketChangePercent"`
	// PreviousClose is the DAY's previous close and is what an intraday window's prevClose comes
	// from. It is null on both captured DAILY bodies and present on all three intraday ones, so it is
	// the second choice for a daily response and the first for an intraday one.
	PreviousClose json.Number `json:"previousClose"`

	// chartPreviousClose IS DELIBERATELY NOT DECLARED HERE, and adding it is trap 1. It is the close
	// before the requested RANGE — 262.52 for AAPL at range=6mo on the afternoon the day's real
	// previous close was 328.21 — so a reader who reaches for it because the name sounds right gets a
	// chart of a stock up 21.9% on a day it fell 2.51%. Nothing in this file may read it; prevClose
	// comes from quoteYahooPrevClose, and TestYahooPrevCloseIsNotChartPreviousClose is the pin.
}

// yahooQuoteArrays are the five parallel series, indexed by yahooChartResult.Timestamp.
//
// The elements are POINTERS because Yahoo pads with nulls: 69 of 0700.HK's 400 one-minute points and
// 89 of 600519.SS's 331 five-minute points are null on the captured bodies, including the LAST point
// of the Hong Kong one. Decoded into []json.Number a null would arrive as the empty string, which
// quoteScaled reads as 0 — and a zero-filled bar is a candle drawn at the bottom of the chart on a
// minute nothing traded. A nil element is a hole, and a hole is skipped.
type yahooQuoteArrays struct {
	Open   []*json.Number `json:"open"`
	High   []*json.Number `json:"high"`
	Low    []*json.Number `json:"low"`
	Close  []*json.Number `json:"close"`
	Volume []*json.Number `json:"volume"`
}

// yahooReader is quoteFenReader's job for a KEYED body: it reads json.Numbers into integer 分 or
// share counts and remembers the first that would not parse, so that reading a dozen fields is not a
// dozen copies of the same error check. It names the FIELD rather than an index, because unlike
// Tencent's array this body has names to give.
type yahooReader struct{ err error }

func (r *yahooReader) fen(what string, n json.Number) int64 { return r.scaled(what, n, 2) }

// shares reads a share count. Yahoo counts SHARES in every market — there is no 手 here and
// quoteMarket.lots must not be applied: that flag is a fact about Tencent's columns, and multiplying
// this one by a hundred reports four billion Apple shares in a day. The refusal of a negative is
// quoteFenReader.shares's, for the same reason: nothing in the drift gate looks at volume, so a
// negative would travel all the way to a bar drawn the wrong way.
func (r *yahooReader) shares(what string, n json.Number) int64 {
	v := r.scaled(what, n, 0)
	if r.err == nil && v < 0 {
		r.err = fmt.Errorf("%w: %s is %d", errQuoteVolume, what, v)
		return 0
	}
	return v
}

func (r *yahooReader) scaled(what string, n json.Number, scale int) int64 {
	if r.err != nil {
		return 0
	}
	v, err := quoteScaled(string(n), scale)
	if err != nil {
		r.err = fmt.Errorf("quote: yahoo %s: %w", what, err)
		return 0
	}
	return v
}

// ---------- the symbol ----------

// quoteYahooSymbol maps this portal's canonical (market, code) onto Yahoo's spelling. Every mapping
// below was MEASURED; a market with no measured mapping is refused rather than guessed at, which is
// the whole lesson of trap 2.
//
//	sh 600519 -> 600519.SS      sz 000001 -> 000001.SZ
//	hk 00700  -> 0700.HK        us AAPL   -> AAPL
//
// Hong Kong is the one that is not a suffix. This portal's canonical HK code is five digits
// (quoteCodeDigits5, the shape Tencent's hk00700 needs); Yahoo's is four, and the five-digit
// spelling `00700.HK` is not an error there — it is a 2019 mutual fund on a different exchange with
// a null currency. So exactly ONE leading zero is dropped, and a five-digit HK code that does not
// begin with one is refused: 80737 and friends exist on that exchange, this file has never measured
// what Yahoo calls them, and "probably 8073.HK" is the sentence trap 2 punishes.
//
// Beijing has no mapping at all. `.BJ` is a plausible suffix and an unmeasured one, and the failure
// mode of a wrong-but-plausible symbol here is a wrong answer rather than an error.
func quoteYahooSymbol(t quoteTarget) (string, error) {
	switch t.Market.id {
	case "sh":
		return t.Code + ".SS", nil
	case "sz":
		return t.Code + ".SZ", nil
	case "hk":
		if !strings.HasPrefix(t.Code, "0") {
			return "", fmt.Errorf("%w: hk %s does not start with the leading zero yahoo drops", errQuoteYahooSymbolShape, t.Code)
		}
		return t.Code[1:] + ".HK", nil
	case "us":
		return t.Code, nil
	}
	return "", fmt.Errorf("%w: %s", errQuoteYahooSymbolShape, t.Market.id)
}

// quoteYahooKnows is quoteYahooSymbol asked as a yes/no question, and it is what this source hands
// the resolver as quoteSource.knows — the same function the fetcher will call, so the two cannot come
// to disagree about which codes this vendor can be asked about.
//
// It exists because a capability grant is a predicate over the MARKET and the Hong Kong refusal is
// about the CODE: canonicalCode accepts any five digits there, and 80737 is a real code with no
// measured Yahoo spelling. Without this the resolver handed such a request to this source, the fetcher
// refused it before any HTTP request, and load recorded a sentence about our own symbol table against
// the vendor's health before answering 503 for a gap no retry closes.
//
// A market with no row is not a target this source can spell either — only a caller that skipped
// quoteResolve produces one, and quoteYahooSymbol would dereference it.
func quoteYahooKnows(t quoteTarget) bool {
	if t.Market == nil {
		return false
	}
	_, err := quoteYahooSymbol(t)
	return err == nil
}

// quoteYahooWindow picks the range and interval parameters for a request. The four combinations at
// the top of each branch are the ones captured in testdata/quote/; the rest of the daily table is
// the same series at a longer window and was not separately measured.
//
// The bar count is a REQUEST for a number of points, and Yahoo takes a window rather than a count —
// so the smallest window that COVERS the request is asked for and the surplus is trimmed off the
// front afterwards, exactly as the Tencent path trims. The thresholds are sessions per window at the
// density the captured body shows (6mo answered 128 bars, about 21.3 a month), and they are
// deliberately conservative: two of quote_api.go's four ranges therefore land on the next window up
// — quoteRange1M asks for 22 bars and a 1mo window holds about 21 — because a chart one bar short of
// the range it is labelled with is worse than a body twice the size.
func quoteYahooWindow(iv quoteInterval, bars int) (rng, gran string, err error) {
	switch iv {
	case quoteIntervalDaily:
		switch {
		case bars <= 21:
			return "1mo", "1d", nil
		case bars <= 63:
			return "3mo", "1d", nil
		case bars <= 125:
			return "6mo", "1d", nil // measured: AAPL, 128 bars
		case bars <= 250:
			return "1y", "1d", nil
		case bars <= 500:
			return "2y", "1d", nil
		}
		return "5y", "1d", nil
	case quoteIntervalIntraday5D:
		// Five sessions at five minutes, asked for by name. The 5日 range carries its own interval
		// now, so this branch does not have to guess from a point count — and it is a separate
		// interval rather than a longer window of the one below because Tencent's minute endpoint
		// can serve that one and cannot serve this. Measured: 331 points for 600519.SS.
		return "5d", "5m", nil
	case quoteIntervalIntraday:
		// One trading day at one-minute resolution — 391 points measured for AAPL, 400 for 0700.HK.
		//
		// The five-day fallback below it is what this branch did before quoteIntervalIntraday5D
		// existed, and it is kept rather than removed: a caller asking for more points than a single
		// session holds is asking for more than one session, and answering that with a day of
		// minutes would silently drop most of what it asked for. The 分时 range asks for
		// quoteRange1D, which is exactly this ceiling, so the shipped path takes the first line.
		if bars <= quoteYahooIntradayDayPoints {
			return "1d", "1m", nil
		}
		return "5d", "5m", nil
	}
	return "", "", fmt.Errorf("%w: %s", errQuoteYahooInterval, iv)
}

// ---------- the fetch ----------

// fetchYahooQuote is the source's fetcher, and it takes the INTERVAL because for this vendor the
// answer depends on it: one URL serves both, and asking for a daily window while the caller wanted
// minutes returns a perfectly well-formed wrong answer. That is why quoteSource.fetchAt exists and
// why this source sets it rather than fetch.
func fetchYahooQuote(ctx context.Context, market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, err
	}
	sym, err := quoteYahooSymbol(target)
	if err != nil {
		return nil, err
	}
	n := quoteBarCount(bars)
	rng, gran, err := quoteYahooWindow(iv, n)
	if err != nil {
		return nil, err
	}
	rawURL := fmt.Sprintf(quoteYahooChartURL, sym, rng, gran)
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()
	// No Referer: this host answers without one, and vendorGet already sends the browser agent that
	// it does need — Go's default agent gets an empty body here.
	body, err := vendorGet(ctx, rawURL, "", quoteBodyLimit)
	if err != nil {
		vendorLogf(rawURL, "quote fetch failed for %s: %v", sym, err)
		return nil, err
	}
	resp, err := parseYahooChart(market, code, body, n, iv)
	if err != nil {
		vendorLogf(rawURL, "quote parse failed for %s: %v", sym, err)
		return nil, err
	}
	return resp, nil
}

// parseYahooChart turns one chart response into a QuoteResp, running this source's gate as it goes.
// The checks, in the order they run and why each is where it is:
//
//  1. the envelope answered at all — an error object, no result, no quote arrays.
//  2. SYMBOL ECHO, case-insensitively. First, because every other check is a question about a
//     response we have not yet established is about the right instrument. This is what a request for
//     `00700.HK` runs into once the mapping is right: the body echoes the symbol it is actually
//     about, and it is not the one we asked for.
//  3. CURRENCY: non-empty, and the one this market's row in quoteMarkets says. It is the check the
//     fund body dies on if the symbol echo ever agrees with it — `"currency": null` — and it is also
//     what would catch a US body answered for a Hong Kong request.
//  4. the SERIES describes itself: timestamp[] and all five quote arrays the same length, nulls
//     skipped rather than zero-filled, and a window more than half holes refused.
//  5. the price CEILING over every snapshot field (quote.go's, unchanged).
//  6. the arithmetic IDENTITY (quote.go's check 3, unchanged): the vendor's own percentage against
//     the last price and the DERIVED prevClose. This is the runtime half of trap 1 — chartPreviousClose
//     as prevClose implies +21.88% where the vendor says -2.511%, which is 1000 times the tolerance.
//  7. SNAPSHOT vs SERIES: meta.regularMarketPrice within one 分 of the last bar's close, or no bars.
//     One call returned both halves, so a disagreement is not staleness, it is a misread.
//  8. the day RANGE (quote.go's check 5, unchanged): the last price inside the day's own high..low.
//     BAR SANITY, quote.go's check 4, ran inside step 4 above — quoteYahooBars applies it to the
//     series it just parsed, which is where the bars are.
func parseYahooChart(market, code string, body []byte, bars int, iv quoteInterval) (*QuoteResp, error) {
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, err
	}
	m := target.Market
	// The canonical code from here on, never the caller's spelling — it is what the symbol was built
	// from and what the response reports back, and "aapl" and "AAPL" have to be one answer.
	code = target.Code
	sym, err := quoteYahooSymbol(target)
	if err != nil {
		return nil, err
	}

	// (1) the envelope.
	var env yahooChartEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("quote: decode yahoo response: %w", err)
	}
	if e := env.Chart.Error; e != nil {
		return nil, fmt.Errorf("quote: yahoo returned %s (%s)", e.Code, e.Description)
	}
	if len(env.Chart.Result) == 0 {
		return nil, fmt.Errorf("quote: yahoo response has no result for %q", sym)
	}
	res := env.Chart.Result[0]
	meta := res.Meta

	// (2) symbol echo.
	if err := quoteCheckYahooSymbol(sym, meta.Symbol); err != nil {
		return nil, err
	}
	// (3) currency.
	if err := quoteCheckYahooCurrency(m, meta.Currency); err != nil {
		return nil, err
	}

	loc, err := quoteZone(m.zone)
	if err != nil {
		return nil, err
	}
	// (4) the series.
	all, err := quoteYahooBars(loc, iv, &res)
	if err != nil {
		return nil, err
	}

	r := &yahooReader{}
	snap := QuoteSnapshot{Session: quoteSessionUnknown}
	// The price is checked for PRESENCE rather than parsed and compared to zero: a null decodes to an
	// empty json.Number, quoteScaled reads that as 0 without complaint, and a snapshot with no price
	// is the fund body's shape as much as it is a suspended stock's. A quote with no price is not a
	// quote.
	if strings.TrimSpace(string(meta.RegularMarketPrice)) == "" {
		return nil, fmt.Errorf("quote: yahoo sent no regularMarketPrice for %s", sym)
	}
	snap.Last = r.fen("regularMarketPrice", meta.RegularMarketPrice)
	snap.High = r.fen("regularMarketDayHigh", meta.RegularMarketDayHigh)
	snap.Low = r.fen("regularMarketDayLow", meta.RegularMarketDayLow)
	snap.Volume = r.shares("regularMarketVolume", meta.RegularMarketVolume)
	snap.PrevClose = quoteYahooPrevClose(r, iv, meta, all)
	snap.Open = quoteYahooSessionOpen(all)
	if r.err != nil {
		return nil, r.err
	}
	// Amount stays 0: this endpoint publishes no 成交额 in any market, and price times volume is a
	// number this parser would have invented. The strip renders what the vendor said and nothing else.
	if snap.PrevClose != 0 {
		// Change is derived from the pair beside it rather than read, because THIS vendor's prevClose
		// is derived too: showing a vendor 涨跌 next to a locally derived prevClose is how a strip
		// comes to display two numbers that contradict each other. The percentage stays the vendor's
		// own (ADR 0028), and check 6 below is what keeps the two consistent — including on an
		// ex-rights day, where the vendor's basis and this unadjusted series can genuinely differ and
		// the response is refused rather than rendered.
		snap.Change = snap.Last - snap.PrevClose
		snap.ChangePct = strings.TrimSpace(string(meta.RegularMarketChangePercent))
		if snap.ChangePct == "" {
			// No vendor percentage: derive one, as the Sina path does, and check 6 then has nothing
			// to say — it would be comparing a division against its own inputs.
			snap.ChangePct = quotePctString(quoteDivRound(snap.Change*10_000, snap.PrevClose))
		}
	} else {
		// No previous close anywhere in the response: a percentage without a basis on screen is worse
		// than none, so the change is reported as nothing rather than as the vendor's number beside a
		// prevClose of zero. Same answer the Sina path gives a stock that has never traded.
		snap.ChangePct = "0.00"
	}

	// (5) the ceiling, before anything multiplies these.
	if err := quoteCheckSnapshotPrices(&snap); err != nil {
		return nil, err
	}
	// (6) the identity.
	if err := quoteCheckIdentity(&snap); err != nil {
		return nil, err
	}
	// (7) the snapshot against the series it arrived with.
	if err := quoteCheckYahooSnapshotAgainstBars(snap.Last, all); err != nil {
		return nil, err
	}
	// (8) the day range. It is not redundant beside the identity above: check 6 returns early
	// whenever prevClose is 0 — a never-traded code, or an intraday body with no meta.previousClose —
	// and this is then the only thing that notices a last price outside the day's own high and low.
	// Skipped on a zero bound, so a suspended stock is not reported as a vendor failure; see
	// quoteCheckRange. All five captured bodies pass it.
	if err := quoteCheckRange(&snap); err != nil {
		return nil, err
	}

	if meta.RegularMarketTime <= 0 {
		return nil, fmt.Errorf("quote: yahoo sent no regularMarketTime for %s", sym)
	}
	snap.AsOf = time.Unix(meta.RegularMarketTime, 0).In(loc).Format(time.RFC3339)
	// Session stays "unknown", and the fields to decide it are RIGHT THERE: meta.currentTradingPeriod
	// carries the regular session's start and end as epochs, and regularMarketTime falls outside them
	// on all three captured intraday bodies. What is missing is a captured body from a market that is
	// OPEN, so "inside the window means trading" is a rule this file has never seen hold. Asserting it
	// from the closed cases alone would be a guess, and the cost of guessing wrong is the five-minute
	// closed-market TTL on a market that is trading, or the reverse. Until it is measured, a quote
	// from here caches as a closed market does — slower to refresh, never wrong about what it is.

	out := all
	if bars > 0 && len(out) > bars {
		out = out[len(out)-bars:] // oldest first, so the surplus comes off the front
	}
	resp := &QuoteResp{
		Symbol:     code,
		Name:       quoteYahooName(meta),
		Market:     m.id,
		Kind:       quoteYahooKind(meta),
		Currency:   m.currency,
		TZ:         m.zone,
		Source:     quoteSourceYahoo,
		Snapshot:   snap,
		Bars:       out,
		BarsSource: quoteSourceYahoo,
	}
	if len(out) == 0 {
		resp.BarsSource = ""
		resp.BarsUnavailable = quoteBarsUnavailableFor(m.id)
	}
	return resp, nil
}

// quoteCheckYahooSymbol is the symbol echo, and it is case-insensitive because that is how the
// vendor spells it back: a lower-case request for a US ticker comes back upper-case. It is an
// EQUALITY and not a prefix or a contains — "0700.HK" must not be satisfied by "00700.HK", which is
// the entire point.
func quoteCheckYahooSymbol(want, got string) error {
	if !strings.EqualFold(strings.TrimSpace(got), want) {
		return fmt.Errorf("%w: asked yahoo for %q, it answered about %q", errQuoteCodeEcho, want, got)
	}
	return nil
}

// quoteCheckYahooCurrency asks the response to agree with the market model about what this price is
// denominated in. Two failures, one check: a body with no currency at all (the fund), and a body
// denominated in something this market does not quote (a US answer to a Hong Kong request).
func quoteCheckYahooCurrency(m *quoteMarket, got string) error {
	got = strings.TrimSpace(got)
	if got == "" {
		return fmt.Errorf("%w: %s expects %s, yahoo sent none", errQuoteYahooCurrency, m.id, m.currency)
	}
	if !strings.EqualFold(got, m.currency) {
		return fmt.Errorf("%w: %s expects %s, yahoo sent %q", errQuoteYahooCurrency, m.id, m.currency, got)
	}
	return nil
}

// quoteCheckYahooSnapshotAgainstBars is the check that replaces nothing in quote.go's list and has no
// equivalent on the Chinese vendors: one Yahoo call returns the snapshot and the series together, so
// the two cannot describe different moments. If meta.regularMarketPrice is not the last bar's close,
// one of them is being read out of the wrong place — the arrays have slipped, or the meta belongs to
// another instrument.
//
// An EMPTY series skips it, because there is then nothing to disagree with: a snapshot-only response
// is a legitimate answer for a market with no series, not a drifted one.
func quoteCheckYahooSnapshotAgainstBars(last int64, bars []QuoteBar) error {
	if len(bars) == 0 {
		return nil
	}
	lastBar := bars[len(bars)-1]
	if d := last - lastBar.Close; d > quoteYahooSnapshotTolerance || d < -quoteYahooSnapshotTolerance {
		return fmt.Errorf("%w: regularMarketPrice %d 分, last bar (%s) closed at %d 分",
			errQuoteYahooDrift, last, lastBar.Date, lastBar.Close)
	}
	return nil
}

// quoteYahooBars turns the parallel arrays into candles, oldest first.
//
// Three things it refuses or drops, all measured:
//   - arrays that are not all the same length as timestamp[]. Nothing else in this parser would
//     notice: a short `close` array read by index against a long `timestamp` is a series that simply
//     stops early, and one read the other way is an index panic in a handler goroutine.
//   - a null anywhere in a point. Yahoo pads with them — 69 of 400 on the captured 0700.HK minute
//     series, INCLUDING ITS LAST POINT, and 89 of 331 on 600519.SS. The point is skipped whole. It is
//     never zero-filled: a zero-filled bar is a candle drawn at the bottom of the chart on a minute
//     that never traded, and it survives every other check in this file because its four prices are
//     ordered exactly as a candle's should be.
//   - a window that is MORE THAN HALF holes. A handful of nulls is a quiet market; a majority of them
//     is a response that is not describing the window it was asked for, and drawing the remainder
//     would space an hour of trading evenly across a day.
func quoteYahooBars(loc *time.Location, iv quoteInterval, res *yahooChartResult) ([]QuoteBar, error) {
	if len(res.Indicators.Quote) == 0 {
		if len(res.Timestamp) == 0 {
			// A snapshot-only body: no series was sent at all. The trap fixture is this shape, and it
			// is refused by the checks above rather than here.
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %d timestamps and no price arrays", errQuoteYahooSeries, len(res.Timestamp))
	}
	q := res.Indicators.Quote[0]
	n := len(res.Timestamp)
	for _, arr := range []struct {
		what string
		len  int
	}{
		{"open", len(q.Open)}, {"high", len(q.High)}, {"low", len(q.Low)},
		{"close", len(q.Close)}, {"volume", len(q.Volume)},
	} {
		if arr.len != n {
			return nil, fmt.Errorf("%w: %d timestamps but %d %s", errQuoteYahooSeries, n, arr.len, arr.what)
		}
	}

	r := &yahooReader{}
	out := make([]QuoteBar, 0, n)
	for i := 0; i < n; i++ {
		if q.Open[i] == nil || q.High[i] == nil || q.Low[i] == nil || q.Close[i] == nil || q.Volume[i] == nil {
			continue
		}
		bar := QuoteBar{
			Date:   quoteYahooBarDate(loc, iv, res.Timestamp[i]),
			Open:   r.fen("open", *q.Open[i]),
			High:   r.fen("high", *q.High[i]),
			Low:    r.fen("low", *q.Low[i]),
			Close:  r.fen("close", *q.Close[i]),
			Volume: r.shares("volume", *q.Volume[i]),
		}
		if r.err != nil {
			return nil, fmt.Errorf("quote: yahoo point %d: %w", i, r.err)
		}
		out = append(out, bar)
	}
	if len(out)*2 < n {
		return nil, fmt.Errorf("%w: %d of %d points are null", errQuoteYahooSeries, n-len(out), n)
	}
	// (8) bar sanity and the per-candle ceiling, quote.go's check 4 unchanged.
	if err := quoteCheckBars(out); err != nil {
		return nil, err
	}
	return out, nil
}

// quoteYahooBarDate stamps a bar in the MARKET's zone, never UTC, so that a bar reads as the
// exchange's own wall clock: 09:30 in Shanghai rather than the 01:30Z the wire carries.
//
// What that is worth, stated precisely rather than dramatically, because the fixtures can be checked:
// all five markets here open during UTC morning hours, so a DAILY bar's date happens to come out the
// same either way today — the difference is visible on an intraday bar, whose whole point is the
// time of day, and it would become a wrong DATE the moment a bar is stamped in an exchange's evening
// (a US post-market point at 20:00 EDT is 00:00 UTC the following day). Getting the zone from the
// market table rather than from the response is the same rule quoteVendorTime follows, for the same
// reason: the alternative is a date that depends on where the server is.
//
// A daily bar carries the date alone, as Tencent's day rows do; an intraday bar carries the whole
// instant, because 09:31 and 13:31 are different bars.
func quoteYahooBarDate(loc *time.Location, iv quoteInterval, sec int64) string {
	t := time.Unix(sec, 0).In(loc)
	// Asked as quoteInterval.intraday() rather than against one constant: 分时 and 5日 are two
	// intervals and BOTH carry minutes, so a comparison against the first alone would stamp a
	// five-day five-minute series with bare dates — 331 points sharing five distinct labels, drawn
	// as a line whose x-axis says nothing.
	if iv.intraday() {
		return t.Format(time.RFC3339)
	}
	return t.Format("2006-01-02")
}

// quoteYahooPrevClose is trap 1 written as code: the previous close is NEVER meta.chartPreviousClose,
// which is the close before the requested window and is off by 65 元 on a six-month Apple chart.
//
// For a DAILY window it is the second-to-last bar's close — the two captured daily bodies both
// confirm it against the vendor's own percentage, and the 000001.SZ one confirms it twice, because
// there chartPreviousClose (11.92) and the real previous close (11.89) are different numbers and only
// the second agrees with the vendor's -1.598%.
//
// For an INTRADAY window the bars are minutes, so the bar before the last one is a minute ago rather
// than a day ago; meta.previousClose is the day's own previous close and is present on all three
// captured intraday bodies. It is also the fallback for a daily window too short to have two bars.
func quoteYahooPrevClose(r *yahooReader, iv quoteInterval, meta yahooChartMeta, bars []QuoteBar) int64 {
	if iv == quoteIntervalDaily && len(bars) >= 2 {
		return bars[len(bars)-2].Close
	}
	return r.fen("previousClose", meta.PreviousClose)
}

// quoteYahooSessionOpen is the day's open, which this endpoint's meta does not carry in any market —
// there is no regularMarketOpen beside the day's high and low.
//
// So it is read off the series: the open of the first bar that shares the last bar's session date.
// For a daily window that is the last bar's own open (one bar per date); for an intraday window it is
// the first minute of the most recent day, which is what the five-day, five-minute window needs and
// what taking the first bar of the whole window would get wrong by four days. Zero when there are no
// bars at all, which is the same thing a suspended stock reports. Nothing in this parser's gate reads
// the open except the price ceiling — the identity works on last and prevClose, and the day range on
// last against high and low — so a zero here is reported, not refused, exactly as a suspended stock's
// zero open is on the Tencent path.
func quoteYahooSessionOpen(bars []QuoteBar) int64 {
	if len(bars) == 0 {
		return 0
	}
	last := bars[len(bars)-1]
	day := quoteYahooBarDay(last.Date)
	open := last.Open
	for i := len(bars) - 1; i >= 0; i-- {
		if quoteYahooBarDay(bars[i].Date) != day {
			break
		}
		open = bars[i].Open
	}
	return open
}

// quoteYahooBarDay is the date half of a bar's stamp — the whole string for a daily bar, and
// everything before the "T" for an intraday one, both of which quoteYahooBarDate produced in the
// market's own zone.
func quoteYahooBarDay(date string) string {
	if i := strings.IndexByte(date, 'T'); i >= 0 {
		return date[:i]
	}
	return date
}

// quoteYahooName is the vendor's own name for the instrument, preferring the short one because that
// is what fits beside a price and what every captured body carries.
func quoteYahooName(meta yahooChartMeta) string {
	if name := strings.TrimSpace(meta.ShortName); name != "" {
		return name
	}
	return strings.TrimSpace(meta.LongName)
}

// quoteYahooKind answers QuoteResp.Kind from the vendor's own instrumentType, and answers only what
// has been measured: "EQUITY" is a stock, and everything else is left EMPTY rather than defaulted.
//
// The empty answer is the Sina path's precedent and it is deliberate. Yahoo's spelling for an index
// has never been captured here, so mapping "anything that is not EQUITY" onto 指数 — or onto 股票 —
// would be this file asserting something it did not read. The trap fixture's "MUTUALFUND" is exactly
// the case a default would have labelled a stock.
func quoteYahooKind(meta yahooChartMeta) string {
	if strings.EqualFold(strings.TrimSpace(meta.InstrumentType), "EQUITY") {
		return quoteKindStock
	}
	return ""
}

// ---------- the declaration ----------

// quoteYahooSource is the registration defaultQuoteSources appends, kept here so that what this
// source claims and what its parser does sit in one file.
//
// optIn is what makes it ship disabled: it is compiled in, the admin panel lists it, the order
// setting accepts its name — and quoteShippedOrder leaves it out, so an unconfigured portal has it
// off. See the head of this file for why that is the operator's decision and not the build's.
func quoteYahooSource() quoteSource {
	return quoteSource{
		name:    quoteSourceYahoo,
		fetchAt: fetchYahooQuote,
		optIn:   true,
		// The code half of the declaration: this source's grants are market predicates, and its Hong
		// Kong spelling is a fact about the code. See quoteYahooKnows.
		knows: quoteYahooKnows,
		caps: []quoteCapability{
			// The US DAILY SERIES: the hole ADR 0030 §4 records, and the reason this source exists.
			// Nothing else in this build declares the pair, so an operator who enables Yahoo gets the
			// US chart from here wherever they put it in the order — by construction, not by position.
			//
			// It is REACHABLE, and nothing else has to be flipped to reach it: the four daily ranges
			// ask for quoteIntervalDaily in every market (quote_api.go), and servableInterval degrades
			// that to a snapshot exactly when no enabled source declares the pair. So a portal with
			// this source off answers a US 3个月 request with the price and an explicit empty chart,
			// as it always has, and the same request draws 66 bars from here the moment an operator
			// adds yahoo to quote_source_order. That was once a market-table column instead, which is
			// why this note used to say the opposite: reading the interval off quoteMarket.history
			// made this grant unreachable and the panel column that advertises it a lie.
			{
				markets:   func(m *quoteMarket) bool { return m.id == "us" },
				intervals: []quoteInterval{quoteIntervalDaily},
			},
			// INTRADAY, in every market this file has a measured symbol for — which is every market
			// except Beijing. The v3 contract says "all markets"; Beijing is excluded because
			// quoteYahooSymbol refuses it, and a grant whose fetcher then refuses is a red streak in
			// the health panel for a request that was never going to work.
			//
			// Unlike Tencent's absent intraday grant, this one is a promise the fetcher can keep:
			// fetchYahooQuote is wired through quoteSource.fetchAt and receives the interval, so an
			// intraday request produces a 1m or 5m window rather than a daily series with a different
			// label. The three intraday fixtures are what pin that.
			//
			// BOTH intraday windows, and that is the difference between this source and the other
			// one that serves minutes: Tencent's endpoint answers today and has no window
			// parameter, so it declares quoteIntervalIntraday alone and the 5日 range resolves to
			// this source or to nobody. An operator who leaves Yahoo off therefore gets 分时 on the
			// A-shares and Hong Kong and no 5日 anywhere — which the handler renders as a price with
			// an empty chart rather than as an error (servableInterval), because it is a standing
			// gap in the configuration and not a vendor having a bad afternoon.
			{
				markets:   func(m *quoteMarket) bool { return m.id != "bj" },
				intervals: []quoteInterval{quoteIntervalIntraday, quoteIntervalIntraday5D},
			},
			// No SNAPSHOT grant. Tencent serves every market's price correctly and is first in the
			// order, so a snapshot grant here would add a fallback nobody asked for — and this source
			// is off by default, which is a strange thing for a fallback to be.
		},
	}
}
