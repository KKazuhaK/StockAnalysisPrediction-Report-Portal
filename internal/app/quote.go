package app

// quote.go —— the A-share quote feature's vendor parsers and its drift gate.
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
// Every price in here is an integer 分 (fen). This feature feeds a schema with 222 TEXT, 61 INTEGER
// and 24 BIGINT columns and not one REAL, DOUBLE, FLOAT or NUMERIC, and a price that has been
// through a float64 does not reliably come back: in float64, 0.29 * 100 is 28.999999999999996, and
// converting that to int64 truncates towards zero and yields 28 分 for a price of 0.29 元. So no
// vendor decimal is ever handed to strconv.ParseFloat — quoteScaled does the whole conversion in
// integer arithmetic, and quote_test.go pins that exact case.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ---------- the wire contract (mirrored in web/src/api/types.ts) ----------

// QuoteBar is one daily candle. Prices are 分; Volume is 股 (shares) on both vendors, which for
// Tencent means its 手 have already been multiplied by 100 on the way in.
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
	Volume    int64  `json:"volume"`
	Amount    int64  `json:"amount"`
	// AsOf is RFC3339 in +08:00 taken from the VENDOR's clock, never this server's. A feed that has
	// stopped updating is only visible as stale if the timestamp shown is the feed's own.
	AsOf    string `json:"asOf"`
	Session string `json:"session"`
}

// QuoteResp is one stock's quote as the SPA receives it.
type QuoteResp struct {
	Symbol          string        `json:"symbol"`
	Name            string        `json:"name"`
	Market          string        `json:"market"`
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

	// quoteBarsMarketUnsupported is the Beijing exchange: Tencent answers "day":[] for it and Sina's
	// series for the same code is over a year stale, which is worse than empty because it looks like
	// data. The UI has to say so out loud — an empty chart rendered as a flat one is a lie.
	quoteBarsMarketUnsupported = "market_unsupported"
	quoteBarsSourceFailed      = "source_failed"

	quoteSessionOpen    = "open"
	quoteSessionClose   = "close"
	quoteSessionUnknown = "unknown"
)

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

// quoteCST is the zone every one of these timestamps is stamped in. It is a fixed +08:00 rather than
// time.LoadLocation("Asia/Shanghai") because it has to be right on a machine with no tzdata
// installed: there LoadLocation returns an error, and the obvious fallback — time.Local — is the
// server's own zone, which is exactly the substitution AsOf exists to prevent. China has observed no
// DST since 1991, so the fixed offset and the zoneinfo entry agree on every timestamp a market feed
// can carry.
var quoteCST = time.FixedZone("CST", 8*60*60)

const quoteCSTLayout = "20060102150405"

// quoteVendorTime turns Tencent's "20260904161458" into RFC3339 in +08:00.
func quoteVendorTime(stamp string) (string, error) {
	stamp = strings.TrimSpace(stamp)
	t, err := time.ParseInLocation(quoteCSTLayout, stamp, quoteCST)
	if err != nil {
		return "", fmt.Errorf("quote: vendor timestamp %q: %w", stamp, err)
	}
	return t.Format(time.RFC3339), nil
}

// quoteSinaTime is quoteVendorTime for Sina, which splits the same instant across two fields
// ("2026-09-04" and "15:34:59") instead of sending one.
func quoteSinaTime(date, clock string) (string, error) {
	date = strings.ReplaceAll(strings.TrimSpace(date), "-", "")
	clock = strings.ReplaceAll(strings.TrimSpace(clock), ":", "")
	return quoteVendorTime(date + clock)
}

// ---------- market session ----------

// quoteMarketSession pulls this market's segment out of Tencent's session string, which looks like
//
//	2026-09-07 05:56:37|HK_close_未开盘|SH_close_未开盘|SZ_close_未开盘|…|NEWSH_close_未开盘|…
//
// The segment is matched on its PREFIX, never with strings.Contains: the same string carries
// NEWSH_, NEWSZ_ and HSZB_ segments, and "SZ_" is a substring of two of them, so a Contains match
// reports the state of a completely different board. The Beijing exchange has no segment of its own
// and keeps Shenzhen's hours, so bj follows SZ.
func quoteMarketSession(market, raw string) string {
	var want string
	switch market {
	case "sh":
		want = "SH_"
	case "sz", "bj":
		want = "SZ_"
	default:
		return quoteSessionUnknown
	}
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
// the most newsworthy days of the year as a source failure. Do not add one.

const (
	// Tencent's qt array is 88 fields for sh/sz and 87 for bj — a captured bj830799 response proves
	// it — so the gate asserts a FLOOR, never a fixed total. An equality against 88 would take the
	// whole Beijing exchange offline. The floor is 39 rather than 38 (the highest index this parser
	// reads is 37, 成交额): two spare slots, because the shortest real response seen carries 87 and
	// a floor that sits exactly on the last field we happen to read today would have to move every
	// time the parser reads one more.
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

// quoteSymbol validates market+code and returns the vendor symbol both vendors use. The check is not
// paranoia about the caller: this symbol is concatenated into a URL whose param= syntax needs literal
// commas, so it cannot go through url.Values, and a code that is not exactly six digits must not
// reach that concatenation.
func quoteSymbol(market, code string) (string, error) {
	if pre := marketPrefix(code); pre == "" || pre != market {
		return "", fmt.Errorf("quote: %q is not a %q-market symbol", code, market)
	}
	return market + code, nil
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
// order as it goes.
func parseTencentQuote(market, code string, body []byte, bars int) (*QuoteResp, error) {
	sym, err := quoteSymbol(market, code)
	if err != nil {
		return nil, err
	}
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
	// (2) code echo.
	if err := quoteCheckCodeEcho(code, qt[2]); err != nil {
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
	// Tencent quotes 成交量 in 手 and 成交额 in 万元, and both conversions below are exact powers of
	// ten — a UNIT CONVERSION, not a claim that the vendor measured anything more finely than it
	// did. One 手 is one hundred 股 by definition; one 万元 is ten thousand 元, which is what asking
	// for four decimal places of a 万元 figure produces directly.
	snap.Volume = r.lots(6)
	snap.Amount = r.money(37, 4)
	if r.err != nil {
		return nil, r.err
	}
	// Last and prevClose were bounded by check 3 before it multiplied them; the four prices read
	// since have not been, and this is where they are.
	if err := quoteCheckSnapshotPrices(&snap); err != nil {
		return nil, err
	}
	if snap.AsOf, err = quoteVendorTime(qt[30]); err != nil {
		return nil, err
	}
	var market0 []string
	if err := json.Unmarshal(qb.Qt["market"], &market0); err == nil && len(market0) > 0 {
		snap.Session = quoteMarketSession(market, market0[0])
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
			Volume: rd.lots(5), // 手 -> 股, as above
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
		Market:     market,
		Source:     quoteSourceTencent,
		Snapshot:   snap,
		Bars:       out,
		BarsSource: quoteSourceTencent,
	}
	if len(out) == 0 {
		resp.BarsSource = ""
		resp.BarsUnavailable = quoteBarsUnavailableFor(market)
	}
	return resp, nil
}

// quoteBarsUnavailableFor names the reason an empty series is empty. Beijing is a standing gap in
// both vendors rather than an outage, and the two have to be distinguishable: one is worth retrying
// and the other never will be.
func quoteBarsUnavailableFor(market string) string {
	if market == "bj" {
		return quoteBarsMarketUnsupported
	}
	return quoteBarsSourceFailed
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

// fetchSinaQuote is the fallback, and it costs two calls: Sina serves the snapshot as a GBK
// assignment statement from one host and the history as JSON from another.
//
// It never asks Sina for Beijing history. Sina answers a bj code with a series that stops over a
// year ago instead of with an error, which is the one failure mode a fallback must not have: an
// empty answer is visibly empty, while a stale one renders as a chart nobody has reason to distrust.
func fetchSinaQuote(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
	sym, err := quoteSymbol(market, code)
	if err != nil {
		return nil, err
	}
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
	if market == "bj" {
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
	sym, err := quoteSymbol(market, code)
	if err != nil {
		return nil, err
	}
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
	if snap.AsOf, err = quoteSinaTime(fields[30], fields[31]); err != nil {
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
		Symbol:   code,
		Name:     strings.TrimSpace(fields[0]),
		Market:   market,
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
