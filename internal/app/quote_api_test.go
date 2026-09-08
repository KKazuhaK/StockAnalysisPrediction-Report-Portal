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
	"sort"
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
	// parseAt is parse for a stub standing in for an INTERVAL-AWARE source. It exists because the
	// real Tencent source is one: it has two endpoints, and a stub wired through the interval-blind
	// fetcher would let a test assert that a 分时 request was served while the thing it was served
	// from was the daily fixture.
	parseAt func(market, code string, bars int, iv quoteInterval) (*QuoteResp, error)
	// parseBatch is the many-symbols shape. Left nil, fetchBatch below answers each target through
	// `parse` — still as ONE counted call, because the count is the claim the batch endpoint makes
	// and a stub that inflated it would make that claim untestable.
	parseBatch func(targets []quoteTarget) (map[string]*QuoteResp, error)
	// fetchRangeFn is the BOUNDED-WINDOW shape. It is a slot of its own here for the same reason it
	// is one on the real source: a stub that answered a windowed request from `parse` would let a
	// test assert that the reader's dates were honoured while what it actually got was the last N
	// sessions — which is the exact defect the separate fetcher exists to prevent.
	fetchRangeFn func(market, code, from, to string) (*QuoteResp, error)
}

// fetchRange is wired onto the source only when the stub sets fetchRangeFn, so a stub without one
// behaves like a vendor that never declared the window.
func (v *quoteVendorStub) fetchRange(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.calls++
	v.mu.Unlock()
	if v.err != nil {
		return nil, v.err
	}
	return v.fetchRangeFn(market, code, from, to)
}

// fetchBatch is the batch shape, and it increments the counter EXACTLY ONCE however many symbols it
// is handed. That is not a convenience: "a page of fifty cards is one upstream call" is a number,
// and this is the thing that makes it observable.
func (v *quoteVendorStub) fetchBatch(ctx context.Context, targets []quoteTarget) (map[string]*QuoteResp, error) {
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
	if v.parseBatch != nil {
		return v.parseBatch(targets)
	}
	out := map[string]*QuoteResp{}
	for _, t := range targets {
		resp, err := v.parse(t.Market.id, t.Code, 0)
		if err != nil {
			// Per symbol, as the real parser is: a body that does not describe this code is that
			// code's problem and not the batch's.
			continue
		}
		out[t.Symbol] = resp
	}
	return out, nil
}

// fetchAt is the interval-aware shape. It counts and gates identically — the two must not diverge,
// or a test that asserts a call count against one of them is asserting nothing about the other.
func (v *quoteVendorStub) fetchAt(ctx context.Context, market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
	if v.parseAt == nil {
		return v.fetch(ctx, market, code, bars)
	}
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
	return v.parseAt(market, code, bars, iv)
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
// The interval is a DAILY range's, narrowed by what the enabled sources declare — the same two steps
// apiQuote takes — because every caller of this helper is one of the four daily ranges. A test that
// wants a 分时 fetch says so by calling fetchUnder itself, which is the only way the interval can be
// part of what it asserts.
func (c *quoteCache) fetch(ctx context.Context, market, code string, bars int) (*QuoteResp, bool, time.Duration, error) {
	cfg := quoteConfigDefault()
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, false, 0, err
	}
	iv := c.servableInterval(cfg, target, quoteRangeFor("3m").interval)
	return c.fetchUnder(ctx, cfg, market, code, bars, iv, quoteWindow{})
}

func failingStub(msg string) *quoteVendorStub {
	return &quoteVendorStub{err: errors.New(msg)}
}

// wireQuoteSources installs the two vendors in failover order, carrying the capability declarations
// defaultQuoteSources gives them — the shipped ones themselves, with only the fetcher swapped for a
// stub, not a copy of them written out here. Wiring bare stubs instead would quietly hand Sina the
// two markets its parser refuses, and a restated declaration would let this suite go on testing a
// source table this portal had stopped running.
// It swaps whichever fetcher the real source uses rather than always setting `fetch`: Tencent is
// wired through fetchAt (it has two endpoints and only the interval says which), and quoteSource.call
// prefers that field — so assigning `fetch` alone would leave the REAL fetcher in place and every
// test in this file would go to the network.
func wireQuoteSources(s *Server, tencent, sina *quoteVendorStub) {
	stubs := map[string]*quoteVendorStub{quoteSourceTencent: tencent, quoteSourceSina: sina}
	srcs := defaultQuoteSources()
	for i := range srcs {
		stub, ok := stubs[srcs[i].name]
		if !ok {
			continue
		}
		if srcs[i].fetchBatch != nil {
			// Replaced UNCONDITIONALLY wherever the real source has one, so that a test which
			// touches /api/quotes without thinking about batching still cannot reach the network.
			srcs[i].fetchBatch = stub.fetchBatch
		}
		if srcs[i].fetchRange != nil {
			// Same rule for the windowed fetcher, and the same reason: a test that sends from= and
			// to= without having thought about windows must not reach a vendor. A stub that set no
			// fetchRangeFn keeps the DECLARATION — so the resolver still routes to it — and fails
			// when called, which is what an unwired source should look like.
			srcs[i].fetchRange = stub.fetchRange
		}
		if srcs[i].fetchAt != nil {
			srcs[i].fetchAt = stub.fetchAt
			continue
		}
		srcs[i].fetch = stub.fetch
	}
	s.quotes.sources = srcs
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
	if _, _, ok := s.quotes.get(quoteCacheKey("sh", "601899", quoteBarsForRange("3m"), quoteIntervalDaily, quoteWindow{})); ok {
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
	if entry, _, ok := s.quotes.get(quoteCacheKey("sh", "601899", quoteBarsForRange("3m"), quoteIntervalDaily, quoteWindow{})); !ok {
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
	if got := def.ttlFor(open, quoteIntervalDaily); got != quoteTTLOpen {
		t.Errorf("an open market caches for %v, want %v", got, quoteTTLOpen)
	}
	for _, session := range []string{quoteSessionClose, quoteSessionUnknown, ""} {
		resp := &QuoteResp{Snapshot: QuoteSnapshot{Session: session}}
		if got := def.ttlFor(resp, quoteIntervalDaily); got != quoteTTLClosed {
			t.Errorf("session %q caches for %v, want %v", session, got, quoteTTLClosed)
		}
	}
	if quoteTTLOpen >= quoteTTLClosed {
		t.Error("a trading market must be refreshed more often than a closed one")
	}

	// The session decides which of the CONFIGURED two, not which of the constants: an admin who
	// shortened both must not have the open-market entry silently keep the shipped 30s.
	tuned := quoteConfig{Order: quoteSourceNames(), TTLOpen: 7 * time.Second, TTLClosed: 90 * time.Second}
	if got := tuned.ttlFor(open, quoteIntervalDaily); got != 7*time.Second {
		t.Errorf("an open market under a tuned config caches for %v, want 7s", got)
	}
	if got := tuned.ttlFor(&QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}, quoteIntervalDaily); got != 90*time.Second {
		t.Errorf("a closed market under a tuned config caches for %v, want 90s", got)
	}
	// A zero config is the SHIPPED behaviour, never a zero TTL: a value struct that reached the
	// cache unfilled would otherwise expire every entry the moment it was stored.
	if got := (quoteConfig{}).ttlFor(open, quoteIntervalDaily); got != quoteTTLOpen {
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
	if def.ttlFor(closed, quoteIntervalDaily) != quoteTTLClosed || def.ttlFor(trading, quoteIntervalDaily) != quoteTTLOpen {
		t.Errorf("captured session %q → %v, flipped to %q → %v",
			closed.Snapshot.Session, def.ttlFor(closed, quoteIntervalDaily), trading.Snapshot.Session, def.ttlFor(trading, quoteIntervalDaily))
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

	if _, err := c.load(ctx, quoteConfigDefault(), "sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{}); !errors.Is(err, context.Canceled) {
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
	if _, err := live.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{}); err == nil {
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
	_, err := c.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{})
	if err == nil {
		t.Fatal("two failing vendors returned no error")
	}
	if !strings.Contains(err.Error(), "tencent is down") {
		t.Errorf("load reported %q, want the primary's reason", err)
	}

	// An empty source list is its own answer rather than a nil response escaping as a success.
	var empty quoteCache
	empty.sources = []quoteSource{}
	if _, err := empty.load(context.Background(), quoteConfigDefault(), "sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{}); !errors.Is(err, errQuoteNoSources) {
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

	key := quoteCacheKey("sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{})
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

	key := quoteCacheKey("sh", "601899", quoteRange3M, quoteIntervalDaily, quoteWindow{})
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
	if got := quoteVisibleBars(quoteIntervalDaily, real); len(got) != 1 || got[0] != real[0] {
		t.Errorf("a real series was not passed through: %+v", got)
	}
	// A SNAPSHOT request never carries bars whatever a vendor sent, which is how a market with no
	// drawable history — and an interval no enabled source serves — both arrive at an empty chart.
	if got := quoteVisibleBars(quoteIntervalSnapshot, real); len(got) != 0 {
		t.Errorf("a snapshot request kept %d bars; neither vendor has a series it asked for", len(got))
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

	quoteRanges["test-widened"] = quoteRangeSpec{bars: quoteMaxBars * 10}
	quoteRanges["test-zero"] = quoteRangeSpec{bars: 0}
	defer func() {
		delete(quoteRanges, "test-widened")
		delete(quoteRanges, "test-zero")
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

// ---------- stage 2: intervals ----------
//
// The captured bodies for the two endpoints this stage added. Each was fetched from the live feed on
// 2026-09-07 and each is here because something about it is a measurement rather than a claim: the
// A-share minute series is what a 分时 chart is drawn from, the Hong Kong one is the market where
// the volume column is already 股, and the US one is a single point with NO trading date, which is
// the reason the US has no intraday grant.
const (
	fixTencentMinuteSH = "tencent_minute_sh600519.json"
	fixTencentMinuteHK = "tencent_minute_hk00700.json"
	fixTencentMinuteBJ = "tencent_minute_bj830799.json"
	fixTencentMinuteUS = "tencent_minute_usAAPL.json"
	fixTencentBatch    = "tencent_batch_six.txt"

	// day/query — the same minute rows for FIVE sessions, and the source of the 5日 window. Each is
	// a measurement: Shanghai and Shenzhen answer 5 x 267 rows, Hong Kong 5 x 332, the US refuses
	// the market outright with {"code":-1,"msg":"param error"}, and Beijing answers five FULL
	// sessions dated APRIL 2025 — the suspended stock, which is why bj is declared for neither
	// intraday window rather than for one of them.
	fixTencentDaysSH = "tencent_days_sh600519.json"
	fixTencentDaysSZ = "tencent_days_sz000001.json"
	fixTencentDaysHK = "tencent_days_hk00700.json"
	fixTencentDaysBJ = "tencent_days_bj830799.json"
	fixTencentDaysUS = "tencent_days_usAAPL.json"
)

// A range is now two facts — how many points, and at what RESOLUTION — and they are read out of one
// table so that they cannot fall back differently. The interval column is the whole of stage 2: it
// used to be a function of the market, and 分时 and 5日 are the same market asking something else.
//
// Every row names an interval, the four daily ones included, and that is not cosmetic: a range says
// what the READER asked for, and 3个月 asks the same question in every market. What this deployment
// can do about it is the resolver's answer, asserted in the two tests below.
func TestQuoteRangeCarriesItsOwnInterval(t *testing.T) {
	for _, tc := range []struct {
		key  string
		bars int
		want quoteInterval
	}{
		{"1d", quoteRange1D, quoteIntervalIntraday},
		{"5d", quoteRange5D, quoteIntervalIntraday5D},
		{"1m", quoteRange1M, quoteIntervalDaily},
		{"3m", quoteRange3M, quoteIntervalDaily},
		{"6m", quoteRange6M, quoteIntervalDaily},
		{"1y", quoteRange1Y, quoteIntervalDaily},
		// And an unknown range falls back as ONE spec: the bar count and the interval come from the
		// same row, so a stale bookmark cannot ask for 分时's point count as a daily series.
		{"nonsense", quoteRange3M, quoteIntervalDaily},
		{"", quoteRange3M, quoteIntervalDaily},
	} {
		spec := quoteRangeFor(tc.key)
		if spec.bars != tc.bars {
			t.Errorf("range %q asks for %d points, want %d", tc.key, spec.bars, tc.bars)
		}
		if spec.interval != tc.want {
			t.Errorf("range %q asks for %q, want %q", tc.key, spec.interval, tc.want)
		}
	}
	// No row may leave it empty. An empty interval used to mean "ask the market table", which is how
	// a US daily request became a snapshot on a portal that had a source for it.
	for key, spec := range quoteRanges {
		if spec.interval == "" {
			t.Errorf("range %q names no interval; there is no market to fall back to any more", key)
		}
	}
}

// What a request ASKS for and what this deployment can SERVE are two questions, and the second one
// is why 5日 is not a 503 on a portal that has only the shipped sources.
//
// The alternative — letting the request fall through to "every source failed" — puts "行情源暂时不可用,
// 请稍后再试" over a gap that no amount of retrying closes, and takes the price, the name and the
// market badge off the screen with it.
func TestQuoteUnservableIntervalDegradesToTheSnapshotRatherThanA503(t *testing.T) {
	c := quoteShippedCache()
	cfg := quoteConfigDefault()
	for _, tc := range []struct {
		market string
		ask    quoteInterval
		want   quoteInterval
	}{
		// Served as asked, out of the box: Tencent declares both windows on these three, from two
		// endpoints on one host.
		{"sh", quoteIntervalIntraday, quoteIntervalIntraday},
		{"sz", quoteIntervalIntraday, quoteIntervalIntraday},
		{"hk", quoteIntervalIntraday, quoteIntervalIntraday},
		{"sh", quoteIntervalIntraday5D, quoteIntervalIntraday5D},
		{"sz", quoteIntervalIntraday5D, quoteIntervalIntraday5D},
		{"hk", quoteIntervalIntraday5D, quoteIntervalIntraday5D},
		// Nobody enabled serves these, so the reader gets the price and an empty chart. The two
		// markets are here for DIFFERENT measured reasons — day/query refuses the US market with
		// `param error`, and answers the only Beijing code this repo has a body for with a week
		// from April 2025 — and both are written at the capability declaration.
		{"bj", quoteIntervalIntraday, quoteIntervalSnapshot},
		{"bj", quoteIntervalIntraday5D, quoteIntervalSnapshot},
		{"us", quoteIntervalIntraday, quoteIntervalSnapshot},
		{"us", quoteIntervalIntraday5D, quoteIntervalSnapshot},
		// The daily chain is untouched by any of this.
		{"sh", quoteIntervalDaily, quoteIntervalDaily},
		{"us", quoteIntervalDaily, quoteIntervalSnapshot},
	} {
		got := c.servableInterval(cfg, quoteAsk(t, tc.market), tc.ask)
		if got != tc.want {
			t.Errorf("%s asking for %q was served %q, want %q", tc.market, tc.ask, got, tc.want)
		}
	}
	// With Yahoo switched on, the two windows it declares stop degrading — which is the operator's
	// side of the same mechanism, and the only thing that changes is the order setting.
	on := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, quoteSourceYahoo}}.orDefaults()
	// The US is now the whole of what enabling Yahoo buys on these two windows — the Chinese
	// exchanges and Hong Kong are served either way, which is what this change was for.
	if got := c.servableInterval(on, quoteAsk(t, "us"), quoteIntervalIntraday5D); got != quoteIntervalIntraday5D {
		t.Errorf("us 5日 with yahoo enabled resolved to %q, want it served", got)
	}
	if got := c.servableInterval(on, quoteAsk(t, "us"), quoteIntervalIntraday); got != quoteIntervalIntraday {
		t.Errorf("us 分时 with yahoo enabled resolved to %q, want it served", got)
	}
	// And Beijing still degrades with every source switched on, because no source has a body for it.
	if got := c.servableInterval(on, quoteAsk(t, "bj"), quoteIntervalIntraday5D); got != quoteIntervalSnapshot {
		t.Errorf("bj 5日 resolved to %q with every source enabled", got)
	}

	// And the ENDPOINT applies it, which is a separate claim from the resolver knowing the answer: a
	// handler that asked for the interval the range names and let the failover report "no sources"
	// would answer this request with 503 and take the price, the name and the market badge off the
	// screen along with the chart nobody can draw.
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentUS))
	wireQuoteSources(s, tencent, sinaStub(t))
	rec := quoteGET(t, s, "/api/quote/AAPL?range=1d")
	if rec.Code != http.StatusOK {
		t.Fatalf("分时 on a market with no minute source → %d (%s), want the price and an empty chart",
			rec.Code, rec.Body.String())
	}
	got := quoteBody(t, rec)
	quoteEqInt(t, "last", got.Snapshot.Last, 31997)
	if len(got.Bars) != 0 {
		t.Errorf("a degraded request carried %d bars", len(got.Bars))
	}
	// interval_unsupported and NOT market_unsupported. The distinction is the whole of this
	// assertion: the US has minute data — Yahoo serves it, and the row above proves the resolver
	// finds it the moment that source is enabled — so "this market has no historical data" is the
	// wrong sentence, and it is the wrong sentence in the expensive direction, because it is what an
	// operator reads after enabling a source for exactly this window.
	quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, quoteBarsIntervalUnsupported)
	quoteEqStr(t, "barsSource", got.BarsSource, "")
	if tencent.n() != 1 {
		t.Errorf("the degraded request cost %d upstream calls, want 1 for the snapshot", tencent.n())
	}

	// The OTHER branch, which must keep saying what it always said: a DAILY range on a market whose
	// daily series is not worth drawing is a standing gap, true whatever an operator enables, and it
	// is the one case market_unsupported is still for.
	daily := quoteBody(t, quoteGET(t, s, "/api/quote/AAPL?range=3m"))
	quoteEqStr(t, "daily barsUnavailable", daily.BarsUnavailable, quoteBarsMarketUnsupported)
	if len(daily.Bars) != 0 {
		t.Errorf("a degraded daily request carried %d bars", len(daily.Bars))
	}
	// An unknown range is the DEFAULT range and not a snapshot request — every entry in the table
	// asks for a series — so it degrades and reports like the daily one above rather than silently
	// becoming a price with no notice attached.
	unknown := quoteBody(t, quoteGET(t, s, "/api/quote/AAPL?range=nonsense"))
	quoteEqStr(t, "fallback barsUnavailable", unknown.BarsUnavailable, quoteBarsMarketUnsupported)
}

// intradayStub answers a 分时 request from the captured minute body and every other request from the
// captured fqkline body, which is what the real source does with its two endpoints.
func intradayStub(t *testing.T) *quoteVendorStub {
	t.Helper()
	daily := readQuoteFixture(t, fixTencentSH)
	minute := readQuoteFixture(t, fixTencentMinuteSH)
	v := &quoteVendorStub{}
	v.parse = func(market, code string, bars int) (*QuoteResp, error) {
		return parseTencentQuote(market, code, daily, bars)
	}
	v.parseAt = func(market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
		if iv.intraday() {
			return parseTencentMinute(market, code, minute, bars)
		}
		return parseTencentQuote(market, code, daily, bars)
	}
	return v
}

// A bar's `d` is a DATE for a daily series and a full timestamp for an intraday one, and the chart
// is told which it is handed rather than sniffing the string. This is the endpoint keeping that
// contract, asserted on both sides of it in one test so that the difference is the assertion.
func TestQuoteIntradayBarsCarryTimestampsAndDailyBarsCarryDates(t *testing.T) {
	s := quoteServer(t)
	wireQuoteSources(s, intradayStub(t), sinaStub(t))

	rec := quoteGET(t, s, "/api/quote/600519?range=1d")
	if rec.Code != http.StatusOK {
		t.Fatalf("分时 → %d (%s)", rec.Code, rec.Body.String())
	}
	intraday := quoteBody(t, rec)
	if len(intraday.Bars) != 267 {
		t.Fatalf("分时 returned %d bars, want the captured session's 267", len(intraday.Bars))
	}
	// The exchange's own wall clock with its offset, not UTC and not a bare date: 09:30 in Shanghai.
	quoteEqStr(t, "first intraday bar", intraday.Bars[0].Date, "2026-09-07T09:30:00+08:00")
	quoteEqStr(t, "last intraday bar", intraday.Bars[266].Date, "2026-09-07T15:30:00+08:00")
	// One price per minute, so the four candle prices are one number — the chart draws a line.
	first := intraday.Bars[0]
	if first.Open != 132400 || first.High != 132400 || first.Low != 132400 || first.Close != 132400 {
		t.Errorf("first minute = %+v, want 1324.00 in all four price fields", first)
	}
	// The volume column is the day's RUNNING TOTAL and a bar's volume is the DIFFERENCE. 227 手 then
	// 995 手 cumulative is a second minute of 768 手 — 76800 股 — and not 99500.
	quoteEqInt(t, "first minute volume", first.Volume, 22700)
	quoteEqInt(t, "second minute volume", intraday.Bars[1].Volume, 76800)
	// The captured session's last two rows carry the same running total, so the last minute traded
	// nothing. Zero is a real answer and must not be a refusal.
	quoteEqInt(t, "last minute volume", intraday.Bars[266].Volume, 0)
	// The snapshot half is the SAME array the daily endpoint carries, through the same gate.
	quoteEqStr(t, "name", intraday.Name, "贵州茅台")
	quoteEqInt(t, "last", intraday.Snapshot.Last, 131601)
	quoteEqInt(t, "prevClose", intraday.Snapshot.PrevClose, 133000)
	quoteEqStr(t, "changePct", intraday.Snapshot.ChangePct, "-1.05")
	quoteEqStr(t, "asOf", intraday.Snapshot.AsOf, "2026-09-07T16:14:58+08:00")
	quoteEqStr(t, "session", intraday.Snapshot.Session, quoteSessionClose)
	// And it is cached under the INTRADAY TTL, not the five-minute closed-market one the session
	// field would otherwise select. This is the assertion that fails if ttlFor stops asking about
	// the interval first: the captured body says SH_close.
	if got := quoteMaxAge(t, rec); got != int(quoteTTLIntraday/time.Second) {
		t.Errorf("a 分时 answer was cached for %ds, want %ds — a five-minute cache on a one-minute chart",
			got, int(quoteTTLIntraday/time.Second))
	}

	rec = quoteGET(t, s, "/api/quote/601899?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("daily → %d (%s)", rec.Code, rec.Body.String())
	}
	daily := quoteBody(t, rec)
	if len(daily.Bars) == 0 {
		t.Fatal("the daily range came back with no bars")
	}
	for _, b := range daily.Bars {
		if strings.Contains(b.Date, "T") {
			t.Fatalf("a daily bar carries a timestamp (%q); the chart would label five months of "+
				"sessions with times", b.Date)
		}
	}
	if got := quoteMaxAge(t, rec); got != int(quoteTTLClosed/time.Second) {
		t.Errorf("a daily answer on a closed market was cached for %ds, want %ds", got, int(quoteTTLClosed/time.Second))
	}
}

// The two series are DIFFERENT ANSWERS and must not share a cache slot. 400 one-minute points and
// 400 daily bars are the same market, the same code and the same bar count; without the interval in
// the key, whichever was asked for first would be drawn under both labels until it expired.
// Hong Kong is the market where BOTH captured bodies describe the same instrument — 00700 has an
// fqkline fixture and a minute fixture from the same afternoon — so this is the one symbol whose two
// series can be asked for in turn without a synthesised body standing in for either.
func TestQuoteIntradayAndDailyDoNotShareACacheSlot(t *testing.T) {
	s := quoteServer(t)
	daily := readQuoteFixture(t, fixTencentHK)
	minute := readQuoteFixture(t, fixTencentMinuteHK)
	stub := &quoteVendorStub{
		parse: func(market, code string, bars int) (*QuoteResp, error) {
			return parseTencentQuote(market, code, daily, bars)
		},
		parseAt: func(market, code string, bars int, iv quoteInterval) (*QuoteResp, error) {
			if iv.intraday() {
				return parseTencentMinute(market, code, minute, bars)
			}
			return parseTencentQuote(market, code, daily, bars)
		},
	}
	wireQuoteSources(s, stub, sinaStub(t))

	// The INTERVAL alone, with the market, the code AND the bar count held equal — which is the only
	// arrangement that tests what the key promises. Going through the endpoint instead would vary the
	// bar count too (分时 asks for 400 points and 3个月 for 66), and a key that had lost its interval
	// component would still separate those two by accident.
	ctx := context.Background()
	cfg := quoteConfigDefault()
	for _, iv := range []quoteInterval{quoteIntervalIntraday, quoteIntervalDaily} {
		if _, _, _, err := s.quotes.fetchUnder(ctx, cfg, "hk", "00700", quoteRange1D, iv, quoteWindow{}); err != nil {
			t.Fatalf("fetch %s: %v", iv, err)
		}
	}
	if stub.n() != 2 {
		t.Fatalf("the same symbol at the same bar count, at two intervals, cost %d upstream calls — "+
			"want 2, one per series", stub.n())
	}
	s.quotes.clear()

	rec := quoteGET(t, s, "/api/quote/00700?range=1d")
	if rec.Code != http.StatusOK {
		t.Fatalf("分时 → %d (%s)", rec.Code, rec.Body.String())
	}
	if n := len(quoteBody(t, rec).Bars); n != 332 {
		t.Fatalf("分时 returned %d bars, want the captured session's 332", n)
	}
	// And the same thing through the endpoint, where what the reader would actually see is that a
	// 3个月 request comes back holding 332 minute bars.
	rec = quoteGET(t, s, "/api/quote/00700?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("daily → %d (%s)", rec.Code, rec.Body.String())
	}
	if stub.n() != 4 {
		t.Errorf("分时 then 日线 through the endpoint cost %d upstream calls in total, want 4 — "+
			"the two above plus one per series here", stub.n())
	}
	bars := quoteBody(t, rec).Bars
	if len(bars) == 0 {
		t.Fatal("the daily request came back with no bars")
	}
	for _, b := range bars {
		if strings.Contains(b.Date, "T") {
			t.Fatalf("the daily request was served the cached 分时 series (%q)", b.Date)
		}
	}
	// And each range is served from its own entry on the way back: neither costs a third call.
	if rec := quoteGET(t, s, "/api/quote/00700?range=1d"); !quoteBody(t, rec).Cached {
		t.Error("the second 分时 request was not a cache hit")
	}
	if stub.n() != 4 {
		t.Errorf("re-asking for 分时 cost another call (%d total)", stub.n())
	}
}

// ---------- the minute parser ----------

// The rows of one captured session, read against the bytes the vendor sent.
func TestTencentMinuteParserReadsTheCapturedSession(t *testing.T) {
	resp, err := parseTencentMinute("sh", "600519", readQuoteFixture(t, fixTencentMinuteSH), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Bars) != 267 {
		t.Fatalf("bars = %d, want 267", len(resp.Bars))
	}
	quoteEqStr(t, "barsSource", resp.BarsSource, quoteSourceTencent)
	quoteEqStr(t, "kind", resp.Kind, quoteKindStock)
	quoteEqStr(t, "tz", resp.TZ, "Asia/Shanghai")

	// Hong Kong counts SHARES already, so the x100 that turns 手 into 股 on the Chinese exchanges
	// must not be applied here — the flag is a property of the market and the value cannot tell you.
	hk, err := parseTencentMinute("hk", "00700", readQuoteFixture(t, fixTencentMinuteHK), 0)
	if err != nil {
		t.Fatalf("parse hk: %v", err)
	}
	if len(hk.Bars) != 332 {
		t.Fatalf("hk bars = %d, want 332", len(hk.Bars))
	}
	quoteEqInt(t, "hk first minute volume", hk.Bars[0].Volume, 489941)
	quoteEqInt(t, "hk first minute price", hk.Bars[0].Close, 44060)
	// Hong Kong's own zone and its own late close: the last captured point is 16:08, after the
	// closing auction, and it is stamped in Asia/Hong_Kong rather than in Shanghai's borrowed clock.
	quoteEqStr(t, "hk last bar", hk.Bars[331].Date, "2026-09-07T16:08:00+08:00")
	quoteEqStr(t, "hk tz", hk.TZ, "Asia/Hong_Kong")
}

// The US body is the measurement behind the missing US intraday grant: ONE point, and no trading
// date at all. A parser that shrugged and used today's date would stamp a row from last Friday's
// close as this morning.
func TestTencentMinuteRefusesAPayloadWithNoTradingDate(t *testing.T) {
	_, err := parseTencentMinute("us", "AAPL", readQuoteFixture(t, fixTencentMinuteUS), 0)
	if err == nil {
		t.Fatal("a payload with an empty date parsed; a bar with no day is an instant this parser invented")
	}
	if !strings.Contains(err.Error(), "trading date") {
		t.Errorf("refusal was %v, want it to name the missing trading date", err)
	}
	// Beijing's body has a date and ONE point, which parses fine — the reason bj is not declared is
	// that one point is not a series, not that the parser cannot read it. Asserted so that the
	// declaration's comment stays a statement about what was measured.
	bj, err := parseTencentMinute("bj", "830799", readQuoteFixture(t, fixTencentMinuteBJ), 0)
	if err != nil {
		t.Fatalf("parse bj: %v", err)
	}
	if len(bj.Bars) != 1 {
		t.Errorf("bj bars = %d, want the single captured point", len(bj.Bars))
	}
}

// The two checks that stand in for the daily gate's candle ordering, which has nothing to say about
// a row carrying one price. Each is verified by breaking the captured body in exactly the way an
// inserted column would break it.
func TestTencentMinuteGateCatchesAShiftedRow(t *testing.T) {
	body := string(readQuoteFixture(t, fixTencentMinuteSH))
	// Each case names the SENTENCE the reader gets, not merely that something failed. A shifted row
	// eventually dies somewhere whatever the gate says — a price does not parse as a timestamp — so
	// "an error came back" would be satisfied by a parser with no gate at all, and the thing worth
	// keeping is that the failure says the response SHAPE moved.
	for _, tc := range []struct{ name, from, to, want string }{
		// A column inserted at the FRONT slides the price into the clock's place.
		{"clock", `"0931 1331.35 995 132011508.27"`, `"1331.35 0931 995 132011508.27"`, "HHMM clock"},
		// A repeated minute: the series can no longer be drawn in the order it arrived.
		{"repeat", `"0931 1331.35 995 132011508.27"`, `"0930 1331.35 995 132011508.27"`, "not later than"},
		// A column inserted in the MIDDLE lands a price where the running total belongs, and a
		// running total is the one column that cannot bounce.
		{"running total", `"0931 1331.35 995 132011508.27"`, `"0931 1331.35 1 132011508.27"`, "running total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := strings.Replace(body, tc.from, tc.to, 1)
			if broken == body {
				t.Fatalf("the fixture no longer contains %s; this test asserts nothing", tc.from)
			}
			_, err := parseTencentMinute("sh", "600519", []byte(broken), 0)
			if err == nil {
				t.Fatal("a shifted row parsed cleanly")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal was %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// ---------- stage 3: the batch ----------

// quoteBatchView is the wire shape of GET /api/quotes.
type quoteBatchView struct {
	Enabled bool                 `json:"enabled"`
	Quotes  map[string]QuoteCard `json:"quotes"`
	Missing []string             `json:"missing"`
}

func quoteBatchMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/quotes", s.requireUserJSON(s.apiQuotes))
	return mux
}

func quoteBatchGET(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign("kazuha")})
	rec := httptest.NewRecorder()
	quoteBatchMux(s).ServeHTTP(rec, r)
	return rec
}

func quoteBatchBody(t *testing.T, rec *httptest.ResponseRecorder) quoteBatchView {
	t.Helper()
	var out quoteBatchView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode batch: %v (body %s)", err, rec.Body.String())
	}
	return out
}

// batchStub answers every symbol out of the ONE captured q= body, through the real parser.
func batchStub(t *testing.T) *quoteVendorStub {
	t.Helper()
	body := string(readQuoteFixture(t, fixTencentBatch))
	daily := readQuoteFixture(t, fixTencentSH)
	return &quoteVendorStub{
		parse: func(market, code string, bars int) (*QuoteResp, error) {
			return parseTencentQuote(market, code, daily, bars)
		},
		parseBatch: func(targets []quoteTarget) (map[string]*QuoteResp, error) {
			return parseTencentBatch(targets, body)
		},
	}
}

// The claim the whole endpoint exists for, asserted as a NUMBER: a page of cards spanning five
// markets is ONE conversation with a vendor, not one per card.
func TestQuoteBatchIsOneUpstreamCallForManySymbols(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	sina := failingStub("sina must not be asked: it has no batch endpoint")
	wireQuoteSources(s, tencent, sina)

	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,000001,00700,AAPL,830799,sh:000001")
	if rec.Code != http.StatusOK {
		t.Fatalf("batch → %d (%s)", rec.Code, rec.Body.String())
	}
	view := quoteBatchBody(t, rec)
	if !view.Enabled {
		t.Error("the shipped portal reports the home cards switched off")
	}
	if tencent.n() != 1 {
		t.Fatalf("six symbols cost %d upstream calls, want 1", tencent.n())
	}
	if sina.n() != 0 {
		t.Errorf("a source with no batch endpoint was called %d time(s); a batch is not a loop", sina.n())
	}
	if len(view.Quotes) != 6 {
		t.Fatalf("batch answered %d of 6 symbols: %+v", len(view.Quotes), view.Quotes)
	}
	// Keyed by the VENDOR symbol, because a code is not unique across markets: sz000001 is 平安银行
	// and sh000001 is 上证指数, and a map keyed by six digits would keep one of them.
	for _, tc := range []struct {
		key, symbol, market, pct string
		last                     int64
	}{
		{"sh601899", "601899", "sh", "-1.44", 3287},
		{"sz000001", "000001", "sz", "-1.60", 1170},
		{"hk00700", "00700", "hk", "-0.99", 43840},
		{"usAAPL", "AAPL", "us", "-2.51", 31997},
		{"bj830799", "830799", "bj", "0.00", 3428},
		{"sh000001", "000001", "sh", "0.07", 393270},
	} {
		card, ok := view.Quotes[tc.key]
		if !ok {
			t.Errorf("no %q card in the answer", tc.key)
			continue
		}
		quoteEqStr(t, tc.key+" symbol", card.Symbol, tc.symbol)
		quoteEqStr(t, tc.key+" market", card.Market, tc.market)
		// The vendor's own percentage string, verbatim — recomputing it manufactures a crash on
		// every ex-rights morning (ADR 0028 §4), and a card is the surface with the least room to
		// explain one.
		quoteEqStr(t, tc.key+" changePct", card.ChangePct, tc.pct)
		quoteEqInt(t, tc.key+" last", card.Last, tc.last)
		if card.Source != quoteSourceTencent {
			t.Errorf("%s card names source %q", tc.key, card.Source)
		}
	}
	// A second identical request is served from the cache: the batch stores what it fetched under
	// the same keys the single endpoint reads.
	if rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,000001,00700,AAPL,830799,sh:000001"); rec.Code != http.StatusOK {
		t.Fatalf("second batch → %d", rec.Code)
	}
	if tencent.n() != 1 {
		t.Errorf("the second identical batch cost another call (%d total)", tencent.n())
	}
	if card := quoteBatchBody(t, quoteBatchGET(t, s, "/api/quotes?symbols=601899")).Quotes["sh601899"]; !card.Cached {
		t.Error("a card served from the cache reported cached=false")
	}
}

// A batch is not all-or-nothing. One code this build refuses and one the vendor does not know are
// both named in `missing`, and neither costs the others their price.
func TestQuoteBatchAnswersPerSymbol(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	wireQuoteSources(s, tencent, sinaStub(t))

	// 12345678 is refused here (no market's shape accepts eight digits) and 600519 resolves fine but
	// is absent from the captured body — the two ways a symbol goes missing, and the request must
	// survive both.
	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,12345678,600519,00700")
	if rec.Code != http.StatusOK {
		t.Fatalf("a batch with one bad symbol → %d (%s)", rec.Code, rec.Body.String())
	}
	view := quoteBatchBody(t, rec)
	if len(view.Quotes) != 2 {
		t.Fatalf("the good symbols answered %d of 2: %+v", len(view.Quotes), view.Quotes)
	}
	if _, ok := view.Quotes["sh601899"]; !ok {
		t.Error("601899 lost its price to another symbol's failure")
	}
	missing := strings.Join(view.Missing, ",")
	if !strings.Contains(missing, "12345678") || !strings.Contains(missing, "sh600519") {
		t.Errorf("missing = %q, want both the refused code and the unanswered one", missing)
	}
	if tencent.n() != 1 {
		t.Errorf("the batch cost %d calls; a failing symbol must not become a retry", tencent.n())
	}
}

// Over the cap the request is REFUSED, not truncated. A truncated batch is a tail of cards whose
// prices never arrive and never will, and nothing on the page can tell that from a slow vendor.
func TestQuoteBatchRefusesMoreSymbolsThanTheCap(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	wireQuoteSources(s, tencent, sinaStub(t))

	// Distinct six-digit codes, one past the cap.
	syms := make([]string, 0, quoteBatchMax+1)
	for i := 0; i <= quoteBatchMax; i++ {
		syms = append(syms, "60"+strconv.Itoa(1000+i))
	}
	rec := quoteBatchGET(t, s, "/api/quotes?symbols="+strings.Join(syms, ","))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d symbols → %d, want 400", len(syms), rec.Code)
	}
	if got := quoteErrCode(t, rec); got != "quote_bad_symbol" {
		t.Errorf("over-cap refusal carried code %q", got)
	}
	if tencent.n() != 0 {
		t.Errorf("a refused batch still cost %d upstream call(s)", tencent.n())
	}
	// Exactly the cap is served, so the refusal is a boundary and not a smaller limit in disguise.
	rec = quoteBatchGET(t, s, "/api/quotes?symbols="+strings.Join(syms[:quoteBatchMax], ","))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d symbols → %d, want 200", quoteBatchMax, rec.Code)
	}
	// And an empty list is a refusal too rather than an upstream call with no symbols in it.
	if rec := quoteBatchGET(t, s, "/api/quotes?symbols="); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty symbols= → %d, want 400", rec.Code)
	}
}

// home_quotes off means the home page sends NOTHING to a vendor, which is the disclosure the switch
// exists to prevent — so the assertion is the call count and not merely the empty body.
func TestQuoteBatchSendsNothingWhenHomeCardsAreOff(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	wireQuoteSources(s, tencent, sinaStub(t))

	s.st.SetSetting(setQuoteHomeCards, "false")
	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,000001")
	if rec.Code != http.StatusOK {
		t.Fatalf("switched off → %d, want 200 with nothing in it (%s)", rec.Code, rec.Body.String())
	}
	view := quoteBatchBody(t, rec)
	if view.Enabled {
		t.Error("the answer says the feature is on while it is off")
	}
	if len(view.Quotes) != 0 {
		t.Errorf("a switched-off feed answered %d quotes", len(view.Quotes))
	}
	if tencent.n() != 0 {
		t.Fatalf("a switched-off feed sent %d code list(s) to a vendor — the one thing the switch is for", tencent.n())
	}

	// Back on, and the same request now costs exactly one call. Without this half the test above
	// would pass against an endpoint that never worked at all.
	s.st.SetSetting(setQuoteHomeCards, "true")
	if rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,000001"); rec.Code != http.StatusOK {
		t.Fatalf("switched on → %d", rec.Code)
	}
	if tencent.n() != 1 {
		t.Errorf("switched back on, the batch made %d calls, want 1", tencent.n())
	}
}

// One cache, both surfaces. A reading page that has just fetched a chart leaves an entry a card can
// answer from, whatever range put it there — so opening the home feed after reading a report costs
// nothing at all.
//
// The other direction cannot work and is not claimed: a card's answer carries no series, so it can
// never satisfy a request for sixty-six daily bars.
func TestQuoteBatchIsServedByWhateverTheReadingPageWarmed(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	wireQuoteSources(s, tencent, sinaStub(t))

	if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusOK {
		t.Fatalf("reading page → %d", rec.Code)
	}
	if tencent.n() != 1 {
		t.Fatalf("the reading page cost %d calls", tencent.n())
	}
	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("batch → %d", rec.Code)
	}
	if tencent.n() != 1 {
		t.Errorf("a card for a symbol the reading page had just fetched cost another call (%d total)", tencent.n())
	}
	card := quoteBatchBody(t, rec).Quotes["sh601899"]
	if !card.Cached {
		t.Error("the card did not report itself as cached")
	}
	quoteEqInt(t, "card last", card.Last, 3335) // the fqkline fixture's own last price
	// And the reverse is NOT claimed: a card's snapshot cannot answer a chart. A fresh cache warmed
	// only by a batch still costs the reading page its own call.
	s.quotes.clear()
	if rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899"); rec.Code != http.StatusOK {
		t.Fatalf("batch after clear → %d", rec.Code)
	}
	before := tencent.n()
	if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusOK {
		t.Fatalf("reading page after a batch → %d", rec.Code)
	}
	if tencent.n() != before+1 {
		t.Errorf("a chart was served out of a card's snapshot: calls went %d → %d", before, tencent.n())
	}
}

// ---------- the batch parser ----------

// One body, six markets, and the per-symbol refusal that keeps a bad line from taking the rest with
// it. The payload of a v_<code> line is the SAME positional array fqkline sends, so it goes through
// the same gate — and this is where that is verified rather than assumed.
func TestTencentBatchParserReadsOneLinePerSymbol(t *testing.T) {
	body := string(readQuoteFixture(t, fixTencentBatch))
	targets := make([]quoteTarget, 0, 6)
	for _, spec := range []string{"sh:601899", "sz:000001", "hk:00700", "us:AAPL", "bj:830799", "sh:000001"} {
		target, err := quoteResolve(spec)
		if err != nil {
			t.Fatalf("resolve %s: %v", spec, err)
		}
		targets = append(targets, target)
	}
	out, err := parseTencentBatch(targets, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("parsed %d of 6 lines: %v", len(out), out)
	}
	quoteEqInt(t, "sh601899 last", out["sh601899"].Snapshot.Last, 3287)
	quoteEqStr(t, "sh601899 name", out["sh601899"].Name, "紫金矿业")
	// The US echoes a Reuters code — ask for AAPL and the line reads AAPL.OQ — which the code-echo
	// check compares against the part before the first dot, and only for that market.
	quoteEqInt(t, "usAAPL last", out["usAAPL"].Snapshot.Last, 31997)
	// Hong Kong's 成交量 is already 股 and its 成交额 already whole HKD, so neither the x100 nor the
	// 万元 scale may be applied there.
	quoteEqInt(t, "hk00700 volume", out["hk00700"].Snapshot.Volume, 10442987)
	quoteEqInt(t, "hk00700 amount", out["hk00700"].Snapshot.Amount, 4591565063)
	// A card carries no series, and the field is an empty array rather than a nil one.
	for sym, resp := range out {
		if resp.Bars == nil || len(resp.Bars) != 0 {
			t.Errorf("%s came back with %v bars; this endpoint carries no series", sym, resp.Bars)
		}
	}
	// The index is the vendor's own answer rather than an inference from six digits: sh000001 is
	// 上证指数 and sz000001 is 平安银行.
	quoteEqStr(t, "sh000001 name", out["sh000001"].Name, "上证指数")
	quoteEqStr(t, "sh000001 kind", out["sh000001"].Kind, quoteKindIndex)
	quoteEqStr(t, "sz000001 kind", out["sz000001"].Kind, quoteKindStock)

	// A symbol nobody sent a line for is absent, not zero-valued.
	extra, err := quoteResolve("sh:600519")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err = parseTencentBatch(append(targets, extra), body)
	if err != nil {
		t.Fatalf("parse with an unanswered symbol: %v", err)
	}
	if _, ok := out["sh600519"]; ok {
		t.Error("a symbol the vendor did not answer came back with a quote")
	}
	if len(out) != 6 {
		t.Errorf("an unanswered symbol changed the other %d answers", len(out))
	}

	// And ONE line that fails the gate costs only its own symbol. The percentage is edited by a whole
	// point, which is the drift the identity check exists for.
	broken := strings.Replace(body, "~-0.48~-1.44~", "~-0.48~-2.44~", 1)
	if broken == body {
		t.Fatal("the fixture no longer carries 紫金矿业's percentage; this assertion is empty")
	}
	out, err = parseTencentBatch(targets, broken)
	if err != nil {
		t.Fatalf("parse with one refused line: %v", err)
	}
	if _, ok := out["sh601899"]; ok {
		t.Error("a line whose percentage disagrees with its own prices was served")
	}
	if len(out) != 5 {
		t.Errorf("one refused line left %d of the other 5 answers", len(out))
	}
}

// batchFieldEdited rewrites ONE field of ONE symbol's line in the captured batch body and leaves
// every other byte of it alone. Editing the positional array by index is the point: these lines have
// no field names, so a test that wants to move the day's low has to count to 34 exactly as the parser
// does.
func batchFieldEdited(t *testing.T, body, sym string, index int, value string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		key, payload, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "v_"+sym {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(payload), ";")), `"`)
		fields := strings.Split(raw, quoteTencentBatchSep)
		if index >= len(fields) {
			t.Fatalf("%s's line has %d fields; this test is about field %d", sym, len(fields), index)
		}
		fields[index] = value
		out := strings.Replace(body, line, key+`="`+strings.Join(fields, quoteTencentBatchSep)+`";`, 1)
		if out == body {
			t.Fatalf("rewriting %s's field %d changed nothing", sym, index)
		}
		return out
	}
	t.Fatalf("the fixture carries no line for %s", sym)
	return ""
}

// Check 5 runs on a card's line too. It was the one check the batch path skipped, and the gap is
// invisible from the fixtures: check 3, the identity, catches a percentage that disagrees with the
// prices — but it returns early whenever prevClose is 0, and a never-traded or newly listed code is
// exactly that. Then nothing noticed a last price outside the day's own high and low, and the wrong
// number rendered on a card.
//
// The mutation is one field of the real body: 紫金矿业's day low lifted above its own last price.
// Checks 1, 2 and 3 all still pass — the field count is unchanged, the code echo is untouched, and
// the identity reads last, prevClose and the percentage, none of which move.
func TestTencentBatchRunsTheDayRangeCheckOnEveryLine(t *testing.T) {
	body := string(readQuoteFixture(t, fixTencentBatch))
	targets := make([]quoteTarget, 0, 6)
	for _, spec := range []string{"sh:601899", "sz:000001", "hk:00700", "us:AAPL", "bj:830799", "sh:000001"} {
		target, err := quoteResolve(spec)
		if err != nil {
			t.Fatalf("resolve %s: %v", spec, err)
		}
		targets = append(targets, target)
	}
	// 33.35 is above the 32.87 that same line reports as the last price. The index is the day's LOW,
	// the same one parseTencentQt reads.
	mutated := batchFieldEdited(t, body, "sh601899", 34, "33.35")
	out, err := parseTencentBatch(targets, mutated)
	if err != nil {
		t.Fatalf("one refused line failed the whole batch: %v", err)
	}
	if _, ok := out["sh601899"]; ok {
		t.Error("a card whose last price sits outside its own day range was served: check 5 is the " +
			"only thing that notices, and the identity returns early on a zero prevClose")
	}
	if len(out) != 5 {
		t.Errorf("the refusal cost %d of the other five answers", 5-len(out)+1)
	}
	// The skip is intact, and it has to be: the captured bj830799 line is a suspended stock reporting
	// high=0 and low=0 beside a real last price, and it is in the answer above. Reporting every
	// suspended stock in the market as a source failure is what a check that did not skip would do.
	if _, ok := out["bj830799"]; !ok {
		t.Error("the suspended Beijing line was refused; quoteCheckRange skips a zero bound for it")
	}
}

// A batch whose every line the GATE refused is not a vendor outage, and the difference is what an
// operator reads to decide whether a source is down.
//
// The failure this pins is a page view, not a theory: the server's page sizes are 15, 30 and 50 and a
// filtered feed routinely shows one card, so a home page whose only card is a suspended or delisted
// code was a batch of one refused line — got empty, the whole SOURCE marked failed, and the
// consecutive-failure streak in 管理 → 行情源 climbing on every single view while Tencent answered
// perfectly. It also contradicted this endpoint's own promise that a per-symbol failure costs only
// that symbol: it cost the vendor's reputation instead.
func TestQuoteBatchGateRefusalIsNotAVendorFailure(t *testing.T) {
	body := string(readQuoteFixture(t, fixTencentBatch))
	// The same edit the parser test uses: a percentage a whole point away from the one the prices
	// imply, which is the drift the identity check exists for.
	broken := strings.Replace(body, "~-0.48~-1.44~", "~-0.48~-2.44~", 1)
	if broken == body {
		t.Fatal("the fixture no longer carries 紫金矿业's percentage; this test is empty")
	}

	s := quoteServer(t)
	tencent := &quoteVendorStub{parseBatch: func(targets []quoteTarget) (map[string]*QuoteResp, error) {
		return parseTencentBatch(targets, broken)
	}}
	wireQuoteSources(s, tencent, sinaStub(t))

	// Three views of a page whose only card is the refused code.
	for i := 0; i < 3; i++ {
		rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899")
		if rec.Code != http.StatusOK {
			t.Fatalf("view %d → %d (%s)", i, rec.Code, rec.Body.String())
		}
		view := quoteBatchBody(t, rec)
		if len(view.Quotes) != 0 {
			t.Errorf("view %d served %d quotes from a line the gate refused", i, len(view.Quotes))
		}
		if len(view.Missing) != 1 || view.Missing[0] != "sh601899" {
			t.Errorf("view %d reported missing=%v, want the one refused symbol", i, view.Missing)
		}
	}
	if tencent.n() != 3 {
		t.Fatalf("three views cost %d upstream calls; this test needs each of them to reach the vendor", tencent.n())
	}
	for _, h := range s.QuoteHealth() {
		if h.Source == quoteSourceTencent && h.Failures != 0 {
			t.Errorf("a vendor that answered every time has a streak of %d failures (%q): our own gate "+
				"dropping every line is not the vendor failing", h.Failures, h.LastError)
		}
	}

	// The control, and it is the reason this is not simply "call it a success": a source that really
	// does fail IS recorded, so the counters still say what they are for.
	down := quoteServer(t)
	wireQuoteSources(down, &quoteVendorStub{err: errors.New("tencent is down")}, sinaStub(t))
	if rec := quoteBatchGET(t, down, "/api/quotes?symbols=601899"); rec.Code != http.StatusOK {
		t.Fatalf("a dead vendor took the feed down: %d", rec.Code)
	}
	recorded := false
	for _, h := range down.QuoteHealth() {
		if h.Source == quoteSourceTencent {
			recorded = h.Failures == 1
		}
	}
	if !recorded {
		t.Error("a vendor that failed the whole batch was not recorded; the distinction only means " +
			"something while the other half still counts")
	}

	// And the vendor half of the distinction: a body that answers about NONE of the symbols asked is
	// the parser's error rather than an empty map, so it arrives as a failure through the branch
	// above. Without it, "answered rubbish all day" and "we refused every line" would be one outcome.
	target, err := quoteResolve("sh:601899")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := parseTencentBatch([]quoteTarget{target}, `v_sh600519="1~贵州茅台~600519~1316.01~";`); err == nil {
		t.Error("a body carrying no line for any symbol asked about parsed as an empty answer; that " +
			"is the vendor answering about something else, and it must reach the health counters")
	}
}

// The batch's own single-flight, which is a different mechanism from fetchUnder's: a separate key
// space ("batch:" plus the sorted symbol set), a separate result field, and a publish-then-close
// ordering its own comment calls delicate. Nothing exercised it — disabling only its follower branch
// left the whole suite green — and this is the busiest page in the portal.
func TestQuoteBatchSingleFlightsConcurrentPages(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	tencent.gate = make(chan struct{})
	wireQuoteSources(s, tencent, sinaStub(t))

	const readers = 8
	var wg sync.WaitGroup
	codes := make([]int, readers)
	counts := make([]int, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// The SAME set of symbols in the same order: one page, opened by eight browsers in the
			// same millisecond.
			rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,000001,00700")
			codes[i] = rec.Code
			if rec.Code == http.StatusOK {
				counts[i] = len(quoteBatchBody(t, rec).Quotes)
			}
		}(i)
	}
	// Wait until the leader is inside the (blocked) call, then let the others pile up behind it.
	for tencent.n() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(tencent.gate)
	wg.Wait()

	if tencent.n() != 1 {
		t.Errorf("%d concurrent pages cost %d upstream calls, want 1", readers, tencent.n())
	}
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("page %d → %d", i, code)
		}
		// Every follower is served the leader's answer rather than an empty map: the flight's result
		// is published before its channel closes, and a waiter that read the field too early would
		// render a page of cards with no prices on it.
		if counts[i] != 3 {
			t.Errorf("page %d got %d of 3 cards", i, counts[i])
		}
	}
}

// Two smaller promises the batch endpoint makes in comments, which nothing held.
//
// The header first. `private, no-store` is not decoration: the body is up to fifty answers with fifty
// different remaining lifetimes, so any single max-age would be wrong for most of them, and `private`
// is the half that matters on a portal behind a shared proxy — this body is assembled from one
// session's home page.
//
// Then the handler's dedupe, whose comment used to claim it keeps a code appearing on three cards
// from being named three times to a vendor. It does not, and cannot: fetchBatchUnder dedupes again
// before a URL is built, so the vendor is protected whether or not the handler bothers. What the
// handler's dedupe actually shapes is the ANSWER — `missing` is built by walking `targets`, so a code
// on three cards that no vendor answers for would be named three times in a list the page renders.
func TestQuoteBatchSetsNoStoreAndNamesARepeatedSymbolOnce(t *testing.T) {
	s := quoteServer(t)
	var handed []string
	tencent := batchStub(t)
	inner := tencent.parseBatch
	tencent.parseBatch = func(targets []quoteTarget) (map[string]*QuoteResp, error) {
		for _, t := range targets {
			handed = append(handed, t.Symbol)
		}
		return inner(targets)
	}
	wireQuoteSources(s, tencent, sinaStub(t))

	// 601899 three times (answered by the captured body) and 600519 twice (resolves, and the body
	// carries no line for it) — one repeat on each side of the answered/missing split.
	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,600519,601899,600519,601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("a batch naming two codes five times → %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q — fifty lifetimes cannot share one max-age",
			got, "private, no-store")
	}
	sort.Strings(handed)
	if got, want := strings.Join(handed, ","), "sh600519,sh601899"; got != want {
		t.Errorf("the vendor was handed %q, want each code exactly once (%q)", got, want)
	}
	view := quoteBatchBody(t, rec)
	if len(view.Quotes) != 1 {
		t.Errorf("three cards for one code produced %d entries: %+v", len(view.Quotes), view.Quotes)
	}
	if got := strings.Join(view.Missing, ","); got != "sh600519" {
		t.Errorf("missing = %q, want the unanswered code named once", got)
	}
	if tencent.n() != 1 {
		t.Errorf("five symbols naming two codes cost %d upstream calls", tencent.n())
	}

	// And the cache's own dedupe, which the handler's hides: it promises the vendor sees one mention
	// "no matter who assembled the list", so the list is assembled here instead — the same repeat,
	// handed straight to fetchBatchUnder with no handler in front of it.
	s.quotes.clear()
	handed = nil
	before := tencent.n()
	target, err := quoteResolve("601899")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s.quotes.fetchBatchUnder(context.Background(), s.quoteConfigLoad(),
		[]quoteTarget{target, target, target})
	if got := strings.Join(handed, ","); got != "sh601899" {
		t.Errorf("a caller that did not dedupe named the code to the vendor as %q", got)
	}
	if n := tencent.n() - before; n != 1 {
		t.Errorf("one code named three times cost %d upstream calls", n)
	}
}

// A market whose daily ranges all degrade to a snapshot — Beijing, and the US with no source enabled
// for it — costs ONE upstream call and ONE cache entry across every range a reader can pick, because
// quoteCacheKey drops the bar count for a snapshot. Four ranges asking for 22, 66, 122 and 244 bars
// are four keys otherwise, and every one of them holds the same price.
//
// The key-equality unit test in quote_cache_claims_test.go states this; this one is what it costs.
func TestEveryRangeOnAMarketWithNoHistoryIsOneCallAndOneEntry(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentBJ))
	wireQuoteSources(s, tencent, sinaStub(t))

	for _, rng := range []string{"1m", "3m", "6m", "1y"} {
		rec := quoteGET(t, s, "/api/quote/830799?range="+rng)
		if rec.Code != http.StatusOK {
			t.Fatalf("bj at %s → %d (%s)", rng, rec.Code, rec.Body.String())
		}
		got := quoteBody(t, rec)
		if len(got.Bars) != 0 || got.BarsUnavailable != quoteBarsMarketUnsupported {
			t.Fatalf("bj at %s stopped degrading (%d bars, %q) — this test measures the degraded path",
				rng, len(got.Bars), got.BarsUnavailable)
		}
	}
	if n := tencent.n(); n != 1 {
		t.Errorf("four ranges of the same degraded answer cost %d upstream calls, want 1", n)
	}
	if n := s.quotes.stats().Entries; n != 1 {
		t.Errorf("four ranges of the same degraded answer hold %d cache entries, want 1", n)
	}
}

// ---------- the 5日 window, from day/query ----------

// Five sessions, oldest first, bucketed to five minutes — and every number in the assertions below
// comes out of the captured body rather than out of this test's arithmetic.
//
// The window exists at all because of what the probe that opened this fix measured: with the
// SHIPPED order, `5d` degraded to a snapshot in every market including the A-shares, so the 5日
// button was dead for every deployment that had not enabled an undocumented third-party endpoint.
func TestTencentFiveDayWindowIsFiveSessionsOldestFirst(t *testing.T) {
	for _, tc := range []struct {
		market, code, fixture string
		days, buckets         int
		firstDate, lastDate   string
	}{
		{"sh", "600519", fixTencentDaysSH, 5, 280, "2026-09-01T09:30:00+08:00", "2026-09-07T15:30:00+08:00"},
		{"sz", "000001", fixTencentDaysSZ, 5, 280, "2026-09-01T09:30:00+08:00", "2026-09-07T15:30:00+08:00"},
		{"hk", "00700", fixTencentDaysHK, 5, 340, "2026-09-01T09:30:00+08:00", "2026-09-07T16:00:00+08:00"},
	} {
		t.Run(tc.market, func(t *testing.T) {
			got, err := parseTencentDays(tc.market, tc.code, readQuoteFixture(t, tc.fixture), 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got.Bars) != tc.buckets {
				t.Fatalf("%d bars, want %d five-minute buckets over %d sessions",
					len(got.Bars), tc.buckets, tc.days)
			}
			quoteEqStr(t, "first bar", got.Bars[0].Date, tc.firstDate)
			quoteEqStr(t, "last bar", got.Bars[len(got.Bars)-1].Date, tc.lastDate)
			quoteEqStr(t, "barsSource", got.BarsSource, quoteSourceTencent)
			quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, "")

			// OLDEST FIRST and strictly increasing across the whole window, which is the half the
			// per-session parser cannot check: the vendor sends the days newest first, and a chart
			// drawn in that order runs backwards through the week under a rising x axis.
			distinctDays := map[string]bool{}
			for i, b := range got.Bars {
				distinctDays[b.Date[:10]] = true
				if i > 0 && b.Date <= got.Bars[i-1].Date {
					t.Fatalf("bar %d is stamped %q, which is not later than %q", i, b.Date, got.Bars[i-1].Date)
				}
			}
			if len(distinctDays) != tc.days {
				t.Errorf("the window covers %d calendar days, want %d", len(distinctDays), tc.days)
			}
			// A bucket is an aggregate of observed prices, never an interpolation: its own high and
			// low have to bound its open and close, and its volume is a sum of real minutes.
			var vol int64
			for i, b := range got.Bars {
				if b.High < b.Open || b.High < b.Close || b.Low > b.Open || b.Low > b.Close {
					t.Fatalf("bucket %d is not a bar: o=%d h=%d l=%d c=%d", i, b.Open, b.High, b.Low, b.Close)
				}
				vol += b.Volume
			}
			if vol <= 0 {
				t.Errorf("five sessions carried %d 股 in total", vol)
			}
		})
	}
}

// The two markets day/query does not serve, and they fail differently — which is the point, because
// only one of them is a statement about the market.
func TestTencentFiveDayWindowRefusesTheMarketsItDoesNotServe(t *testing.T) {
	// The US is refused by the VENDOR: {"code":-1,"msg":"param error"}. It must arrive as an error
	// and never as an empty series, or a market Tencent does not serve here reads as a quiet week.
	if _, err := parseTencentDays("us", "AAPL", readQuoteFixture(t, fixTencentDaysUS), 0); err == nil {
		t.Error("the US body parsed; it says param error")
	}

	// Beijing is the opposite: the body is WELL FORMED and carries five full sessions, so nothing in
	// the parser can refuse it. It is dated April 2025 — the suspended stock every bj fixture here
	// is captured from — and that is a fact about this CODE, not about the market. So the guard is
	// the capability declaration and not a parse error, and this asserts the declaration.
	bj, err := parseTencentDays("bj", "830799", readQuoteFixture(t, fixTencentDaysBJ), 0)
	if err != nil {
		t.Fatalf("the bj body no longer parses, so the note below is now wrong: %v", err)
	}
	if len(bj.Bars) == 0 {
		t.Fatal("the bj fixture is meant to carry a full, and stale, series")
	}
	if year := bj.Bars[0].Date[:4]; year == "2026" {
		t.Fatalf("the bj fixture is no longer the stale capture this test is about (%s)", bj.Bars[0].Date)
	}
	// And nothing can ask for it: bj is declared for neither intraday window.
	c := &quoteCache{}
	c.init()
	tg, err := quoteResolve("830799")
	if err != nil {
		t.Fatal(err)
	}
	cfg := quoteConfig{Order: quoteShippedOrder()}
	for _, iv := range []quoteInterval{quoteIntervalIntraday, quoteIntervalIntraday5D} {
		if got := c.sourcesFor(cfg, tg, iv); len(got) != 0 {
			t.Errorf("%s on bj resolves to %v; the only bj body measured is a year old", iv, got)
		}
	}
}

// The regression this whole change exists for: with the SHIPPED order and no third-party source
// enabled, every market's 5日 degraded to a snapshot, so the button drew nothing for anybody.
func TestFiveDayWindowIsServedOutOfTheBoxOnTheMarketsItWasMeasuredFor(t *testing.T) {
	c := &quoteCache{}
	c.init()
	cfg := quoteConfig{Order: quoteShippedOrder()}
	spec, ok := quoteRanges["5d"]
	if !ok {
		t.Fatal("the 5d range is gone")
	}
	for _, in := range []string{"601899", "000001", "00700"} {
		tg, err := quoteResolve(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.servableInterval(cfg, tg, spec.interval); got != quoteIntervalIntraday5D {
			t.Errorf("%s at 5日 degrades to %s with the shipped sources; it must be served", in, got)
		}
	}
	// Unchanged, and stated so the two are not confused: the US 5日 still has no shipped source,
	// because day/query refuses the market outright.
	us, err := quoteResolve("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.servableInterval(cfg, us, spec.interval); got != quoteIntervalSnapshot {
		t.Errorf("the US 5日 resolved to %s out of the box; nothing shipped serves it", got)
	}
}

// ---------- the reader's own date window ----------

const (
	// fqkline with its two DATE slots filled instead of left empty, captured 2026-09-08. The window
	// is 2026-03-02..2026-04-10 in both, and the two markets traded a different number of sessions
	// in it — 29 and 27 — which is the point: the answer is the sessions that EXIST in the window,
	// not a count this portal chose.
	fixTencentRangeSH = "tencent_fqkline_range_sh600519.json"
	fixTencentRangeHK = "tencent_fqkline_range_hk00700.json"
)

// quoteParseWindow is the allowlist for two strings that reach a vendor URL, so it is asserted the
// way quoteRanges is: what it accepts, and — mostly — what it refuses.
func TestQuoteWindowRefusesEverythingButAWellFormedPair(t *testing.T) {
	// Accepted, and NORMALISED: what comes out is a string this code formatted, never the caller's.
	got, err := quoteParseWindow("  2026-03-02 ", "2026-04-10")
	if err != nil {
		t.Fatalf("a well-formed window was refused: %v", err)
	}
	if !got.bounded() || got.from != "2026-03-02" || got.to != "2026-04-10" {
		t.Fatalf("parsed to %+v", got)
	}
	// Absent is not an error — it is every request this endpoint served before windows existed.
	if none, err := quoteParseWindow("", ""); err != nil || none.bounded() {
		t.Errorf("an absent window: %+v, %v", none, err)
	}
	// A single day is a window, not a degenerate one.
	if one, err := quoteParseWindow("2026-03-02", "2026-03-02"); err != nil || !one.bounded() {
		t.Errorf("a one-day window: %+v, %v", one, err)
	}

	for _, tc := range []struct {
		name, from, to string
		want           error
	}{
		{"only from", "2026-03-02", "", errQuoteWindowHalfOpen},
		{"only to", "", "2026-04-10", errQuoteWindowHalfOpen},
		{"backwards", "2026-04-10", "2026-03-02", errQuoteWindowOrder},
		{"month 13", "2026-13-01", "2026-13-02", errQuoteWindowFormat},
		{"day 32", "2026-03-32", "2026-04-01", errQuoteWindowFormat},
		{"unpadded", "2026-3-2", "2026-04-10", errQuoteWindowFormat},
		{"slashes", "2026/03/02", "2026/04/10", errQuoteWindowFormat},
		{"epoch", "1772409600", "1775001600", errQuoteWindowFormat},
		{"a whole century", "1926-01-01", "2026-01-01", errQuoteWindowSpan},
		// The two that would reach the vendor URL as something other than a date. They are refused
		// by the LAYOUT, which is the only reason nothing here has to escape anything.
		{"injection", "2026-03-02,day,,,9999", "2026-04-10", errQuoteWindowFormat},
		{"a second param", "2026-03-02&x=1", "2026-04-10", errQuoteWindowFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := quoteParseWindow(tc.from, tc.to)
			if !errors.Is(err, tc.want) {
				t.Fatalf("(%q, %q) → %+v, %v; want %v", tc.from, tc.to, got, err, tc.want)
			}
			if got.bounded() {
				t.Errorf("a refused window still came back bounded: %+v", got)
			}
		})
	}
}

// The window is part of the cache key, and an ABSENT one contributes nothing — so every key written
// before this existed is byte-identical to the one written now.
func TestQuoteWindowIsPartOfTheCacheKeyAndAnAbsentOneIsNot(t *testing.T) {
	plain := quoteCacheKey("sh", "600519", 66, quoteIntervalDaily, quoteWindow{})
	if got := quoteCacheKey("sh", "600519", 66, quoteIntervalDaily, quoteWindow{from: "", to: ""}); got != plain {
		t.Errorf("an empty window changed the key: %q vs %q", got, plain)
	}
	march := quoteWindow{from: "2026-03-02", to: "2026-04-10"}
	april := quoteWindow{from: "2026-04-02", to: "2026-05-10"}
	a := quoteCacheKey("sh", "600519", quoteMaxBars, quoteIntervalDailyRange, march)
	b := quoteCacheKey("sh", "600519", quoteMaxBars, quoteIntervalDailyRange, april)
	if a == b {
		t.Errorf("two different windows share a cache slot: %q", a)
	}
	if a == plain {
		t.Errorf("a bounded window shares the unbounded slot: %q", a)
	}
}

// End to end: the reader's dates reach the vendor, and what comes back is the sessions in that
// window rather than the last N ending today.
func TestQuoteCustomWindowAnswersTheSessionsInIt(t *testing.T) {
	for _, tc := range []struct {
		market, code, fixture string
		bars                  int
		first, last           string
	}{
		{"sh", "600519", fixTencentRangeSH, 29, "2026-03-02", "2026-04-10"},
		{"hk", "00700", fixTencentRangeHK, 27, "2026-03-02", "2026-04-10"},
	} {
		t.Run(tc.market, func(t *testing.T) {
			s := quoteServer(t)
			var askedFrom, askedTo string
			v := &quoteVendorStub{}
			body := readQuoteFixture(t, tc.fixture)
			v.parse = func(market, code string, bars int) (*QuoteResp, error) {
				return parseTencentQuote(market, code, readQuoteFixture(t, fixTencentSH), bars)
			}
			v.fetchRangeFn = func(market, code, from, to string) (*QuoteResp, error) {
				askedFrom, askedTo = from, to
				return parseTencentQuote(market, code, body, 0)
			}
			wireQuoteSources(s, v, sinaStub(t))

			rec := quoteGET(t, s, "/api/quote/"+tc.code+"?from=2026-03-02&to=2026-04-10")
			if rec.Code != http.StatusOK {
				t.Fatalf("a windowed request → %d (%s)", rec.Code, rec.Body.String())
			}
			got := quoteBody(t, rec)
			// The vendor was handed the reader's OWN dates, not a bar count derived from them.
			quoteEqStr(t, "from", askedFrom, "2026-03-02")
			quoteEqStr(t, "to", askedTo, "2026-04-10")
			if len(got.Bars) != tc.bars {
				t.Fatalf("%d bars, want the %d sessions the window holds", len(got.Bars), tc.bars)
			}
			quoteEqStr(t, "first bar", got.Bars[0].Date, tc.first)
			quoteEqStr(t, "last bar", got.Bars[len(got.Bars)-1].Date, tc.last)
			quoteEqStr(t, "barsSource", got.BarsSource, quoteSourceTencent)
			quoteEqStr(t, "barsUnavailable", got.BarsUnavailable, "")
			// A window OVERRIDES the range key rather than combining with it: honouring both would
			// let 近1月 truncate the window the reader chose.
			if rec := quoteGET(t, s, "/api/quote/"+tc.code+"?range=1m&from=2026-03-02&to=2026-04-10"); rec.Code == http.StatusOK {
				if n := len(quoteBody(t, rec).Bars); n != tc.bars {
					t.Errorf("range=1m beside a window gave %d bars, want the window's %d", n, tc.bars)
				}
			}
		})
	}
}

// A malformed window is REFUSED, and refused before any vendor is called: quietly falling back to
// the default range would draw the default window under the reader's own dates.
func TestQuoteMalformedWindowIsRefusedBeforeAnyVendorCall(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))

	for _, q := range []string{
		"?from=2026-03-02",
		"?to=2026-04-10",
		"?from=2026-04-10&to=2026-03-02",
		"?from=2026-13-01&to=2026-13-02",
		"?from=1926-01-01&to=2026-01-01",
	} {
		rec := quoteGET(t, s, "/api/quote/600519"+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", q, rec.Code)
			continue
		}
		if code := quoteErrCode(t, rec); code != "quote_bad_range" {
			t.Errorf("%s carried code %q", q, code)
		}
	}
	if tencent.n() != 0 {
		t.Errorf("%d refused window(s) still reached a vendor", tencent.n())
	}
}

// A source that has not declared the window is SKIPPED rather than called with the count it would
// otherwise fall back to — which would answer the last N sessions under the reader's own dates.
func TestQuoteWindowSkipsASourceThatCannotTakeDates(t *testing.T) {
	c := quoteShippedCache()
	cfg := quoteConfigDefault()
	for _, id := range []string{"sh", "sz", "hk"} {
		got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, id), quoteIntervalDailyRange))
		// Tencent takes dates; Sina's kline endpoint takes a count and nothing else, so it is absent
		// here while it is present in the plain daily chain below.
		if got != "tencent" {
			t.Errorf("%s dailyRange resolves to [%s], want [tencent]", id, got)
		}
	}
	if got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, "sh"), quoteIntervalDaily)); got != "tencent,sina" {
		t.Errorf("the plain daily chain changed to [%s]; the window must not have narrowed it", got)
	}
	// And the markets Tencent cannot draw daily are the markets it cannot draw a window of either.
	for _, id := range []string{"us", "bj"} {
		if got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, id), quoteIntervalDailyRange)); got != "" {
			t.Errorf("%s dailyRange resolves to [%s]", id, got)
		}
	}
	// A source that DECLARED the interval and has no range fetcher is a wiring mistake, and it says
	// so rather than falling through to the count-based fetcher.
	bad := quoteSource{
		name:  "declared-but-unwired",
		fetch: func(context.Context, string, string, int) (*QuoteResp, error) { return nil, nil },
		caps:  []quoteCapability{{intervals: []quoteInterval{quoteIntervalDailyRange}}},
	}
	_, err := bad.call(context.Background(), "sh", "600519", 60, quoteIntervalDailyRange,
		quoteWindow{from: "2026-03-02", to: "2026-04-10"})
	if err == nil {
		t.Error("a source declaring dailyRange with no range fetcher answered from its count fetcher")
	}
}
