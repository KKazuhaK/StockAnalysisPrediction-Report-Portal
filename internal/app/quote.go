package app

// quote.go —— the quote feature's vendor parsers and its drift gate, for five markets.
//
// Two vendors answer the same question in two different shapes. Tencent packs history, snapshot and
// market-session state into a single JSON call; Sina needs two calls and answers one of them as a
// GBK assignment statement. What they have in common is that both are POSITIONAL: the numbers
// arrive as a bare array (Tencent) or a comma-separated line (Sina) with no field names anywhere,
// so a vendor that inserts one column silently renames every field after it — open becomes close,
// close becomes high, and the chart that comes out is wrong in a way that still looks like a chart.
// That is what the drift gate at the bottom of this file exists to catch, and it is why the parsing
// lives here rather than inside the handler.
//
// The SAME Tencent endpoint answers Shanghai, Shenzhen, Beijing, Hong Kong and the US, and answering
// them in one array is exactly what makes it dangerous: the array has the same LENGTH-ish shape and
// the same field POSITIONS everywhere, while three of the things read out of those positions mean
// different things per market — the timestamp's layout and zone, the unit of 成交量, and the unit of
// 成交额. None of the three is a parse error when it is got wrong; each one is a number that renders
// perfectly and is off by a factor of a hundred or by half a day. quoteMarkets below is the one
// place those differences live, and every one of its rows was measured against the captured bodies
// in testdata/quote/ rather than assumed from the others.
//
// Every price in here is an integer 分 (fen) — cents of whatever currency the market quotes, per
// ADR 0028 §10. This feature feeds a schema with 222 TEXT, 61 INTEGER and 24 BIGINT columns and not
// one REAL, DOUBLE, FLOAT or NUMERIC, and a price that has been through a float64 does not reliably
// come back: in float64, 0.29 * 100 is 28.999999999999996, and converting that to int64 truncates
// towards zero and yields 28 分 for a price of 0.29 元. So no vendor decimal is ever handed to
// strconv.ParseFloat — quoteScaled does the whole conversion in integer arithmetic, and
// quote_test.go pins that exact case.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	// time/tzdata is imported for its side effect: it embeds the IANA zone database in the binary.
	// America/New_York is the reason. It is -04:00 in September and -05:00 in January, so unlike
	// +08:00 it cannot be a time.FixedZone, and reading it means time.LoadLocation, which without
	// this import reads /usr/share/zoneinfo off the filesystem. The release image installs tzdata
	// (Dockerfile.release), but a parser must not depend on the image it happens to be running in:
	// a scratch or distroless build, or a `go test` on a stripped host, turns that read into an
	// error — and the only fallback the standard library offers is time.Local, the server's own
	// zone, which is precisely the substitution QuoteSnapshot.AsOf exists to prevent. It is stdlib,
	// so it costs go.mod nothing and the binary about 450 KB.
	_ "time/tzdata"
)

// ---------- the wire contract (mirrored in web/src/api/types.ts) ----------

// QuoteBar is one daily candle. Prices are 分; Volume is 股 (shares) everywhere, which on the
// Chinese exchanges means Tencent's 手 have already been multiplied by 100 on the way in — and on
// Hong Kong and the US means they have NOT, because those two count shares already. See
// quoteMarket.lots.
type QuoteBar struct {
	Date   string `json:"d"`
	Open   int64  `json:"o"`
	High   int64  `json:"h"`
	Low    int64  `json:"l"`
	Close  int64  `json:"c"`
	Volume int64  `json:"v"`
}

// QuoteSnapshot is the live line at the top of the stock page.
type QuoteSnapshot struct {
	Last      int64 `json:"last"`
	PrevClose int64 `json:"prevClose"`
	Open      int64 `json:"open"`
	High      int64 `json:"high"`
	Low       int64 `json:"low"`
	Change    int64 `json:"change"`
	// ChangePct is the VENDOR'S own display string, passed through verbatim and never recomputed
	// from Last and PrevClose. On an ex-rights day 昨收 is the unadjusted previous close while the
	// vendor's own percentage is computed against the adjusted one, so recomputing it manufactures a
	// double-digit crash that the vendor's own page does not show. Change above is read from the
	// vendor's own 涨跌 column for the same reason, so that the number and the percentage rendered
	// beside it can never contradict each other. Sina publishes neither, which is the one place this
	// promise cannot be kept — see parseSinaSnapshot.
	ChangePct string `json:"changePct"`
	// Volume is 股 (shares) whatever unit the vendor quoted, and Amount is a whole unit of the
	// market's own currency — 元, HKD or USD. Neither unit is the same across markets on the wire we
	// read: Tencent sends 成交量 in 手 on the three Chinese exchanges and in shares on Hong Kong and
	// the US, and 成交额 in 万元 on the Chinese exchanges and in whole currency on the other two.
	Volume int64 `json:"volume"`
	Amount int64 `json:"amount"`
	// AsOf is RFC3339 in the MARKET's own zone, taken from the VENDOR's clock and never from this
	// server's. A feed that has stopped updating is only visible as stale if the timestamp shown is
	// the feed's own, and a New York close stamped +08:00 is half a day out — see quoteVendorTime.
	AsOf    string `json:"asOf"`
	Session string `json:"session"`
}

// QuoteResp is one stock's quote as the SPA receives it.
type QuoteResp struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Market string `json:"market"`
	// Kind is what the VENDOR calls this instrument, "stock" or "index" — never inferred from the
	// code, because the same six digits are an index on one exchange and a company on the other
	// (000001 is 上证指数 on Shanghai and 平安银行 on Shenzhen). See quoteMarket.kindAt.
	Kind string `json:"kind"`
	// Currency and TZ exist so that the strip cannot print a bare number: 319.97 beside a Chinese
	// report is a yuan price to every reader who is not told otherwise, and an instant with no zone
	// is a time the browser will happily render in its own.
	Currency        string        `json:"currency"`
	TZ              string        `json:"tz"`
	Source          string        `json:"source"`
	Snapshot        QuoteSnapshot `json:"snapshot"`
	Bars            []QuoteBar    `json:"bars"`
	BarsSource      string        `json:"barsSource"`
	BarsUnavailable string        `json:"barsUnavailable"`
	Adjusted        bool          `json:"adjusted"`
	Cached          bool          `json:"cached"`
}

const (
	quoteSourceTencent = "tencent"
	quoteSourceSina    = "sina"

	// quoteBarsMarketUnsupported is a market whose daily series does not exist rather than one that
	// failed today: Beijing, where Tencent answers "day":[] and Sina's series for the same code is
	// over a year stale, and the US, where the same endpoint answers a sixty-bar request with two
	// rows fifteen years apart. Both are worse than empty because they look like data, and the UI
	// has to say so out loud — an empty chart rendered as a flat one is a lie.
	quoteBarsMarketUnsupported = "market_unsupported"
	quoteBarsSourceFailed      = "source_failed"

	quoteSessionOpen    = "open"
	quoteSessionClose   = "close"
	quoteSessionUnknown = "unknown"

	quoteKindStock = "stock"
	quoteKindIndex = "index"
)

// ---------- the market model ----------
//
// Everything below is a MEASURED difference between the five markets one Tencent endpoint serves.
// Each row of quoteMarkets was read off the captured bodies in testdata/quote/ on 2026-09-07, and
// each column is here because getting it wrong produces a plausible wrong number rather than an
// error: a US volume multiplied by a hundred is four billion Apple shares a day, a Hong Kong
// 成交额 read as 万 is ten thousand times the money that changed hands, and a New York close
// stamped +08:00 is an instant half a day from the one the vendor meant.

const (
	// The three timestamp layouts qt[30] arrives in. They are not interchangeable and they are not
	// guessable from the string: "2026-09-04 16:00:01" and "2026/09/07 14:41:33" differ only in a
	// separator, and time.Parse is happy to be told the wrong one about the right instant.
	quoteStampCN = "20060102150405"      // sh/sz/bj: 20260907145703
	quoteStampHK = "2006/01/02 15:04:05" // hk:       2026/09/07 14:57:39
	quoteStampUS = "2006-01-02 15:04:05" // us:       2026-09-04 16:00:01

	// quoteVendorKindIndex is the vendor's own word for 指数, in the instrument-type field that
	// quoteMarket.kindAt points at. Everything else seen there — "GP" 股票, "GP-A" A股, "ETF" — is
	// something a person can hold, so it is a stock as far as this contract's two values go.
	quoteVendorKindIndex = "ZS"

	// quoteUSMaxLetters bounds a US ticker so that an arbitrarily long string cannot reach a vendor
	// URL. Listed tickers run one to five letters (F, AAPL, GOOGL); six leaves a slot spare without
	// letting the field become free text.
	quoteUSMaxLetters = 6
)

// quoteCodeShape is what a code may look like in one market. It is the whole of the URL-construction
// boundary for the two markets marketPrefix knows nothing about: an hk or us code never passes
// through marketPrefix, so THIS is what stands between a browser's input and http.NewRequest for
// them — the same job, on the same grounds, as the all-digits check in marketPrefix itself.
type quoteCodeShape uint8

const (
	quoteCodeDigits6 quoteCodeShape = iota // 601899, 000001, 830799
	quoteCodeDigits5                       // 00700
	quoteCodeLetters                       // AAPL
)

// quoteMarket is everything about one market that the parsers below must not assume from another.
type quoteMarket struct {
	id       string // "sh" | "sz" | "bj" | "hk" | "us", the value QuoteResp.Market carries
	currency string // ISO 4217, so the strip never prints a bare number
	// zone is an IANA NAME, deliberately not an offset: America/New_York is -04:00 in September and
	// -05:00 in January, and a fixed offset would be right for half the year.
	zone string
	// stamp is the layout qt[30] arrives in for this market.
	stamp string
	// session is the prefix of this market's segment inside qt.market, or "" for a market with no
	// segment of its own. Beijing has none and keeps Shenzhen's hours, so bj follows SZ_.
	session string
	// lots is true where 成交量 — qt[6] and a day row's last column — is 手 and needs the x100 that
	// turns it into 股. It is FALSE for Hong Kong and the US, which count shares already. This is
	// the flag whose default would report about four billion Apple shares traded in a day.
	lots bool
	// amountScale is how many decimal places 成交额 (qt[37]) is read at to become a whole unit of
	// currency: 4 on the Chinese exchanges, where the vendor quotes 万元, and 0 on Hong Kong and the
	// US, where it already sends the whole thing.
	amountScale int
	// kindAt is the index of the vendor's own instrument-type code — "ZS" 指数, "GP"/"GP-A" 股票,
	// "ETF". It is NOT qt[0] and it is NOT a list of index codes, and both of those need saying.
	//
	// A LIST rots. New indices are published continuously, and the same six digits are an index on
	// one exchange and a company on the other: 000001 is 上证指数 on Shanghai and 平安银行 on
	// Shenzhen. A list written today is wrong the first time an index is listed, silently, in the
	// direction of labelling an index a stock.
	//
	// qt[0] cannot answer it either, which is worth recording because it looks like it should.
	// Measured on the committed fixtures: 上证指数 (sh000001) and 紫金矿业 (sh601899) BOTH send
	// qt[0] = "1", and 平安银行 (sz000001) sends "51" like 深证成指 does. The field is the EXCHANGE
	// — 1 Shanghai, 51 Shenzhen, 62 Beijing, 100 Hong Kong — not the instrument. And on the fqkline
	// body this file actually parses, the US response puts the literal string "delay" there, where
	// the qt.gtimg.cn snapshot endpoint puts "200".
	//
	// The index differs per market because the arrays differ, and all three sit far past
	// quoteTencentQtFields: a response long enough for the prices but short of this field is read
	// as a stock rather than refused, because the kind is a label beside a price and never a price.
	kindAt int
	// history is whether this market's day array is a series worth drawing. Two markets fail it and
	// for the same reason — what arrives is not data, it is something that renders like data:
	// Beijing answers "day":[] (ADR 0028 §7), and the US answers TWO rows fifteen years apart.
	history bool
	// sina is whether the fallback's A-share snapshot line covers this market. It does not cover
	// Hong Kong or the US: those are `rt_hk00700` and `gb_aapl` there, in lines of 19 and 36 fields
	// whose columns are in a different order entirely. Pointing the A-share parser at either would
	// not be a second source, it would be a second shape read as the first.
	sina bool
	// shape is what a code may look like here; dottedEcho is how the vendor echoes it back.
	shape      quoteCodeShape
	dottedEcho bool
}

// quoteMarkets is the table. Adding a market means measuring every column of a row against a real
// captured body — not copying the row above and changing the id.
var quoteMarkets = map[string]*quoteMarket{
	"sh": {
		id: "sh", currency: "CNY", zone: "Asia/Shanghai", stamp: quoteStampCN, session: "SH_",
		lots: true, amountScale: 4, kindAt: 61, history: true, sina: true, shape: quoteCodeDigits6,
	},
	"sz": {
		id: "sz", currency: "CNY", zone: "Asia/Shanghai", stamp: quoteStampCN, session: "SZ_",
		lots: true, amountScale: 4, kindAt: 61, history: true, sina: true, shape: quoteCodeDigits6,
	},
	"bj": {
		id: "bj", currency: "CNY", zone: "Asia/Shanghai", stamp: quoteStampCN, session: "SZ_",
		lots: true, amountScale: 4, kindAt: 61, history: false, sina: true, shape: quoteCodeDigits6,
	},
	// Hong Kong keeps its own IANA zone rather than borrowing Shanghai's. The two have agreed on
	// +08:00 since 1979, so every instant this parser produces is identical either way — what
	// differs is the name QuoteResp.TZ hands the browser, and Asia/Hong_Kong is the zone a Hong
	// Kong close is actually in.
	"hk": {
		id: "hk", currency: "HKD", zone: "Asia/Hong_Kong", stamp: quoteStampHK, session: "HK_",
		lots: false, amountScale: 0, kindAt: 63, history: true, sina: false, shape: quoteCodeDigits5,
	},
	"us": {
		id: "us", currency: "USD", zone: "America/New_York", stamp: quoteStampUS, session: "US_",
		lots: false, amountScale: 0, kindAt: 56, history: false, sina: false,
		shape: quoteCodeLetters, dottedEcho: true,
	},
}

// quoteTarget is a symbol that has been resolved: a market, the code in the one form that may reach
// a vendor URL, and the vendor symbol built from the two.
//
// There is deliberately no Kind here. It cannot be known at resolve time — the code's shape does not
// carry it (000001 again) and neither does the exchange the code belongs to — so the only honest
// place to decide it is where the vendor's own answer is in hand, in parseTencentQuote.
type quoteTarget struct {
	Market *quoteMarket
	Code   string // 601899, 00700, AAPL — canonical, and what the vendor echoes back
	Symbol string // sh601899, hk00700, usAAPL — what goes into the URL
}

// quoteResolve turns what a user typed into a target, and it is the only function that decides a
// market from a code's shape.
//
// The explicit "<market>:<code>" form always wins, and it is not sugar: it is the only way to ask
// for the codes whose shape lies. 000001 is a Shenzhen company AND a Shanghai index, and the bare
// six digits have to keep meaning what they have always meant here — marketPrefix's answer, the
// same one the name fetch and every other vendor call in this tree uses — so 上证指数 is reachable
// only as "sh:000001".
//
// Surrounding whitespace is trimmed and the result is THEN validated, which is the order that
// matters: a code pasted with a trailing newline resolves, and what leaves here is six digits with
// no newline in them. Callers must key caches and responses on the Code that comes back rather than
// on what the user typed, or one stock arrives as several.
func quoteResolve(input string) (quoteTarget, error) {
	s := strings.TrimSpace(input)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return quoteTargetFor(strings.ToLower(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:]))
	}
	switch {
	case len(s) == 6 && quoteAllDigits(s):
		market := marketPrefix(s)
		if market == "" {
			return quoteTarget{}, fmt.Errorf("quote: %q is not a code on any exchange this portal knows", input)
		}
		return quoteTargetFor(market, s)
	case len(s) == 5 && quoteAllDigits(s):
		return quoteTargetFor("hk", s)
	case len(s) > 0 && quoteAllLetters(s):
		return quoteTargetFor("us", s)
	}
	return quoteTarget{}, fmt.Errorf("quote: %q is not a symbol this portal can resolve", input)
}

// quoteTargetFor resolves an already-split market and code. Whatever survives here is what gets
// concatenated into a vendor URL, so it validates every byte rather than trusting its caller: this
// symbol cannot go through url.Values (the param= syntax needs literal commas), and a six-byte
// symbol with a NUL in it is what once reached http.NewRequest and took the process down on the nil
// request it returned.
func quoteTargetFor(market, code string) (quoteTarget, error) {
	m, ok := quoteMarkets[market]
	if !ok {
		return quoteTarget{}, fmt.Errorf("quote: %q is not a market this portal serves", market)
	}
	canon, err := m.canonicalCode(code)
	if err != nil {
		return quoteTarget{}, err
	}
	return quoteTarget{Market: m, Code: canon, Symbol: m.id + canon}, nil
}

// canonicalCode checks a code against this market's shape and returns the ONE spelling of it that
// may be used from here on. The single spelling matters beyond tidiness: two spellings of one
// symbol are two cache keys, two vendor calls and two code-echo comparisons.
func (m *quoteMarket) canonicalCode(code string) (string, error) {
	switch m.shape {
	case quoteCodeDigits6:
		// Six ASCII digits, and NOT "marketPrefix(code) == m.id". The prefix function stays the
		// rule for INFERRING a market from bare digits, and stays A-share only — but it maps 000001
		// to Shenzhen, and sh000001 (上证指数) is a real vendor symbol that the explicit form has to
		// be able to reach. What must not be relaxed is the byte check, and it is not: six digits or
		// nothing.
		if len(code) != 6 || !quoteAllDigits(code) {
			return "", fmt.Errorf("quote: %q is not the six digits the %q market is keyed by", code, m.id)
		}
		return code, nil
	case quoteCodeDigits5:
		if len(code) != 5 || !quoteAllDigits(code) {
			return "", fmt.Errorf("quote: %q is not the five digits the %q market is keyed by", code, m.id)
		}
		return code, nil
	case quoteCodeLetters:
		if len(code) == 0 || len(code) > quoteUSMaxLetters || !quoteAllLetters(code) {
			return "", fmt.Errorf("quote: %q is not a 1-%d letter ticker", code, quoteUSMaxLetters)
		}
		// Upper case is the vendor's own spelling and the one the URL carries.
		return strings.ToUpper(code), nil
	}
	return "", fmt.Errorf("quote: market %q has no code shape", m.id)
}

// kindOf asks the VENDOR what this instrument is. A field that is not there is answered "stock"
// rather than guessed at or turned into a failure — see quoteMarket.kindAt.
func (m *quoteMarket) kindOf(qt []string) string {
	if m.kindAt <= 0 || m.kindAt >= len(qt) {
		return quoteKindStock
	}
	if strings.TrimSpace(qt[m.kindAt]) == quoteVendorKindIndex {
		return quoteKindIndex
	}
	return quoteKindStock
}

// quoteMarketHasDailyHistory reports whether this market's daily series is worth drawing. It is what
// the handler asks before serving bars at all: the parser reports what the vendor sent, and the
// promise that an untrustworthy market NEVER carries bars belongs one level up, where it holds
// whatever a vendor starts returning tomorrow.
func quoteMarketHasDailyHistory(market string) bool {
	m, ok := quoteMarkets[market]
	return ok && m.history
}

func quoteAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// quoteAllLetters is ASCII-only on purpose: the letters go straight into a URL, and a rune-aware
// check would accept a Cyrillic А that is not the A the vendor knows.
func quoteAllLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return len(s) > 0
}

// ---------- decimal string -> integer, without float64 ----------

// quoteFen parses a vendor decimal such as "33.850" into 分. It is the single conversion every price
// in this file goes through; see the float64 note in the file comment for why it may not be one line
// of strconv.ParseFloat.
func quoteFen(s string) (int64, error) { return quoteScaled(s, 2) }

// quoteScaled parses a decimal string into an integer scaled by 10^scale, rounding half away from
// zero. It is the generalisation quoteFen needs anyway: Tencent quotes 成交额 in 万元 and Sina quotes
// it in 元, and scale turns both into the same integer 元 with no unit maths at the call site.
//
// An empty string is 0 and not an error, because that is what a vendor sends for a field that does
// not apply to a suspended stock; a string that is not a number at all IS an error, because that is
// what an interstitial page looks like once it has been split on commas.
func quoteScaled(s string, scale int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" && frac == "" {
		return 0, fmt.Errorf("quote: %q is not a number", s)
	}
	// maxBeforeDigit is the largest value that can still absorb one more digit. A vendor field long
	// enough to pass it is an interstitial page, not a price, and letting it wrap would turn a
	// garbage response into a plausible negative one instead of into an error.
	const maxBeforeDigit = (math.MaxInt64 - 9) / 10
	var v int64
	digits := func(part string) error {
		for i := 0; i < len(part); i++ {
			d := part[i]
			if d < '0' || d > '9' {
				return fmt.Errorf("quote: %q is not a number", s)
			}
			if v > maxBeforeDigit {
				return fmt.Errorf("quote: %q does not fit in an int64", s)
			}
			v = v*10 + int64(d-'0')
		}
		return nil
	}
	if err := digits(whole); err != nil {
		return 0, err
	}
	keep := frac
	if len(keep) > scale {
		keep = frac[:scale]
	}
	if err := digits(keep); err != nil {
		return 0, err
	}
	// Padding a short fraction out to the scale is the same multiply and needs the same guard;
	// leaving it out is how "922337203685477580" would become a negative price.
	for i := len(keep); i < scale; i++ {
		if v > maxBeforeDigit {
			return 0, fmt.Errorf("quote: %q does not fit in an int64", s)
		}
		v *= 10
	}
	if len(frac) > scale {
		// The digits past the scale still have to BE digits — "1.0a" is a broken feed, not 1.00 —
		// so they are validated even though only the first of them can change the result.
		for i := scale; i < len(frac); i++ {
			if frac[i] < '0' || frac[i] > '9' {
				return 0, fmt.Errorf("quote: %q is not a number", s)
			}
		}
		// Half away from zero: the sign is reapplied below, so rounding up here rounds away from
		// zero for a negative input too. The guard above leaves at least eight of headroom, so this
		// increment cannot be the thing that overflows.
		if frac[scale] >= '5' {
			v++
		}
	}
	if neg {
		v = -v
	}
	return v, nil
}

// quoteDivRound divides rounding half away from zero, so that a percentage derived here rounds the
// way the vendors' own display strings do rather than towards zero.
func quoteDivRound(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	neg := (num < 0) != (den < 0)
	if num < 0 {
		num = -num
	}
	if den < 0 {
		den = -den
	}
	q := (num + den/2) / den
	if neg {
		return -q
	}
	return q
}

// quotePctString renders a percentage held as an integer number of 0.01 percent — the same two
// decimals the vendors' own display strings carry — for the one source that sends no percentage.
func quotePctString(pct int64) string {
	sign := ""
	if pct < 0 {
		sign, pct = "-", -pct
	}
	return fmt.Sprintf("%s%d.%02d", sign, pct/100, pct%100)
}

// ---------- the vendor's clock ----------

// quoteZones caches the loaded zones. LoadLocation re-reads and re-parses the zone file on every
// call, and the same three or four names are asked for on every cache miss in the process's life.
var (
	quoteZonesMu sync.Mutex
	quoteZones   = map[string]*time.Location{}
)

// quoteZone loads one market's IANA zone.
//
// The error is RETURNED and never swallowed, which is the whole point of this function existing
// instead of a time.LoadLocation call at each site. The tempting fallback — time.Local — is the
// server's own zone, and using it would relabel a vendor's instant with an operator's deployment
// choice: the same body would produce a different AsOf in Frankfurt than in Singapore, and a US
// close would be stamped as a Chinese afternoon. A quote whose zone cannot be established is a
// quote this portal declines to serve, which is the same trade the drift gate takes everywhere.
func quoteZone(name string) (*time.Location, error) {
	quoteZonesMu.Lock()
	defer quoteZonesMu.Unlock()
	if loc := quoteZones[name]; loc != nil {
		return loc, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("quote: market zone %q: %w", name, err)
	}
	quoteZones[name] = loc
	return loc, nil
}

// quoteVendorTime turns one market's own timestamp — Tencent sends three different layouts in the
// same array position — into RFC3339 in that market's own zone.
//
// The zone is the market's and not a constant, because the difference is half a day rather than a
// formatting detail. "2026-09-04 16:00:01" from the US feed is a New York close: stamped in
// America/New_York it is 20:00:01Z, and stamped +08:00 it is 08:00:01Z, twelve hours earlier in
// September and thirteen in January. The reader sees a quote line that claims to be from the middle
// of a Chinese working day, and the direction of the error changes twice a year with US daylight
// saving — which is also why this market cannot be a fixed offset.
func quoteVendorTime(m *quoteMarket, stamp string) (string, error) {
	loc, err := quoteZone(m.zone)
	if err != nil {
		return "", err
	}
	stamp = strings.TrimSpace(stamp)
	t, err := time.ParseInLocation(m.stamp, stamp, loc)
	if err != nil {
		return "", fmt.Errorf("quote: %s vendor timestamp %q: %w", m.id, stamp, err)
	}
	return t.Format(time.RFC3339), nil
}

// quoteSinaTime is quoteVendorTime for Sina, which splits the same instant across two fields
// ("2026-09-04" and "15:34:59") instead of sending one. It reassembles them into the layout the
// Chinese exchanges use, which is the only shape it is ever asked for: the fallback serves sh, sz
// and bj and no other market (quoteMarket.sina).
func quoteSinaTime(m *quoteMarket, date, clock string) (string, error) {
	date = strings.ReplaceAll(strings.TrimSpace(date), "-", "")
	clock = strings.ReplaceAll(strings.TrimSpace(clock), ":", "")
	return quoteVendorTime(m, date+clock)
}

// ---------- market session ----------

// quoteMarketSession pulls this market's segment out of Tencent's session string, which carries
// every board the vendor knows in one pipe-delimited line:
//
//	2026-09-07 14:51:13|HK_open_交易中|SH_open_交易中|SZ_open_交易中|US_close_劳动节休市|…
//	  …|NEWSH_open_交易中|NEWSZ_open_交易中|NEWHK_open_交易中|NEWUS_close_劳动节休市|USA_close_…|…
//
// The segment is matched on its PREFIX, never with strings.Contains, and the second line above is
// why: "SZ_" is a substring of "NEWSZ_" and "HSZB_", and "US_" is a substring of "NEWUS_". A
// Contains match latches onto whichever of those comes first in the line and then, because the
// state is sliced off at the prefix's own length, reads it from the wrong offset — so a market that
// is trading is reported as "unknown", which the cache turns into the five-minute closed-market TTL
// on a market that is open. The Beijing exchange has no segment of its own and keeps Shenzhen's
// hours, so bj follows SZ_ (see quoteMarket.session).
func quoteMarketSession(m *quoteMarket, raw string) string {
	if m == nil || m.session == "" {
		return quoteSessionUnknown
	}
	want := m.session
	for _, seg := range strings.Split(raw, "|") {
		if !strings.HasPrefix(seg, want) {
			continue
		}
		state := seg[len(want):]
		if i := strings.IndexByte(state, '_'); i >= 0 {
			state = state[:i]
		}
		switch state {
		case quoteSessionOpen:
			return quoteSessionOpen
		case quoteSessionClose:
			return quoteSessionClose
		}
		return quoteSessionUnknown
	}
	return quoteSessionUnknown
}

// ---------- the drift gate ----------
//
// A vendor that inserts one column into a positional response breaks every field after it while
// every individual value stays perfectly plausible, so a range check alone catches nothing. These
// five checks run in this order, and each of them is here because one of them alone is not enough:
//
//  1. field count — enough columns to reach the indices we read.
//  2. code echo   — the vendor answered about the stock we asked about.
//  3. arithmetic identity — the vendor's own percentage agrees with its own last and prevClose.
//     This is the assertion a single inserted column actually fails.
//  4. bar sanity  — a candle's four prices are ordered like a candle's four prices.
//  5. range       — the last price is inside the day's range.
//
// Check 3 has a precondition of its own, quotePriceCeiling, and the ceiling is not a judgement
// about what a share may cost: the identity folds its two factors into a single multiply by 10^6
// and an int64 wraps at about 9.2e18, so a vendor-controlled price large enough to wrap it lands
// the product on zero, where it agrees with a vendor "0.00" perfectly. Bounding the prices before
// the multiply is what makes the multiply mean what it says.
//
// Check 3 runs on the TENCENT path only. Sina publishes neither the change nor the percentage, so
// the two this file shows for it are derived here from last and prevClose — which leaves check 3
// comparing that derivation against the very numbers it came from, where it can only ever agree.
// quoteCheckSnapshotOrder stands in its place there and asks a question about Sina's own columns
// instead; see parseSinaSnapshot.
//
// There is deliberately NO band on the size of the change. A-share daily limits are ±5, ±10, ±20 or
// ±30 percent depending on the board, and a first-day listing has no limit at all, so any band wide
// enough not to reject a real limit-up day is too wide to catch anything — and a narrow one reports
// the most newsworthy days of the year as a source failure. The four markets make this worse rather
// than better: Hong Kong and the US have no daily limit whatsoever, so there is not even a number a
// band could be argued down to. Do not add one.

const (
	// Tencent's qt array is 88 fields for sh/sz, 87 for bj, 78 for hk and 71 for us — four captured
	// responses prove it — so the gate asserts a FLOOR, never a fixed total. An equality against 88
	// would have taken the whole Beijing exchange offline; against anything above 71 it takes the
	// US offline. ONE floor, not a table: every market reads the same indices for the fields that
	// matter (3 last, 4 prevClose, 5 open, 6 volume, 30 timestamp, 31 change, 32 percentage,
	// 33 high, 34 low, 37 amount), and the shortest of the four still carries 71 of them. A
	// per-market floor would be four numbers to keep true where one is enough, and it would let a
	// market quietly fall below the indices this parser actually reads. The instrument-type field
	// at 56/61/63 is the one read that sits past this floor, and it is read defensively for exactly
	// that reason — see quoteMarket.kindAt.
	quoteTencentQtFields = 39
	quoteSinaSnapFields  = 32
	quoteDayRowFields    = 6

	// quotePctScale holds percentages as integers of 10^-4 percent: the vendor sends 2 decimals, so
	// two spare digits keep the comparison below from being decided by its own rounding.
	quotePctScale = 4
	// quoteIdentityTolerance is 0.02 percentage points at quotePctScale. The slack is for the
	// vendor's own rounding of a 2-decimal string, not for disagreement.
	quoteIdentityTolerance = 200

	// quotePriceCeiling is the largest 分 any price field may carry: 10^12 分 is ten billion 元,
	// which is over a million times the most expensive share ever listed on a Chinese exchange and
	// so refuses nothing real. Its job is NOT to judge a price — it is to keep quoteCheckIdentity's multiply
	// inside an int64, because a price big enough to wrap that multiply turns the one check the
	// whole design rests on into a check that accepts anything. A difference that is a multiple of
	// 2^58 分 wraps (last-prevClose)*10^6 to exactly zero, which is precisely what a vendor "0.00"
	// says, so without this bound a last price of 2.88e15 元 beside a prevClose of 1 元 passes all
	// five checks.
	quotePriceCeiling = 1_000_000_000_000

	// quoteMaxLots is the largest 手 count that can still become 股. Tencent quotes volume in 手 and
	// the x100 that turns it into 股 sits OUTSIDE quoteScaled's own overflow guard, so a field of
	// "9223372036854775799" — a legal int64 — becomes -900 股 on the wire and a negative bar in the
	// chart. Nothing in the drift gate looks at volume, so this is the only thing standing there.
	quoteMaxLots = math.MaxInt64 / 100
)

var (
	errQuoteFieldCount = errors.New("quote: vendor sent fewer fields than the contract requires")
	errQuoteCodeEcho   = errors.New("quote: vendor echoed a different symbol than the one requested")
	errQuoteIdentity   = errors.New("quote: vendor percentage disagrees with its own last and prevClose")
	errQuoteBarSanity  = errors.New("quote: candle prices are not ordered like a candle")
	errQuoteRange      = errors.New("quote: last price is outside the day's low..high")
	errQuotePriceBound = errors.New("quote: price is beyond the ceiling the arithmetic checks need")
	errQuoteOrdering   = errors.New("quote: snapshot prices are not ordered like one day's prices")
	errQuoteVolume     = errors.New("quote: share count is not a share count")
	errQuoteAmount     = errors.New("quote: turnover is not a turnover")
)

// quoteCheckFieldCount is drift-gate check 1.
func quoteCheckFieldCount(what string, got, need int) error {
	if got < need {
		return fmt.Errorf("%w: %s has %d fields, need %d", errQuoteFieldCount, what, got, need)
	}
	return nil
}

// quoteCheckCodeEcho is drift-gate check 2. It compares the echoed code for EXACT equality, and it
// takes the echoed field rather than the response body: the request's own param= carries the code,
// so a strings.Contains over the body is satisfied by the URL we sent and proves nothing at all.
func quoteCheckCodeEcho(want, got string) error {
	if got != want {
		return fmt.Errorf("%w: asked for %q, vendor echoed %q", errQuoteCodeEcho, want, got)
	}
	return nil
}

// quoteCheckQtCodeEcho is check 2 as the four markets actually answer it. Three of them echo the
// code back exactly; the US echoes a Reuters instrument code — ask for AAPL and qt[2] reads
// "AAPL.OQ" — so an exact comparison fails every US response there has ever been, which is a whole
// market permanently dark rather than a drift caught.
//
// So for that market, and only that market, the comparison is against the part before the first "."
// and is case-insensitive. Two things it deliberately is NOT: it is not a prefix match (a request
// for "AAP" must not be satisfied by "AAPL.OQ"), and it is not a search over the response body — it
// is still one FIELD compared against one requested code, because param= in the URL we sent carries
// the code too and a body search would be satisfied by our own request.
//
// The known limit, stated rather than papered over: a US INDEX echoes ".IXIC" or ".DJI", whose part
// before the first "." is empty, so an index ticker fails this check and the request goes dark. That
// is the correct half of the trade — the alternative is serving a body nothing has verified — and
// the US form this portal documents is a ticker.
func quoteCheckQtCodeEcho(m *quoteMarket, want, got string) error {
	if !m.dottedEcho {
		return quoteCheckCodeEcho(want, got)
	}
	head := got
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i]
	}
	if !strings.EqualFold(head, want) {
		return fmt.Errorf("%w: asked for %q, vendor echoed %q", errQuoteCodeEcho, want, got)
	}
	return nil
}

// quoteCheckIdentity is drift-gate check 3: the vendor's percentage must agree with the percentage
// implied by the two prices beside it. A column inserted anywhere after the code leaves checks 1 and
// 2 satisfied — the response is longer, not shorter, and the code still echoes — and every value the
// parser now reads is a real price that simply belongs to a different field. This is the check that
// notices, and it is the reason a range check alone is not a drift gate.
//
// It is skipped when prevClose is zero: a stock that has never traded has no percentage to agree
// with, and the division would be undefined rather than wrong.
//
// The arithmetic is integer throughout. It could be a float64 without harming anything, since it
// decides a boolean and never becomes a stored price, but it does not have to be, and an integer
// comparison cannot be copied into a code path where it would matter.
func quoteCheckIdentity(snap *QuoteSnapshot) error {
	// The bound comes first because it is what makes the multiply below trustworthy — see
	// quotePriceCeiling. It lives inside this function rather than beside its call site so that a
	// second caller cannot reintroduce the wrap by forgetting it.
	if err := quoteCheckPriceBound("last", snap.Last); err != nil {
		return err
	}
	if err := quoteCheckPriceBound("prevClose", snap.PrevClose); err != nil {
		return err
	}
	if snap.PrevClose == 0 {
		return nil
	}
	vendor, err := quoteScaled(snap.ChangePct, quotePctScale)
	if err != nil {
		return fmt.Errorf("%w: percentage %q: %v", errQuoteIdentity, snap.ChangePct, err)
	}
	// (last-prevClose)/prevClose * 100, held at 10^-4 percent: the two factors fold into one
	// multiply by 10^6 so the division happens once, at the end, on the widest numerator.
	implied := quoteDivRound((snap.Last-snap.PrevClose)*1_000_000, snap.PrevClose)
	if d := implied - vendor; d > quoteIdentityTolerance || d < -quoteIdentityTolerance {
		return fmt.Errorf("%w: last %d prevClose %d imply %s%%, vendor says %s%%",
			errQuoteIdentity, snap.Last, snap.PrevClose, quotePctString(quoteDivRound(implied, 100)), snap.ChangePct)
	}
	return nil
}

// quoteCheckPriceBound refuses one price that is beyond quotePriceCeiling. Both directions are
// checked because a negative price is as capable of wrapping the identity's multiply as a positive
// one, and a negative price is not a price either way.
func quoteCheckPriceBound(what string, v int64) error {
	if v > quotePriceCeiling || v < -quotePriceCeiling {
		return fmt.Errorf("%w: %s is %d 分, past the %d 分 ceiling", errQuotePriceBound, what, v, quotePriceCeiling)
	}
	return nil
}

// quoteCheckSnapshotPrices bounds every price in a snapshot, not only the two the identity
// multiplies. The others reach arithmetic too — Sina derives its change and its percentage from
// last and prevClose, and the SPA divides all of them by a hundred to render them — and a field
// that is out of range by a factor of 10^13 is a broken feed whichever column it lands in.
func quoteCheckSnapshotPrices(snap *QuoteSnapshot) error {
	for _, p := range []struct {
		what string
		v    int64
	}{
		{"last", snap.Last}, {"prevClose", snap.PrevClose}, {"open", snap.Open},
		{"high", snap.High}, {"low", snap.Low}, {"change", snap.Change},
	} {
		if err := quoteCheckPriceBound(p.what, p.v); err != nil {
			return err
		}
	}
	return nil
}

// quoteCheckSnapshotOrder is what stands in for check 3 on the Sina path, where check 3 has nothing
// to say: Sina publishes no percentage, so the one shown for it is derived from last and prevClose
// and agrees with them by construction. This check interrogates Sina's OWN columns instead — the
// day's low is at or below both the open and the last, the high is at or above both, and the
// previous close is a real price. Sina sends name, open, prevClose, last, high, low in that order,
// so ONE inserted field at index 1 slides low onto the old high and high onto the old last, and the
// ordering breaks. Catching that single-inserted-field case is the whole point of this check,
// exactly as it is the whole point of the identity on the Tencent path.
//
// It is SKIPPED on the same zero bounds as check 5 and for the same reason: a suspended stock
// reports high=0 and low=0 beside a real last price, and a check that did not skip would report
// every suspended stock in the market as a source failure.
func quoteCheckSnapshotOrder(snap *QuoteSnapshot) error {
	if snap.High == 0 || snap.Low == 0 {
		return nil
	}
	if snap.PrevClose <= 0 {
		return fmt.Errorf("%w: prevClose is %d beside a high of %d and a low of %d",
			errQuoteOrdering, snap.PrevClose, snap.High, snap.Low)
	}
	if snap.Low > min(snap.Open, snap.Last) || snap.High < max(snap.Open, snap.Last) {
		return fmt.Errorf("%w: open %d and last %d are not inside %d..%d",
			errQuoteOrdering, snap.Open, snap.Last, snap.Low, snap.High)
	}
	return nil
}

// quoteCheckBars is drift-gate check 4. Swapping two columns of a candle usually shows up here and
// nowhere else, because each individual number remains a believable price.
func quoteCheckBars(bars []QuoteBar) error {
	for _, b := range bars {
		if b.Open <= 0 || b.High <= 0 || b.Low <= 0 || b.Close <= 0 {
			return fmt.Errorf("%w: %s has a non-positive price (o=%d h=%d l=%d c=%d)",
				errQuoteBarSanity, b.Date, b.Open, b.High, b.Low, b.Close)
		}
		// The same ceiling the identity needs, applied per candle: a bar's prices are arithmetic
		// the moment anything scales or compares them, and a wrapped price is least visible in the
		// middle of a series of eight hundred.
		if b.Open > quotePriceCeiling || b.High > quotePriceCeiling ||
			b.Low > quotePriceCeiling || b.Close > quotePriceCeiling {
			return fmt.Errorf("%w: %s has o=%d h=%d l=%d c=%d, past the %d 分 ceiling",
				errQuotePriceBound, b.Date, b.Open, b.High, b.Low, b.Close, quotePriceCeiling)
		}
		if b.Low > min(b.Open, b.Close) || b.High < max(b.Open, b.Close) {
			return fmt.Errorf("%w: %s has o=%d h=%d l=%d c=%d",
				errQuoteBarSanity, b.Date, b.Open, b.High, b.Low, b.Close)
		}
	}
	return nil
}

// quoteCheckRange is drift-gate check 5.
//
// It is SKIPPED when either bound is zero. A suspended stock legitimately reports high=0, low=0 and
// volume=0 alongside a real last price — the captured bj830799 fixture is exactly this — and a check
// that did not skip would report every suspended stock in the market as a source failure.
func quoteCheckRange(snap *QuoteSnapshot) error {
	if snap.High == 0 || snap.Low == 0 {
		return nil
	}
	if snap.Last < snap.Low || snap.Last > snap.High {
		return fmt.Errorf("%w: last %d not within %d..%d", errQuoteRange, snap.Last, snap.Low, snap.High)
	}
	return nil
}

// ---------- Tencent (primary) ----------

const (
	// One call returns history, snapshot and market-session state together, which is the reason
	// Tencent is primary: the three cannot disagree with each other about which instant they describe.
	// bfq = 不复权 (unadjusted). NEVER qfq: a front-adjusted series rewrites history every time a
	// dividend is paid, so a report scored against it stops matching the price it was written about.
	quoteTencentKlineURL = "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=%s,day,,,%d,bfq"
	quoteTencentReferer  = "https://gu.qq.com/"

	// A daily candle is under 100 bytes on either vendor, so quoteMaxBars of them plus a snapshot
	// stays far below this; a body at this ceiling is an interstitial, not a quote.
	quoteBodyLimit = 1 << 19
	// One Sina snapshot line is about 300 bytes.
	quoteLineLimit = 64 << 10

	quoteDefaultBars = 60
	// quoteMaxBars is roughly three trading years, which is more than any range the UI offers.
	quoteMaxBars = 800

	// A backstop for a caller that hands over a context with no deadline; context.WithTimeout keeps
	// whichever deadline is tighter, so a caller's own budget still wins.
	quoteFetchTimeout = 8 * time.Second
)

// quoteBarCount clamps a requested bar count into something a vendor URL may carry.
func quoteBarCount(bars int) int {
	if bars <= 0 {
		return quoteDefaultBars
	}
	if bars > quoteMaxBars {
		return quoteMaxBars
	}
	return bars
}

// quoteSymbol validates market+code and returns the vendor symbol both vendors use. It is the thin
// wrapper the fetchers want over quoteTargetFor, which is where the byte-level rules live.
func quoteSymbol(market, code string) (string, error) {
	t, err := quoteTargetFor(market, code)
	if err != nil {
		return "", err
	}
	return t.Symbol, nil
}

type tencentQuoteEnvelope struct {
	Code int                        `json:"code"`
	Msg  string                     `json:"msg"`
	Data map[string]json.RawMessage `json:"data"`
}

type tencentQuoteBody struct {
	// A day row is six strings — except on an ex-dividend date, where Tencent appends a SEVENTH
	// element that is an OBJECT ({"nd":"2025","fh_sh":"3.8",…}). Decoding into [][]string therefore
	// fails outright on any stock that has ever paid a dividend, which is most of them; RawMessage
	// lets the six we read stay strings and the one we do not read stay unexamined.
	Day [][]json.RawMessage        `json:"day"`
	Qt  map[string]json.RawMessage `json:"qt"`
}

// fetchTencentQuote fetches history, snapshot and session state for one stock in a single call.
func fetchTencentQuote(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
	sym, err := quoteSymbol(market, code)
	if err != nil {
		return nil, err
	}
	n := quoteBarCount(bars)
	rawURL := fmt.Sprintf(quoteTencentKlineURL, sym, n)
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()
	body, err := vendorGet(ctx, rawURL, quoteTencentReferer, quoteBodyLimit)
	if err != nil {
		vendorLogf(rawURL, "quote fetch failed for %s: %v", sym, err)
		return nil, err
	}
	resp, err := parseTencentQuote(market, code, body, n)
	if err != nil {
		vendorLogf(rawURL, "quote parse failed for %s: %v", sym, err)
		return nil, err
	}
	return resp, nil
}

// parseTencentQuote turns one fqkline response into a QuoteResp, running drift-gate checks 1-5 in
// order as it goes. The same array serves five markets, so every read below that is a UNIT or a
// FORMAT goes through the market's row in quoteMarkets rather than through a constant.
func parseTencentQuote(market, code string, body []byte, bars int) (*QuoteResp, error) {
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, err
	}
	m, sym := target.Market, target.Symbol
	// The CANONICAL code from here on, not the caller's spelling: it is what the section key was
	// built from, what the echo check compares against and what the response reports back, and all
	// three have to be the same string or "aapl" and "AAPL" become two different answers.
	code = target.Code
	var env tencentQuoteEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("quote: decode tencent response: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("quote: tencent returned code %d (%s)", env.Code, env.Msg)
	}
	raw, ok := env.Data[sym]
	if !ok {
		return nil, fmt.Errorf("quote: tencent response has no %q section", sym)
	}
	var qb tencentQuoteBody
	if err := json.Unmarshal(raw, &qb); err != nil {
		return nil, fmt.Errorf("quote: decode tencent %s: %w", sym, err)
	}
	rawQt, ok := qb.Qt[sym]
	if !ok {
		return nil, fmt.Errorf("quote: tencent response has no %q snapshot", sym)
	}
	var qt []string
	if err := json.Unmarshal(rawQt, &qt); err != nil {
		return nil, fmt.Errorf("quote: decode tencent %s snapshot: %w", sym, err)
	}

	// (1) field count, snapshot first and then every day row, before anything is read by index.
	if err := quoteCheckFieldCount("tencent qt", len(qt), quoteTencentQtFields); err != nil {
		return nil, err
	}
	for i, row := range qb.Day {
		if err := quoteCheckFieldCount(fmt.Sprintf("tencent day row %d", i), len(row), quoteDayRowFields); err != nil {
			return nil, err
		}
	}
	// (2) code echo, in the form this market echoes it — exact everywhere but the US.
	if err := quoteCheckQtCodeEcho(m, code, qt[2]); err != nil {
		return nil, err
	}

	r := &quoteFenReader{fields: qt}
	snap := QuoteSnapshot{Session: quoteSessionUnknown}
	// The two prices check 3 needs are read FIRST, so that the identity runs before any field a
	// shifted response would have turned into garbage. Ordering matters here for a reason that is
	// easy to state wrongly: it is NOT that the parse would otherwise die on the timestamp. Measured
	// on the captured fixture, an inserted column is caught three lines earlier than that, by the
	// price bound on `change` — the slide moves the vendor's own "20260904161458" into the 涨跌 slot,
	// where it reads as 2.03e15 分 and blows past quotePriceCeiling. Either way the request fails;
	// the point of reading the identity's two inputs first is that the reader learns the response
	// SHAPE moved, instead of a downstream field being reported as an unremarkable parse error.
	snap.Last = r.fen(3)
	snap.PrevClose = r.fen(4)
	snap.ChangePct = strings.TrimSpace(qt[32])
	if r.err != nil {
		return nil, r.err
	}
	// (3) arithmetic identity.
	if err := quoteCheckIdentity(&snap); err != nil {
		return nil, err
	}

	snap.Open = r.fen(5)
	snap.Change = r.fen(31)
	snap.High = r.fen(33)
	snap.Low = r.fen(34)
	// Both of these are UNIT CONVERSIONS by an exact power of ten, not a claim that the vendor
	// measured anything more finely than it did — one 手 is one hundred 股 by definition, and one
	// 万元 is ten thousand 元, which is what asking for four decimal places of a 万元 figure
	// produces directly. Both units are per-market and neither is guessable from the value: qt[6]
	// reads 39606884 for Apple, which is a day's shares, and 1421604 for 紫金矿业, which is a day's
	// 手. Multiply the first by a hundred and the portal reports four billion Apple shares traded;
	// read the second as shares and it reports one percent of the real figure.
	snap.Volume = r.volume(6, m.lots)
	snap.Amount = r.money(37, m.amountScale)
	if r.err != nil {
		return nil, r.err
	}
	// Last and prevClose were bounded by check 3 before it multiplied them; the four prices read
	// since have not been, and this is where they are.
	if err := quoteCheckSnapshotPrices(&snap); err != nil {
		return nil, err
	}
	if snap.AsOf, err = quoteVendorTime(m, qt[30]); err != nil {
		return nil, err
	}
	var market0 []string
	if err := json.Unmarshal(qb.Qt["market"], &market0); err == nil && len(market0) > 0 {
		snap.Session = quoteMarketSession(m, market0[0])
	}

	rows := qb.Day
	if bars > 0 && len(rows) > bars {
		rows = rows[len(rows)-bars:] // oldest first, so a surplus is trimmed from the front
	}
	out := make([]QuoteBar, 0, len(rows))
	for i, row := range rows {
		// Re-checked rather than assumed, for the same reason quoteFenReader carries a bounds check:
		// check 1 above already guarantees this, and the day the two get separated by an edit the
		// failure here must still be an error and not an index panic in a handler goroutine.
		if err := quoteCheckFieldCount(fmt.Sprintf("tencent day row %d", i), len(row), quoteDayRowFields); err != nil {
			return nil, err
		}
		var f [quoteDayRowFields]string
		for j := range f {
			if err := json.Unmarshal(row[j], &f[j]); err != nil {
				return nil, fmt.Errorf("quote: tencent day row %d field %d: %w", i, j, err)
			}
		}
		// The order is date, open, CLOSE, high, low, volume. Close is index 2, NOT the last price
		// column — reading it positionally as o/h/l/c draws a chart that is plausible and wrong.
		rd := &quoteFenReader{fields: f[:]}
		bar := QuoteBar{
			Date:   strings.TrimSpace(f[0]),
			Open:   rd.fen(1),
			Close:  rd.fen(2),
			High:   rd.fen(3),
			Low:    rd.fen(4),
			Volume: rd.volume(5, m.lots), // 手 -> 股 where the market counts 手, as above
		}
		if rd.err != nil {
			return nil, fmt.Errorf("quote: tencent day row %d: %w", i, rd.err)
		}
		out = append(out, bar)
	}
	// (4) bar sanity.
	if err := quoteCheckBars(out); err != nil {
		return nil, err
	}
	// (5) range sanity.
	if err := quoteCheckRange(&snap); err != nil {
		return nil, err
	}

	resp := &QuoteResp{
		Symbol:     code,
		Name:       strings.TrimSpace(qt[1]),
		Market:     m.id,
		Kind:       m.kindOf(qt),
		Currency:   m.currency,
		TZ:         m.zone,
		Source:     quoteSourceTencent,
		Snapshot:   snap,
		Bars:       out,
		BarsSource: quoteSourceTencent,
	}
	if len(out) == 0 {
		resp.BarsSource = ""
		resp.BarsUnavailable = quoteBarsUnavailableFor(m.id)
	}
	return resp, nil
}

// quoteBarsUnavailableFor names the reason an empty series is empty. A market whose daily history is
// a standing gap in the vendor rather than an outage has to be distinguishable from one that merely
// failed today: one is worth retrying and the other never will be.
func quoteBarsUnavailableFor(market string) string {
	if quoteMarketHasDailyHistory(market) {
		return quoteBarsSourceFailed
	}
	return quoteBarsMarketUnsupported
}

// quoteFenReader reads numbers out of a positional vendor response, remembering the first field that
// would not parse. Reading eight indices in a row would otherwise be eight copies of the same
// four-line error check, and the copy somebody forgets to write is the bug. Its bounds check is not
// redundant with drift-gate check 1: the gate proves the response is long enough for the indices
// this file reads TODAY, and this is what stops the index somebody adds tomorrow from panicking in a
// handler goroutine instead of returning an error.
type quoteFenReader struct {
	fields []string
	err    error
}

// fen reads field i as 分.
func (r *quoteFenReader) fen(i int) int64 { return r.scaled(i, 2) }

// shares reads field i as a share count that is already in 股. It refuses a negative one because
// there is no such thing: nothing in the drift gate looks at volume — check 4 checks the four
// prices of a candle and nothing else — so a negative share count would travel all the way to the
// chart, where it draws a bar pointing the wrong way.
func (r *quoteFenReader) shares(i int) int64 {
	v := r.scaled(i, 0)
	if r.err != nil {
		return 0
	}
	if v < 0 {
		r.err = fmt.Errorf("%w: field %d is %d", errQuoteVolume, i, v)
		return 0
	}
	return v
}

// money reads field i as a non-negative 元 figure. 成交额 is a running total of value traded, so a
// negative one is this parser landing on the wrong column, never a market event — and the strip
// that renders it groups digits off the absolute value, so an unguarded negative would reach a
// reader as a perfectly plausible positive number.
func (r *quoteFenReader) money(i, scale int) int64 {
	v := r.scaled(i, scale)
	if r.err != nil {
		return 0
	}
	if v < 0 {
		r.err = fmt.Errorf("%w: field %d is %d", errQuoteAmount, i, v)
		return 0
	}
	return v
}

// volume reads field i as a share count in whatever unit THIS MARKET quotes it in. The flag is a
// property of the market and never of the value, because the value cannot tell you: a 成交量 of
// 39606884 is a day of Apple shares and a 成交量 of 1421604 is a day of 紫金矿业 手, and both are
// ordinary numbers in the same field of the same array.
func (r *quoteFenReader) volume(i int, lots bool) int64 {
	if lots {
		return r.lots(i)
	}
	return r.shares(i)
}

// lots reads field i as Tencent's 手 count and returns 股. The bound is not decoration: this
// multiply by a hundred sits OUTSIDE quoteScaled's overflow guard, so "9223372036854775799" — a
// perfectly legal int64 — silently becomes -900 股 without it.
func (r *quoteFenReader) lots(i int) int64 {
	v := r.shares(i)
	if r.err != nil {
		return 0
	}
	if v > quoteMaxLots {
		r.err = fmt.Errorf("%w: field %d is %d 手, which does not fit in 股", errQuoteVolume, i, v)
		return 0
	}
	return v * 100
}

// scaled reads field i as an integer scaled by 10^scale, which is how a 手 or a 万元 column becomes
// the 股 or 元 the contract stores.
func (r *quoteFenReader) scaled(i, scale int) int64 {
	if r.err != nil {
		return 0
	}
	if i < 0 || i >= len(r.fields) {
		r.err = fmt.Errorf("quote: field %d beyond the %d fields the vendor sent", i, len(r.fields))
		return 0
	}
	v, err := quoteScaled(r.fields[i], scale)
	if err != nil {
		r.err = fmt.Errorf("quote: field %d: %w", i, err)
		return 0
	}
	return v
}

// ---------- Sina (fallback) ----------

const (
	quoteSinaKlineURL    = "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s&scale=240&ma=no&datalen=%d"
	quoteSinaSnapshotURL = "https://hq.sinajs.cn/list=%s"
	quoteSinaReferer     = "https://finance.sina.com.cn/"
)

// quoteSinaTarget resolves a symbol AND refuses the markets Sina's A-share line does not describe.
//
// Sina does serve Hong Kong and the US — as `rt_hk00700` and `gb_aapl`, in lines of 19 and 36 fields
// whose columns are in a completely different order (hk starts with an English name and puts the
// last price at index 6; the US line starts with the last price and the percentage). This parser is
// written against the A-share line, whose first six fields are name, open, prevClose, last, high,
// low. Pointing it at either of those would not be a second source, it would be a second shape read
// as the first — and the failure would look like a quote rather than like an error. The refusal is
// here, in words, rather than left to the field-count floor to catch by luck.
func quoteSinaTarget(market, code string) (quoteTarget, error) {
	t, err := quoteTargetFor(market, code)
	if err != nil {
		return quoteTarget{}, err
	}
	if !t.Market.sina {
		return quoteTarget{}, fmt.Errorf("quote: sina has no A-share-shaped line for the %q market", market)
	}
	return t, nil
}

// fetchSinaQuote is the fallback, and it costs two calls: Sina serves the snapshot as a GBK
// assignment statement from one host and the history as JSON from another.
//
// It never asks Sina for Beijing history. Sina answers a bj code with a series that stops over a
// year ago instead of with an error, which is the one failure mode a fallback must not have: an
// empty answer is visibly empty, while a stale one renders as a chart nobody has reason to distrust.
func fetchSinaQuote(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
	t, err := quoteSinaTarget(market, code)
	if err != nil {
		return nil, err
	}
	sym := t.Symbol
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()

	snapURL := fmt.Sprintf(quoteSinaSnapshotURL, sym)
	line, err := vendorGetGBK(ctx, snapURL, quoteSinaReferer, quoteLineLimit)
	if err != nil {
		vendorLogf(snapURL, "quote snapshot fetch failed for %s: %v", sym, err)
		return nil, err
	}
	resp, err := parseSinaSnapshot(market, code, line)
	if err != nil {
		vendorLogf(snapURL, "quote snapshot parse failed for %s: %v", sym, err)
		return nil, err
	}
	if !quoteMarketHasDailyHistory(market) {
		resp.BarsUnavailable = quoteBarsMarketUnsupported
		return resp, nil
	}

	klineURL := fmt.Sprintf(quoteSinaKlineURL, sym, quoteBarCount(bars))
	body, err := vendorGet(ctx, klineURL, quoteSinaReferer, quoteBodyLimit)
	if err == nil {
		var barsOut []QuoteBar
		if barsOut, err = parseSinaKLine(body); err == nil {
			resp.Bars = barsOut
			resp.BarsSource = quoteSourceSina
			return resp, nil
		}
	}
	// The snapshot is already in hand and is the half the page needs most, so a history failure
	// degrades the response rather than discarding it.
	vendorLogf(klineURL, "quote history failed for %s: %v", sym, err)
	resp.BarsUnavailable = quoteBarsSourceFailed
	return resp, nil
}

// parseSinaSnapshot turns one `var hq_str_sh601899="…";` line into the snapshot half of a QuoteResp,
// running drift-gate checks 1, 2 and 5 over it, with quoteCheckSnapshotOrder in place of check 3.
func parseSinaSnapshot(market, code, raw string) (*QuoteResp, error) {
	t, err := quoteSinaTarget(market, code)
	if err != nil {
		return nil, err
	}
	m, sym := t.Market, t.Symbol
	code = t.Code
	key, payload, err := quoteSinaLine(raw)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(payload, ",")
	// (1) field count.
	if err := quoteCheckFieldCount("sina snapshot", len(fields), quoteSinaSnapFields); err != nil {
		return nil, err
	}
	// (2) code echo — the variable name is Sina's echo of what we asked for, and like Tencent's it is
	// compared for exact equality rather than searched for in the body.
	if err := quoteCheckCodeEcho(sym, key); err != nil {
		return nil, err
	}

	r := &quoteFenReader{fields: fields}
	snap := QuoteSnapshot{Session: quoteSessionUnknown}
	snap.Open = r.fen(1)
	snap.PrevClose = r.fen(2)
	snap.Last = r.fen(3)
	snap.High = r.fen(4)
	snap.Low = r.fen(5)
	if r.err != nil {
		return nil, r.err
	}
	// The bound runs before any arithmetic touches these numbers, the subtraction and the multiply
	// below included: Change*10_000 wraps for exactly the reason the identity's multiply does.
	if err := quoteCheckSnapshotPrices(&snap); err != nil {
		return nil, err
	}
	// (3, substituted) an ordering check across Sina's own price columns. Check 3 itself is NOT run
	// here and would be worthless if it were: Sina publishes neither the change nor the percentage,
	// so both are derived from last and prevClose just below, and asking whether that derivation
	// agrees with last and prevClose is asking a division to agree with itself. This check asks
	// something the vendor's line can actually get wrong — see quoteCheckSnapshotOrder.
	if err := quoteCheckSnapshotOrder(&snap); err != nil {
		return nil, err
	}

	// Sina ships no percentage of its own, so this one is derived — the only place in this file where
	// a displayed percentage is not the vendor's. It is the ex-rights caveat on QuoteSnapshot.ChangePct
	// made real, and one more reason Tencent is primary rather than merely first.
	snap.Change = snap.Last - snap.PrevClose
	if snap.PrevClose != 0 {
		snap.ChangePct = quotePctString(quoteDivRound(snap.Change*10_000, snap.PrevClose))
	} else {
		snap.ChangePct = "0.00"
	}

	snap.Volume = r.shares(8)   // already 股
	snap.Amount = r.money(9, 0) // already 元
	if r.err != nil {
		return nil, r.err
	}
	if snap.AsOf, err = quoteSinaTime(m, fields[30], fields[31]); err != nil {
		return nil, err
	}
	// Session stays "unknown": Sina's line carries no market-state field, and deciding it from this
	// server's wall clock would be the one thing AsOf exists to avoid — a server whose clock or zone
	// has drifted would confidently label a closed market as trading.

	// (5) range sanity; check 4 belongs to the history call, which this one does not make. On this
	// path it can no longer be the check that FIRES: the ordering check above is strictly stronger
	// about last, since low <= min(open,last) <= last <= max(open,last) <= high. It stays because it
	// is the contract's check 5 and because the stronger check above is the one an edit could remove.
	if err := quoteCheckRange(&snap); err != nil {
		return nil, err
	}
	return &QuoteResp{
		Symbol: code,
		Name:   strings.TrimSpace(fields[0]),
		Market: m.id,
		// Kind is left EMPTY here rather than defaulted to "stock", and the difference is not
		// pedantry. Sina's line for 上证指数 carries 34 comma-separated fields whose first six are
		// in the same order as a stock's, so it sails through check 1 and this parser cannot tell
		// the two apart — there is no instrument-type column anywhere in it. Saying "stock" would be
		// this file asserting something it did not read; saying nothing is the same answer it gives
		// for Session, on the same path, for the same reason.
		//
		// The gap that comes with it, recorded rather than fixed: an index served by this fallback
		// also gets its 成交量 read as 股, and Sina quotes an index's in 手 on Shanghai (measured
		// 2026-09-07: 459,293,905 against a 868.8 亿元 turnover) while quoting a stock's in 股. So a
		// Shanghai index served while Tencent is down is a hundred times short on volume alone.
		// Detecting it needs a column this line does not have: the only signal that separates an
		// index from a stock here is an empty order book, which is also exactly what a SUSPENDED
		// stock looks like, and refusing those is the failure ADR 0028 §7 spends a paragraph on.
		Kind:     "",
		Currency: m.currency,
		TZ:       m.zone,
		Source:   quoteSourceSina,
		Snapshot: snap,
		Bars:     []QuoteBar{},
	}, nil
}

// quoteSinaLine splits `var hq_str_sh601899="a,b,c";` into its variable suffix and its payload.
func quoteSinaLine(raw string) (key, payload string, err error) {
	const marker = "hq_str_"
	i := strings.Index(raw, marker)
	if i < 0 {
		return "", "", fmt.Errorf("quote: sina response is not an hq_str assignment")
	}
	rest := raw[i+len(marker):]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return "", "", fmt.Errorf("quote: sina response has no assignment")
	}
	key = strings.TrimSpace(rest[:eq])
	rest = rest[eq+1:]
	open := strings.IndexByte(rest, '"')
	if open < 0 {
		return "", "", fmt.Errorf("quote: sina payload is not quoted")
	}
	rest = rest[open+1:]
	closing := strings.IndexByte(rest, '"')
	if closing < 0 {
		return "", "", fmt.Errorf("quote: sina payload is not terminated")
	}
	return key, rest[:closing], nil
}

// sinaKLineRow is Sina's history row. Unlike Tencent's it is keyed rather than positional, so an
// inserted column cannot shift it — which is why drift-gate checks 1 and 2 have nothing to do here
// and check 4 has everything.
type sinaKLineRow struct {
	Day    string `json:"day"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

// parseSinaKLine turns Sina's daily history JSON into bars, oldest first.
func parseSinaKLine(body []byte) ([]QuoteBar, error) {
	var rows []sinaKLineRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("quote: decode sina history: %w", err)
	}
	out := make([]QuoteBar, 0, len(rows))
	for i, row := range rows {
		r := &quoteFenReader{fields: []string{row.Open, row.High, row.Low, row.Close, row.Volume}}
		bar := QuoteBar{
			Date:   strings.TrimSpace(row.Day),
			Open:   r.fen(0),
			High:   r.fen(1),
			Low:    r.fen(2),
			Close:  r.fen(3),
			Volume: r.shares(4), // already 股
		}
		if r.err != nil {
			return nil, fmt.Errorf("quote: sina history row %d: %w", i, r.err)
		}
		out = append(out, bar)
	}
	// (4) bar sanity.
	if err := quoteCheckBars(out); err != nil {
		return nil, err
	}
	return out, nil
}
