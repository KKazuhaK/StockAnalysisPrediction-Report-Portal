package app

// quote_yahoo_test.go —— every case here runs against the bodies in testdata/quote/, captured live
// from query1.finance.yahoo.com on 2026-09-07. Nothing here talks to the network and nothing asserts
// against a hand-written miniature of a vendor response: a mock that restates the parser agrees with
// the parser by construction, including when both are wrong. The gate tests take a real captured
// body and change exactly one thing about it.
//
// The three traps have a test each, and each one was checked by reverting the code it names:
//   - trap 1 (chartPreviousClose): TestYahooPrevCloseIsNeverChartPreviousClose.
//   - trap 2 (00700.HK is a 2019 mutual fund): TestYahooRefusesTheFundThatAnswersToTheWrongHKCode.
//   - trap 3 (prices are JSON floats): TestYahooPricesComeFromTheDecimalStringNotAFloat64.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const (
	fixYahooAAPLDaily     = "yahoo_chart_AAPL_6mo_1d.json"
	fixYahooAAPLMinute    = "yahoo_chart_AAPL_1d_1m.json"
	fixYahooMoutaiFiveMin = "yahoo_chart_600519ss_5d_5m.json"
	fixYahooTencentMinute = "yahoo_chart_0700hk_1d_1m.json"
	fixYahooPingAnDaily   = "yahoo_chart_000001sz_5d_1d.json"
	// The trap: `00700.HK?range=1d&interval=1m` answered HTTP 200 with this — a MUTUALFUND on "YHD",
	// a null currency, no prices and a June 2019 timestamp. Hong Kong on Yahoo is four digits.
	fixYahooHKFund = "yahoo_chart_00700hk_trap.json"
)

// ---------- helpers ----------

// yahooDoc decodes a captured body with UseNumber, so a mutation and a re-encode return every number
// this file does not touch to the wire byte for byte. Without it a re-marshal would round-trip
// 328.30999755859375 through float64 — which is the very conversion trap 3 is about.
func yahooDoc(t *testing.T, file string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(readQuoteFixture(t, file)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode fixture %s: %v", file, err)
	}
	return doc
}

// yahooResult reaches chart.result[0] of a decoded body.
func yahooResult(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	chart, ok := doc["chart"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no chart object")
	}
	results, ok := chart["result"].([]any)
	if !ok || len(results) == 0 {
		t.Fatal("fixture has no chart.result[0]")
	}
	res, ok := results[0].(map[string]any)
	if !ok {
		t.Fatal("chart.result[0] is not an object")
	}
	return res
}

func yahooMeta(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	meta, ok := yahooResult(t, doc)["meta"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no meta object")
	}
	return meta
}

// yahooSeries reaches indicators.quote[0], the five parallel arrays.
func yahooSeries(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	ind, ok := yahooResult(t, doc)["indicators"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no indicators object")
	}
	arrays, ok := ind["quote"].([]any)
	if !ok || len(arrays) == 0 {
		t.Fatal("fixture has no indicators.quote[0]")
	}
	q, ok := arrays[0].(map[string]any)
	if !ok {
		t.Fatal("indicators.quote[0] is not an object")
	}
	return q
}

func yahooFixtureFen(t *testing.T, meta map[string]any, field string) int64 {
	t.Helper()
	n, ok := meta[field].(json.Number)
	if !ok {
		t.Fatalf("meta.%s is %T, not a number", field, meta[field])
	}
	v, err := quoteFen(string(n))
	if err != nil {
		t.Fatalf("meta.%s = %q: %v", field, n, err)
	}
	return v
}

// yahooParse runs the real parser over a captured body. bars is 0 — no trimming — everywhere except
// the one test that is about trimming.
func yahooParse(t *testing.T, file, market, code string, iv quoteInterval) *QuoteResp {
	t.Helper()
	resp, err := parseYahooChart(market, code, readQuoteFixture(t, file), 0, iv)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return resp
}

// yahooWantGate demands one specific gate failure from a mutated body, and demands that no response
// escapes with it. Both halves matter: a parser that returned a partial response beside its error
// would hand a caller something to render.
func yahooWantGate(t *testing.T, market, code string, body []byte, iv quoteInterval, sentinel error) {
	t.Helper()
	resp, err := parseYahooChart(market, code, body, 0, iv)
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
	if resp != nil {
		t.Errorf("a refused response still came back: %+v", resp)
	}
}

// ---------- every captured body, field by field ----------

// The whole contract of this parser against the five real responses. The expected values are the
// vendor's own numbers converted by hand, not by the parser — the point of a fixture test is that it
// disagrees with the parser when the parser is wrong.
func TestYahooParsesEveryCapturedBody(t *testing.T) {
	for _, tc := range []struct {
		file, market, code string
		iv                 quoteInterval
		name, currency, tz string
		bars               int
		firstDate          string
		firstBar           QuoteBar
		lastDate           string
		lastBar            QuoteBar
		snap               QuoteSnapshot
	}{
		{
			// The response this source exists for: 128 daily bars for a US ticker, where Tencent
			// answers a sixty-bar request with two rows fifteen years apart.
			file: fixYahooAAPLDaily, market: "us", code: "AAPL", iv: quoteIntervalDaily,
			name: "Apple Inc.", currency: "USD", tz: "America/New_York", bars: 128,
			firstDate: "2026-03-05",
			firstBar:  QuoteBar{Date: "2026-03-05", Open: 26079, High: 26156, Low: 25725, Close: 26029, Volume: 49658600},
			lastDate:  "2026-09-04",
			lastBar:   QuoteBar{Date: "2026-09-04", Open: 32831, High: 32893, Low: 31786, Close: 31997, Volume: 39551800},
			snap: QuoteSnapshot{
				Last: 31997, PrevClose: 32821, Open: 32831, High: 32893, Low: 31786,
				Change: -824, ChangePct: "-2.511", Volume: 39606884, Amount: 0,
				AsOf: "2026-09-04T16:00:01-04:00", Session: quoteSessionUnknown,
			},
		},
		{
			// A Shenzhen daily series, and the second witness for trap 1: here the real previous
			// close (11.89) and chartPreviousClose (11.92) are DIFFERENT numbers.
			file: fixYahooPingAnDaily, market: "sz", code: "000001", iv: quoteIntervalDaily,
			name: "PAB", currency: "CNY", tz: "Asia/Shanghai", bars: 5,
			firstDate: "2026-09-01",
			firstBar:  QuoteBar{Date: "2026-09-01", Open: 1168, High: 1196, Low: 1165, Close: 1192, Volume: 152316453},
			lastDate:  "2026-09-07",
			lastBar:   QuoteBar{Date: "2026-09-07", Open: 1186, High: 1188, Low: 1165, Close: 1170, Volume: 108727552},
			snap: QuoteSnapshot{
				Last: 1170, PrevClose: 1189, Open: 1186, High: 1188, Low: 1165,
				Change: -19, ChangePct: "-1.598", Volume: 108727552, Amount: 0,
				AsOf: "2026-09-07T15:04:24+08:00", Session: quoteSessionUnknown,
			},
		},
		{
			// One minute at a time: 391 points, and a bar stamp that is an instant rather than a
			// date, because 09:31 and 13:31 are different bars.
			file: fixYahooAAPLMinute, market: "us", code: "AAPL", iv: quoteIntervalIntraday,
			name: "Apple Inc.", currency: "USD", tz: "America/New_York", bars: 391,
			firstDate: "2026-09-04T09:30:00-04:00",
			firstBar:  QuoteBar{Date: "2026-09-04T09:30:00-04:00", Open: 32830, High: 32890, Low: 32746, Close: 32802, Volume: 1105409},
			lastDate:  "2026-09-04T16:00:00-04:00",
			lastBar:   QuoteBar{Date: "2026-09-04T16:00:00-04:00", Open: 31997, High: 31997, Low: 31997, Close: 31997, Volume: 0},
			snap: QuoteSnapshot{
				Last: 31997, PrevClose: 32821, Open: 32830, High: 32893, Low: 31786,
				Change: -824, ChangePct: "-2.511", Volume: 39606884, Amount: 0,
				AsOf: "2026-09-04T16:00:01-04:00", Session: quoteSessionUnknown,
			},
		},
		{
			// Hong Kong, where 69 of the 400 points are null INCLUDING THE LAST ONE — so the last bar
			// here is the vendor's last real minute (16:08, the closing auction) and not a hole.
			file: fixYahooTencentMinute, market: "hk", code: "00700", iv: quoteIntervalIntraday,
			name: "TENCENT", currency: "HKD", tz: "Asia/Hong_Kong", bars: 331,
			firstDate: "2026-09-07T09:30:00+08:00",
			firstBar:  QuoteBar{Date: "2026-09-07T09:30:00+08:00", Open: 44060, High: 44260, Low: 43920, Close: 44040, Volume: 573553},
			lastDate:  "2026-09-07T16:08:00+08:00",
			lastBar:   QuoteBar{Date: "2026-09-07T16:08:00+08:00", Open: 43840, High: 43840, Low: 43840, Close: 43840, Volume: 748800},
			snap: QuoteSnapshot{
				Last: 43840, PrevClose: 44280, Open: 44060, High: 44260, Low: 43820,
				Change: -440, ChangePct: "-0.994", Volume: 10443687, Amount: 0,
				AsOf: "2026-09-07T16:08:14+08:00", Session: quoteSessionUnknown,
			},
		},
		{
			// Five days of five-minute bars, which is the case that makes the day's open a search
			// rather than "the first bar": the first bar of THIS window is four days before the
			// session the snapshot describes.
			file: fixYahooMoutaiFiveMin, market: "sh", code: "600519", iv: quoteIntervalIntraday,
			name: "KWEICHOW MOUTAI", currency: "CNY", tz: "Asia/Shanghai", bars: 242,
			firstDate: "2026-09-01T09:30:00+08:00",
			firstBar:  QuoteBar{Date: "2026-09-01T09:30:00+08:00", Open: 129222, High: 129275, Low: 128630, Close: 129088, Volume: 0},
			lastDate:  "2026-09-07T15:00:00+08:00",
			lastBar:   QuoteBar{Date: "2026-09-07T15:00:00+08:00", Open: 131601, High: 131601, Low: 131601, Close: 131601, Volume: 0},
			snap: QuoteSnapshot{
				Last: 131601, PrevClose: 133000, Open: 132500, High: 133360, Low: 131266,
				Change: -1399, ChangePct: "-1.052", Volume: 2524962, Amount: 0,
				AsOf: "2026-09-07T15:00:01+08:00", Session: quoteSessionUnknown,
			},
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			resp := yahooParse(t, tc.file, tc.market, tc.code, tc.iv)
			quoteEqStr(t, "symbol", resp.Symbol, tc.code)
			quoteEqStr(t, "name", resp.Name, tc.name)
			quoteEqStr(t, "market", resp.Market, tc.market)
			quoteEqStr(t, "currency", resp.Currency, tc.currency)
			quoteEqStr(t, "tz", resp.TZ, tc.tz)
			quoteEqStr(t, "source", resp.Source, quoteSourceYahoo)
			quoteEqStr(t, "barsSource", resp.BarsSource, quoteSourceYahoo)
			// Every captured body is an EQUITY, so every one of them is a stock. The vendor's word
			// for an index has never been captured here, which is why quoteYahooKind answers "" to
			// anything else rather than guessing.
			quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
			// Unadjusted, because this parser reads indicators.quote and never indicators.adjclose.
			if resp.Adjusted {
				t.Error("a yahoo response claimed to be adjusted; only indicators.quote is read")
			}
			if len(resp.Bars) != tc.bars {
				t.Fatalf("bars = %d, want %d", len(resp.Bars), tc.bars)
			}
			if got := resp.Bars[0]; got != tc.firstBar {
				t.Errorf("first bar = %+v, want %+v", got, tc.firstBar)
			}
			if got := resp.Bars[len(resp.Bars)-1]; got != tc.lastBar {
				t.Errorf("last bar = %+v, want %+v", got, tc.lastBar)
			}
			quoteEqStr(t, "first bar date", resp.Bars[0].Date, tc.firstDate)
			quoteEqStr(t, "last bar date", resp.Bars[len(resp.Bars)-1].Date, tc.lastDate)
			if resp.Snapshot != tc.snap {
				t.Errorf("snapshot = %+v\n          want %+v", resp.Snapshot, tc.snap)
			}
		})
	}
}

// A bar count trims from the FRONT, so the newest data survives — the same rule the Tencent path
// follows, and the reason a range the vendor overshoots (6mo answered 128 bars for a 125-bar
// request) does not shift the right-hand edge of the chart.
func TestYahooTrimsTheOldestBarsAndKeepsTheNewest(t *testing.T) {
	resp, err := parseYahooChart("us", "AAPL", readQuoteFixture(t, fixYahooAAPLDaily), 5, quoteIntervalDaily)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Bars) != 5 {
		t.Fatalf("a 5-bar request returned %d bars", len(resp.Bars))
	}
	quoteEqStr(t, "last bar after trimming", resp.Bars[4].Date, "2026-09-04")
	// And the snapshot's previous close still comes from the bar BEFORE the last one, which survives
	// the trim because the trim takes from the front.
	quoteEqInt(t, "prevClose after trimming", resp.Snapshot.PrevClose, 32821)
}

// ---------- trap 1: chartPreviousClose ----------

// meta.chartPreviousClose is the close before the REQUESTED RANGE, not yesterday's close: 262.52 on
// the six-month Apple body whose real previous close was 328.21. Reaching for it renders a stock up
// 21.9% on a day it fell 2.51%.
//
// Two halves, and both are needed. The first is that the parser does not use it — asserted against
// the number actually in the fixture, so the test fails if someone points prevClose at it. The
// second is that the drift gate would CATCH it if they did: the vendor's own percentage disagrees
// with that pair by a thousand times the tolerance, which is what makes this a runtime guarantee
// rather than only a fact about today's code.
func TestYahooPrevCloseIsNeverChartPreviousClose(t *testing.T) {
	for _, tc := range []struct {
		file, market, code string
		wantPrev, wantChar int64
	}{
		{fixYahooAAPLDaily, "us", "AAPL", 32821, 26252},
		// The Shenzhen body is the sharper of the two: 11.92 against 11.89 is a plausible previous
		// close rather than an obviously silly one, and only 11.89 agrees with the vendor's -1.598%.
		{fixYahooPingAnDaily, "sz", "000001", 1189, 1192},
	} {
		t.Run(tc.file, func(t *testing.T) {
			meta := yahooMeta(t, yahooDoc(t, tc.file))
			if got := yahooFixtureFen(t, meta, "chartPreviousClose"); got != tc.wantChar {
				t.Fatalf("the fixture's chartPreviousClose is %d 分, not the %d this test is about", got, tc.wantChar)
			}
			// previousClose is null on a daily body, so the field whose NAME reads like the right one
			// is not even available here — the series is the only source for it.
			if v, ok := meta["previousClose"]; ok && v != nil {
				t.Errorf("meta.previousClose = %v on a daily body; this test assumes it is null", v)
			}

			resp := yahooParse(t, tc.file, tc.market, tc.code, quoteIntervalDaily)
			quoteEqInt(t, "prevClose", resp.Snapshot.PrevClose, tc.wantPrev)
			if resp.Snapshot.PrevClose == tc.wantChar {
				t.Fatal("prevClose is chartPreviousClose: the chart is drawn against the close before the window")
			}
			// It is the second-to-last bar's close, and that is where it came from.
			quoteEqInt(t, "the bar before the last one", resp.Bars[len(resp.Bars)-2].Close, tc.wantPrev)

			// The gate, on the same body: the vendor's percentage agrees with the derived pair and
			// refuses the chartPreviousClose one.
			if err := quoteCheckIdentity(&resp.Snapshot); err != nil {
				t.Errorf("the derived previous close disagrees with the vendor's own percentage: %v", err)
			}
			drifted := resp.Snapshot
			drifted.PrevClose = tc.wantChar
			if err := quoteCheckIdentity(&drifted); !errors.Is(err, errQuoteIdentity) {
				t.Errorf("chartPreviousClose as prevClose gave %v, want the identity to refuse it", err)
			}
		})
	}
}

// ---------- trap 2: 00700.HK is a 2019 mutual fund ----------

// The single case that argues for gating a new source at all. `00700.HK` — the naive mapping from
// this portal's five-digit Hong Kong code — answers HTTP 200 with a well-formed body about something
// else entirely, and every field in it is plausible on its own.
//
// Three layers are tested here, in the order they stand: the symbol this portal actually sends, the
// echo check that catches the body if the wrong symbol is ever sent anyway, and the currency check
// that catches it even if the echo agrees.
func TestYahooRefusesTheFundThatAnswersToTheWrongHKCode(t *testing.T) {
	doc := yahooDoc(t, fixYahooHKFund)
	meta := yahooMeta(t, doc)
	// What the fixture IS, stated so that this test cannot quietly become a test of an empty file.
	if got, _ := meta["symbol"].(string); got != "00700.HK" {
		t.Fatalf("the trap fixture echoes %q, want 00700.HK", got)
	}
	if got, _ := meta["instrumentType"].(string); got != "MUTUALFUND" {
		t.Fatalf("the trap fixture is a %q; this test is about the mutual fund", got)
	}
	if meta["currency"] != nil {
		t.Fatalf("the trap fixture's currency is %v, want null", meta["currency"])
	}

	// Layer 1: the symbol this portal sends is four digits, so it never asks for this body at all.
	target, err := quoteTargetFor("hk", "00700")
	if err != nil {
		t.Fatalf("resolve hk 00700: %v", err)
	}
	sym, err := quoteYahooSymbol(target)
	if err != nil {
		t.Fatalf("yahoo symbol for hk 00700: %v", err)
	}
	quoteEqStr(t, "the hk symbol", sym, "0700.HK")

	// Layer 2: served this body anyway — a mapping regression, a cache, a proxy — the echo check
	// refuses it, because the body says which instrument it is about and it is not the one asked for.
	yahooWantGate(t, "hk", "00700", readQuoteFixture(t, fixYahooHKFund), quoteIntervalIntraday, errQuoteCodeEcho)

	// Layer 3: with the echo silenced — one field changed on the real body, nothing else — the
	// currency check is what stands. A response that will not name the currency of the money it is
	// quoting is not a quote for this market.
	meta["symbol"] = "0700.HK"
	yahooWantGate(t, "hk", "00700", quoteRemarshal(t, doc), quoteIntervalIntraday, errQuoteYahooCurrency)
}

// The Hong Kong mapping is the only one that is not a suffix, and it is measured in both directions:
// exactly one leading zero comes off, and a five-digit code that has none is refused rather than
// guessed at. Beijing has no mapping at all — `.BJ` is plausible and unmeasured, and trap 2 is what
// a plausible unmeasured symbol costs.
func TestYahooSymbolMappingIsMeasuredOrRefused(t *testing.T) {
	for _, tc := range []struct {
		market, code, want string
		refuse             bool
	}{
		{market: "sh", code: "600519", want: "600519.SS"},
		{market: "sz", code: "000001", want: "000001.SZ"},
		{market: "hk", code: "00700", want: "0700.HK"},
		{market: "us", code: "AAPL", want: "AAPL"},
		// The Beijing exchange: a row in the market table, no measured Yahoo symbol.
		{market: "bj", code: "830799", refuse: true},
		// A real five-digit Hong Kong code that does not begin with a zero. Dropping a leading zero
		// it does not have would produce a four-digit code belonging to a different instrument.
		{market: "hk", code: "80737", refuse: true},
	} {
		t.Run(tc.market+tc.code, func(t *testing.T) {
			target, err := quoteTargetFor(tc.market, tc.code)
			if err != nil {
				t.Fatalf("resolve %s %s: %v", tc.market, tc.code, err)
			}
			got, err := quoteYahooSymbol(target)
			if tc.refuse {
				if !errors.Is(err, errQuoteYahooSymbolShape) {
					t.Fatalf("%s %s mapped to %q (%v), want a refusal", tc.market, tc.code, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %s: %v", tc.market, tc.code, err)
			}
			quoteEqStr(t, "symbol", got, tc.want)
		})
	}
}

// ---------- trap 3: the prices are JSON floats ----------

// `328.30999755859375` is how this wire spells 328.31. The decimal STRING is parsed by quoteFen —
// integer arithmetic, rounding half away from zero — and never by ParseFloat: the obvious float64
// route truncates the same literal to 32830, one 分 low, on a real value out of a real fixture.
func TestYahooPricesComeFromTheDecimalStringNotAFloat64(t *testing.T) {
	// The literals below are copied out of the captured bodies; the first assertion is that they
	// still are, so this test cannot drift away from the fixture it claims to be about.
	q := yahooSeries(t, yahooDoc(t, fixYahooAAPLDaily))
	opens, ok := q["open"].([]any)
	if !ok || len(opens) == 0 {
		t.Fatal("the fixture has no open array")
	}
	lastOpen, ok := opens[len(opens)-1].(json.Number)
	if !ok {
		t.Fatalf("the last open is %T, not a json.Number", opens[len(opens)-1])
	}
	quoteEqStr(t, "the literal on the wire", string(lastOpen), "328.30999755859375")

	fen, err := quoteFen(string(lastOpen))
	if err != nil {
		t.Fatalf("quoteFen(%q): %v", lastOpen, err)
	}
	quoteEqInt(t, "328.30999755859375 in 分", fen, 32831)
	// The route this repository forbids, on the same literal: float64 multiplication then a
	// truncating conversion, which is a 分 short and would print 328.30.
	asFloat := 328.30999755859375 // a float64 variable, so the multiply below is not folded at compile time
	if viaFloat := int64(asFloat * 100); viaFloat != 32830 {
		t.Fatalf("the float64 route gave %d; this test's premise was that it gives 32830", viaFloat)
	}
	// And the parser agrees with the decimal string rather than with the float.
	resp := yahooParse(t, fixYahooAAPLDaily, "us", "AAPL", quoteIntervalDaily)
	quoteEqInt(t, "the last bar's open", resp.Bars[len(resp.Bars)-1].Open, 32831)
}

// ---------- the series: lengths and nulls ----------

// Yahoo pads its arrays with nulls — 69 of 400 on the captured Hong Kong minute body, including the
// LAST point. A null is a hole and is skipped whole. It is never zero-filled, because a zero-filled
// bar is a candle drawn at the bottom of the chart on a minute nothing traded, and it passes every
// other check in the file: its four prices are ordered exactly as a candle's should be.
func TestYahooSkipsNullPointsInsteadOfZeroFillingThem(t *testing.T) {
	q := yahooSeries(t, yahooDoc(t, fixYahooTencentMinute))
	closes := q["close"].([]any)
	nulls := 0
	for _, v := range closes {
		if v == nil {
			nulls++
		}
	}
	if len(closes) != 400 || nulls != 69 {
		t.Fatalf("the fixture has %d points and %d nulls; this test is about 400 and 69", len(closes), nulls)
	}
	if closes[len(closes)-1] != nil {
		t.Fatal("the fixture's LAST point is no longer null, which is the half of this test that matters")
	}

	resp := yahooParse(t, fixYahooTencentMinute, "hk", "00700", quoteIntervalIntraday)
	if len(resp.Bars) != len(closes)-nulls {
		t.Fatalf("bars = %d, want %d — the 69 holes dropped and nothing else", len(resp.Bars), len(closes)-nulls)
	}
	for _, b := range resp.Bars {
		if b.Open == 0 || b.High == 0 || b.Low == 0 || b.Close == 0 {
			t.Fatalf("a zero-filled candle reached the chart: %+v", b)
		}
	}
	// The last bar is the vendor's last REAL minute — 16:08, the closing auction — and not the null
	// that follows it. This is also what the snapshot check compares against: had the trailing null
	// become a zero bar, regularMarketPrice would have disagreed with it by 438.40.
	quoteEqStr(t, "the last bar", resp.Bars[len(resp.Bars)-1].Date, "2026-09-07T16:08:00+08:00")
	quoteEqInt(t, "the last bar's close", resp.Bars[len(resp.Bars)-1].Close, 43840)
}

// timestamp[] and the five price arrays are read by the SAME index, so a length disagreement is not
// a cosmetic one: read short it is a series that stops early, read long it is an index panic in a
// handler goroutine. Nothing else in the parser would notice.
func TestYahooRefusesArraysThatAreNotTheSameLength(t *testing.T) {
	for _, field := range []string{"open", "high", "low", "close", "volume"} {
		t.Run(field, func(t *testing.T) {
			doc := yahooDoc(t, fixYahooPingAnDaily)
			q := yahooSeries(t, doc)
			arr := q[field].([]any)
			q[field] = arr[:len(arr)-1] // one element short of timestamp[]
			yahooWantGate(t, "sz", "000001", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteYahooSeries)
		})
	}
}

// A handful of nulls is a quiet market; a majority of them is a response that is not describing the
// window it was asked for, and drawing the remainder spreads an hour of trading evenly across a day.
// The 242-of-331 real body must survive, and the same body with the holes widened must not.
func TestYahooRefusesASeriesThatIsMostlyHoles(t *testing.T) {
	// The captured body is 27% null and is served.
	resp := yahooParse(t, fixYahooMoutaiFiveMin, "sh", "600519", quoteIntervalIntraday)
	if len(resp.Bars) != 242 {
		t.Fatalf("the real body gave %d bars, want 242 — a fixture this test no longer describes", len(resp.Bars))
	}

	doc := yahooDoc(t, fixYahooMoutaiFiveMin)
	q := yahooSeries(t, doc)
	closes := q["close"].([]any)
	// Null out the front half of the closes: 89 holes were already there, so this puts the body well
	// past half and leaves the rest of the arrays exactly as the vendor sent them.
	for i := 0; i < len(closes)/2+1; i++ {
		closes[i] = nil
	}
	yahooWantGate(t, "sh", "600519", quoteRemarshal(t, doc), quoteIntervalIntraday, errQuoteYahooSeries)
}

// ---------- the rest of the gate ----------

// One call returns the snapshot and the series together, so they cannot be two moments in time: if
// meta.regularMarketPrice is not the last bar's close, something is being read out of the wrong
// place. One 分 of tolerance, for the float64 the vendor rendered both from.
func TestYahooRefusesASnapshotThatDisagreesWithItsOwnLastBar(t *testing.T) {
	doc := yahooDoc(t, fixYahooAAPLDaily)
	meta := yahooMeta(t, doc)
	// Two 分 away, and the percentage moved with it so that the identity check cannot be what fires:
	// this test is about the snapshot-versus-series check and nothing else.
	meta["regularMarketPrice"] = json.Number("319.99")
	meta["regularMarketChangePercent"] = json.Number("-2.505")
	yahooWantGate(t, "us", "AAPL", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteYahooDrift)

	// One 分 away is inside the tolerance and is served, or "within one 分" would be untested in the
	// direction that matters.
	meta["regularMarketPrice"] = json.Number("319.98")
	if _, err := parseYahooChart("us", "AAPL", quoteRemarshal(t, doc), 0, quoteIntervalDaily); err != nil {
		t.Errorf("a one-分 difference was refused: %v", err)
	}
}

// The currency is the check the fund body dies on, and it has two halves: absent, and present but
// wrong for this market. A USD figure beside a CNY one, both bare numbers, reads as the same kind of
// quantity — which is why ADR 0030 put currency on the wire in the first place.
func TestYahooRefusesACurrencyThisMarketDoesNotQuote(t *testing.T) {
	for _, tc := range []struct {
		what string
		set  any
	}{
		{"absent", nil},
		{"blank", ""},
		{"another market's", "CNY"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			doc := yahooDoc(t, fixYahooAAPLDaily)
			yahooMeta(t, doc)["currency"] = tc.set
			yahooWantGate(t, "us", "AAPL", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteYahooCurrency)
		})
	}
	// Case is the vendor's business, not a mismatch.
	doc := yahooDoc(t, fixYahooAAPLDaily)
	yahooMeta(t, doc)["currency"] = "usd"
	if _, err := parseYahooChart("us", "AAPL", quoteRemarshal(t, doc), 0, quoteIntervalDaily); err != nil {
		t.Errorf("a lower-case currency was refused: %v", err)
	}
}

// The per-candle checks are quote.go's, unchanged, and they run over this vendor's bars too: a low
// above the open is not a candle, and a price past the ceiling is not a price. Both are asserted
// through the real parser rather than by calling quoteCheckBars directly, because the thing worth
// testing is that this path reaches it.
func TestYahooBarsGoThroughTheSharedCandleChecks(t *testing.T) {
	t.Run("ordering", func(t *testing.T) {
		doc := yahooDoc(t, fixYahooPingAnDaily)
		q := yahooSeries(t, doc)
		q["low"].([]any)[0] = json.Number("99.99") // a low far above that day's open and close
		yahooWantGate(t, "sz", "000001", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteBarSanity)
	})
	t.Run("ceiling", func(t *testing.T) {
		doc := yahooDoc(t, fixYahooPingAnDaily)
		q := yahooSeries(t, doc)
		// Past quotePriceCeiling (10^12 分) while staying a well-ordered candle: high and low move
		// with the close, so this is the ceiling firing and not the ordering check.
		for _, field := range []string{"open", "high", "low", "close"} {
			q[field].([]any)[0] = json.Number("99999999999.99")
		}
		yahooWantGate(t, "sz", "000001", quoteRemarshal(t, doc), quoteIntervalDaily, errQuotePriceBound)
	})
}

// The vendor's own percentage is what the strip shows (ADR 0028), and the identity is what keeps it
// honest against the two prices beside it. The v3 contract expected no percentage here at all; all
// five captured bodies carry one, and every one of them agrees with its own derived previous close —
// so a body whose percentage does NOT agree is a source drifting, and is refused.
func TestYahooIdentityRunsOnTheVendorsOwnPercentage(t *testing.T) {
	for _, tc := range []struct {
		file, market, code string
		iv                 quoteInterval
		pct                string
	}{
		{fixYahooAAPLDaily, "us", "AAPL", quoteIntervalDaily, "-2.511"},
		{fixYahooPingAnDaily, "sz", "000001", quoteIntervalDaily, "-1.598"},
		{fixYahooTencentMinute, "hk", "00700", quoteIntervalIntraday, "-0.994"},
		{fixYahooMoutaiFiveMin, "sh", "600519", quoteIntervalIntraday, "-1.052"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			resp := yahooParse(t, tc.file, tc.market, tc.code, tc.iv)
			// Verbatim, three decimals and all: it is the vendor's string, not a rendering of ours.
			quoteEqStr(t, "changePct", resp.Snapshot.ChangePct, tc.pct)
			// And the change beside it is the two prices' own difference, so the strip cannot show a
			// number that contradicts the pair it sits between.
			quoteEqInt(t, "change", resp.Snapshot.Change, resp.Snapshot.Last-resp.Snapshot.PrevClose)
		})
	}

	// A percentage that does not agree with the prices is a refusal, not a rounding difference: one
	// field changed on a real body, by a whole percentage point.
	doc := yahooDoc(t, fixYahooAAPLDaily)
	yahooMeta(t, doc)["regularMarketChangePercent"] = json.Number("-1.511")
	yahooWantGate(t, "us", "AAPL", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteIdentity)
}

// ---------- the window ----------

// The interval decides the URL, which is the whole reason this fetcher takes one: the same symbol at
// the same bar count is a six-month daily chart or one day of minutes depending on it. The four
// combinations that have captured bodies are named here; the rest of the daily table is the same
// series over a longer window.
func TestYahooWindowIsChosenByIntervalAndCount(t *testing.T) {
	for _, tc := range []struct {
		iv        quoteInterval
		bars      int
		rng, gran string
		refuse    bool
	}{
		{iv: quoteIntervalDaily, bars: 21, rng: "1mo", gran: "1d"},
		// The UI's four ranges. 1m and 3m land on the NEXT window up rather than the one whose name
		// matches, and that is the arrangement working: a 1mo window holds about 21 sessions and
		// quoteRange1M asks for 22, so the smaller window would draw a chart one bar short of the one
		// that was asked for. The surplus is trimmed off the front.
		{iv: quoteIntervalDaily, bars: quoteRange1M, rng: "3mo", gran: "1d"},
		{iv: quoteIntervalDaily, bars: quoteRange3M, rng: "6mo", gran: "1d"},
		{iv: quoteIntervalDaily, bars: quoteRange6M, rng: "6mo", gran: "1d"}, // the captured AAPL body: 128 bars for 125
		{iv: quoteIntervalDaily, bars: quoteRange1Y, rng: "1y", gran: "1d"},
		{iv: quoteIntervalDaily, bars: quoteMaxBars, rng: "5y", gran: "1d"},
		{iv: quoteIntervalIntraday, bars: 391, rng: "1d", gran: "1m"}, // AAPL, one day of minutes
		{iv: quoteIntervalIntraday, bars: 800, rng: "5d", gran: "5m"}, // 600519, five days of five
		{iv: quoteIntervalSnapshot, bars: 60, refuse: true},           // never routed here
		{iv: quoteInterval("weekly"), bars: 60, refuse: true},         // nor is anything invented
	} {
		t.Run(string(tc.iv)+"/"+strconv.Itoa(tc.bars), func(t *testing.T) {
			rng, gran, err := quoteYahooWindow(tc.iv, tc.bars)
			if tc.refuse {
				if !errors.Is(err, errQuoteYahooInterval) {
					t.Fatalf("%s gave %q/%q (%v), want a refusal", tc.iv, rng, gran, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.iv, err)
			}
			if rng != tc.rng || gran != tc.gran {
				t.Errorf("%s at %d bars = %s/%s, want %s/%s", tc.iv, tc.bars, rng, gran, tc.rng, tc.gran)
			}
		})
	}
}

// The interval reaches the FETCHER, which is what separates this source's intraday grant from a
// declaration nobody can keep: quoteSource.call hands it over, and a source that declared intraday
// with an interval-blind fetcher would answer a minute request with a daily series and no error.
func TestYahooSourceIsWiredThroughTheIntervalAwareFetcher(t *testing.T) {
	src := quoteYahooSource()
	if src.fetchAt == nil {
		t.Fatal("the yahoo source has no interval-aware fetcher; its intraday grant is a promise nothing keeps")
	}
	if src.fetch != nil {
		t.Error("the yahoo source also has an interval-blind fetcher; exactly one of the two is set")
	}

	// call is the seam, so what it hands over is asserted rather than assumed.
	var seen quoteInterval
	probe := quoteSource{name: "probe", fetchAt: func(_ context.Context, _, _ string, _ int, iv quoteInterval) (*QuoteResp, error) {
		seen = iv
		return &QuoteResp{}, nil
	}}
	if _, err := probe.call(context.Background(), "us", "AAPL", 60, quoteIntervalIntraday); err != nil {
		t.Fatalf("call: %v", err)
	}
	if seen != quoteIntervalIntraday {
		t.Errorf("the fetcher was told %q, want %q", seen, quoteIntervalIntraday)
	}
	// A source with neither fetcher is an error rather than a nil dereference in a handler goroutine.
	if _, err := (quoteSource{name: "empty"}).call(context.Background(), "us", "AAPL", 60, quoteIntervalDaily); err == nil {
		t.Error("a source with no fetcher was called without complaint")
	}
}

// ---------- it ships disabled ----------

// The whole of stage 1's deployment story, in one place: compiled in, listed, accepted by the order
// setting, and OFF until an operator says otherwise. Each half is asserted, because each half is a
// separate way for this to be wrong — a source that shipped enabled would send every US chart to an
// unlicensed endpoint nobody chose, and one the order refused to name could never be switched on.
func TestYahooShipsCompiledInAndDisabled(t *testing.T) {
	if got := strings.Join(quoteShippedOrder(), ","); got != "tencent,sina" {
		t.Errorf("the shipped order is %q, want tencent,sina — yahoo must not be in it", got)
	}
	if got := strings.Join(quoteConfigDefault().Order, ","); got != "tencent,sina" {
		t.Errorf("an unconfigured portal runs %q, want tencent,sina", got)
	}
	// Compiled in: the panel lists it and the order setting will accept the name.
	var found bool
	for _, name := range quoteSourceNames() {
		found = found || name == quoteSourceYahoo
	}
	if !found {
		t.Fatalf("yahoo is not among the compiled-in sources %v", quoteSourceNames())
	}
	order, unknown := quoteParseOrder("tencent,sina,yahoo")
	if unknown != "" {
		t.Errorf("saving an order naming yahoo was refused: %q is unknown", unknown)
	}
	if got := strings.Join(order, ","); got != "tencent,sina,yahoo" {
		t.Errorf("the parsed order is %q, want tencent,sina,yahoo", got)
	}

	// Off means the resolver never returns it, whatever is asked for.
	c := &quoteCache{sources: defaultQuoteSources()}
	def := quoteConfigDefault()
	for id := range quoteMarkets {
		for _, iv := range quoteAllIntervals {
			for _, src := range c.sourcesFor(def, quoteAsk(t, id), iv) {
				if src.name == quoteSourceYahoo {
					t.Errorf("an unconfigured portal resolves %s/%s to yahoo", id, iv)
				}
			}
		}
	}

	// And on means it is used — for the two pairs it declared, and for nothing else. Without this
	// half, a source that was disabled by being broken would pass everything above.
	on := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, quoteSourceYahoo}}.orDefaults()
	for _, tc := range []struct {
		market string
		iv     quoteInterval
		want   string
	}{
		// The US daily series: nobody else declares it, so the operator's ordering does not matter.
		{"us", quoteIntervalDaily, "yahoo"},
		// 分时 in the three markets Tencent's minute endpoint also serves: Yahoo FOLLOWS it, because
		// the order is a preference among the capable and the operator put it last. It is the only
		// source for the US, where Tencent's minute body is a single point with no trading date.
		{"sh", quoteIntervalIntraday, "tencent,yahoo"},
		{"hk", quoteIntervalIntraday, "tencent,yahoo"},
		{"us", quoteIntervalIntraday, "yahoo"},
		// The five-day, five-minute window is Yahoo's alone in every market: Tencent's endpoint
		// answers today and has no window parameter to ask it for more.
		{"sh", quoteIntervalIntraday5D, "yahoo"},
		{"us", quoteIntervalIntraday5D, "yahoo"},
		// Beijing has no measured Yahoo symbol, so it is not in either intraday grant.
		{"bj", quoteIntervalIntraday, ""},
		{"bj", quoteIntervalIntraday5D, ""},
		// And it takes over nothing it did not claim: the chains the portal walks today are the same.
		{"sh", quoteIntervalDaily, "tencent,sina"},
		{"us", quoteIntervalSnapshot, "tencent"},
		{"hk", quoteIntervalSnapshot, "tencent"},
	} {
		got := quoteSourceNamesOf(c.sourcesFor(on, quoteAsk(t, tc.market), tc.iv))
		if got != tc.want {
			t.Errorf("with yahoo enabled, %s/%s resolves to [%s], want [%s]", tc.market, tc.iv, got, tc.want)
		}
	}
}

// ---------- the day range, on this parser too ----------

// Check 5 runs here, and its absence was invisible: check 6, the identity, returns early whenever
// prevClose is 0 — a never-traded code, or an intraday body whose meta carries no previousClose — and
// then NOTHING else in this parser looks at the last price against the day's own high and low.
//
// The mutation is one field of a real captured body: the day's low lifted three 分 above the last
// price the same body reports. Every earlier check still passes — the bound, the identity (which
// reads last and prevClose only), the snapshot-against-series comparison — so this is the check that
// has to catch it, and with quoteCheckRange removed from parseYahooChart the response is served.
func TestYahooRefusesALastPriceOutsideTheDayRange(t *testing.T) {
	doc := yahooDoc(t, fixYahooAAPLDaily)
	meta := yahooMeta(t, doc)
	if got := yahooFixtureFen(t, meta, "regularMarketDayLow"); got != 31786 {
		t.Fatalf("the fixture's day low is %d 分; this test is about 31786", got)
	}
	if got := yahooFixtureFen(t, meta, "regularMarketPrice"); got != 31997 {
		t.Fatalf("the fixture's last is %d 分; this test is about 31997", got)
	}
	// A low ABOVE the last price. Nothing else in the body is touched, so the vendor's own percentage
	// still agrees with last and prevClose and the last bar still matches the snapshot.
	meta["regularMarketDayLow"] = json.Number("320.00")
	yahooWantGate(t, "us", "AAPL", quoteRemarshal(t, doc), quoteIntervalDaily, errQuoteRange)

	// And the skip that keeps a suspended stock from being read as a vendor failure is intact: with
	// no day bounds at all there is no range to check against, and the body is served.
	doc = yahooDoc(t, fixYahooAAPLDaily)
	meta = yahooMeta(t, doc)
	meta["regularMarketDayLow"] = json.Number("0")
	meta["regularMarketDayHigh"] = json.Number("0")
	resp, err := parseYahooChart("us", "AAPL", quoteRemarshal(t, doc), 0, quoteIntervalDaily)
	if err != nil {
		t.Fatalf("a body with no day bounds was refused: %v", err)
	}
	quoteEqInt(t, "last", resp.Snapshot.Last, 31997)
}

// ---------- 5日: the interval reaches the parser ----------

// quoteIntervalIntraday5D is a real interval that a real request arrives with, and until this test it
// never reached either parser: the five-minute fixture was parsed as quoteIntervalIntraday, so
// quoteYahooBarDate's iv.intraday() could be narrowed to a comparison against the FIRST intraday
// constant with the whole suite still green.
//
// What that narrowing costs is written out at quoteYahooBarDate and asserted here: a five-day,
// five-minute series stamped with bare dates is 242 points sharing five labels, drawn as a line whose
// x-axis says nothing. So the assertion is not "the parse succeeded" — it is that every label carries
// a TIME, and that the labels are as many as the bars.
func TestYahooStampsTheFiveDayWindowWithTimesAndNotBareDates(t *testing.T) {
	resp := yahooParse(t, fixYahooMoutaiFiveMin, "sh", "600519", quoteIntervalIntraday5D)
	if len(resp.Bars) != 242 {
		t.Fatalf("the 5d/5m body gave %d bars, want 242 — a fixture this test no longer describes", len(resp.Bars))
	}
	days := map[string]bool{}
	labels := map[string]bool{}
	for _, bar := range resp.Bars {
		if !strings.Contains(bar.Date, "T") {
			t.Fatalf("bar %q carries no time; a five-minute series stamped with bare dates is five "+
				"labels for %d points", bar.Date, len(resp.Bars))
		}
		days[quoteYahooBarDay(bar.Date)] = true
		labels[bar.Date] = true
	}
	// Five sessions — which is what makes the bare-date stamp so nearly plausible — and a distinct
	// label per bar, which is what it would destroy.
	if len(days) != 5 {
		t.Errorf("the window covers %d sessions, want 5", len(days))
	}
	if len(labels) != len(resp.Bars) {
		t.Errorf("%d bars share %d labels", len(resp.Bars), len(labels))
	}
	quoteEqStr(t, "first bar", resp.Bars[0].Date, "2026-09-01T09:30:00+08:00")
	quoteEqStr(t, "last bar", resp.Bars[len(resp.Bars)-1].Date, "2026-09-07T15:00:00+08:00")
	// The rest of the response is the same as at the one-session interval: the window is the
	// vendor's, not something this parser derives from the label it was handed.
	same := yahooParse(t, fixYahooMoutaiFiveMin, "sh", "600519", quoteIntervalIntraday)
	if same.Snapshot != resp.Snapshot {
		t.Errorf("the snapshot differs between the two intraday intervals:\n 5d %+v\n 1d %+v", resp.Snapshot, same.Snapshot)
	}
}

// ---------- the kind: measured or empty ----------

// "Everything that is not EQUITY is left empty" was untested, because every body that reaches the end
// of the parser is an EQUITY and the one that is not — the fund — is refused two checks earlier. So
// the function is asked directly, with the fund body's OWN meta: a default of quoteKindStock passes
// every other test in this file and labels this MUTUALFUND a 股票.
func TestYahooKindIsMeasuredAndNeverGuessed(t *testing.T) {
	var trap yahooChartEnvelope
	if err := json.Unmarshal(readQuoteFixture(t, fixYahooHKFund), &trap); err != nil {
		t.Fatalf("decode the fund fixture: %v", err)
	}
	if len(trap.Chart.Result) == 0 {
		t.Fatal("the fund fixture has no result")
	}
	meta := trap.Chart.Result[0].Meta
	if meta.InstrumentType != "MUTUALFUND" {
		t.Fatalf("the fund fixture says instrumentType %q; this test is about MUTUALFUND", meta.InstrumentType)
	}
	if got := quoteYahooKind(meta); got != "" {
		t.Errorf("a MUTUALFUND was labelled %q; the vendor's word for an index has never been "+
			"captured here, so anything but EQUITY is answered with nothing rather than guessed at", got)
	}
	// The measured case, so the line above cannot pass on a function that answers "" to everything.
	var apple yahooChartEnvelope
	if err := json.Unmarshal(readQuoteFixture(t, fixYahooAAPLDaily), &apple); err != nil {
		t.Fatalf("decode the AAPL fixture: %v", err)
	}
	if got := quoteYahooKind(apple.Chart.Result[0].Meta); got != quoteKindStock {
		t.Errorf("an EQUITY was labelled %q, want %q", got, quoteKindStock)
	}
}

// ---------- what enabling this source actually does ----------

// yahooStub answers through the REAL parser over a captured body, wired as the interval-aware
// fetcher this source really has, so a test cannot assert that an intraday request was served out of
// a daily fixture.
func yahooStub(t *testing.T, file string) *quoteVendorStub {
	t.Helper()
	body := readQuoteFixture(t, file)
	return &quoteVendorStub{parseAt: func(market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
		return parseYahooChart(market, code, body, bars, iv)
	}}
}

// wireYahooSource swaps the yahoo source's fetcher for a stub and leaves its DECLARATION — grants and
// quoteSource.knows — exactly as it ships. Call it after wireQuoteSources, which installs the source
// table this then edits.
func wireYahooSource(s *Server, stub *quoteVendorStub) {
	for i := range s.quotes.sources {
		if s.quotes.sources[i].name == quoteSourceYahoo {
			s.quotes.sources[i].fetchAt = stub.fetchAt
		}
	}
}

// The headline of stage 1, end to end: 128 US daily bars where Tencent answers a sixty-bar request
// with two rows fifteen years apart — and the ONLY thing an operator does to get them is add yahoo to
// quote_source_order.
//
// This is what the panel's 日线 column promises and what, until this change, no request could reach:
// the four daily ranges read their interval off quoteMarket.history, which says the US has no series,
// so a US 3个月 request asked for a snapshot no matter what was enabled and (us, daily) was a pair
// nothing ever resolved. Both halves are asserted here, because each is a separate way to be wrong —
// with the source off the reader must get exactly today's answer, and with it on a real chart.
func TestYahooUSChartAppearsTheMomentAnOperatorEnablesTheSource(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentUS))
	wireQuoteSources(s, tencent, sinaStub(t))
	yahoo := yahooStub(t, fixYahooAAPLDaily)
	wireYahooSource(s, yahoo)

	// OFF — the shipped order. The price, and a chart that says out loud there is no series.
	rec := quoteGET(t, s, "/api/quote/AAPL?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("us 3个月 with yahoo off → %d (%s)", rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqInt(t, "last", got.Snapshot.Last, 31997)
	if len(got.Bars) != 0 {
		t.Errorf("a portal with no US daily source drew %d bars", len(got.Bars))
	}
	quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, quoteBarsMarketUnsupported)
	if yahoo.n() != 0 {
		t.Errorf("a disabled source was called %d time(s)", yahoo.n())
	}
	if tencent.n() != 1 {
		t.Errorf("the price cost %d upstream calls, want 1", tencent.n())
	}

	// ON — one setting, nothing else.
	s.st.SetSetting(setQuoteSourceOrder, "tencent,sina,yahoo")
	s.quotes.clear()
	before := tencent.n()
	rec = quoteGET(t, s, "/api/quote/AAPL?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("us 3个月 with yahoo on → %d (%s)", rec.Code, rec.Body.String())
	}
	got = quoteBody(t, rec)
	if len(got.Bars) != quoteRange3M {
		t.Fatalf("the US chart came back with %d bars, want the %d asked for", len(got.Bars), quoteRange3M)
	}
	quoteEqStr(t, "barsSource", got.BarsSource, quoteSourceYahoo)
	quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, "")
	quoteEqStr(t, "source", got.Source, quoteSourceYahoo)
	quoteEqStr(t, "last bar", got.Bars[len(got.Bars)-1].Date, "2026-09-04")
	if yahoo.n() != 1 {
		t.Errorf("the chart cost %d calls to yahoo, want 1", yahoo.n())
	}
	if tencent.n() != before {
		t.Errorf("tencent was asked for a US daily series it answers with two rows fifteen years apart")
	}

	// And the markets nobody serves a series for are untouched by enabling it: Beijing still degrades
	// to the price and an empty chart, which is the half of the old behaviour that has to survive.
	c := quoteShippedCache()
	on := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, quoteSourceYahoo}}.orDefaults()
	if iv := c.servableInterval(on, quoteAsk(t, "bj"), quoteRangeFor("3m").interval); iv != quoteIntervalSnapshot {
		t.Errorf("bj 3个月 with yahoo on asks for %q; no source serves a Beijing daily series", iv)
	}
}

// The grant is a predicate over the MARKET and quoteYahooSymbol refuses per CODE, which is a gap only
// quoteSource.knows can close: canonicalCode accepts any five digits for Hong Kong, and 80737 has no
// measured Yahoo spelling.
//
// Without it, GET /api/quote/80737?range=5d resolved to the one source that declares 5日, that source
// refused the symbol BEFORE any HTTP request, and load recorded a sentence about our own symbol table
// against the vendor's health before answering 503 — a retry-inviting error for a gap no retry
// closes. Both halves are asserted: the counters stay clean, and the reader gets Tencent's price with
// an empty chart instead.
func TestYahooIsSkippedForAnHKCodeItHasNoSymbolFor(t *testing.T) {
	c := quoteShippedCache()
	on := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, quoteSourceYahoo}}.orDefaults()
	spellable, err := quoteTargetFor("hk", "00700")
	if err != nil {
		t.Fatalf("resolve hk 00700: %v", err)
	}
	unspellable, err := quoteTargetFor("hk", "80737")
	if err != nil {
		t.Fatalf("resolve hk 80737: %v", err)
	}
	// The same market, the same interval, two codes: the one Yahoo can spell resolves to it and the
	// one it cannot resolves to nobody — so the 5日 request degrades rather than 503s.
	if got := quoteSourceNamesOf(c.sourcesFor(on, spellable, quoteIntervalIntraday5D)); got != "yahoo" {
		t.Errorf("hk 00700 5日 resolved to [%s], want [yahoo]", got)
	}
	if got := quoteSourceNamesOf(c.sourcesFor(on, unspellable, quoteIntervalIntraday5D)); got != "" {
		t.Errorf("hk 80737 5日 resolved to [%s]; this portal has no yahoo symbol for that code, and a "+
			"source that cannot be asked must not be reached through the resolver", got)
	}
	if iv := c.servableInterval(on, unspellable, quoteIntervalIntraday5D); iv != quoteIntervalSnapshot {
		t.Errorf("hk 80737 5日 was served as %q; with no source able to answer it, the reader gets the "+
			"price and an empty chart rather than a 503", iv)
	}

	// End to end, which is where the 503 and the health record were observable. The Tencent stub
	// answers this code because the real endpoint does — there is no captured body for it, and what
	// is asserted here is the routing rather than a parse.
	s := quoteServer(t)
	tencent := &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		return &QuoteResp{
			Symbol: code, Name: "一家公司", Market: market, Kind: quoteKindStock,
			Currency: "HKD", TZ: "Asia/Hong_Kong", Source: quoteSourceTencent,
			Snapshot: QuoteSnapshot{Last: 1234, PrevClose: 1234, ChangePct: "0.00", Session: quoteSessionUnknown},
			Bars:     []QuoteBar{},
		}, nil
	}}
	wireQuoteSources(s, tencent, sinaStub(t))
	yahoo := yahooStub(t, fixYahooTencentMinute)
	wireYahooSource(s, yahoo)
	s.st.SetSetting(setQuoteSourceOrder, "tencent,sina,yahoo")

	rec := quoteGET(t, s, "/api/quote/80737?range=5d")
	if rec.Code != http.StatusOK {
		t.Fatalf("hk 80737 5日 → %d (%s), want the price and an empty chart", rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqStr(t, "source", got.Source, quoteSourceTencent)
	quoteEqInt(t, "last", got.Snapshot.Last, 1234)
	if len(got.Bars) != 0 {
		t.Errorf("a degraded 5日 request carried %d bars", len(got.Bars))
	}
	if yahoo.n() != 0 {
		t.Errorf("yahoo was called %d time(s) about a code it has no symbol for", yahoo.n())
	}
	for _, h := range s.QuoteHealth() {
		if h.Source == quoteSourceYahoo {
			t.Errorf("a skip reached the health counters: %+v — that error is a fact about OUR symbol "+
				"table, and an operator reads these to decide whether a VENDOR is down", h)
		}
	}
}
