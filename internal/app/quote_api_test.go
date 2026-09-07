package app

// quote_api_test.go —— the endpoint and the cache in front of it.
//
// Nothing here talks to the network, and nothing here invents a vendor response either. The stubs
// replace the TRANSPORT and hand the real parser the exact bytes Tencent and Sina sent on
// 2026-09-06, so a test that says "Tencent answered" means the parser ran over a real answer. A stub
// that returned a hand-built QuoteResp would agree with the handler by construction — including on
// the day both are wrong about which field is the close.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// ---------- harness ----------

func quoteServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.UpsertUser(User{Username: "kazuha", PasswordHash: "h", Role: "user"})
	return s
}

// quoteMux registers the route exactly as server.go does, so these tests exercise the wildcard
// extraction and the session gate rather than a handler called by hand.
func quoteMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/quote/{symbol}", s.requireUserJSON(s.apiQuote))
	return mux
}

// quoteGET makes an authenticated request through the mux.
func quoteGET(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign("kazuha")})
	rec := httptest.NewRecorder()
	quoteMux(s).ServeHTTP(rec, r)
	return rec
}

func quoteBody(t *testing.T, rec *httptest.ResponseRecorder) QuoteResp {
	t.Helper()
	var out QuoteResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode quote response: %v (body %s)", err, rec.Body.String())
	}
	return out
}

func quoteErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	code, _ := out["code"].(string)
	return code
}

// ---------- vendor stubs: a real captured body behind a countable transport ----------

// quoteVendorStub counts calls and can be held open, which is what makes both the cache claim and
// the single-flight claim observable: "one upstream request" is a number, not an impression.
type quoteVendorStub struct {
	mu    sync.Mutex
	calls int
	gate  chan struct{} // non-nil: every call parks here until the test opens it
	err   error         // non-nil: this vendor fails
	parse func(market, code string, bars int) (*QuoteResp, error)
}

func (v *quoteVendorStub) fetch(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
	// A real vendor call fails immediately on a context that is already done, and the cancellation
	// tests below depend on this stub behaving the same way: without it a cancelled leader would be
	// rescued by a fallback that never noticed, and those tests would pass for the wrong reason.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.calls++
	gate := v.gate
	v.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if v.err != nil {
		return nil, v.err
	}
	return v.parse(market, code, bars)
}

func (v *quoteVendorStub) n() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

// tencentStub answers from a captured fqkline body, through parseTencentQuote.
func tencentStub(body []byte) *quoteVendorStub {
	return &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		return parseTencentQuote(market, code, body, bars)
	}}
}

// sinaStub is fetchSinaQuote's two calls, from the two captured Sina fixtures. It reproduces that
// function's own rule for Beijing — snapshot yes, history never — because a fallback that quietly
// dropped that rule would let a sixteen-month-stale series reach the chart.
func sinaStub(t *testing.T) *quoteVendorStub {
	t.Helper()
	snap := string(readQuoteFixture(t, fixSinaSnap))
	kline := readQuoteFixture(t, fixSinaKline)
	return &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		resp, err := parseSinaSnapshot(market, code, snap)
		if err != nil {
			return nil, err
		}
		if market == "bj" {
			resp.BarsUnavailable = quoteBarsMarketUnsupported
			return resp, nil
		}
		rows, err := parseSinaKLine(kline)
		if err != nil {
			return nil, err
		}
		if bars > 0 && len(rows) > bars {
			rows = rows[len(rows)-bars:]
		}
		resp.Bars, resp.BarsSource = rows, quoteSourceSina
		return resp, nil
	}}
}

// fetch runs fetchUnder against the SHIPPED configuration. It lives here rather than in
// quote_cache.go for the reason helpers_test.go gives about the methods a deadcode sweep found:
// every production caller has a Server to read the operator's choices from and goes through
// fetchUnder with them (quote_api.go), so a no-config fetch is a thing only the cache's own tests
// want. What it must keep being is the DEFAULTS an unconfigured portal runs, not a fourth
// configuration that only exists under test.
func (c *quoteCache) fetch(ctx context.Context, market, code string, bars int) (*QuoteResp, bool, time.Duration, error) {
	return c.fetchUnder(ctx, quoteConfigDefault(), market, code, bars)
}

func failingStub(msg string) *quoteVendorStub {
	return &quoteVendorStub{err: errors.New(msg)}
}

// wireQuoteSources installs the two vendors in failover order, carrying the same market predicates
// defaultQuoteSources gives them. Wiring bare stubs instead would quietly hand Sina the two markets
// its parser refuses, and the tests that turn on which source gets CALLED would then be testing a
// source table this portal never runs.
func wireQuoteSources(s *Server, tencent, sina *quoteVendorStub) {
	s.quotes.sources = []quoteSource{
		{name: quoteSourceTencent, fetch: tencent.fetch, serves: func(*quoteMarket) bool { return true }},
		{name: quoteSourceSina, fetch: sina.fetch, serves: func(m *quoteMarket) bool { return m.sina }},
	}
}

// ---------- the gate ----------

func TestQuoteAPIRequiresASession(t *testing.T) {
	s := quoteServer(t)
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSH)), sinaStub(t))

	rec := httptest.NewRecorder()
	quoteMux(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/quote/601899", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous quote → %d, want 401", rec.Code)
	}
	// An unauthenticated quote is not a harmless read: it makes this server fetch from a vendor on
	// an anonymous caller's behalf, and OUR address is the one that gets blocked for it.
	if got := quoteErrCode(t, rec); got != "session_expired" {
		t.Errorf("401 code = %q, want session_expired", got)
	}
}

// ---------- symbol validation ----------

// The rule is quoteResolve's, not a second one written here: this endpoint's code is interpolated
// into a vendor URL through the same resolver every other vendor call in the tree now uses, and two
// validators that disagree is how the one at the URL boundary gets bypassed.
//
// The list below is what the WIDER symbol space still refuses. Three of these used to be refused for
// a reason that has gone away — "abc" is a US ticker now and "12345" is a Hong Kong code — so what
// is left is what no market's shape accepts: too many digits, too many letters, a market nobody
// serves, a code of the wrong shape for the market it was asked for, and any byte that cannot go in
// a URL.
//
// Called directly rather than through the mux because one of these cases cannot reach the mux at
// all: a ServeMux wildcard does not match an empty path segment, so /api/quote/ is a 404 from the
// router and the handler's own answer to an empty symbol would never be exercised.
func TestQuoteAPIRefusesABadSymbol(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))

	for _, symbol := range []string{
		"1234567", "60189\x00", "", "60189１", "７０１８９９", "AAPL.OQ", "AAPLTOOLONG",
		"nyse:AAPL", "sh:00000", "hk:0070", "us:AAPL1", "us:", "601899sh",
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/quote/x", nil)
		r.SetPathValue("symbol", symbol)
		s.apiQuote(rec, r, "kazuha")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("symbol %q → %d, want 400", symbol, rec.Code)
		}
		if got := quoteErrCode(t, rec); got != "quote_bad_symbol" {
			t.Errorf("symbol %q → code %q, want quote_bad_symbol", symbol, got)
		}
	}
	// And none of them reached a vendor. A refusal that still made the request would leave the
	// amplification hole open while looking like it had been closed.
	if tencent.n() != 0 {
		t.Errorf("a rejected symbol still cost %d upstream call(s)", tencent.n())
	}
}

// ---------- the happy path ----------

func TestQuoteAPIServesTencent(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))

	rec := quoteGET(t, s, "/api/quote/601899?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("quote → %d (%s)", rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqStr(t, "symbol", got.Symbol, "601899")
	quoteEqStr(t, "name", got.Name, "紫金矿业")
	quoteEqStr(t, "market", got.Market, "sh")
	quoteEqStr(t, "source", got.Source, quoteSourceTencent)
	quoteEqStr(t, "barsSource", got.BarsSource, quoteSourceTencent)
	quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, "")
	// The vendor's own percentage, passed through as a string. Re-deriving it is what turns an
	// ex-rights day into a fabricated crash.
	quoteEqStr(t, "changePct", got.Snapshot.ChangePct, "0.12")
	quoteEqInt(t, "last", got.Snapshot.Last, 3335)
	quoteEqInt(t, "prevClose", got.Snapshot.PrevClose, 3331)
	quoteEqStr(t, "session", got.Snapshot.Session, quoteSessionClose)
	quoteEqStr(t, "asOf", got.Snapshot.AsOf, "2026-09-04T16:14:58+08:00")
	if got.Adjusted {
		t.Error("adjusted must stay false: the series served is bfq (不复权)")
	}
	if got.Cached {
		t.Error("the first request cannot be a cache hit")
	}
	// The fixture holds 60 sessions and 3m asks for 66, so this is the whole captured series.
	if len(got.Bars) != 60 {
		t.Fatalf("bars = %d, want the fixture's 60", len(got.Bars))
	}
	if got.Bars[0].Date >= got.Bars[len(got.Bars)-1].Date {
		t.Error("bars must be oldest first")
	}
	last := got.Bars[len(got.Bars)-1]
	quoteEqStr(t, "last bar date", last.Date, "2026-09-04")
	// open, CLOSE, high, low is the vendor's column order; reading it as o/h/l/c draws a chart that
	// is plausible and wrong, so the four are pinned to the captured row.
	quoteEqInt(t, "last bar open", last.Open, 3385)
	quoteEqInt(t, "last bar close", last.Close, 3335)
	quoteEqInt(t, "last bar high", last.High, 3405)
	quoteEqInt(t, "last bar low", last.Low, 3307)
	quoteEqInt(t, "last bar volume", last.Volume, 176934100) // 手 → 股
	if tencent.n() != 1 {
		t.Errorf("one page view cost %d upstream calls", tencent.n())
	}
}

// range= decides how many sessions come back, and an unknown value falls back rather than failing:
// a stale bookmark should still draw a chart.
func TestQuoteAPIRangeSelectsTheBarCount(t *testing.T) {
	s := quoteServer(t)
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSH)), sinaStub(t))

	for _, c := range []struct {
		query string
		bars  int
	}{
		{"?range=1m", 22},
		{"?range=3m", 60}, // the fixture only has 60 sessions; 3m asks for 66
		{"?range=6m", 60},
		{"?range=1y", 60},
		{"", 60},
		{"?range=10y", 60},
		{"?range=%20", 60},
	} {
		rec := quoteGET(t, s, "/api/quote/601899"+c.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("range %q → %d", c.query, rec.Code)
		}
		if n := len(quoteBody(t, rec).Bars); n != c.bars {
			t.Errorf("range %q → %d bars, want %d", c.query, n, c.bars)
		}
	}
	// The count that reaches a vendor URL is one of four constants, never anything a caller typed.
	// Pinned to the constants themselves: the assertion this replaced was n != quoteBarCount(n),
	// which is true of every value the map can hold and therefore could never fail. The clamp is
	// proved separately, on a map deliberately widened, in TestQuoteBarsForRangeClampsWhateverTheMapHolds.
	for key, want := range map[string]int{"1m": quoteRange1M, "3m": quoteRange3M, "6m": quoteRange6M, "1y": quoteRange1Y} {
		if n := quoteBarsForRange(key); n != want {
			t.Errorf("range %q maps to %d bars, want %d", key, n, want)
		}
	}
	if quoteBarsForRange("nonsense") != quoteBarsForRange(quoteRangeDefault) {
		t.Error("an unknown range must fall back to the default, not to zero bars")
	}
}

// ---------- failover ----------

func TestQuoteAPIFailsOverToSina(t *testing.T) {
	s := quoteServer(t)
	tencent := failingStub("tencent is down")
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	rec := quoteGET(t, s, "/api/quote/601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("failover → %d (%s)", rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqStr(t, "source", got.Source, quoteSourceSina)
	quoteEqStr(t, "barsSource", got.BarsSource, quoteSourceSina)
	quoteEqStr(t, "name", got.Name, "紫金矿业")
	// Sina publishes no percentage, so this one is derived — and the two vendors' prices agree
	// exactly, which is why the derived string matches Tencent's own.
	quoteEqStr(t, "changePct", got.Snapshot.ChangePct, "0.12")
	// Sina's line carries no market-state field, and guessing one from this server's clock is the
	// substitution the whole feature exists to avoid.
	quoteEqStr(t, "session", got.Snapshot.Session, quoteSessionUnknown)
	if len(got.Bars) == 0 {
		t.Error("the fallback still has to carry history")
	}
	if tencent.n() != 1 || sina.n() != 1 {
		t.Errorf("failover made tencent=%d sina=%d calls, want 1 and 1", tencent.n(), sina.n())
	}
}

// The drift gate is not advisory. A vendor that inserts one column answers 200 with a body full of
// real prices in the wrong fields, and the endpoint has to treat that as a DOWN source and fail
// over — not serve it, and not 503 while a working fallback sits behind it.
func TestQuoteAPIFailsOverWhenTheDriftGateTrips(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(quoteInsertedColumn(t))
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	rec := quoteGET(t, s, "/api/quote/601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("drift → %d, want the fallback to answer 200 (%s)", rec.Code, rec.Body.String())
	}
	if src := quoteBody(t, rec).Source; src != quoteSourceSina {
		t.Errorf("source = %q; a gate failure must count as a source failure", src)
	}
	// And the gate is what rejected it, rather than the response merely being unparseable.
	if _, err := parseTencentQuote("sh", "601899", quoteInsertedColumn(t), 66); !errors.Is(err, errQuoteIdentity) {
		t.Errorf("the mutated fixture failed with %v, want the identity check", err)
	}
}

// quoteInsertedColumn is the real sh601899 response with exactly one thing changed: a plausible
// price inserted straight after the code echo. Checks 1 and 2 still pass — the array got LONGER and
// the code is still at index 2 — and every value the parser then reads is a real price belonging to
// the wrong field. This is the shape a vendor schema change actually has.
func quoteInsertedColumn(t *testing.T) []byte {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(readQuoteFixture(t, fixTencentSH), &env); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	data, ok := env["data"].(map[string]any)["sh601899"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no sh601899 section")
	}
	qtMap, ok := data["qt"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no qt section")
	}
	qt, ok := qtMap["sh601899"].([]any)
	if !ok || len(qt) < quoteTencentQtFields {
		t.Fatalf("fixture qt is %T with %d fields", qtMap["sh601899"], len(qt))
	}
	shifted := append([]any{}, qt[:3]...)
	shifted = append(shifted, "33.31")
	qtMap["sh601899"] = append(shifted, qt[3:]...)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("re-encode fixture: %v", err)
	}
	return out
}

func TestQuoteAPIReportsEveryStrategyExhausted(t *testing.T) {
	s := quoteServer(t)
	tencent := failingStub("tencent is down")
	sina := failingStub("sina is down")
	wireQuoteSources(s, tencent, sina)

	rec := quoteGET(t, s, "/api/quote/601899")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("both sources down → %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if got := quoteErrCode(t, rec); got != "quote_unavailable" {
		t.Errorf("code = %q, want quote_unavailable", got)
	}
	// A failure is never cached, or one bad minute would be served for five.
	if _, _, ok := s.quotes.get(quoteCacheKey("sh", "601899", quoteBarsForRange("3m"))); ok {
		t.Error("a failed fetch left an entry in the cache")
	}
	// The health counters an admin panel will read. Consecutive failures is the field that separates
	// "down" from "blipped", so it has to count rather than latch.
	health := s.QuoteHealth()
	if len(health) != 2 {
		t.Fatalf("health = %+v, want one row per source", health)
	}
	for _, h := range health {
		if h.Failures != 1 || h.LastError == "" || h.LastErrorAt.IsZero() {
			t.Errorf("health for %q = %+v", h.Source, h)
		}
	}
	// A success resets the streak; a counter that only ever went up would report a source that
	// recovered an hour ago as still broken.
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSZ)), sina)
	if rec := quoteGET(t, s, "/api/quote/000001"); rec.Code != http.StatusOK {
		t.Fatalf("recovered tencent → %d", rec.Code)
	}
	for _, h := range s.QuoteHealth() {
		if h.Source == quoteSourceTencent && (h.Failures != 0 || h.LastSuccess.IsZero()) {
			t.Errorf("a recovered source still reads %+v", h)
		}
	}
}

// ---------- Beijing ----------

// The Beijing exchange has no usable daily history anywhere: Tencent answers with an empty day
// array and Sina answers with a series that stopped sixteen months ago, which is the worse of the
// two because a stale chart is indistinguishable from a current one. The snapshot is real and is
// served; bars is an empty ARRAY (never null, or the SPA's QuoteBar[] blows up) and the UI is told
// why in a code it can translate.
func TestQuoteAPIBeijingServesSnapshotWithoutBars(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentBJ))
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	rec := quoteGET(t, s, "/api/quote/830799?range=1y")
	if rec.Code != http.StatusOK {
		t.Fatalf("bj quote → %d (%s)", rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqStr(t, "market", got.Market, "bj")
	quoteEqStr(t, "name", got.Name, "艾融软件")
	quoteEqInt(t, "last", got.Snapshot.Last, 3428)
	if len(got.Bars) != 0 {
		t.Errorf("bj carried %d bars; neither vendor has trustworthy bj history", len(got.Bars))
	}
	quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, quoteBarsMarketUnsupported)
	quoteEqStr(t, "barsSource", got.BarsSource, "")
	// Empty ARRAY on the wire. `"bars":null` is a runtime error in a chart that types it QuoteBar[],
	// and it is exactly what a nil slice marshals to.
	if !strings.Contains(rec.Body.String(), `"bars":[]`) {
		t.Errorf("bars must serialize as [], got %s", rec.Body.String())
	}
	// A suspended stock reports high=0, low=0, volume=0 beside a real last price, and the range
	// check has to skip that or every suspended stock reads as a source failure.
	if got.Snapshot.High != 0 || got.Snapshot.Low != 0 || got.Snapshot.Volume != 0 {
		t.Errorf("the captured bj snapshot is suspended: %+v", got.Snapshot)
	}
	if sina.n() != 0 {
		t.Errorf("bj fell through to sina %d time(s); its bj series is sixteen months stale", sina.n())
	}
}

// ---------- the cache ----------

func TestQuoteAPICachesTheSecondRequest(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))

	// The clock is a seam, and it has to be moved: both requests firing inside the same millisecond
	// is what made the remaining-TTL assertion below unfalsifiable — a handler that always
	// advertised the FULL TTL passed it, which is the exact bug the header exists to prevent.
	now := time.Now()
	s.quotes.now = func() time.Time { return now }

	first := quoteGET(t, s, "/api/quote/601899?range=3m")
	now = now.Add(time.Minute)
	second := quoteGET(t, s, "/api/quote/601899?range=3m")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("codes %d/%d", first.Code, second.Code)
	}
	if tencent.n() != 1 {
		t.Errorf("two page views cost %d upstream calls, want 1", tencent.n())
	}
	if quoteBody(t, first).Cached {
		t.Error("the first response is not a cache hit")
	}
	if !quoteBody(t, second).Cached {
		t.Error("the second response must say it came from the cache")
	}
	// Same content either way — a cache that also changed the answer would be a different bug.
	quoteEqStr(t, "cached name", quoteBody(t, second).Name, quoteBody(t, first).Name)
	quoteEqInt(t, "cached bars", int64(len(quoteBody(t, second).Bars)), int64(len(quoteBody(t, first).Bars)))

	// Cache-Control carries what is LEFT, so a browser refresh cannot outlive the server's own copy.
	// A minute passed between the two, so the numbers are exact rather than merely ordered.
	firstAge := quoteMaxAge(t, first)
	secondAge := quoteMaxAge(t, second)
	if want := int(quoteTTLClosed / time.Second); firstAge != want {
		t.Errorf("the fetch that filled the cache advertised max-age=%d, want the full %d (the fixture's market is closed)", firstAge, want)
	}
	if want := firstAge - 60; secondAge != want {
		t.Errorf("a minute later the cache hit advertised max-age=%d, want %d: the header carries the entry's REMAINING life, not the full TTL", secondAge, want)
	}

	// And the entry itself was never stamped. The handler answers from a COPY, so the value every
	// other request shares still reads Cached=false — otherwise whoever fetches it next is told
	// they were served a cache hit that they in fact paid for.
	if entry, _, ok := s.quotes.get(quoteCacheKey("sh", "601899", quoteBarsForRange("3m"))); !ok {
		t.Error("the entry the two requests shared is gone")
	} else if entry.Cached {
		t.Error("the handler stamped Cached on the shared cache entry instead of on its own copy")
	}

	// A different range is a different vendor call and must not be answered from the 3m entry.
	if rec := quoteGET(t, s, "/api/quote/601899?range=1m"); quoteBody(t, rec).Cached {
		t.Error("a different range was served from another range's entry")
	}
	if tencent.n() != 2 {
		t.Errorf("after a second range: %d upstream calls, want 2", tencent.n())
	}
}

func quoteMaxAge(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	cc := rec.Header().Get("Cache-Control")
	if !strings.HasPrefix(cc, "private, max-age=") {
		t.Fatalf("Cache-Control = %q, want a private max-age", cc)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(cc, "private, max-age="))
	if err != nil {
		t.Fatalf("Cache-Control = %q: %v", cc, err)
	}
	return n
}

// WHICH TTL applies comes from the vendor's own session field, which is why there is no trading
// calendar in this repo to get the Spring Festival wrong every year. How LONG each one lasts is the
// admin's, and the shipped configuration is still these two constants.
func TestQuoteTTLFollowsTheVendorSession(t *testing.T) {
	def := quoteConfigDefault()
	open := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionOpen}}
	if got := def.ttlFor(open); got != quoteTTLOpen {
		t.Errorf("an open market caches for %v, want %v", got, quoteTTLOpen)
	}
	for _, session := range []string{quoteSessionClose, quoteSessionUnknown, ""} {
		resp := &QuoteResp{Snapshot: QuoteSnapshot{Session: session}}
		if got := def.ttlFor(resp); got != quoteTTLClosed {
			t.Errorf("session %q caches for %v, want %v", session, got, quoteTTLClosed)
		}
	}
	if quoteTTLOpen >= quoteTTLClosed {
		t.Error("a trading market must be refreshed more often than a closed one")
	}

	// The session decides which of the CONFIGURED two, not which of the constants: an admin who
	// shortened both must not have the open-market entry silently keep the shipped 30s.
	tuned := quoteConfig{Order: quoteSourceNames(), TTLOpen: 7 * time.Second, TTLClosed: 90 * time.Second}
	if got := tuned.ttlFor(open); got != 7*time.Second {
		t.Errorf("an open market under a tuned config caches for %v, want 7s", got)
	}
	if got := tuned.ttlFor(&QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}); got != 90*time.Second {
		t.Errorf("a closed market under a tuned config caches for %v, want 90s", got)
	}
	// A zero config is the SHIPPED behaviour, never a zero TTL: a value struct that reached the
	// cache unfilled would otherwise expire every entry the moment it was stored.
	if got := (quoteConfig{}).ttlFor(open); got != quoteTTLOpen {
		t.Errorf("a zero config caches an open market for %v, want the shipped %v", got, quoteTTLOpen)
	}

	// And it is read from the response, not decided by the handler: the same fixture with its SH
	// segment flipped to open takes the short TTL.
	body := readQuoteFixture(t, fixTencentSH)
	closed, err := parseTencentQuote("sh", "601899", body, 60)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	trading, err := parseTencentQuote("sh", "601899", []byte(strings.Replace(string(body), "SH_close_", "SH_open_", 1)), 60)
	if err != nil {
		t.Fatalf("parse (open): %v", err)
	}
	if def.ttlFor(closed) != quoteTTLClosed || def.ttlFor(trading) != quoteTTLOpen {
		t.Errorf("captured session %q → %v, flipped to %q → %v",
			closed.Snapshot.Session, def.ttlFor(closed), trading.Snapshot.Session, def.ttlFor(trading))
	}
}

// An entry past its TTL is a miss, and it is dropped rather than merely ignored, so a symbol nobody
// asks for again stops occupying the byte budget.
func TestQuoteCacheExpires(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))
	// The clock is a field on the cache, not a package var, so this cannot disturb another test.
	now := time.Now()
	s.quotes.now = func() time.Time { return now }

	quoteGET(t, s, "/api/quote/601899")
	quoteGET(t, s, "/api/quote/601899")
	if tencent.n() != 1 {
		t.Fatalf("within the TTL: %d calls, want 1", tencent.n())
	}
	now = now.Add(quoteTTLClosed + time.Second)
	if rec := quoteGET(t, s, "/api/quote/601899"); quoteBody(t, rec).Cached {
		t.Error("an expired entry was still served as a cache hit")
	}
	if tencent.n() != 2 {
		t.Errorf("past the TTL: %d calls, want 2", tencent.n())
	}
}

// Both bounds are live. The entry count is what binds when the ranges are short, and the byte
// budget is what stops a caller who only ever asks for 1y from turning the same 256 keys into 5 MB.
func TestQuoteCacheIsBoundedOnEntriesAndBytes(t *testing.T) {
	var c quoteCache
	small := &QuoteResp{Symbol: "601899", Snapshot: QuoteSnapshot{Session: quoteSessionClose}}
	for i := 0; i < quoteCacheMaxEntries*3; i++ {
		c.put("small:"+strconv.Itoa(i), small, quoteTTLClosed)
	}
	if len(c.entries) != quoteCacheMaxEntries || c.bytes > quoteCacheMaxBytes {
		t.Errorf("after %d small entries: %d entries / %d bytes", quoteCacheMaxEntries*3, len(c.entries), c.bytes)
	}

	var fat quoteCache
	big := &QuoteResp{Symbol: "601899", Bars: make([]QuoteBar, quoteRange1Y)}
	for i := range big.Bars {
		big.Bars[i].Date = "2026-09-04"
	}
	for i := 0; i < quoteCacheMaxEntries; i++ {
		fat.put("big:"+strconv.Itoa(i), big, quoteTTLClosed)
	}
	if fat.bytes > quoteCacheMaxBytes {
		t.Errorf("byte budget exceeded: %d > %d", fat.bytes, quoteCacheMaxBytes)
	}
	if len(fat.entries) >= quoteCacheMaxEntries {
		t.Errorf("%d full-year entries fit under the entry cap; the byte budget never bound", len(fat.entries))
	}
	// Eviction is least-recently-used, so the newest survivors are the ones still there.
	if _, _, ok := fat.get("big:" + strconv.Itoa(quoteCacheMaxEntries-1)); !ok {
		t.Error("the most recent entry was evicted")
	}
	if _, _, ok := fat.get("big:0"); ok {
		t.Error("the oldest entry survived a full eviction sweep")
	}
}

// ---------- single flight ----------

// Twenty browsers opening the same stock in the same millisecond all miss the cache together. Without
// a single-flight that is twenty identical requests aimed at one vendor; with it, one.
func TestQuoteAPISingleFlightsConcurrentReaders(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	tencent.gate = make(chan struct{})
	wireQuoteSources(s, tencent, sinaStub(t))

	const readers = 20
	var wg sync.WaitGroup
	codes := make([]int, readers)
	cached := make([]bool, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := quoteGET(t, s, "/api/quote/601899?range=3m")
			codes[i] = rec.Code
			if rec.Code == http.StatusOK {
				var out QuoteResp
				json.Unmarshal(rec.Body.Bytes(), &out)
				cached[i] = out.Cached
			}
		}(i)
	}
	// Wait for the leader to be inside the (blocked) fetch, then give the other nineteen a moment to
	// pile up behind it. The assertion below holds either way — a latecomer that arrives after the
	// leader finished is served from the cache — but without the pause this could pass by racing
	// past the single-flight instead of through it.
	for tencent.n() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(tencent.gate)
	wg.Wait()

	if tencent.n() != 1 {
		t.Errorf("%d concurrent readers cost %d upstream calls, want 1", readers, tencent.n())
	}
	fresh := 0
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("reader %d → %d", i, code)
		}
		if !cached[i] {
			fresh++
		}
	}
	// Exactly one caller paid for the fetch; the other nineteen were handed its answer.
	if fresh != 1 {
		t.Errorf("%d readers reported a fresh fetch, want exactly 1", fresh)
	}
}

// A failure is shared with the waiters too, rather than each of them starting its own retry against
// a vendor that has just proved it is down.
func TestQuoteSingleFlightSharesAFailure(t *testing.T) {
	s := quoteServer(t)
	tencent := failingStub("tencent is down")
	tencent.gate = make(chan struct{})
	sina := failingStub("sina is down")
	wireQuoteSources(s, tencent, sina)

	const readers = 8
	var wg sync.WaitGroup
	codes := make([]int, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = quoteGET(t, s, "/api/quote/601899").Code
		}(i)
	}
	for tencent.n() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(tencent.gate)
	wg.Wait()

	if tencent.n() != 1 {
		t.Errorf("a failing vendor was called %d times by %d readers, want 1", tencent.n(), readers)
	}
	for i, code := range codes {
		if code != http.StatusServiceUnavailable {
			t.Errorf("reader %d → %d, want 503", i, code)
		}
	}
}

// The ceiling exists so that a caller enumerating the market cannot turn this portal into an
// amplifier: however many symbols are asked for at once, only a few conversations with a vendor are
// open at a time.
func TestQuoteUpstreamConcurrencyIsBounded(t *testing.T) {
	s := quoteServer(t)
	// The captured sh601899 response with exactly one thing changed — its code — so that each of the
	// symbols below gets a real vendor body that passes the code-echo gate.
	body := readQuoteFixture(t, fixTencentSH)
	gate := make(chan struct{})
	var mu sync.Mutex
	inFlight, peak := 0, 0
	counting := &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-gate
		mu.Lock()
		inFlight--
		mu.Unlock()
		return parseTencentQuote(market, code, bytes.ReplaceAll(body, []byte("601899"), []byte(code)), bars)
	}}
	// Every symbol is a different cache key, so the single-flight cannot mask the ceiling here.
	wireQuoteSources(s, counting, sinaStub(t))

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			quoteGET(t, s, "/api/quote/60"+strconv.Itoa(1000+i))
		}(i)
	}
	for counting.n() < quoteUpstreamConcurrency {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > quoteUpstreamConcurrency {
		t.Errorf("%d vendor calls were open at once, ceiling is %d", peak, quoteUpstreamConcurrency)
	}
	if peak < quoteUpstreamConcurrency {
		t.Errorf("peak concurrency %d never reached the ceiling %d; the test proved nothing", peak, quoteUpstreamConcurrency)
	}
}

// ---------- the Beijing scrub, against a vendor that starts sending bars ----------

// quoteBeijingBodyWithBars is the captured SHANGHAI response relabelled as bj830799 — sixty real
// bars and all. It is the one thing no captured fixture can be, because no vendor sends it today: a
// Beijing response that carries history. Deleting the handler's bj block leaves every other test
// green precisely because the real bj fixture already parses to zero bars, so the promise that a bj
// response never carries bars WHATEVER a vendor starts returning has to be tested against a vendor
// that has started returning them.
func quoteBeijingBodyWithBars(t *testing.T) []byte {
	t.Helper()
	body := bytes.ReplaceAll(readQuoteFixture(t, fixTencentSH), []byte("sh601899"), []byte("bj830799"))
	return bytes.ReplaceAll(body, []byte("601899"), []byte("830799"))
}

func TestQuoteAPIStripsBarsAVendorStartsSendingForBeijing(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(quoteBeijingBodyWithBars(t))
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	// The parser hands the handler sixty bars and names a source for them, so the handler is the
	// only thing that can take them away — which is the whole of the assertion below.
	parsed, err := parseTencentQuote("bj", "830799", quoteBeijingBodyWithBars(t), quoteRange3M)
	if err != nil {
		t.Fatalf("the relabelled fixture no longer parses: %v", err)
	}
	if len(parsed.Bars) == 0 || parsed.BarsSource != quoteSourceTencent || parsed.BarsUnavailable != "" {
		t.Fatalf("the stub must hand the handler real bars: %d bars from %q (%q)",
			len(parsed.Bars), parsed.BarsSource, parsed.BarsUnavailable)
	}

	for _, pass := range []string{"fresh", "from the cache"} {
		rec := quoteGET(t, s, "/api/quote/830799?range=3m")
		if rec.Code != http.StatusOK {
			t.Fatalf("bj quote (%s) → %d (%s)", pass, rec.Code, rec.Body.String())
		}
		got := quoteBody(t, rec)
		if len(got.Bars) != 0 {
			t.Errorf("a bj response (%s) carried %d bars; neither vendor has trustworthy bj history", pass, len(got.Bars))
		}
		quoteEqStr(t, "barsUnavailable ("+pass+")", got.BarsUnavailable, quoteBarsMarketUnsupported)
		quoteEqStr(t, "barsSource ("+pass+")", got.BarsSource, "")
		if !strings.Contains(rec.Body.String(), `"bars":[]`) {
			t.Errorf("bars must serialize as [] (%s), got %s", pass, rec.Body.String())
		}
		// The snapshot half is still served: Beijing's quote is real, only its history is not.
		quoteEqInt(t, "last ("+pass+")", got.Snapshot.Last, 3335)
	}
}

// ---------- an abandoned leader ----------

// The first tab to arrive is the one that pays for the upstream call, and Go cancels its request
// context the moment that tab navigates away. Everyone else waiting on that flight is still there,
// and the vendor was never down: a link shared into a group chat used to answer nine healthy
// readers with 503 quote_unavailable because the tenth pressed Escape.
func TestQuoteLeaderCancellationStillServesItsFollowers(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	tencent.gate = make(chan struct{})
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	const bars = quoteRange3M
	leaderCtx, abandonLeader := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		s.quotes.fetch(leaderCtx, "sh", "601899", bars)
	}()
	for tencent.n() == 0 {
		time.Sleep(time.Millisecond)
	}

	const followers = 9
	var wg sync.WaitGroup
	resps := make([]*QuoteResp, followers)
	errs := make([]error, followers)
	for i := 0; i < followers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resps[i], _, _, errs[i] = s.quotes.fetch(context.Background(), "sh", "601899", bars)
		}(i)
	}
	// Long enough for all nine to be parked on the leader's flight. A straggler that arrives after
	// the leader has finished is served from the cache and still satisfies the assertions below.
	time.Sleep(20 * time.Millisecond)
	abandonLeader()
	time.Sleep(10 * time.Millisecond)
	close(tencent.gate)
	wg.Wait()
	<-leaderDone

	for i := range errs {
		if errs[i] != nil {
			t.Errorf("follower %d got %v; its own request was never cancelled", i, errs[i])
			continue
		}
		if resps[i] == nil || resps[i].Snapshot.Last != 3335 {
			t.Errorf("follower %d got %+v, want the leader's answer", i, resps[i])
		}
	}
	if tencent.n() != 1 {
		t.Errorf("the abandoned flight cost %d upstream calls, want 1", tencent.n())
	}
	if sina.n() != 0 {
		t.Errorf("the fallback was called %d time(s) for a primary that answered", sina.n())
	}
	// And the health counters record what happened to the VENDOR, which is that it answered. A
	// client disconnect written into them is an outage an operator would go looking for.
	health := s.QuoteHealth()
	if len(health) != 1 || health[0].Source != quoteSourceTencent {
		t.Fatalf("health = %+v, want one row, for the source that actually answered", health)
	}
	if health[0].Failures != 0 || health[0].LastError != "" || health[0].LastSuccess.IsZero() {
		t.Errorf("a cancelled leader was recorded as a vendor failure: %+v", health[0])
	}
}

// The counters again, one layer down. load can still be handed a context that dies under it — the
// detached one has a deadline of its own — and a failure that arrives because THIS side stopped
// waiting is not evidence about the vendor.
func TestQuoteLoadDoesNotBlameAVendorForACancelledCaller(t *testing.T) {
	var c quoteCache
	ctx, cancel := context.WithCancel(context.Background())
	// The cancellation happens INSIDE the first vendor call, which is where a browser closing a tab
	// lands: cancelling before load starts would be refused by acquire and never reach a source at
	// all, so the counters would go untouched for a reason that proves nothing.
	tencent := &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		cancel()
		return nil, context.Canceled
	}}
	sina := sinaStub(t)
	c.sources = []quoteSource{
		{name: quoteSourceTencent, fetch: tencent.fetch},
		{name: quoteSourceSina, fetch: sina.fetch},
	}

	if _, err := c.load(ctx, quoteConfigDefault(), "sh", "601899", quoteRange3M); !errors.Is(err, context.Canceled) {
		t.Fatalf("load under a caller that walked away = %v, want context.Canceled", err)
	}
	if health := c.snapshotHealth(); len(health) != 0 {
		t.Errorf("a client disconnect was recorded as a vendor outage: %+v", health)
	}

	// And the guard is not a blanket "never record anything": a vendor that fails on a live context
	// still counts, or the health panel would be blind to a real outage.
	var live quoteCache
	live.sources = []quoteSource{
		{name: quoteSourceTencent, fetch: failingStub("tencent is down").fetch},
		{name: quoteSourceSina, fetch: failingStub("sina is down").fetch},
	}
	if _, err := live.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M); err == nil {
		t.Fatal("two failing vendors returned no error")
	}
	for _, h := range live.snapshotHealth() {
		if h.Failures != 1 {
			t.Errorf("a real vendor failure was not counted: %+v", h)
		}
	}
}

// load reports the FIRST error, not the last: the primary's reason is the one worth reporting, and
// the fallback's is usually the less interesting "and that one too".
func TestQuoteLoadReportsTheFirstError(t *testing.T) {
	var c quoteCache
	c.sources = []quoteSource{
		{name: quoteSourceTencent, fetch: failingStub("tencent is down").fetch},
		{name: quoteSourceSina, fetch: failingStub("sina is down").fetch},
	}
	_, err := c.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M)
	if err == nil {
		t.Fatal("two failing vendors returned no error")
	}
	if !strings.Contains(err.Error(), "tencent is down") {
		t.Errorf("load reported %q, want the primary's reason", err)
	}

	// An empty source list is its own answer rather than a nil response escaping as a success.
	var empty quoteCache
	empty.sources = []quoteSource{}
	if _, err := empty.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M); !errors.Is(err, errQuoteNoSources) {
		t.Errorf("a cache with no sources returned %v, want %v", err, errQuoteNoSources)
	}
}

// ---------- the single-flight's two orderings ----------

// The re-check after the flight is registered. A request that arrived while the PREVIOUS flight was
// between storing its answer and deleting its map entry would otherwise start a second identical
// upstream call against a cache that already holds the answer.
//
// That window is microseconds wide in production, so the test holds it open with flightMu — which is
// exactly the lock such an arrival has to wait on — and fills the cache while the arrival is parked.
func TestQuoteFlightRechecksTheCacheBeforeCallingAVendor(t *testing.T) {
	body := readQuoteFixture(t, fixTencentSH)
	vendor := &quoteVendorStub{parse: func(market, code string, bars int) (*QuoteResp, error) {
		return parseTencentQuote(market, code, body, bars)
	}}
	fresh, err := parseTencentQuote("sh", "601899", body, quoteRange3M)
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}

	var c quoteCache
	c.sources = []quoteSource{{name: quoteSourceTencent, fetch: vendor.fetch}}
	// The clock is both a seam and a tripwire. get reads it only when it finds an entry, so the
	// cache is primed with one that has EXPIRED: the arrival's read of the clock is then the signal
	// that it has already missed and is on its way to the flight registration.
	var mu sync.Mutex
	now := time.Now()
	clockReads := 0
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clockReads++
		return now
	}
	c.init()

	key := quoteCacheKey("sh", "601899", quoteRange3M)
	c.put(key, &QuoteResp{Symbol: "601899"}, quoteTTLClosed)
	mu.Lock()
	now = now.Add(quoteTTLClosed + time.Second)
	baseline := clockReads
	mu.Unlock()

	c.flightMu.Lock() // the door the arrival has to come through
	var cached bool
	var fetchErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, cached, _, fetchErr = c.fetch(context.Background(), "sh", "601899", quoteRange3M)
	}()
	for {
		mu.Lock()
		seen := clockReads
		mu.Unlock()
		if seen > baseline {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// The previous flight's answer lands while the arrival is parked on flightMu — which is exactly
	// the window between a put and the endFlight that follows it.
	c.put(key, fresh, quoteTTLClosed)
	c.flightMu.Unlock()
	<-done

	if fetchErr != nil {
		t.Fatalf("the arrival got %v", fetchErr)
	}
	if vendor.n() != 0 {
		t.Errorf("the answer was already in the cache and a vendor was called %d time(s) anyway", vendor.n())
	}
	if !cached {
		t.Error("an answer taken from the cache reported itself as a fresh upstream fetch")
	}
}

// endFlight deletes the map entry BEFORE it closes the channel, so a goroutine woken by that close
// cannot still find the finished flight and join it. It matters most when the flight FAILED: a
// failure is never cached, so a latecomer joining it is handed a stale error instead of retrying a
// vendor that may well be answering again.
//
// The order is observable by holding flightMu: with the delete first, endFlight blocks on the lock
// before it can close anything, so no waiter can be woken while the entry is still in the map.
func TestQuoteEndFlightDeletesBeforeItCloses(t *testing.T) {
	vendor := failingStub("tencent is down")
	vendor.gate = make(chan struct{})
	var c quoteCache
	c.sources = []quoteSource{{name: quoteSourceTencent, fetch: vendor.fetch}}
	c.init()

	key := quoteCacheKey("sh", "601899", quoteRange3M)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.fetch(context.Background(), "sh", "601899", quoteRange3M)
	}()
	for vendor.n() == 0 {
		time.Sleep(time.Millisecond)
	}

	c.flightMu.Lock()
	fl := c.flights[key]
	if fl == nil {
		c.flightMu.Unlock()
		t.Fatal("the leader registered no flight")
	}
	close(vendor.gate) // the leader fails and walks straight into endFlight
	time.Sleep(20 * time.Millisecond)
	select {
	case <-fl.done:
		c.flightMu.Unlock()
		<-done
		t.Fatal("the flight's channel closed while its map entry was still registered: a goroutine woken by it can join a flight that has already answered")
	default:
	}
	c.flightMu.Unlock()
	<-done

	c.flightMu.Lock()
	left := len(c.flights)
	c.flightMu.Unlock()
	if left != 0 {
		t.Errorf("%d finished flight(s) still registered", left)
	}
}

// ---------- the small guarantees the handler makes ----------

// max-age is floored at one second. Zero tells the browser to revalidate immediately, which turns
// the last moment of every cache entry's life into the stampede the header exists to prevent.
func TestQuoteMaxAgeNeverAdvertisesZero(t *testing.T) {
	for _, c := range []struct {
		ttl  time.Duration
		want int
	}{
		{0, 1},
		{-time.Second, 1},
		{time.Millisecond, 1},
		{999 * time.Millisecond, 1},
		{time.Second, 1},
		{2500 * time.Millisecond, 2},
		{quoteTTLOpen, int(quoteTTLOpen / time.Second)},
		{quoteTTLClosed, int(quoteTTLClosed / time.Second)},
	} {
		if got := quoteMaxAgeSeconds(c.ttl); got != c.want {
			t.Errorf("quoteMaxAgeSeconds(%v) = %d, want %d", c.ttl, got, c.want)
		}
	}
}

// An expired entry is DROPPED, not merely reported as a miss: the byte budget is the claim, and a
// symbol nobody asks for again has to stop occupying it the moment anyone notices it has expired.
func TestQuoteCacheDropsTheEntryItReportsAsAMiss(t *testing.T) {
	var c quoteCache
	now := time.Now()
	c.now = func() time.Time { return now }
	c.init()

	const key = "sh601899:250"
	resp := &QuoteResp{Symbol: "601899", Bars: make([]QuoteBar, quoteRange1Y)}
	c.put(key, resp, quoteTTLClosed)
	c.mu.Lock()
	entries, bytes := len(c.entries), c.bytes
	c.mu.Unlock()
	if entries != 1 || bytes <= 0 {
		t.Fatalf("after one put: %d entries / %d bytes", entries, bytes)
	}

	now = now.Add(quoteTTLClosed + time.Second)
	if _, _, ok := c.get(key); ok {
		t.Fatal("an expired entry was served as a hit")
	}
	c.mu.Lock()
	entries, bytes = len(c.entries), c.bytes
	c.mu.Unlock()
	if entries != 0 || bytes != 0 {
		t.Errorf("the expired entry still costs %d entries / %d bytes of the budget, want 0/0", entries, bytes)
	}
}

// bars is QuoteBar[] in the TypeScript contract, and a JSON null there is a runtime error in the
// chart rather than an empty chart.
func TestQuoteVisibleBarsIsNeverNull(t *testing.T) {
	empty := quoteVisibleBars("sh", nil)
	if empty == nil {
		t.Fatal("a nil slice reached the encoder; it marshals to null, and the SPA maps over it")
	}
	b, err := json.Marshal(map[string]any{"bars": empty})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"bars":[]}` {
		t.Errorf("nil bars serialized as %s, want {\"bars\":[]}", b)
	}

	real := []QuoteBar{{Date: "2026-09-04", Open: 3385, High: 3405, Low: 3307, Close: 3335}}
	if got := quoteVisibleBars("sh", real); len(got) != 1 || got[0] != real[0] {
		t.Errorf("a real series was not passed through: %+v", got)
	}
	if got := quoteVisibleBars("bj", real); len(got) != 0 {
		t.Errorf("bj kept %d bars; neither vendor has trustworthy bj history", len(got))
	}
}

// The four ranges are an allowlist AND the count is clamped after it, which is belt and braces on
// purpose: the map is the thing an edit could widen by accident. Every value it holds today is
// already inside the clamp, so the only way to see the clamp bind is to widen the map here.
func TestQuoteBarsForRangeClampsWhateverTheMapHolds(t *testing.T) {
	for key, want := range map[string]int{"1m": quoteRange1M, "3m": quoteRange3M, "6m": quoteRange6M, "1y": quoteRange1Y} {
		if got := quoteBarsForRange(key); got != want {
			t.Errorf("range %q maps to %d bars, want %d", key, got, want)
		}
	}
	if got, want := quoteBarsForRange("nonsense"), quoteBarsForRange(quoteRangeDefault); got != want {
		t.Errorf("an unknown range asked for %d bars, want the default's %d", got, want)
	}

	quoteRangeBars["test-widened"] = quoteMaxBars * 10
	quoteRangeBars["test-zero"] = 0
	defer func() {
		delete(quoteRangeBars, "test-widened")
		delete(quoteRangeBars, "test-zero")
	}()
	if got := quoteBarsForRange("test-widened"); got != quoteMaxBars {
		t.Errorf("a range mapped to %d bars reached the vendor URL as %d, want the %d clamp", quoteMaxBars*10, got, quoteMaxBars)
	}
	if got := quoteBarsForRange("test-zero"); got != quoteDefaultBars {
		t.Errorf("a range mapped to zero bars reached the vendor URL as %d, want %d", got, quoteDefaultBars)
	}
}

// ---------- the wider symbol space ----------

// The four fields the strip cannot render without: which market answered, whether the thing is an
// instrument or an index, what currency the number is in, and which zone the instant belongs to.
// They are asserted THROUGH the endpoint rather than off the parser (quote_test.go already pins the
// parser) because the handler is what serves them, and a handler that dropped Currency would leave
// 319.97 next to a Chinese report reading as yuan to every reader.
func TestQuoteAPIServesEveryMarketsOwnLabels(t *testing.T) {
	for _, c := range []struct {
		path                             string
		fixture                          string
		market, kind, currency, tz, name string
		last                             int64
	}{
		{"/api/quote/601899", fixTencentSH, "sh", quoteKindStock, "CNY", "Asia/Shanghai", "紫金矿业", 3335},
		{"/api/quote/000001", fixTencentSZ, "sz", quoteKindStock, "CNY", "Asia/Shanghai", "平安银行", 1189},
		{"/api/quote/00700", fixTencentHK, "hk", quoteKindStock, "HKD", "Asia/Hong_Kong", "腾讯控股", 44020},
		{"/api/quote/AAPL", fixTencentUS, "us", quoteKindStock, "USD", "America/New_York", "苹果", 31997},
		// The explicit form is the only way to reach 上证指数: the bare six digits keep meaning what
		// marketPrefix has always said they mean, and marketPrefix maps 000001 to Shenzhen — which
		// is the row above, from the same six digits, answering with a different company.
		{"/api/quote/sh:000001", fixTencentIndex, "sh", quoteKindIndex, "CNY", "Asia/Shanghai", "上证指数", 393299},
	} {
		t.Run(c.path, func(t *testing.T) {
			s := quoteServer(t)
			wireQuoteSources(s, tencentStub(readQuoteFixture(t, c.fixture)), sinaStub(t))
			rec := quoteGET(t, s, c.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s → %d (%s)", c.path, rec.Code, rec.Body.String())
			}
			got := quoteBody(t, rec)
			quoteEqStr(t, "market", got.Market, c.market)
			quoteEqStr(t, "kind", got.Kind, c.kind)
			quoteEqStr(t, "currency", got.Currency, c.currency)
			quoteEqStr(t, "tz", got.TZ, c.tz)
			quoteEqStr(t, "name", got.Name, c.name)
			quoteEqInt(t, "last", got.Snapshot.Last, c.last)
		})
	}
}

// A US ticker is case-insensitive on the way in and canonical from there on. The assertion that
// matters is the CALL COUNT: two spellings that each got their own cache key would be two vendor
// calls for one stock, and the second reader would be told they had paid for a fresh fetch.
func TestQuoteAPICanonicalisesAUSTicker(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentUS))
	wireQuoteSources(s, tencent, sinaStub(t))

	lower := quoteGET(t, s, "/api/quote/aapl")
	upper := quoteGET(t, s, "/api/quote/AAPL")
	explicit := quoteGET(t, s, "/api/quote/us:AaPl")
	for _, rec := range []*httptest.ResponseRecorder{lower, upper, explicit} {
		if rec.Code != http.StatusOK {
			t.Fatalf("→ %d (%s)", rec.Code, rec.Body.String())
		}
		quoteEqStr(t, "symbol", quoteBody(t, rec).Symbol, "AAPL")
	}
	if tencent.n() != 1 {
		t.Errorf("three spellings of one ticker cost %d upstream calls, want 1", tencent.n())
	}
	if quoteBody(t, lower).Cached {
		t.Error("the first spelling paid for the fetch and must not report itself as cached")
	}
	if !quoteBody(t, upper).Cached || !quoteBody(t, explicit).Cached {
		t.Error("a second spelling of the same ticker was served as a fresh fetch")
	}
}

// The US series is two rows fifteen years apart — real candles that pass every check in the gate and
// draw as a straight line labelled 3个月. The parser reports what the vendor sent; taking them away
// is the handler's job, and this is the market where that is observable, because the captured body
// really does carry bars (unlike bj, whose fixture is already empty).
func TestQuoteAPIStripsTheTwoBarsTheVendorSendsForTheUS(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentUS))
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	// The stub hands the handler real bars, so the handler is the only thing that can remove them.
	parsed, err := parseTencentQuote("us", "AAPL", readQuoteFixture(t, fixTencentUS), quoteRange3M)
	if err != nil {
		t.Fatalf("parse the captured US body: %v", err)
	}
	if len(parsed.Bars) != 2 || parsed.BarsSource != quoteSourceTencent {
		t.Fatalf("the stub must hand the handler bars: %d bar(s) from %q", len(parsed.Bars), parsed.BarsSource)
	}

	for _, pass := range []string{"fresh", "from the cache"} {
		rec := quoteGET(t, s, "/api/quote/AAPL?range=3m")
		if rec.Code != http.StatusOK {
			t.Fatalf("us quote (%s) → %d (%s)", pass, rec.Code, rec.Body.String())
		}
		got := quoteBody(t, rec)
		if len(got.Bars) != 0 {
			t.Errorf("a us response (%s) carried %d bars; the vendor's series is two rows fifteen years apart", pass, len(got.Bars))
		}
		quoteEqStr(t, "barsUnavailable ("+pass+")", got.BarsUnavailable, quoteBarsMarketUnsupported)
		quoteEqStr(t, "barsSource ("+pass+")", got.BarsSource, "")
		if !strings.Contains(rec.Body.String(), `"bars":[]`) {
			t.Errorf("bars must serialize as [] (%s), got %s", pass, rec.Body.String())
		}
		// The snapshot half is still served: the US quote is real, only its history is not.
		quoteEqInt(t, "last ("+pass+")", got.Snapshot.Last, 31997)
	}
	if sina.n() != 0 {
		t.Errorf("a us symbol reached the A-share fallback %d time(s)", sina.n())
	}
}
