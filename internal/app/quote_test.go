package app

// quote_test.go —— every case here runs against the responses in testdata/quote/, captured live from
// Tencent and Sina on 2026-09-06. Nothing in this file talks to the network, and nothing in it
// asserts against a hand-written miniature of a vendor response: a mock that restates the parser
// agrees with the parser by construction, including when both are wrong. The drift tests take a real
// captured response and change exactly one thing about it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fixTencentSH = "tencent_fqkline_sh601899.json"
	fixTencentSZ = "tencent_fqkline_sz000001.json"
	fixTencentBJ = "tencent_fqkline_bj830799.json"
	// Captured 2026-09-07 from the same endpoint as the three above, one per market difference the
	// parser now carries: a US ticker (dotted code echo, shares not 手, a New York clock), a Hong
	// Kong stock (a third timestamp layout, shares, whole-HKD turnover) and a Shanghai INDEX, which
	// is a stock's twin in every field except the one the vendor calls the instrument type.
	fixTencentUS    = "tencent_fqkline_usAAPL.json"
	fixTencentHK    = "tencent_fqkline_hk00700.json"
	fixTencentIndex = "tencent_fqkline_sh000001.json"
	fixSinaKline    = "sina_kline_sh601899.json"
	fixSinaSnap     = "sina_snapshot_sh601899.txt"
)

// quoteTencentFixtures says which market and code each captured Tencent body belongs to. It is a
// table rather than a rule read off the file name: usAAPL and hk00700 have no six digits to infer
// anything from, and sh000001 is a Shanghai index whose code marketPrefix maps to Shenzhen.
var quoteTencentFixtures = []struct{ file, market, code, sym string }{
	{fixTencentSH, "sh", "601899", "sh601899"},
	{fixTencentSZ, "sz", "000001", "sz000001"},
	{fixTencentBJ, "bj", "830799", "bj830799"},
	{fixTencentUS, "us", "AAPL", "usAAPL"},
	{fixTencentHK, "hk", "00700", "hk00700"},
	{fixTencentIndex, "sh", "000001", "sh000001"},
}

func readQuoteFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "quote", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func quoteEqInt(t *testing.T, what string, got, want int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}

func quoteEqStr(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// ---------- the fen parser everything else stands on ----------

func TestQuoteFenParser(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{in: "33.850", want: 3385},
		{in: "0.07", want: 7},
		{in: "", want: 0},
		{in: "-1.5", want: -150},
		{in: "1", want: 100},
		{in: "1.005", want: 101}, // half away from zero, not banker's rounding
		{in: "abc", bad: true},
		// 0.29 is the case that makes this helper exist: float64("0.29")*100 is
		// 28.999999999999996, so the obvious ParseFloat implementation yields 28 分.
		{in: "0.29", want: 29},
		{in: "-0.29", want: -29},
		{in: "1.004", want: 100},
		{in: "-1.005", want: -101},
		{in: "  33.85  ", want: 3385},
		{in: "+2.50", want: 250},
		{in: ".5", want: 50},
		{in: "5.", want: 500},
		{in: "0", want: 0},
		{in: "-0.004", want: 0},
		{in: "5939060556.000", want: 593906055600},
		{in: ".", bad: true},
		{in: "-", bad: true},
		{in: "1.0a", bad: true},
		{in: "1,000", bad: true},
		{in: "1e3", bad: true},
		{in: "99999999999999999999", bad: true},
		// Long enough to survive the digit loop and overflow only when the fraction is padded out
		// to two places, which is the case a guard on the digit loop alone would miss.
		{in: "922337203685477580", bad: true},
	}
	for _, c := range cases {
		got, err := quoteFen(c.in)
		switch {
		case c.bad && err == nil:
			t.Errorf("quoteFen(%q) = %d, want an error", c.in, got)
		case !c.bad && err != nil:
			t.Errorf("quoteFen(%q): %v", c.in, err)
		case !c.bad && got != c.want:
			t.Errorf("quoteFen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestQuoteScaledUnits pins the two unit conversions the vendors force on us: Tencent's 成交额 is
// 万元 and becomes 元 by asking for four decimal places of it, and a share count has none at all.
func TestQuoteScaledUnits(t *testing.T) {
	got, err := quoteScaled("593906", 4)
	if err != nil {
		t.Fatalf("quoteScaled: %v", err)
	}
	quoteEqInt(t, "593906 万元 in 元", got, 5_939_060_000)
	if got, err = quoteScaled("1769341.000", 0); err != nil {
		t.Fatalf("quoteScaled: %v", err)
	}
	quoteEqInt(t, "1769341.000 手 as a whole number", got, 1_769_341)
	if got, err = quoteScaled("", 4); err != nil {
		t.Fatalf("quoteScaled(%q): %v", "", err)
	}
	quoteEqInt(t, "an empty vendor field", got, 0)
}

func TestQuoteDivRoundHalfAwayFromZero(t *testing.T) {
	cases := []struct{ num, den, want int64 }{
		{5, 2, 3}, {-5, 2, -3}, {5, -2, -3}, {4, 2, 2}, {3, 2, 2}, {-3, 2, -2}, {1, 0, 0},
	}
	for _, c := range cases {
		if got := quoteDivRound(c.num, c.den); got != c.want {
			t.Errorf("quoteDivRound(%d, %d) = %d, want %d", c.num, c.den, got, c.want)
		}
	}
}

// ---------- goldens ----------

func TestQuoteTencentGolden(t *testing.T) {
	resp, err := parseTencentQuote("sh", "601899", readQuoteFixture(t, fixTencentSH), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote: %v", err)
	}
	quoteEqStr(t, "symbol", resp.Symbol, "601899")
	quoteEqStr(t, "name", resp.Name, "紫金矿业")
	quoteEqStr(t, "market", resp.Market, "sh")
	quoteEqStr(t, "source", resp.Source, "tencent")
	quoteEqStr(t, "barsSource", resp.BarsSource, "tencent")
	quoteEqStr(t, "barsUnavailable", resp.BarsUnavailable, "")
	if resp.Adjusted {
		t.Error("adjusted = true, want false: we ask for bfq and must never claim otherwise")
	}
	if resp.Cached {
		t.Error("cached = true, want false: a fresh parse is not a cache hit")
	}

	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 3335)
	quoteEqInt(t, "prevClose", s.PrevClose, 3331)
	quoteEqInt(t, "open", s.Open, 3385)
	quoteEqInt(t, "high", s.High, 3405)
	quoteEqInt(t, "low", s.Low, 3307)
	quoteEqInt(t, "change", s.Change, 4)
	quoteEqStr(t, "changePct", s.ChangePct, "0.12")
	quoteEqInt(t, "volume", s.Volume, 176_934_100) // 1769341 手 x 100
	quoteEqInt(t, "amount", s.Amount, 5_939_060_000)
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-04T16:14:58+08:00")
	quoteEqStr(t, "session", s.Session, "close")

	if len(resp.Bars) != 60 {
		t.Fatalf("bars = %d, want 60", len(resp.Bars))
	}
	first, last := resp.Bars[0], resp.Bars[len(resp.Bars)-1]
	// Oldest first, and close is the vendor's index 2 — reading the row as o/h/l/c would give this
	// day a close of 29.76 instead of 29.09.
	quoteEqStr(t, "bars[0].date", first.Date, "2026-06-12")
	quoteEqInt(t, "bars[0].open", first.Open, 2809)
	quoteEqInt(t, "bars[0].high", first.High, 2976)
	quoteEqInt(t, "bars[0].low", first.Low, 2781)
	quoteEqInt(t, "bars[0].close", first.Close, 2909)
	quoteEqInt(t, "bars[0].volume", first.Volume, 509_853_900)
	quoteEqStr(t, "bars[59].date", last.Date, "2026-09-04")
	quoteEqInt(t, "bars[59].open", last.Open, 3385)
	quoteEqInt(t, "bars[59].high", last.High, 3405)
	quoteEqInt(t, "bars[59].low", last.Low, 3307)
	quoteEqInt(t, "bars[59].close", last.Close, 3335)
	quoteEqInt(t, "bars[59].volume", last.Volume, 176_934_100)

	// 2026-06-26 is an ex-dividend date, and Tencent appends a seventh element to that row that is a
	// JSON OBJECT rather than a string. Decoding day into [][]string fails outright on it, which is
	// most of the market once a year.
	var dividendDay *QuoteBar
	for i := range resp.Bars {
		if resp.Bars[i].Date == "2026-06-26" {
			dividendDay = &resp.Bars[i]
		}
	}
	if dividendDay == nil {
		t.Fatal("the ex-dividend row 2026-06-26 is missing from the parsed bars")
	}
	quoteEqInt(t, "ex-dividend row close", dividendDay.Close, 2510)
}

func TestQuoteTencentGoldenShenzhen(t *testing.T) {
	resp, err := parseTencentQuote("sz", "000001", readQuoteFixture(t, fixTencentSZ), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote: %v", err)
	}
	quoteEqStr(t, "name", resp.Name, "平安银行")
	quoteEqStr(t, "market", resp.Market, "sz")
	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 1189)
	quoteEqInt(t, "prevClose", s.PrevClose, 1188)
	quoteEqInt(t, "open", s.Open, 1186)
	quoteEqInt(t, "high", s.High, 1200)
	quoteEqInt(t, "low", s.Low, 1185)
	quoteEqInt(t, "change", s.Change, 1)
	quoteEqStr(t, "changePct", s.ChangePct, "0.08")
	quoteEqInt(t, "volume", s.Volume, 81_437_300)
	quoteEqInt(t, "amount", s.Amount, 969_950_000)
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-04T16:15:00+08:00")
	quoteEqStr(t, "session", s.Session, "close")
	if len(resp.Bars) != 60 {
		t.Fatalf("bars = %d, want 60", len(resp.Bars))
	}
	last := resp.Bars[59]
	quoteEqStr(t, "bars[59].date", last.Date, "2026-09-04")
	quoteEqInt(t, "bars[59].open", last.Open, 1186)
	quoteEqInt(t, "bars[59].close", last.Close, 1189)
	quoteEqInt(t, "bars[59].high", last.High, 1200)
	quoteEqInt(t, "bars[59].low", last.Low, 1185)
	quoteEqInt(t, "bars[59].volume", last.Volume, 81_437_300)
}

// TestQuoteTencentSuspendedBeijing is the fixture that breaks every naive version of this parser:
// its qt array is 87 fields and not 88, its history is an empty array, and it reports high, low and
// volume as zero while quoting a real last price.
func TestQuoteTencentSuspendedBeijing(t *testing.T) {
	body := readQuoteFixture(t, fixTencentBJ)
	resp, err := parseTencentQuote("bj", "830799", body, 0)
	if err != nil {
		t.Fatalf("parseTencentQuote(bj830799): %v", err)
	}
	quoteEqStr(t, "name", resp.Name, "艾融软件")
	quoteEqStr(t, "market", resp.Market, "bj")
	quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
	quoteEqStr(t, "currency", resp.Currency, "CNY")
	quoteEqStr(t, "tz", resp.TZ, "Asia/Shanghai")
	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 3428)
	quoteEqInt(t, "prevClose", s.PrevClose, 3428)
	quoteEqInt(t, "open", s.Open, 3428)
	quoteEqInt(t, "high", s.High, 0)
	quoteEqInt(t, "low", s.Low, 0)
	quoteEqInt(t, "volume", s.Volume, 0)
	quoteEqInt(t, "amount", s.Amount, 0)
	quoteEqStr(t, "changePct", s.ChangePct, "0.00")
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-04T09:00:00+08:00")
	quoteEqStr(t, "session", s.Session, "close") // Beijing has no segment of its own and follows SZ

	if len(resp.Bars) != 0 {
		t.Fatalf("bars = %d, want 0", len(resp.Bars))
	}
	if resp.Bars == nil {
		t.Error("bars is nil; the contract says [] and the SPA maps over it")
	}
	quoteEqStr(t, "barsUnavailable", resp.BarsUnavailable, "market_unsupported")
	quoteEqStr(t, "barsSource", resp.BarsSource, "")

	// The point of the skip in check 5, stated as an assertion: this is a real quote whose last
	// price sits outside a low..high of 0..0, and a range check that did not skip zeros would report
	// every suspended stock in the market as a source failure.
	if s.High != 0 || s.Low != 0 || s.Last <= 0 {
		t.Fatalf("fixture no longer has the suspended shape (high %d low %d last %d)", s.High, s.Low, s.Last)
	}
	if err := quoteCheckRange(&s); err != nil {
		t.Errorf("quoteCheckRange on a suspended stock: %v, want nil", err)
	}

	// And the qt array really is 87 fields, so a len == 88 assertion would take the whole Beijing
	// exchange offline.
	if n := len(tencentQtArray(t, tencentDoc(t, fixTencentBJ), "bj830799")); n != 87 {
		t.Fatalf("bj830799 qt has %d fields; the contract is built on it having 87", n)
	}
}

// TestQuoteTencentGoldenUS pins every field of the captured Apple response. Three of them are the
// per-market traps and are asserted as numbers rather than as shapes: the volume is shares and not
// 手, the turnover is whole dollars and not 万, and the timestamp is a New York instant.
func TestQuoteTencentGoldenUS(t *testing.T) {
	resp, err := parseTencentQuote("us", "AAPL", readQuoteFixture(t, fixTencentUS), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote(usAAPL): %v", err)
	}
	quoteEqStr(t, "symbol", resp.Symbol, "AAPL")
	quoteEqStr(t, "name", resp.Name, "苹果")
	quoteEqStr(t, "market", resp.Market, "us")
	quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
	quoteEqStr(t, "currency", resp.Currency, "USD")
	quoteEqStr(t, "tz", resp.TZ, "America/New_York")
	quoteEqStr(t, "source", resp.Source, "tencent")

	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 31997)
	quoteEqInt(t, "prevClose", s.PrevClose, 32821)
	quoteEqInt(t, "open", s.Open, 32831)
	quoteEqInt(t, "high", s.High, 32893)
	quoteEqInt(t, "low", s.Low, 31786)
	quoteEqInt(t, "change", s.Change, -824)
	quoteEqStr(t, "changePct", s.ChangePct, "-2.51")
	quoteEqInt(t, "volume", s.Volume, 39_606_884)     // shares already; NOT 手
	quoteEqInt(t, "amount", s.Amount, 12_721_652_691) // whole USD; NOT 万
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-04T16:00:01-04:00")
	quoteEqStr(t, "session", s.Session, "close") // Labor Day, per the vendor's own US_ segment

	// The whole series the vendor has for this market — see TestQuoteUSDailyHistoryIsNotASeries.
	if len(resp.Bars) != 2 {
		t.Fatalf("bars = %d, want the 2 the captured body carries", len(resp.Bars))
	}
	first, last := resp.Bars[0], resp.Bars[1]
	quoteEqStr(t, "bars[0].date", first.Date, "2011-06-02")
	quoteEqInt(t, "bars[0].open", first.Open, 34622)
	quoteEqInt(t, "bars[0].close", first.Close, 34622)
	quoteEqInt(t, "bars[0].high", first.High, 34784)
	quoteEqInt(t, "bars[0].low", first.Low, 34453)
	quoteEqInt(t, "bars[0].volume", first.Volume, 16_780_815)
	quoteEqStr(t, "bars[1].date", last.Date, "2026-09-04")
	quoteEqInt(t, "bars[1].open", last.Open, 32831)
	quoteEqInt(t, "bars[1].close", last.Close, 31997)
	quoteEqInt(t, "bars[1].high", last.High, 32893)
	quoteEqInt(t, "bars[1].low", last.Low, 31786)
	quoteEqInt(t, "bars[1].volume", last.Volume, 39_606_884)
}

func TestQuoteTencentGoldenHongKong(t *testing.T) {
	resp, err := parseTencentQuote("hk", "00700", readQuoteFixture(t, fixTencentHK), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote(hk00700): %v", err)
	}
	quoteEqStr(t, "symbol", resp.Symbol, "00700")
	quoteEqStr(t, "name", resp.Name, "腾讯控股")
	quoteEqStr(t, "market", resp.Market, "hk")
	quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
	quoteEqStr(t, "currency", resp.Currency, "HKD")
	quoteEqStr(t, "tz", resp.TZ, "Asia/Hong_Kong")

	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 44020)
	quoteEqInt(t, "prevClose", s.PrevClose, 44280)
	quoteEqInt(t, "open", s.Open, 44060)
	quoteEqInt(t, "high", s.High, 44260)
	quoteEqInt(t, "low", s.Low, 43820)
	quoteEqInt(t, "change", s.Change, -260)
	quoteEqStr(t, "changePct", s.ChangePct, "-0.59")
	quoteEqInt(t, "volume", s.Volume, 7_940_789)     // shares already
	quoteEqInt(t, "amount", s.Amount, 3_492_941_240) // whole HKD, rounded from .648
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-07T14:57:39+08:00")
	quoteEqStr(t, "session", s.Session, "open") // captured mid-session, from the HK_ segment

	if len(resp.Bars) != 61 {
		t.Fatalf("bars = %d, want 61", len(resp.Bars))
	}
	// Row 0 carries a SEVENTH element that is a JSON object — a buyback note here rather than the
	// dividend record the Shanghai fixture has. Same trap, a different cause and a different
	// market: [][]string would fail on it, and 腾讯 buys its own shares back most trading days.
	first, last := resp.Bars[0], resp.Bars[60]
	quoteEqStr(t, "bars[0].date", first.Date, "2026-06-11")
	quoteEqInt(t, "bars[0].open", first.Open, 46980)
	quoteEqInt(t, "bars[0].close", first.Close, 45720)
	quoteEqInt(t, "bars[0].high", first.High, 47560)
	quoteEqInt(t, "bars[0].low", first.Low, 45500)
	quoteEqInt(t, "bars[0].volume", first.Volume, 28_580_619)
	quoteEqStr(t, "bars[60].date", last.Date, "2026-09-07")
	quoteEqInt(t, "bars[60].open", last.Open, 44060)
	quoteEqInt(t, "bars[60].close", last.Close, 44020)
	quoteEqInt(t, "bars[60].high", last.High, 44260)
	quoteEqInt(t, "bars[60].low", last.Low, 43820)
	quoteEqInt(t, "bars[60].volume", last.Volume, 7_940_789)
}

// TestQuoteTencentGoldenShanghaiIndex is 上证指数, which is a stock in every field the parser reads
// except one: same exchange, same array length, same units, same layout — and qt[0] says "1" exactly
// as 紫金矿业 does. Only the instrument-type field separates them.
func TestQuoteTencentGoldenShanghaiIndex(t *testing.T) {
	resp, err := parseTencentQuote("sh", "000001", readQuoteFixture(t, fixTencentIndex), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote(sh000001): %v", err)
	}
	quoteEqStr(t, "symbol", resp.Symbol, "000001")
	quoteEqStr(t, "name", resp.Name, "上证指数")
	quoteEqStr(t, "market", resp.Market, "sh")
	quoteEqStr(t, "kind", resp.Kind, quoteKindIndex)
	quoteEqStr(t, "currency", resp.Currency, "CNY")
	quoteEqStr(t, "tz", resp.TZ, "Asia/Shanghai")

	s := resp.Snapshot
	quoteEqInt(t, "last", s.Last, 393299)
	quoteEqInt(t, "prevClose", s.PrevClose, 393012)
	quoteEqInt(t, "open", s.Open, 394251)
	quoteEqInt(t, "high", s.High, 394842)
	quoteEqInt(t, "low", s.Low, 391649)
	quoteEqInt(t, "change", s.Change, 287)
	quoteEqStr(t, "changePct", s.ChangePct, "0.07")
	quoteEqInt(t, "volume", s.Volume, 47_138_397_900)  // 471383979 手 x 100: an index IS quoted in 手
	quoteEqInt(t, "amount", s.Amount, 888_521_610_000) // 88852161 万元
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-07T14:57:03+08:00")
	quoteEqStr(t, "session", s.Session, "open")

	if len(resp.Bars) != 61 {
		t.Fatalf("bars = %d, want 61", len(resp.Bars))
	}
	first, last := resp.Bars[0], resp.Bars[60]
	quoteEqStr(t, "bars[0].date", first.Date, "2026-06-12")
	quoteEqInt(t, "bars[0].open", first.Open, 401786)
	quoteEqInt(t, "bars[0].close", first.Close, 403151)
	quoteEqInt(t, "bars[0].high", first.High, 406027)
	quoteEqInt(t, "bars[0].low", first.Low, 400818)
	quoteEqInt(t, "bars[0].volume", first.Volume, 74_313_109_200)
	quoteEqStr(t, "bars[60].date", last.Date, "2026-09-07")
	quoteEqInt(t, "bars[60].close", last.Close, 393299)
	quoteEqInt(t, "bars[60].volume", last.Volume, 47_138_397_900)
}

// ---------- the per-market traps, one test each ----------

// A US 成交量 must NOT be multiplied by a hundred. The number is asserted against the fixture's own
// field rather than against a constant copied out of it, so the test cannot quietly agree with a
// parser that has started scaling: 39,606,884 shares is a day of Apple, and 3,960,688,400 is about
// four billion shares — a quarter of the company, changing hands, every day.
func TestQuoteUSVolumeIsSharesNotLots(t *testing.T) {
	doc := tencentDoc(t, fixTencentUS)
	rawVolume, ok := tencentQtArray(t, doc, "usAAPL")[6].(string)
	if !ok {
		t.Fatal("the fixture's qt[6] is not a string")
	}
	shares, err := quoteScaled(rawVolume, 0)
	if err != nil {
		t.Fatalf("fixture volume %q: %v", rawVolume, err)
	}
	resp, err := parseTencentQuote("us", "AAPL", readQuoteFixture(t, fixTencentUS), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote: %v", err)
	}
	quoteEqInt(t, "us snapshot volume", resp.Snapshot.Volume, shares)
	if resp.Snapshot.Volume == shares*100 {
		t.Fatalf("the 手 conversion was applied to a US quote: %d shares became %d", shares, resp.Snapshot.Volume)
	}
	// The same unit on a bar, because the two are read by different lines of the parser and the
	// snapshot alone would leave the chart wrong.
	quoteEqInt(t, "us bar volume", resp.Bars[1].Volume, 39_606_884)

	// And the conversion is still applied where it belongs — a test that only proved the absence of
	// a multiply would also pass with the multiply deleted for every market.
	ashare, err := parseTencentQuote("sh", "601899", readQuoteFixture(t, fixTencentSH), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote(sh601899): %v", err)
	}
	quoteEqInt(t, "a-share snapshot volume", ashare.Snapshot.Volume, 176_934_100) // 1769341 手 x 100
}

// The US 成交额 is whole dollars where the Chinese exchanges send 万元, and this is the assertion
// that a shared scale of 4 would fail: it would report Apple turning over 127 trillion dollars.
func TestQuoteAmountUnitIsPerMarket(t *testing.T) {
	for _, c := range []struct {
		file, market, code string
		want               int64
	}{
		{fixTencentUS, "us", "AAPL", 12_721_652_691},
		{fixTencentHK, "hk", "00700", 3_492_941_240},
		{fixTencentIndex, "sh", "000001", 888_521_610_000},
		{fixTencentSH, "sh", "601899", 5_939_060_000},
	} {
		resp, err := parseTencentQuote(c.market, c.code, readQuoteFixture(t, c.file), 0)
		if err != nil {
			t.Fatalf("parseTencentQuote(%s): %v", c.file, err)
		}
		quoteEqInt(t, c.market+" amount", resp.Snapshot.Amount, c.want)
	}
}

// Check 2 across the markets: the US echoes a Reuters instrument code and everybody else echoes the
// code we asked for. Both halves are load-bearing — a comparison loose enough for "AAPL.OQ" must
// still refuse "MSFT.OQ", and it must not loosen the other three markets on the way past.
func TestQuoteCodeEchoAcrossMarkets(t *testing.T) {
	usEcho := func(t *testing.T, echo string) []byte {
		t.Helper()
		doc := tencentDoc(t, fixTencentUS)
		arr := tencentQtArray(t, doc, "usAAPL")
		if arr[2] != "AAPL.OQ" {
			t.Fatalf("fixture qt[2] = %v, want AAPL.OQ", arr[2])
		}
		arr[2] = echo
		setTencentQtArray(t, doc, "usAAPL", arr)
		return quoteRemarshal(t, doc)
	}

	for _, echo := range []string{"AAPL.OQ", "AAPL", "aapl.oq", "AAPL.N"} {
		if _, err := parseTencentQuote("us", "AAPL", usEcho(t, echo), 0); err != nil {
			t.Errorf("a US echo of %q was refused: %v", echo, err)
		}
	}
	for _, echo := range []string{"MSFT.OQ", "AAP.OQ", "AAPLE.OQ", ".OQ", ""} {
		body := usEcho(t, echo)
		if echo == "MSFT.OQ" && !strings.Contains(string(body), "AAPL") {
			// The comparison is a FIELD against a FIELD. The body still carries "usAAPL" as its own
			// section key, so a strings.Contains over it would be satisfied by the response we are
			// trying to reject.
			t.Fatal("the mutated body no longer contains AAPL, so this case proves nothing")
		}
		if _, err := parseTencentQuote("us", "AAPL", body, 0); !errors.Is(err, errQuoteCodeEcho) {
			t.Errorf("a US echo of %q got %v, want %v", echo, err, errQuoteCodeEcho)
		}
	}

	t.Run("the other markets keep exact equality", func(t *testing.T) {
		// The dotted comparison is US-only. A Shanghai response that started echoing "601899.SS"
		// would be a vendor change nobody has verified, and it must fail rather than be tolerated by
		// a rule written for a different market.
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		arr[2] = "601899.SS"
		setTencentQtArray(t, doc, "sh601899", arr)
		wantDrift(t, quoteRemarshal(t, doc), errQuoteCodeEcho)

		hk := tencentDoc(t, fixTencentHK)
		hkArr := tencentQtArray(t, hk, "hk00700")
		if hkArr[2] != "00700" {
			t.Fatalf("hk fixture qt[2] = %v, want 00700", hkArr[2])
		}
		hkArr[2] = "00700.HK"
		setTencentQtArray(t, hk, "hk00700", hkArr)
		if _, err := parseTencentQuote("hk", "00700", quoteRemarshal(t, hk), 0); !errors.Is(err, errQuoteCodeEcho) {
			t.Fatalf("hk echo: got %v, want %v", err, errQuoteCodeEcho)
		}
	})
}

// TestQuoteKindComesFromTheVendorNotTheCode pins the one field a code's shape cannot decide.
func TestQuoteKindComesFromTheVendorNotTheCode(t *testing.T) {
	for _, c := range []struct{ file, market, code, want string }{
		{fixTencentIndex, "sh", "000001", quoteKindIndex},
		{fixTencentSH, "sh", "601899", quoteKindStock},
		{fixTencentSZ, "sz", "000001", quoteKindStock}, // the same six digits, the other exchange
		{fixTencentBJ, "bj", "830799", quoteKindStock},
		{fixTencentHK, "hk", "00700", quoteKindStock},
		{fixTencentUS, "us", "AAPL", quoteKindStock},
	} {
		resp, err := parseTencentQuote(c.market, c.code, readQuoteFixture(t, c.file), 0)
		if err != nil {
			t.Fatalf("parseTencentQuote(%s): %v", c.file, err)
		}
		quoteEqStr(t, c.file+" kind", resp.Kind, c.want)
	}

	// The table's three indices, checked against the bodies they were measured on. A kindAt that has
	// slid by one lands on a price or on an empty string, which still READS as "stock" — so the
	// answers above cannot catch it and this is what does.
	for _, f := range quoteTencentFixtures {
		qt := tencentQtArray(t, tencentDoc(t, f.file), f.sym)
		m := quoteTestMarket(t, f.market)
		if m.kindAt <= 0 || m.kindAt >= len(qt) {
			t.Errorf("%s: kindAt %d is outside the %d fields the body carries", f.file, m.kindAt, len(qt))
			continue
		}
		switch got := qt[m.kindAt]; got {
		case "GP", "GP-A", "ETF", quoteVendorKindIndex: // 股票, A股, 基金, 指数
		default:
			t.Errorf("%s: qt[%d] is %v, which is not one of the vendor's instrument types — the index has moved",
				f.file, m.kindAt, got)
		}
	}

	// The premise of reading a field at 56/61/63 instead of qt[0]: on the real bodies, qt[0] is the
	// same for 上证指数 and 紫金矿业, so it cannot be the source of this answer. If that ever stops
	// being true this test fails and the parser can be simplified.
	index := tencentQtArray(t, tencentDoc(t, fixTencentIndex), "sh000001")
	stock := tencentQtArray(t, tencentDoc(t, fixTencentSH), "sh601899")
	if index[0] != stock[0] {
		t.Fatalf("qt[0] is %v for 上证指数 and %v for 紫金矿业; kind could come from qt[0] after all", index[0], stock[0])
	}
	// And on the fqkline body the US response does not even carry a market type there.
	if us := tencentQtArray(t, tencentDoc(t, fixTencentUS), "usAAPL"); us[0] != "delay" {
		t.Errorf("the US qt[0] is %v; the comment in quoteMarket.kindAt says it is the literal \"delay\"", us[0])
	}

	t.Run("a body too short for the type field is a stock, not a failure", func(t *testing.T) {
		// The field sits far past quoteTencentQtFields, so a response that is long enough for the
		// prices but short of it must still serve a quote. The kind is a label beside a price.
		doc := tencentDoc(t, fixTencentIndex)
		setTencentQtArray(t, doc, "sh000001", tencentQtArray(t, doc, "sh000001")[:quoteTencentQtFields])
		resp, err := parseTencentQuote("sh", "000001", quoteRemarshal(t, doc), 0)
		if err != nil {
			t.Fatalf("a truncated-but-sufficient body was refused: %v", err)
		}
		quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
		quoteEqInt(t, "last", resp.Snapshot.Last, 393299)
	})
}

// TestQuoteUSDailyHistoryIsNotASeries records the measurement that took the US out of the markets
// with drawable history, because it is the one place this parser contradicts the table it was
// written from.
//
// Asked for sixty daily bars, Tencent's fqkline endpoint answers a US ticker with TWO rows: the
// oldest it holds and the latest session. Both are real candles, both pass every check in the gate,
// and a chart drawn from them is a straight line from 2011 to last Friday labelled "3个月". That is
// ADR 0028 §7's Beijing decision arriving from a different direction — stale data that looks like
// data is worse than no data — so the market carries history=false and the handler is told to serve
// no bars for it at all.
func TestQuoteUSDailyHistoryIsNotASeries(t *testing.T) {
	rows := tencentDayRows(t, tencentDoc(t, fixTencentUS), "usAAPL")
	if len(rows) != 2 {
		t.Fatalf("the captured US body has %d day rows; the finding this test records was 2", len(rows))
	}
	first := rows[0].([]any)[0]
	last := rows[1].([]any)[0]
	if first != "2011-06-02" || last != "2026-09-04" {
		t.Fatalf("the two US rows are %v and %v; the finding was a fifteen-year gap", first, last)
	}

	if quoteMarketHasDailyHistory("us") {
		t.Error("the us market claims drawable daily history; the vendor sends two rows fifteen years apart")
	}
	if !quoteMarketHasDailyHistory("hk") || !quoteMarketHasDailyHistory("sh") || !quoteMarketHasDailyHistory("sz") {
		t.Error("hk, sh and sz all have real daily series in the captured fixtures")
	}
	if quoteMarketHasDailyHistory("bj") {
		t.Error("bj has no daily history on either vendor (ADR 0028 §7)")
	}
	// The two empty-series reasons stay distinguishable: one is worth retrying and the other is not.
	quoteEqStr(t, "us reason", quoteBarsUnavailableFor("us"), quoteBarsMarketUnsupported)
	quoteEqStr(t, "bj reason", quoteBarsUnavailableFor("bj"), quoteBarsMarketUnsupported)
	quoteEqStr(t, "sh reason", quoteBarsUnavailableFor("sh"), quoteBarsSourceFailed)
}

// The fallback is A-share shaped, and asking it about Hong Kong or the US is refused in words rather
// than left to fail somewhere downstream where the reason would read as an outage.
//
// The mutation this had to survive: with the refusal removed, a re-keyed A-share line parses
// perfectly well as "hk00700", because every check the Sina path runs is satisfied — the field count
// is the A-share line's, and the code echo compares the variable name against the symbol we built.
// So the line below is deliberately one that WOULD parse. Handing the parser a line whose key does
// not match would have tested the code-echo check instead, and passed with this guard deleted.
func TestQuoteSinaServesOnlyTheAShareMarkets(t *testing.T) {
	for _, c := range []struct{ market, code, sym string }{
		{"hk", "00700", "hk00700"},
		{"us", "AAPL", "usAAPL"},
	} {
		if _, err := quoteSinaTarget(c.market, c.code); err == nil {
			t.Errorf("quoteSinaTarget(%q) succeeded; sina's line for that market is a different shape", c.market)
		}
		line := sinaSnapshotWith(t, c.sym, func(f []string) []string { return f })
		if resp, err := parseSinaSnapshot(c.market, c.code, line); err == nil {
			t.Errorf("parseSinaSnapshot(%q) read an A-share line as %s: last=%d", c.market, c.market, resp.Snapshot.Last)
		}
		// The fetcher refuses BEFORE it dials, which is why an already-cancelled context comes back
		// as the market refusal rather than as context.Canceled. Without the guard this line makes a
		// real request to Sina about a symbol Sina keys differently.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fetchSinaQuote(ctx, c.market, c.code, 60)
		if err == nil {
			t.Errorf("fetchSinaQuote(%q) succeeded on a cancelled context", c.market)
		} else if errors.Is(err, context.Canceled) {
			t.Errorf("fetchSinaQuote(%q) reached the network before refusing the market: %v", c.market, err)
		}
	}
	// And the market it does serve still parses — a guard that refused everything would pass the
	// whole loop above.
	if _, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap))); err != nil {
		t.Fatalf("parseSinaSnapshot(sh): %v", err)
	}
}

func TestQuoteSinaGolden(t *testing.T) {
	resp, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap)))
	if err != nil {
		t.Fatalf("parseSinaSnapshot: %v", err)
	}
	quoteEqStr(t, "symbol", resp.Symbol, "601899")
	quoteEqStr(t, "name", resp.Name, "紫金矿业")
	quoteEqStr(t, "source", resp.Source, "sina")
	quoteEqStr(t, "currency", resp.Currency, "CNY")
	quoteEqStr(t, "tz", resp.TZ, "Asia/Shanghai")
	// Empty and not "stock": Sina's line has no instrument-type column, and 上证指数 comes down it
	// with the same 34-field shape a stock does. Claiming a kind here would be this parser asserting
	// something it did not read — the same reason Session stays "unknown" on this path.
	quoteEqStr(t, "kind", resp.Kind, "")
	s := resp.Snapshot
	quoteEqInt(t, "open", s.Open, 3385)
	quoteEqInt(t, "prevClose", s.PrevClose, 3331)
	quoteEqInt(t, "last", s.Last, 3335)
	quoteEqInt(t, "high", s.High, 3405)
	quoteEqInt(t, "low", s.Low, 3307)
	quoteEqInt(t, "change", s.Change, 4)
	quoteEqStr(t, "changePct", s.ChangePct, "0.12")
	quoteEqInt(t, "volume", s.Volume, 176_934_067) // already 股, no x100
	quoteEqInt(t, "amount", s.Amount, 5_939_060_556)
	quoteEqStr(t, "asOf", s.AsOf, "2026-09-04T15:34:59+08:00")
	// Sina's line carries no market-state field, and inventing one from this server's clock is
	// exactly what AsOf exists to prevent.
	quoteEqStr(t, "session", s.Session, "unknown")

	bars, err := parseSinaKLine(readQuoteFixture(t, fixSinaKline))
	if err != nil {
		t.Fatalf("parseSinaKLine: %v", err)
	}
	if len(bars) != 60 {
		t.Fatalf("bars = %d, want 60", len(bars))
	}
	first, last := bars[0], bars[59]
	quoteEqStr(t, "bars[0].date", first.Date, "2026-06-12")
	quoteEqInt(t, "bars[0].open", first.Open, 2809)
	quoteEqInt(t, "bars[0].high", first.High, 2976)
	quoteEqInt(t, "bars[0].low", first.Low, 2781)
	quoteEqInt(t, "bars[0].close", first.Close, 2909)
	quoteEqInt(t, "bars[0].volume", first.Volume, 509_853_858)
	quoteEqStr(t, "bars[59].date", last.Date, "2026-09-04")
	quoteEqInt(t, "bars[59].open", last.Open, 3385)
	quoteEqInt(t, "bars[59].high", last.High, 3405)
	quoteEqInt(t, "bars[59].low", last.Low, 3307)
	quoteEqInt(t, "bars[59].close", last.Close, 3335)
	quoteEqInt(t, "bars[59].volume", last.Volume, 176_934_067)
}

// TestQuoteVendorsAgreeOnOverlappingDays proves — rather than assumes — that the fallback draws the
// same chart as the primary. The two vendors send the six columns of a day row in DIFFERENT orders
// (Tencent: date, open, close, high, low; Sina: named keys), so a transposition in either parser
// shows up here as a mismatched fen on some date.
func TestQuoteVendorsAgreeOnOverlappingDays(t *testing.T) {
	tencent, err := parseTencentQuote("sh", "601899", readQuoteFixture(t, fixTencentSH), 0)
	if err != nil {
		t.Fatalf("parseTencentQuote: %v", err)
	}
	sina, err := parseSinaKLine(readQuoteFixture(t, fixSinaKline))
	if err != nil {
		t.Fatalf("parseSinaKLine: %v", err)
	}
	byDate := make(map[string]QuoteBar, len(sina))
	for _, b := range sina {
		byDate[b.Date] = b
	}
	overlap := 0
	for _, a := range tencent.Bars {
		b, ok := byDate[a.Date]
		if !ok {
			continue
		}
		overlap++
		if a.Open != b.Open || a.High != b.High || a.Low != b.Low || a.Close != b.Close {
			t.Errorf("%s: tencent o/h/l/c %d/%d/%d/%d, sina %d/%d/%d/%d",
				a.Date, a.Open, a.High, a.Low, a.Close, b.Open, b.High, b.Low, b.Close)
		}
		// The volume unit conversion, proved the same way: Tencent counts whole 手 and so cannot
		// resolve the last two digits of a share count, but multiplying by 100 has to land within
		// one 手 of Sina's 股. A missing or doubled x100 would be off by a factor of a hundred.
		if d := a.Volume - b.Volume; d <= -100 || d >= 100 {
			t.Errorf("%s: tencent volume %d 股 is %d away from sina's %d", a.Date, a.Volume, d, b.Volume)
		}
	}
	if overlap != 60 {
		t.Fatalf("compared %d overlapping days, want 60 — the test must not pass by comparing nothing", overlap)
	}
}

// ---------- the vendor clock and the session string ----------

// quoteTestMarket is the market row a test is talking about. It fails rather than returns on an
// unknown id, so a typo in a case table is a test failure and not a nil dereference.
func quoteTestMarket(t *testing.T, id string) *quoteMarket {
	t.Helper()
	m, ok := quoteMarkets[id]
	if !ok {
		t.Fatalf("no market %q in quoteMarkets", id)
	}
	return m
}

// TestQuoteAsOfIsStampedInTheMarketsOwnZone moves the process's own zone somewhere it has never
// been and then walks all three layouts the vendor sends in the one array position. Two promises are
// under test at once: AsOf is the VENDOR's instant and never this server's — a stale feed only looks
// stale if the timestamp shown is the feed's own — and it is stamped in the MARKET's zone rather
// than in one constant, which is the difference between a New York close and a Chinese afternoon.
func TestQuoteAsOfIsStampedInTheMarketsOwnZone(t *testing.T) {
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("nowhere", -5*60*60)

	for _, c := range []struct {
		market, stamp, want string
	}{
		{"sh", "20260904161458", "2026-09-04T16:14:58+08:00"},
		{"sz", "20260904161500", "2026-09-04T16:15:00+08:00"},
		{"bj", "20260904090000", "2026-09-04T09:00:00+08:00"},
		{"hk", "2026/09/07 14:57:39", "2026-09-07T14:57:39+08:00"},
		{"us", "2026-09-04 16:00:01", "2026-09-04T16:00:01-04:00"},
	} {
		got, err := quoteVendorTime(quoteTestMarket(t, c.market), c.stamp)
		if err != nil {
			t.Fatalf("quoteVendorTime(%s, %q): %v", c.market, c.stamp, err)
		}
		quoteEqStr(t, c.market+" timestamp", got, c.want)
	}

	t.Run("the US instant is not +08:00", func(t *testing.T) {
		// The assertion stated as an INSTANT rather than as a string, because the string is only
		// wrong in its last six characters and that is easy to read past. A US close stamped +08:00
		// is 08:00:01Z; stamped in New York it is 20:00:01Z. Twelve hours apart in September — and
		// the reader is shown a quote line claiming to be from the middle of a Chinese working day.
		got, err := quoteVendorTime(quoteTestMarket(t, "us"), "2026-09-04 16:00:01")
		if err != nil {
			t.Fatalf("quoteVendorTime: %v", err)
		}
		at, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		wrong := time.Date(2026, 9, 4, 16, 0, 1, 0, time.FixedZone("CST", 8*60*60))
		if at.Equal(wrong) {
			t.Fatalf("the US close was stamped +08:00 (%s)", got)
		}
		if want := time.Date(2026, 9, 4, 20, 0, 1, 0, time.UTC); !at.Equal(want) {
			t.Errorf("US close is %s, want the instant %s (%v off)", at.UTC(), want, at.Sub(want))
		}
		if _, offset := at.Zone(); offset != -4*60*60 {
			t.Errorf("US offset is %ds, want -14400 (EDT)", offset)
		}
	})

	t.Run("the US zone moves with daylight saving", func(t *testing.T) {
		// The reason the zone is an IANA NAME and not an offset: the same market is -05:00 in
		// January. A time.FixedZone that is right today is an hour wrong for four months of the
		// year, and this is the case that catches anyone replacing the lookup with one.
		got, err := quoteVendorTime(quoteTestMarket(t, "us"), "2026-01-05 16:00:01")
		if err != nil {
			t.Fatalf("quoteVendorTime: %v", err)
		}
		quoteEqStr(t, "a January US close", got, "2026-01-05T16:00:01-05:00")
	})

	t.Run("sina splits the same instant across two fields", func(t *testing.T) {
		got, err := quoteSinaTime(quoteTestMarket(t, "sh"), "2026-09-04", "15:34:59")
		if err != nil {
			t.Fatalf("quoteSinaTime: %v", err)
		}
		quoteEqStr(t, "sina timestamp", got, "2026-09-04T15:34:59+08:00")
	})

	t.Run("a layout is not interchangeable with another market's", func(t *testing.T) {
		// Each market's stamp must be parsed with that market's layout: the US and Hong Kong
		// strings differ from each other only in a separator, and time.Parse is perfectly willing
		// to be told the wrong one about a real instant.
		for _, c := range []struct{ market, stamp string }{
			{"sh", ""}, {"sh", "not a time"}, {"sh", "2026-09-04 16:14:58"},
			{"us", "20260904161458"}, {"us", "2026/09/04 16:00:01"},
			{"hk", "2026-09-07 14:57:39"}, {"hk", "20260907145739"},
		} {
			if got, err := quoteVendorTime(quoteTestMarket(t, c.market), c.stamp); err == nil {
				t.Errorf("quoteVendorTime(%s, %q) = %q, want an error", c.market, c.stamp, got)
			}
		}
	})
}

// TestQuoteMarketZonesLoad pins the zone NAMES in the table. A typo there is not a compile error and
// not a parse error either — it is a quote that fails at 3am on a market nobody was looking at — so
// it is worth one assertion per row.
//
// What this does NOT prove is that the binary carries the zone database: the machine running the
// tests has /usr/share/zoneinfo, so time.LoadLocation would answer here with or without the
// time/tzdata import in quote.go. The import's job is the machine that does not.
func TestQuoteMarketZonesLoad(t *testing.T) {
	for id, m := range quoteMarkets {
		loc, err := quoteZone(m.zone)
		if err != nil {
			t.Errorf("market %q names zone %q, which does not load: %v", id, m.zone, err)
			continue
		}
		if loc.String() != m.zone {
			t.Errorf("market %q loaded %q for %q", id, loc, m.zone)
		}
		if m.currency == "" || m.stamp == "" {
			t.Errorf("market %q has currency %q and stamp %q; both are shown to a reader", id, m.currency, m.stamp)
		}
	}

	// A zone that cannot be loaded must be an ERROR and never time.Local. The fallback is the one
	// mistake this function exists to make impossible: it would stamp a vendor's instant with the
	// operator's deployment choice, so the same body would produce a different AsOf in Frankfurt
	// than in Singapore. This is the only way to reach that branch on a machine that has tzdata.
	if loc, err := quoteZone("Nowhere/Special"); err == nil {
		t.Errorf("an unloadable zone resolved to %v instead of failing", loc)
	}
}

func TestQuoteMarketSession(t *testing.T) {
	// The captured session string, trimmed to the segments that matter. Every one of NEWSH_, NEWSZ_,
	// NEWUS_ and HSZB_ is a real segment of the real line, and each of them CONTAINS the prefix of a
	// market we serve — "SZ_" sits inside "NEWSZ_" and "HSZB_", "US_" inside "NEWUS_" — so a
	// strings.Contains match would report a different board's state while looking right.
	const raw = "2026-09-07 05:56:37|HK_close_x|SH_close_x|SZ_close_x|US_close_x|NEWSH_open_x|NEWSZ_open_x|NEWUS_open_x|HSZB_open_x|KCB_close_x"
	cases := []struct{ market, raw, want string }{
		{"sh", raw, "close"},
		{"sz", raw, "close"},
		{"bj", raw, "close"}, // Beijing has no segment of its own and follows SZ
		{"hk", raw, "close"},
		{"us", raw, "close"},
		{"sh", "2026-09-07 09:45:00|SH_open_交易中|SZ_open_交易中", "open"},
		{"bj", "2026-09-07 09:45:00|SH_open_交易中|SZ_open_交易中", "open"},
		// The captured line from the US fixture: Hong Kong and the mainland trading, New York shut
		// for Labor Day. One line, four different answers.
		{"hk", "2026-09-07 14:51:13|HK_open_交易中|SH_open_交易中|SZ_open_交易中|US_close_劳动节休市|NEWUS_close_劳动节休市", "open"},
		{"us", "2026-09-07 14:51:13|HK_open_交易中|SH_open_交易中|SZ_open_交易中|US_close_劳动节休市|NEWUS_close_劳动节休市", "close"},
		{"sh", "2026-09-07 09:45:00|SH_suspended_x", "unknown"},
		{"sh", "", "unknown"},
		{"sh", "2026-09-07 09:45:00|NEWSH_open_x", "unknown"}, // NEWSH is not SH
		{"us", "2026-09-07 09:45:00|NEWUS_open_x", "unknown"}, // and NEWUS is not US
		// The case that separates HasPrefix from Contains, which the two above do not: a NEW-
		// segment standing BEFORE the market's own. A prefix match walks past it and answers from
		// US_; a Contains match latches onto NEWUS_, then slices the state at the wrong offset and
		// reports a trading market as unknown.
		{"us", "2026-09-07 22:31:00|NEWUS_close_x|US_open_交易中", "open"},
		{"sz", "2026-09-07 09:45:00|HSZB_close_x|SZ_open_交易中", "open"},
	}
	for _, c := range cases {
		if got := quoteMarketSession(quoteTestMarket(t, c.market), c.raw); got != c.want {
			t.Errorf("quoteMarketSession(%q, %.60q) = %q, want %q", c.market, c.raw, got, c.want)
		}
	}
	if got := quoteMarketSession(nil, raw); got != quoteSessionUnknown {
		t.Errorf("quoteMarketSession(nil) = %q, want %q", got, quoteSessionUnknown)
	}
}

// ---------- drift gate: one mutated real fixture per rule ----------

func tencentDoc(t *testing.T, file string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readQuoteFixture(t, file), &doc); err != nil {
		t.Fatalf("decode fixture %s: %v", file, err)
	}
	return doc
}

func tencentSection(t *testing.T, doc map[string]any, sym string) map[string]any {
	t.Helper()
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no data object")
	}
	sec, ok := data[sym].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no %q section", sym)
	}
	return sec
}

func tencentQtArray(t *testing.T, doc map[string]any, sym string) []any {
	t.Helper()
	qt, ok := tencentSection(t, doc, sym)["qt"].(map[string]any)
	if !ok {
		t.Fatalf("fixture %q has no qt object", sym)
	}
	arr, ok := qt[sym].([]any)
	if !ok {
		t.Fatalf("fixture %q has no qt array", sym)
	}
	return arr
}

func setTencentQtArray(t *testing.T, doc map[string]any, sym string, arr []any) {
	t.Helper()
	tencentSection(t, doc, sym)["qt"].(map[string]any)[sym] = arr
}

func tencentDayRows(t *testing.T, doc map[string]any, sym string) []any {
	t.Helper()
	rows, ok := tencentSection(t, doc, sym)["day"].([]any)
	if !ok {
		t.Fatalf("fixture %q has no day array", sym)
	}
	return rows
}

func quoteRemarshal(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encode mutated fixture: %v", err)
	}
	return b
}

// wantDrift runs the Shanghai fixture through the parser and demands one specific gate failure.
func wantDrift(t *testing.T, body []byte, sentinel error) {
	t.Helper()
	resp, err := parseTencentQuote("sh", "601899", body, 0)
	if err == nil {
		t.Fatalf("mutated fixture parsed cleanly into %+v; the drift gate let it through", resp.Snapshot)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
}

// Check 1 — field count. The mutation truncates a real response to one field short of the highest
// index the parser reads.
func TestQuoteDriftFieldCount(t *testing.T) {
	t.Run("tencent qt truncated to 38", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		setTencentQtArray(t, doc, "sh601899", tencentQtArray(t, doc, "sh601899")[:quoteTencentQtFields-1])
		wantDrift(t, quoteRemarshal(t, doc), errQuoteFieldCount)
	})

	t.Run("tencent day row truncated to 5", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		rows := tencentDayRows(t, doc, "sh601899")
		rows[7] = rows[7].([]any)[:quoteDayRowFields-1]
		wantDrift(t, quoteRemarshal(t, doc), errQuoteFieldCount)
	})

	t.Run("sina snapshot truncated to 31", func(t *testing.T) {
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string { return f[:quoteSinaSnapFields-1] })
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteFieldCount) {
			t.Fatalf("got %v, want %v", err, errQuoteFieldCount)
		}
	})

	t.Run("an unknown symbol answers with an empty payload", func(t *testing.T) {
		// Sina does not 404 a symbol it has never heard of; it sends the assignment with nothing in
		// it, which splits into exactly one empty field.
		if _, err := parseSinaSnapshot("sh", "601899", `var hq_str_sh601899="";`); !errors.Is(err, errQuoteFieldCount) {
			t.Fatalf("got %v, want %v", err, errQuoteFieldCount)
		}
	})
}

// Check 2 — code echo. The mutation changes only the echoed code, and the test also shows why the
// comparison may not be a strings.Contains over the body: the body still contains the requested code
// in three other places.
func TestQuoteDriftCodeEcho(t *testing.T) {
	doc := tencentDoc(t, fixTencentSH)
	arr := tencentQtArray(t, doc, "sh601899")
	if arr[2] != "601899" {
		t.Fatalf("fixture qt[2] = %v, want 601899", arr[2])
	}
	arr[2] = "601898" // the neighbouring listing: a plausible six digits, and the wrong company
	setTencentQtArray(t, doc, "sh601899", arr)
	body := quoteRemarshal(t, doc)

	if !strings.Contains(string(body), "601899") {
		t.Fatal("the mutated body no longer contains the requested code, so this test proves nothing")
	}
	wantDrift(t, body, errQuoteCodeEcho)

	t.Run("sina variable name", func(t *testing.T) {
		line := strings.Replace(string(readQuoteFixture(t, fixSinaSnap)), "hq_str_sh601899", "hq_str_sh601898", 1)
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteCodeEcho) {
			t.Fatalf("got %v, want %v", err, errQuoteCodeEcho)
		}
	})
}

// Check 3 — the arithmetic identity, and the only check that survives a vendor inserting a column.
func TestQuoteDriftIdentity(t *testing.T) {
	t.Run("vendor percentage contradicts its own prices", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		arr[32] = "5.00" // last/prevClose imply 0.12
		setTencentQtArray(t, doc, "sh601899", arr)
		wantDrift(t, quoteRemarshal(t, doc), errQuoteIdentity)
	})

	t.Run("a single inserted column", func(t *testing.T) {
		// This is the mutation the whole gate is built around. One extra field at index 3 shifts
		// every price after it by one: the response is still 89 plausible strings, the length check
		// passes, the code at index 2 is untouched and still echoes correctly, and every value the
		// parser now reads is a real price of a real stock — just not the one it is labelled with.
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		shifted := append([]any{}, arr[:3]...)
		shifted = append(shifted, "1.00")
		shifted = append(shifted, arr[3:]...)
		setTencentQtArray(t, doc, "sh601899", shifted)
		body := quoteRemarshal(t, doc)

		if err := quoteCheckFieldCount("qt", len(shifted), quoteTencentQtFields); err != nil {
			t.Fatalf("check 1 rejected the shifted response (%v); check 3 must be what catches it", err)
		}
		if err := quoteCheckCodeEcho("601899", shifted[2].(string)); err != nil {
			t.Fatalf("check 2 rejected the shifted response (%v); check 3 must be what catches it", err)
		}
		wantDrift(t, body, errQuoteIdentity)
	})

	t.Run("skipped when prevClose is zero", func(t *testing.T) {
		// A stock that has never traded has no percentage to disagree with, and dividing by its
		// prevClose would be undefined rather than wrong.
		snap := QuoteSnapshot{Last: 1000, PrevClose: 0, ChangePct: "99.99"}
		if err := quoteCheckIdentity(&snap); err != nil {
			t.Errorf("quoteCheckIdentity with prevClose 0: %v, want nil", err)
		}
	})

	t.Run("a real limit-up day is not drift", func(t *testing.T) {
		// The reason there is deliberately no percentage band on the change: +10% is an ordinary
		// main-board limit-up, +20% an ordinary 创业板 one, +30% an ordinary 北交所 one, and a
		// first-day listing has no limit at all. Every one of these agrees with its own prices, and
		// a band narrow enough to catch a shifted column would report the most newsworthy days of
		// the year as a source failure.
		for _, c := range []struct {
			prev, last int64
			what       string
		}{
			{1000, 1100, "a main-board limit-up"},
			{1000, 1200, "a 创业板 limit-up"},
			{1000, 1300, "a 北交所 limit-up"},
			{950, 500, "an ex-rights gap down"},
			{1000, 28834, "a first-day listing with no limit at all"},
		} {
			// The percentage is derived from the prices rather than the prices from the percentage,
			// because a price is only ever a whole 分 and working backwards from a percentage
			// invents a quantisation error the vendors never send.
			pct := quotePctString(quoteDivRound((c.last-c.prev)*10_000, c.prev))
			snap := QuoteSnapshot{PrevClose: c.prev, Last: c.last, ChangePct: pct}
			if err := quoteCheckIdentity(&snap); err != nil {
				t.Errorf("%s (%s%%) was rejected as drift: %v", c.what, pct, err)
			}
		}
	})
}

// Check 4 — per-bar sanity.
func TestQuoteDriftBarSanity(t *testing.T) {
	mutateRow := func(t *testing.T, field int, value string) []byte {
		t.Helper()
		doc := tencentDoc(t, fixTencentSH)
		rows := tencentDayRows(t, doc, "sh601899")
		row := rows[0].([]any)
		row[field] = value
		rows[0] = row
		return quoteRemarshal(t, doc)
	}

	t.Run("low above the open", func(t *testing.T) {
		// Row 0 is ["2026-06-12","28.090","29.090","29.760","27.810",…]; a low of 29.900 is above
		// both the open and the close while remaining a perfectly plausible price for this stock.
		wantDrift(t, mutateRow(t, 4, "29.900"), errQuoteBarSanity)
	})
	t.Run("high below the close", func(t *testing.T) {
		wantDrift(t, mutateRow(t, 3, "28.500"), errQuoteBarSanity)
	})
	t.Run("a zero price", func(t *testing.T) {
		wantDrift(t, mutateRow(t, 1, "0.000"), errQuoteBarSanity)
	})
	t.Run("sina history is checked the same way", func(t *testing.T) {
		var rows []map[string]string
		if err := json.Unmarshal(readQuoteFixture(t, fixSinaKline), &rows); err != nil {
			t.Fatalf("decode sina fixture: %v", err)
		}
		rows[3]["low"] = "999.000"
		b, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("re-encode sina fixture: %v", err)
		}
		if _, err := parseSinaKLine(b); !errors.Is(err, errQuoteBarSanity) {
			t.Fatalf("got %v, want %v", err, errQuoteBarSanity)
		}
	})
}

// Check 5 — the last price inside the day's range, and the zero-bound skip that keeps a suspended
// stock from being reported as a source failure (asserted on the bj830799 fixture above).
func TestQuoteDriftRange(t *testing.T) {
	doc := tencentDoc(t, fixTencentSH)
	arr := tencentQtArray(t, doc, "sh601899")
	arr[33] = "33.00" // a high below the 33.35 last, with the 33.07 low left alone so nothing is zero
	setTencentQtArray(t, doc, "sh601899", arr)
	wantDrift(t, quoteRemarshal(t, doc), errQuoteRange)

	t.Run("sina snapshot is caught by the stronger check that replaced check 3", func(t *testing.T) {
		// The same mutation, and on the Sina path it trips quoteCheckSnapshotOrder rather than
		// check 5 — that check is strictly stronger about last (low <= min(open,last) <= last <=
		// max(open,last) <= high), so nothing check 5 could catch here survives it. Asserting the
		// sentinel that actually fires is the point: an assertion of errQuoteRange would have to be
		// wrong about which check is doing the work.
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string {
			f[4] = "33.000" // high, below the 33.350 last
			return f
		})
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteOrdering) {
			t.Fatalf("got %v, want %v", err, errQuoteOrdering)
		}
		// And check 5 itself still refuses that shape when it is the one asked, so the sentinel
		// above is a statement about ORDER, not about check 5 having quietly stopped working.
		snap := QuoteSnapshot{Last: 3335, High: 3300, Low: 3307}
		if err := quoteCheckRange(&snap); !errors.Is(err, errQuoteRange) {
			t.Fatalf("quoteCheckRange on last 33.35 outside 33.07..33.00: %v, want %v", err, errQuoteRange)
		}
	})
}

// sinaSnapshotWith rebuilds the captured Sina line after fn has edited its comma-separated fields.
func sinaSnapshotWith(t *testing.T, sym string, fn func([]string) []string) string {
	t.Helper()
	_, payload, err := quoteSinaLine(string(readQuoteFixture(t, fixSinaSnap)))
	if err != nil {
		t.Fatalf("split sina fixture: %v", err)
	}
	return fmt.Sprintf("var hq_str_%s=%q;", sym, strings.Join(fn(strings.Split(payload, ",")), ","))
}

// ---------- symbol validation ----------

func TestQuoteSymbolRejectsWhatCannotGoInAURL(t *testing.T) {
	bad := []struct{ market, code string }{
		{"sh", "60189"},      // five digits
		{"sh", "6018991"},    // seven
		{"sh", "60189a"},     // not digits
		{"sh", ""},           // the thematic reports that legitimately carry no symbol
		{"sh", "601899\r\n"}, // header injection, which is why this check exists at all
		{"us", "601899"},     // digits are not a ticker
		{"us", ""},           //
		{"us", "AAPL.OQ"},    // the vendor's echo is not an input
		{"us", "ABCDEFG"},    // seven letters
		{"us", "AA PL"},      // a space reaches the URL as a space
		{"us", "AAPL\n"},     //
		{"hk", "0700"},       // four digits
		{"hk", "007000"},     // six
		{"hk", "0070a"},      //
		{"hk", "AAPL"},       //
		{"nyse", "AAPL"},     // a market nothing serves
		{"", "601899"},       //
		{"sh", "６０１８９９"},     // full-width digits: six runes, eighteen bytes
	}
	for _, c := range bad {
		if sym, err := quoteSymbol(c.market, c.code); err == nil {
			t.Errorf("quoteSymbol(%q, %q) = %q, want an error", c.market, c.code, sym)
		}
	}
	for _, c := range []struct{ market, code, want string }{
		{"sh", "601899", "sh601899"},
		{"sz", "000001", "sz000001"},
		{"sz", "300750", "sz300750"},
		{"bj", "830799", "bj830799"},
		{"hk", "00700", "hk00700"},
		{"us", "AAPL", "usAAPL"},
		// Lower case is canonicalised rather than refused, and the reason is not politeness: two
		// spellings of one symbol are two cache keys, two vendor calls and two code-echo
		// comparisons, and the vendor's own spelling is upper case.
		{"us", "aapl", "usAAPL"},
		{"us", "Tsla", "usTSLA"},
		// A six-digit code the OTHER exchange's prefix rule would claim. quoteSymbol checks the
		// SHAPE and leaves the market to the caller, because the explicit "sh:000001" form has to be
		// able to reach 上证指数 — whose code marketPrefix maps to Shenzhen. The byte rule is
		// unchanged: six digits or nothing, as the four entries above prove.
		{"sh", "000001", "sh000001"},
		{"sz", "601899", "sz601899"},
	} {
		got, err := quoteSymbol(c.market, c.code)
		if err != nil {
			t.Errorf("quoteSymbol(%q, %q): %v", c.market, c.code, err)
			continue
		}
		quoteEqStr(t, "symbol", got, c.want)
	}
}

// TestQuoteResolve is the market model's front door: what a user types, and what it is allowed to
// become. Everything here is either a market decided from a code's shape or the explicit form that
// overrides that decision.
func TestQuoteResolve(t *testing.T) {
	for _, c := range []struct{ in, market, code, sym string }{
		// Six digits keep meaning exactly what marketPrefix has always said they mean.
		{"601899", "sh", "601899", "sh601899"},
		{"000001", "sz", "000001", "sz000001"},
		{"300750", "sz", "300750", "sz300750"},
		{"830799", "bj", "830799", "bj830799"},
		{"920116", "bj", "920116", "bj920116"},
		// Five digits are Hong Kong, letters are the US, and case is not information.
		{"00700", "hk", "00700", "hk00700"},
		{"AAPL", "us", "AAPL", "usAAPL"},
		{"aapl", "us", "AAPL", "usAAPL"},
		{"  tsla  ", "us", "TSLA", "usTSLA"},
		{"F", "us", "F", "usF"},
		// The explicit form wins, and this is the case it exists for: 000001 is 平安银行 on
		// Shenzhen and 上证指数 on Shanghai, and no rule about the digits can tell them apart.
		{"sh:000001", "sh", "000001", "sh000001"},
		{"sz:399001", "sz", "399001", "sz399001"},
		{"SH:000300", "sh", "000300", "sh000300"},
		{"us:AAPL", "us", "AAPL", "usAAPL"},
		{"US:tsla", "us", "TSLA", "usTSLA"},
		{"hk:00700", "hk", "00700", "hk00700"},
		{" hk : 00700 ", "hk", "00700", "hk00700"},
	} {
		got, err := quoteResolve(c.in)
		if err != nil {
			t.Errorf("quoteResolve(%q): %v", c.in, err)
			continue
		}
		quoteEqStr(t, "market of "+c.in, got.Market.id, c.market)
		quoteEqStr(t, "code of "+c.in, got.Code, c.code)
		quoteEqStr(t, "symbol of "+c.in, got.Symbol, c.sym)
	}

	// The bare-digits path and the explicit path must disagree about 000001, or the explicit form is
	// decoration. This is the assertion that fails if quoteResolve ever "simplifies" by running the
	// shape rule over the explicit form too.
	bare, err := quoteResolve("000001")
	if err != nil {
		t.Fatalf("quoteResolve(000001): %v", err)
	}
	explicit, err := quoteResolve("sh:000001")
	if err != nil {
		t.Fatalf("quoteResolve(sh:000001): %v", err)
	}
	if bare.Symbol == explicit.Symbol {
		t.Fatalf("000001 and sh:000001 both resolved to %q; the explicit form decides nothing", bare.Symbol)
	}

	for _, bad := range []string{
		"", "   ", "6018991", "60189a", "1234567",
		"500001",    // six digits on no exchange's prefix
		"ABCDEFG",   // seven letters
		"BRK.B",     // a dot is not a letter, and the US echo check is built on the dot
		"aa pl",     //
		"紫金矿业",      // a name, not a code
		"nyse:AAPL", // a market nothing serves
		":601899",   //
		"sh:",       //
		"sh:00700",  // the right shape for the wrong market
		"hk:601899", //
		"us:0700",   //
		"sh:6018 99",
		"6018\r99", // whitespace INSIDE the code, which no trim can make safe
	} {
		if got, err := quoteResolve(bad); err == nil {
			t.Errorf("quoteResolve(%q) = %+v, want an error", bad, got)
		}
	}

	// Surrounding whitespace is trimmed and the result is then validated, and that ORDER is the
	// promise: what leaves this function is what gets concatenated into a vendor URL, and it carries
	// no newline whatever a browser sent. Callers key their cache on the code that comes BACK, never
	// on what was typed, or one stock becomes several entries. quoteSymbol itself does not trim — it
	// is the boundary, and a caller reaching it with an untrimmed code has gone around this function.
	for _, c := range []struct{ in, sym string }{
		{"601899\r\n", "sh601899"},
		{"\taapl\n", "usAAPL"},
		{"sh:601899\r\n", "sh601899"},
	} {
		got, err := quoteResolve(c.in)
		if err != nil {
			t.Errorf("quoteResolve(%q): %v", c.in, err)
			continue
		}
		quoteEqStr(t, fmt.Sprintf("symbol of %q", c.in), got.Symbol, c.sym)
	}
	if sym, err := quoteSymbol("sh", "601899\r\n"); err == nil {
		t.Errorf("quoteSymbol let a trailing CRLF through as %q", sym)
	}
}

// TestQuoteMarketsCoverEveryResolvableMarket keeps the table and the wire contract in step: every
// market a symbol can resolve to has to carry the four things the SPA is promised.
func TestQuoteMarketsCoverEveryResolvableMarket(t *testing.T) {
	want := map[string]string{"sh": "CNY", "sz": "CNY", "bj": "CNY", "hk": "HKD", "us": "USD"}
	if len(quoteMarkets) != len(want) {
		t.Fatalf("quoteMarkets has %d rows, the wire contract names %d", len(quoteMarkets), len(want))
	}
	for id, currency := range want {
		m := quoteTestMarket(t, id)
		quoteEqStr(t, id+" currency", m.currency, currency)
		if m.id != id {
			t.Errorf("quoteMarkets[%q].id is %q", id, m.id)
		}
	}
}

func TestQuoteBarCountClamps(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{0, quoteDefaultBars}, {-1, quoteDefaultBars}, {30, 30}, {quoteMaxBars + 1, quoteMaxBars},
	} {
		if got := quoteBarCount(c.in); got != c.want {
			t.Errorf("quoteBarCount(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ---------- the ceiling that keeps check 3's arithmetic honest ----------

// The int64 wrap that used to walk through all five checks untouched. The identity folds its two
// factors into one multiply by 10^6, so a difference that is a multiple of 2^58 分 turns the product
// into exactly 0 — which is what a vendor percentage of "0.00" says, exactly. The suspended Beijing
// fixture is where it does the most damage: check 5 skips because high and low are zero, and check 4
// sees no bars, so nothing else is left to notice.
func TestQuoteDriftPriceCeiling(t *testing.T) {
	// The wrap has to be REAL for the rest of this test to prove anything, and it is asserted at
	// runtime because a constant expression that overflows an int64 will not compile.
	last, prevClose := int64(288_230_376_151_711_844), int64(100)
	if product := (last - prevClose) * 1_000_000; product != 0 {
		t.Fatalf("(last-prevClose)*10^6 = %d; this case no longer wraps and the test proves nothing", product)
	}
	wrapping := QuoteSnapshot{Last: last, PrevClose: prevClose, ChangePct: "0.00"}
	if err := quoteCheckIdentity(&wrapping); !errors.Is(err, errQuotePriceBound) {
		t.Fatalf("the wrapping identity got %v, want %v", err, errQuotePriceBound)
	}

	t.Run("end to end on the suspended beijing fixture", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentBJ)
		arr := tencentQtArray(t, doc, "bj830799")
		arr[3] = "2882303761517118.44" // last, in 元
		arr[4] = "1.00"                // prevClose
		arr[32] = "0.00"               // the percentage the wrapped product agrees with perfectly
		setTencentQtArray(t, doc, "bj830799", arr)
		body := quoteRemarshal(t, doc)
		resp, err := parseTencentQuote("bj", "830799", body, 0)
		if err == nil {
			t.Fatalf("a last price of 2.88e15 元 parsed cleanly into %+v", resp.Snapshot)
		}
		if !errors.Is(err, errQuotePriceBound) {
			t.Fatalf("got %v, want %v", err, errQuotePriceBound)
		}
	})

	t.Run("a candle price past the ceiling", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		rows := tencentDayRows(t, doc, "sh601899")
		row := rows[0].([]any)
		row[1] = "99999999999.99" // an open of 10^13 分, ten times the ceiling
		rows[0] = row
		wantDrift(t, quoteRemarshal(t, doc), errQuotePriceBound)
	})

	t.Run("the ceiling refuses nothing a market could print", func(t *testing.T) {
		// It is a bound on the arithmetic, not an opinion about prices: a million 元 a share is
		// four hundred times the most expensive A-share ever listed, and it passes untouched.
		rich := QuoteSnapshot{
			Last: 100_000_000, PrevClose: 99_000_000, Open: 99_000_000,
			High: 100_000_000, Low: 99_000_000, Change: 1_000_000,
		}
		rich.ChangePct = quotePctString(quoteDivRound(rich.Change*10_000, rich.PrevClose))
		if err := quoteCheckSnapshotPrices(&rich); err != nil {
			t.Errorf("a price of 1,000,000 元 was refused: %v", err)
		}
		if err := quoteCheckIdentity(&rich); err != nil {
			t.Errorf("the identity refused a real percentage at that price: %v", err)
		}
		if err := quoteCheckBars([]QuoteBar{{Date: "2026-09-04", Open: 99_000_000, High: 100_000_000, Low: 99_000_000, Close: 100_000_000}}); err != nil {
			t.Errorf("a candle at that price was refused: %v", err)
		}
	})
}

// ---------- 成交量 ----------

// Tencent quotes 成交量 in 手 and the x100 that turns it into 股 sits OUTSIDE quoteScaled's overflow
// guard. Nothing in the drift gate looks at volume — check 4 checks four prices and nothing else —
// so an unguarded multiply puts a NEGATIVE 成交量 on the wire and a bar pointing the wrong way in the
// chart, with every gate check reporting the response as sound.
func TestQuoteVolumeIsNeverNegative(t *testing.T) {
	const overflowing = "9223372036854775799" // MaxInt64 - 8: a legal int64 that wraps when x100
	lots := int64(9_223_372_036_854_775_799)
	if wrapped := lots * 100; wrapped != -900 {
		t.Fatalf("%d 手 x 100 = %d, want the -900 wrap; this test would otherwise prove nothing", lots, wrapped)
	}

	t.Run("tencent snapshot volume", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		arr[6] = overflowing
		setTencentQtArray(t, doc, "sh601899", arr)
		wantDrift(t, quoteRemarshal(t, doc), errQuoteVolume)
	})

	t.Run("tencent bar volume", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		rows := tencentDayRows(t, doc, "sh601899")
		row := rows[0].([]any)
		row[5] = overflowing
		rows[0] = row
		wantDrift(t, quoteRemarshal(t, doc), errQuoteVolume)
	})

	t.Run("a negative share count on either vendor", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		arr[6] = "-1769341"
		setTencentQtArray(t, doc, "sh601899", arr)
		wantDrift(t, quoteRemarshal(t, doc), errQuoteVolume)

		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string { f[8] = "-176934067"; return f })
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteVolume) {
			t.Fatalf("sina snapshot volume: got %v, want %v", err, errQuoteVolume)
		}

		var rows []map[string]string
		if err := json.Unmarshal(readQuoteFixture(t, fixSinaKline), &rows); err != nil {
			t.Fatalf("decode sina fixture: %v", err)
		}
		rows[3]["volume"] = "-509853858"
		b, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("re-encode sina fixture: %v", err)
		}
		if _, err := parseSinaKLine(b); !errors.Is(err, errQuoteVolume) {
			t.Fatalf("sina history volume: got %v, want %v", err, errQuoteVolume)
		}
	})
}

// ---------- what actually guards the Sina path in check 3's place ----------

func TestQuoteSinaOrderingStandsInForTheIdentity(t *testing.T) {
	t.Run("the identity cannot fail on a derived percentage", func(t *testing.T) {
		// This is why the call was removed rather than left in as belt and braces. parseSinaSnapshot
		// DERIVES ChangePct from Last and PrevClose, so handing that snapshot to check 3 asks a
		// division whether it agrees with itself — and it does, for every input, including inputs
		// that are nothing like the stock they claim to describe.
		for _, c := range []struct{ prev, last int64 }{{3331, 3335}, {1000, 1300}, {950, 500}, {1, 999_999}} {
			snap := QuoteSnapshot{PrevClose: c.prev, Last: c.last}
			snap.Change = snap.Last - snap.PrevClose
			snap.ChangePct = quotePctString(quoteDivRound(snap.Change*10_000, snap.PrevClose))
			if err := quoteCheckIdentity(&snap); err != nil {
				t.Fatalf("a derived percentage disagreed with the prices it came from (%+v): %v", snap, err)
			}
		}
	})

	t.Run("a single inserted field", func(t *testing.T) {
		// The mutation the replacement exists for. Sina's line is name, open, prevClose, last,
		// high, low, so one plausible price inserted at index 1 shifts all five: low reads the old
		// high (34.05) and high reads the old last (33.35), and the day's low ends up ABOVE its
		// high. Checks 1 and 2 are satisfied — the line got longer, not shorter, and the variable
		// name still echoes the symbol — which is exactly the shape a vendor schema change has.
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string {
			return append([]string{f[0], "33.360"}, f[1:]...)
		})
		key, payload, err := quoteSinaLine(line)
		if err != nil {
			t.Fatalf("split the mutated line: %v", err)
		}
		if n := len(strings.Split(payload, ",")); n < quoteSinaSnapFields {
			t.Fatalf("the mutated line has %d fields; check 1 would catch it and this test would prove nothing", n)
		}
		if err := quoteCheckCodeEcho("sh601899", key); err != nil {
			t.Fatalf("check 2 rejected the mutated line (%v); the ordering check must be what catches it", err)
		}
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteOrdering) {
			t.Fatalf("got %v, want %v", err, errQuoteOrdering)
		}
	})

	t.Run("an open outside the day's range, which check 5 cannot see", func(t *testing.T) {
		// Check 5 only ever looks at last, so a shifted open is invisible to it: an open of 30.00
		// sits below the 33.07 low while the 33.35 last stays comfortably inside 33.07..34.05.
		// This is the case that proves the ordering check does work of its own.
		if err := quoteCheckRange(&QuoteSnapshot{Last: 3335, High: 3405, Low: 3307}); err != nil {
			t.Fatalf("check 5 rejects this shape (%v); it must be the ordering check that catches it", err)
		}
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string { f[1] = "30.000"; return f })
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteOrdering) {
			t.Fatalf("got %v, want %v", err, errQuoteOrdering)
		}
	})

	t.Run("a prevClose of zero beside a real range", func(t *testing.T) {
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string { f[2] = "0.000"; return f })
		if _, err := parseSinaSnapshot("sh", "601899", line); !errors.Is(err, errQuoteOrdering) {
			t.Fatalf("got %v, want %v", err, errQuoteOrdering)
		}
	})

	t.Run("a suspended stock is skipped, exactly as check 5 skips it", func(t *testing.T) {
		// high=0 and low=0 beside a real last price is what a suspended stock reports, and a check
		// that did not skip would report every suspended stock in the market as a source failure.
		line := sinaSnapshotWith(t, "sh601899", func(f []string) []string {
			f[4], f[5] = "0.000", "0.000"
			return f
		})
		resp, err := parseSinaSnapshot("sh", "601899", line)
		if err != nil {
			t.Fatalf("a suspended sina snapshot was rejected: %v", err)
		}
		quoteEqInt(t, "last", resp.Snapshot.Last, 3335)
	})
}
