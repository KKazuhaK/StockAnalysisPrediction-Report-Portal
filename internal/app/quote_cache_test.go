package app

// quote_cache_test.go —— the source table and the resolver in front of it.
//
// The LRU, the single-flight, the TTLs and the health counters are exercised through the endpoint in
// quote_api_test.go, because that is where their claims are observable ("one upstream call", "503
// rather than a nil response"). What is here is the layer above them: which source a request is
// allowed to reach at all.
//
// Every assertion below is a NAMED source list rather than a count. A resolver bug is almost never
// "no sources"; it is the wrong one — Sina asked about Hong Kong, or Tencent handed a US daily
// series it answers with two rows fifteen years apart — and a length check passes through all of
// those.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// quoteSourceNamesOf renders a resolved chain for comparison and for a failure message that says
// which chain was walked rather than how long it was.
func quoteSourceNamesOf(srcs []quoteSource) string {
	names := make([]string, 0, len(srcs))
	for _, src := range srcs {
		names = append(names, src.name)
	}
	return strings.Join(names, ",")
}

// quoteShippedCache is a cache wired to the SHIPPED source table — declarations and all, fetchers
// included, none of which are called here: sourcesFor resolves, it does not fetch.
func quoteShippedCache() *quoteCache {
	return &quoteCache{sources: defaultQuoteSources()}
}

// quoteTestCodes is one real code per market. The resolver takes a TARGET rather than a market —
// whether a source has a spelling for the code is part of what it answers — so every row of every
// matrix below is asked about a code that market actually keys by, not about a market alone.
//
// They are the codes the captured fixtures are of, which is what makes them real: 00700 in
// particular is the Hong Kong shape Yahoo can spell (five digits starting with the zero it drops),
// and its neighbour 80737, which it cannot, is the subject of its own test.
var quoteTestCodes = map[string]string{
	"sh": "601899", "sz": "000001", "bj": "830799", "hk": "00700", "us": "AAPL",
}

// quoteAsk is the target a resolver row is asserted about. It fails rather than skips on a market
// with no code here, so that adding a market to quoteMarkets cannot quietly drop it out of the
// matrices that walk every row of that table.
func quoteAsk(t *testing.T, market string) quoteTarget {
	t.Helper()
	code, ok := quoteTestCodes[market]
	if !ok {
		t.Fatalf("no test code for market %q; every market in quoteMarkets needs one", market)
	}
	target, err := quoteTargetFor(market, code)
	if err != nil {
		t.Fatalf("resolve %s %s: %v", market, code, err)
	}
	return target
}

// ---------- the capability model itself ----------

// The two edges of a declaration, both of which other tests in this package lean on without saying
// so: a source with NO declaration is asked for everything (every bare stub in quote_api_test.go and
// quote_guards_test.go depends on it), and a grant that names markets but no intervals grants
// nothing rather than everything — which is the reason a source declares (markets, intervals) pairs
// instead of two independent sets that a half-filled literal could leave meaning the opposite.
func TestQuoteCapabilityDeclarationEdges(t *testing.T) {
	sh := quoteMarkets["sh"]

	var undeclared quoteSource
	for _, iv := range quoteAllIntervals {
		if !undeclared.covers(sh, iv) {
			t.Errorf("a source with no declaration refused %s; every bare stub in this package assumes the opposite", iv)
		}
	}

	marketsOnly := quoteSource{caps: []quoteCapability{{markets: func(*quoteMarket) bool { return true }}}}
	for _, iv := range quoteAllIntervals {
		if marketsOnly.covers(sh, iv) {
			t.Errorf("a grant naming markets and no intervals served %s: an empty interval list reads as "+
				"\"everything\" and must mean \"nothing\"", iv)
		}
	}

	// A market this build has no row for leaves the interval to decide alone. Only a caller that
	// skipped quoteResolve can produce one, and refusing there would turn a bad symbol into "every
	// vendor is down".
	if !quoteShippedCache().sources[0].covers(nil, quoteIntervalDaily) {
		t.Error("an unknown market was refused by the declaration rather than by quoteResolve")
	}

	// The CODE half of a declaration, which a market predicate cannot express. A source that has no
	// spelling for this code is not asked about it — and because that decision is made here rather
	// than inside the fetcher, it is a skip and never a failure charged to the vendor.
	picky := quoteSource{
		name:  "picky",
		caps:  []quoteCapability{{intervals: quoteAllIntervals}},
		knows: func(t quoteTarget) bool { return t.Code == "601899" },
	}
	if !picky.serves(quoteAsk(t, "sh"), quoteIntervalDaily) {
		t.Error("a source refused the one code it says it knows")
	}
	other, err := quoteTargetFor("sh", "600519")
	if err != nil {
		t.Fatalf("resolve sh 600519: %v", err)
	}
	if picky.serves(other, quoteIntervalDaily) {
		t.Error("a code the source has no spelling for was resolved to it anyway: a grant is a " +
			"predicate over the market, and quoteSource.knows is the half that answers about the code")
	}
	// And a source that declares nothing about codes still answers about every one of them, which is
	// what every bare stub in this package relies on.
	if !undeclared.serves(other, quoteIntervalDaily) {
		t.Error("a source with no knows function refused a code")
	}
}

// ---------- the resolver ----------

// The matrix, spelled out. This is the table an operator's order is applied WITHIN, and it is the
// whole of stage 0: capability is declared per (market, interval) pair, so that "the US daily series
// does not come from Tencent" is a property of the build and not of a setting somebody has to get
// right.
//
// Read the empty rows deliberately. (bj, daily) and (us, daily) resolve to NOBODY on an unconfigured
// portal — Tencent answers the first with "day":[] and the second with two rows fifteen years apart
// (ADR 0030 §4), and Sina will not fetch a Beijing series either. Those two pairs ARE asked for: every
// daily range asks for a daily series in every market, and an empty chain is what servableInterval
// turns into the price plus an empty chart. That is what makes the US row a hole a source can fill
// rather than a question nobody asks — see TestQuoteDailyRangeAsksTheResolverAndNotTheMarketTable.
func TestQuoteResolverAnswersThePairAndNotJustTheMarket(t *testing.T) {
	c := quoteShippedCache()
	cfg := quoteConfigDefault()
	for _, tc := range []struct {
		market string
		iv     quoteInterval
		want   string
	}{
		{"sh", quoteIntervalSnapshot, "tencent,sina"},
		{"sz", quoteIntervalSnapshot, "tencent,sina"},
		{"bj", quoteIntervalSnapshot, "tencent,sina"},
		{"hk", quoteIntervalSnapshot, "tencent"},
		{"us", quoteIntervalSnapshot, "tencent"},

		{"sh", quoteIntervalDaily, "tencent,sina"},
		{"sz", quoteIntervalDaily, "tencent,sina"},
		{"bj", quoteIntervalDaily, ""},
		{"hk", quoteIntervalDaily, "tencent"},
		// The line this whole stage exists for: nothing shipped serves a US daily series, so a
		// source that declares one is the only source that can be asked for it, wherever the
		// operator puts it in quote_source_order.
		{"us", quoteIntervalDaily, ""},

		// 分时 — one session of minutes. Tencent's minute endpoint answers the three markets where
		// a real series was measured and is the only ENABLED source for any of them; Yahoo declares
		// all four but ships out of the order, so it appears in none of these rows.
		{"sh", quoteIntervalIntraday, "tencent"},
		{"sz", quoteIntervalIntraday, "tencent"},
		{"hk", quoteIntervalIntraday, "tencent"},
		// Beijing's only captured minute body is a suspended stock answering ONE point, and the US
		// body has one point and no trading date at all, so neither is declared and neither resolves
		// to anybody. The handler serves the price with an empty chart rather than a 503
		// (servableInterval) — which is what makes an undeclared pair a safe thing to leave alone.
		{"bj", quoteIntervalIntraday, ""},
		{"us", quoteIntervalIntraday, ""},

		// 5日 — five sessions at five minutes, from Tencent's SECOND endpoint on the same host. The
		// two windows are separate declarations and not one, and the reason is still worth reading
		// twice: minute/query answers today and has no window parameter, so folding them together
		// would answer a 5日 request with a single day under the wrong label. What changed is that
		// day/query was measured and now backs the second declaration; the mechanism did not.
		{"sh", quoteIntervalIntraday5D, "tencent"},
		{"hk", quoteIntervalIntraday5D, "tencent"},
		// Neither of these, and for two different measured reasons — day/query refuses the US market
		// outright, and answers the one Beijing code with a week from April 2025.
		{"us", quoteIntervalIntraday5D, ""},
		{"bj", quoteIntervalIntraday5D, ""},
	} {
		got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, tc.market), tc.iv))
		if got != tc.want {
			t.Errorf("sourcesFor(%s, %s) = [%s], want [%s]", tc.market, tc.iv, got, tc.want)
		}
	}
}

// Behaviour identical to before capabilities existed, which is the acceptance criterion for the
// refactor: for every market the portal serves, the chain a REAL request walks is the one it walked
// when the source table said "Tencent everywhere, Sina where m.sina".
//
// The want column below is therefore not derived from the declarations — deriving it would make this
// test agree with whatever they say — it is the old table, written out.
func TestQuoteResolverKeepsEveryChainThePortalActuallyWalks(t *testing.T) {
	c := quoteShippedCache()
	cfg := quoteConfigDefault()
	for _, tc := range []struct{ market, want string }{
		{"sh", "tencent,sina"},
		{"sz", "tencent,sina"},
		{"bj", "tencent,sina"},
		{"hk", "tencent"},
		{"us", "tencent"},
	} {
		// The interval a 3个月 request arrives at on THIS deployment: what the range asks for,
		// narrowed to what the enabled sources declare. That composition is the production path
		// (apiQuote) and the reason bj and us still walk the snapshot chain here.
		target := quoteAsk(t, tc.market)
		iv := c.servableInterval(cfg, target, quoteRangeFor("3m").interval)
		got := quoteSourceNamesOf(c.sourcesFor(cfg, target, iv))
		if got != tc.want {
			t.Errorf("a %s request (%s) now walks [%s], want [%s] — the chain it walked before the "+
				"declarations existed", tc.market, iv, got, tc.want)
		}
	}
	// And the two markets a daily range degrades to a snapshot on are the two the handler blanks bars
	// for. They agree today by arithmetic rather than by construction — quoteMarket.history is a fact
	// about fqkline and Sina's kline, and the degradation is a fact about the whole enabled table — so
	// this row is worth asserting: if they ever disagree, one of them is asking a vendor for a series
	// the other is about to throw away.
	for id := range quoteMarkets {
		wantSnapshot := !quoteMarketHasDailyHistory(id)
		iv := c.servableInterval(cfg, quoteAsk(t, id), quoteIntervalDaily)
		if got := iv == quoteIntervalSnapshot; got != wantSnapshot {
			t.Errorf("%s: a daily range degrades to a snapshot %v, market-has-no-history %v", id, got, wantSnapshot)
		}
	}
}

// An order naming a source that cannot serve the pair falls through to one that can, rather than
// failing or serving the request from a source that never claimed it. The order is a preference
// among the capable, not an instruction to the incapable.
func TestQuoteResolverFallsThroughASourceThatCannotServeThePair(t *testing.T) {
	c := quoteShippedCache()

	// Sina first, and Hong Kong is a market it has no line shape for.
	cfg := quoteConfig{Order: []string{quoteSourceSina, quoteSourceTencent}}.orDefaults()
	if got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, "hk"), quoteIntervalSnapshot)); got != "tencent" {
		t.Errorf("hk with sina first resolved to [%s], want [tencent]", got)
	}
	// The same order on a market Sina DOES serve still honours it, or the line above would pass on a
	// resolver that had simply lost the operator's order.
	if got := quoteSourceNamesOf(c.sourcesFor(cfg, quoteAsk(t, "sh"), quoteIntervalDaily)); got != "sina,tencent" {
		t.Errorf("sh with sina first resolved to [%s], want [sina,tencent]", got)
	}

	// The shape the next source to be added has, ahead of it existing: one that serves US dailies and
	// every market's intraday, placed LAST in the order. It still gets the US daily series, because
	// it is the only source that declared the pair — by construction, not by position.
	withNew := &quoteCache{sources: append(defaultQuoteSources(), quoteSource{
		name: "newcomer",
		caps: []quoteCapability{
			{markets: func(m *quoteMarket) bool { return m.id == "us" }, intervals: []quoteInterval{quoteIntervalDaily}},
			{intervals: []quoteInterval{quoteIntervalIntraday5D}},
		},
	})}
	last := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, "newcomer"}}.orDefaults()
	if got := quoteSourceNamesOf(withNew.sourcesFor(last, quoteAsk(t, "us"), quoteIntervalDaily)); got != "newcomer" {
		t.Errorf("us daily resolved to [%s], want [newcomer] — the only source that declared the pair", got)
	}
	// The 5日 window on a market Tencent does NOT declare it for: the newcomer's grant is every
	// market, so it picks up the US from last place in the order while Tencent, which refuses that
	// market at this endpoint, is skipped rather than tried and failed.
	if got := quoteSourceNamesOf(withNew.sourcesFor(last, quoteAsk(t, "us"), quoteIntervalIntraday5D)); got != "newcomer" {
		t.Errorf("us 5d intraday resolved to [%s], want [newcomer]", got)
	}
	// And where Tencent DOES declare it, the order is a preference among the capable: Tencent is
	// first and the newcomer follows, exactly as for the one-session window below.
	if got := quoteSourceNamesOf(withNew.sourcesFor(last, quoteAsk(t, "sh"), quoteIntervalIntraday5D)); got != "tencent,newcomer" {
		t.Errorf("sh 5d intraday resolved to [%s], want [tencent,newcomer]", got)
	}
	// And it does NOT displace the source that already serves the one-session window: the order is
	// a preference among the capable, so Tencent stays first for 分时 and the newcomer follows it.
	if got := quoteSourceNamesOf(withNew.sourcesFor(last, quoteAsk(t, "sh"), quoteIntervalIntraday)); got != "tencent" {
		t.Errorf("sh intraday resolved to [%s], want [tencent]", got)
	}
	// And it does not take over what it never claimed: the A-share daily chain is untouched by its
	// presence.
	if got := quoteSourceNamesOf(withNew.sourcesFor(last, quoteAsk(t, "sh"), quoteIntervalDaily)); got != "tencent,sina" {
		t.Errorf("sh daily resolved to [%s] once a third source existed, want [tencent,sina]", got)
	}
	// A name in the order that this build has no source for is still dropped rather than counted.
	stale := quoteConfig{Order: []string{"gone", quoteSourceTencent}}.orDefaults()
	if got := quoteSourceNamesOf(c.sourcesFor(stale, quoteAsk(t, "sh"), quoteIntervalDaily)); got != "tencent" {
		t.Errorf("an order naming a source this build lacks resolved to [%s], want [tencent]", got)
	}
}

// A skip is not a failure. The health counters are what an operator reads to decide whether a vendor
// is down, and a source this build never asked has no record to be judged on: asking Sina about
// usAAPL returns "sina has no A-share-shaped line for the us market", a fact about our own code,
// and counting it would paint a permanent red streak on the fallback for every US symbol looked up
// while Tencent is having a bad afternoon.
func TestQuoteSkippedSourceIsNeverBlamedInTheHealthRecord(t *testing.T) {
	sina := failingStub("sina should never have been called")
	c := &quoteCache{sources: []quoteSource{
		{name: quoteSourceTencent, fetch: failingStub("tencent is down").fetch,
			caps: []quoteCapability{{intervals: quoteAllIntervals}}},
		{name: quoteSourceSina, fetch: sina.fetch,
			caps: []quoteCapability{{markets: func(m *quoteMarket) bool { return m.sina },
				intervals: []quoteInterval{quoteIntervalSnapshot, quoteIntervalDaily}}}},
	}}

	// Hong Kong: Tencent is asked and fails, Sina is not asked at all.
	if _, err := c.load(context.Background(), quoteConfigDefault(), "hk", "00700", 60, quoteIntervalSnapshot); err == nil {
		t.Fatal("a failing primary with a skipped fallback returned no error")
	}
	if sina.n() != 0 {
		t.Errorf("sina was called %d times about a market it does not serve", sina.n())
	}
	health := c.snapshotHealth()
	if len(health) != 1 || health[0].Source != quoteSourceTencent || health[0].Failures != 1 {
		t.Fatalf("health after one failure and one skip = %+v, want tencent alone with 1 failure", health)
	}

	// The control: on a market Sina DOES serve, the same stub is called and the same failure IS
	// recorded. Without this half, a resolver that returned nothing at all would pass the assertions
	// above.
	if _, err := c.load(context.Background(), quoteConfigDefault(), "sh", "601899", 60, quoteIntervalDaily); err == nil {
		t.Fatal("two failing vendors returned no error")
	}
	if sina.n() != 1 {
		t.Errorf("sina was called %d times about an A-share, want 1", sina.n())
	}
	if health := c.snapshotHealth(); len(health) != 2 {
		t.Fatalf("health after a real fallback failure = %+v, want both sources recorded", health)
	}

	// A pair nobody declared is not a vendor outage either: nobody is called, nobody is blamed, and
	// the caller gets the sentinel that reaches the handler's 503 rather than a nil response.
	fresh := &quoteCache{sources: defaultQuoteSources()}
	if _, err := fresh.load(context.Background(), quoteConfigDefault(), "us", "AAPL", 60, quoteIntervalDaily); !errors.Is(err, errQuoteNoSources) {
		t.Errorf("us daily, which no shipped source declares, returned %v, want %v", err, errQuoteNoSources)
	}
	if h := fresh.snapshotHealth(); len(h) != 0 {
		t.Errorf("a pair no source declared was recorded against a vendor: %+v", h)
	}
}

// ---------- what the admin panel is told ----------

// The panel has one markets column per source and a source's grants now differ per interval, so that
// column is the UNION and has to be: Tencent's row must keep naming the US, whose PRICE it serves,
// while the resolver keeps it away from the US daily SERIES. A markets list built from the daily
// grant alone would quietly drop the US and Beijing rows an operator uses to see that the primary
// covers them at all.
func TestQuoteSourceMarketsAreTheUnionOverIntervals(t *testing.T) {
	byName := map[string]quoteSource{}
	for _, src := range defaultQuoteSources() {
		byName[src.name] = src
	}
	tencent := byName[quoteSourceTencent]
	if got := strings.Join(tencent.marketIDs(), ","); got != "bj,hk,sh,sz,us" {
		t.Errorf("tencent markets = %s, want every market in the table", got)
	}
	if tencent.covers(quoteMarkets["us"], quoteIntervalDaily) {
		t.Error("tencent's markets list names the US AND its declaration serves the US daily series: " +
			"the union is only honest while the resolver still refuses the pair")
	}
	if got := strings.Join(byName[quoteSourceSina].marketIDs(), ","); got != "bj,sh,sz" {
		t.Errorf("sina markets = %s, want the three A-share markets", got)
	}
}

// ---------- what a daily range asks for ----------

// The interval a 3个月 request arrives at, for every market and both configurations. It is the
// composition of two things and neither one alone is the answer: the RANGE says "a daily series"
// everywhere, and the RESOLVER says whether anything enabled can produce one.
//
// This is the test that would have caught stage 1 shipping unreachable. The interval used to be read
// off quoteMarket.history — a column that says what fqkline and Sina's kline answer — so the US asked
// for a snapshot however the sources were configured, (us, daily) was a pair nothing ever resolved,
// and the panel's 日线 column advertised a capability an operator could switch on for no effect. The
// two `us` rows below are the whole difference; the other rows are the behaviour that must not move.
func TestQuoteDailyRangeAsksTheResolverAndNotTheMarketTable(t *testing.T) {
	c := quoteShippedCache()
	off := quoteConfigDefault()
	on := quoteConfig{Order: []string{quoteSourceTencent, quoteSourceSina, quoteSourceYahoo}}.orDefaults()
	// The range asks the same question in every market — that is the fix — so it is read once here
	// rather than per row.
	asked := quoteRangeFor("3m").interval
	if asked != quoteIntervalDaily {
		t.Fatalf("3个月 asks for %q; this test is about the daily ranges", asked)
	}
	for _, tc := range []struct {
		market string
		yahoo  bool
		want   quoteInterval
		chain  string
	}{
		// The US: today's answer while nothing serves its series, and a real chart from the one
		// source that declares it the moment an operator enables that source. Nothing else changes —
		// no column is flipped, no second setting is consulted.
		{"us", false, quoteIntervalSnapshot, "tencent"},
		{"us", true, quoteIntervalDaily, "yahoo"},
		// Beijing degrades either way: Tencent answers "day":[], Sina answers sixteen months stale,
		// and Yahoo has no symbol for that exchange at all. This row is the reason the fix is "ask
		// the resolver" and not "flip quoteMarket.history for the US".
		{"bj", false, quoteIntervalSnapshot, "tencent,sina"},
		{"bj", true, quoteIntervalSnapshot, "tencent,sina"},
		// And the markets that always had a chart still get it from the same place, in the same
		// order: enabling a third source must not move an A-share or Hong Kong series.
		{"sh", false, quoteIntervalDaily, "tencent,sina"},
		{"sh", true, quoteIntervalDaily, "tencent,sina"},
		{"sz", true, quoteIntervalDaily, "tencent,sina"},
		{"hk", true, quoteIntervalDaily, "tencent"},
	} {
		cfg := off
		if tc.yahoo {
			cfg = on
		}
		target := quoteAsk(t, tc.market)
		iv := c.servableInterval(cfg, target, asked)
		if iv != tc.want {
			t.Errorf("3个月 on %s with yahoo=%v is served as %q, want %q", tc.market, tc.yahoo, iv, tc.want)
		}
		// The chain matters as much as the interval: "daily" served by nobody would be a 503, and
		// "daily" served by Tencent on the US would be two rows fifteen years apart drawn as a chart.
		if got := quoteSourceNamesOf(c.sourcesFor(cfg, target, iv)); got != tc.chain {
			t.Errorf("3个月 on %s with yahoo=%v walks [%s], want [%s]", tc.market, tc.yahoo, got, tc.chain)
		}
	}
}

// ---------- the panel may not advertise what the resolver never asks for ----------

// Every (source, market, interval) the 管理 → 行情源 panel prints is a claim an operator acts on: they
// read Yahoo's 日线 column, see the one market nothing else covers, and add it to quote_source_order.
// So every claim has to be REACHABLE — there must be a range a reader can pick that asks for that
// interval in that market and resolves to that source once it is enabled.
//
// That was exactly what stage 1 shipped without: "yahoo 日线 = us" was printed, pinned by a test, and
// unreachable, because no request ever asked for (us, daily). This walks the panel's own columns
// rather than the declarations, so it fails on the day the two disagree in either direction.
func TestQuotePanelAdvertisesNothingTheResolverCannotBeAskedFor(t *testing.T) {
	c := quoteShippedCache()
	// Every compiled-in source enabled: the panel lists the disabled ones too, and what it says about
	// them is a claim about what enabling them would do.
	all := quoteConfig{Order: quoteSourceNames()}.orDefaults()
	// The intervals a reader can actually ask for, taken from the range table rather than from
	// quoteAllIntervals: a capability no range asks for is unreachable however loudly it is declared.
	asked := map[quoteInterval]string{}
	for key, spec := range quoteRanges {
		asked[spec.interval] = key
	}
	for _, src := range c.describeSources() {
		// ONE interval per column, which is the half of this test that was missing. The 分时 column
		// used to accept EITHER intraday interval, so a source declaring only the one-session window
		// satisfied the check for a column an operator reads as covering both — and that is exactly
		// how the panel came to advertise 5日 in three markets while every 5日 request degraded to a
		// snapshot. A column that can be satisfied by a different interval than the one it names
		// cannot catch an over-claim, which is the only thing this test is for.
		for _, column := range []struct {
			name string
			ids  []string
			iv   quoteInterval
		}{
			{"日线", src.Daily, quoteIntervalDaily},
			{"分时", src.Intraday, quoteIntervalIntraday},
			{"5日", src.Intraday5D, quoteIntervalIntraday5D},
		} {
			for _, id := range column.ids {
				reached := false
				if key, askable := asked[column.iv]; askable {
					// The whole path a reader takes: the range's interval, narrowed by what is
					// enabled, then resolved. A column entry is honest when this ends at that source.
					target := quoteAsk(t, id)
					served := c.servableInterval(all, target, quoteRangeFor(key).interval)
					if served == column.iv {
						for _, got := range c.sourcesFor(all, target, served) {
							reached = reached || got.name == src.Name
						}
					}
				}
				if !reached {
					t.Errorf("the panel says %s serves %s for %s, and no range a reader can pick "+
						"resolves that pair to it: the column is a claim an operator acts on, so it "+
						"must be reachable or it must not be printed", src.Name, column.name, id)
				}
			}
		}
	}
}

// The panel's two intraday columns must be able to DISAGREE, and with the shipped sources they
// cannot be observed to: Tencent declares both windows on the same three markets and Yahoo both on
// the same four, so a server that unioned them back into one column would print exactly what the
// split prints and every assertion above would still pass.
//
// That is the state the panel was in when it advertised 5日 in three markets nothing served. So the
// asymmetry is supplied here rather than waited for: one source, one window, and a check that the
// column for the OTHER window does not borrow it.
func TestPanelKeepsTheTwoIntradayWindowsApartWhenASourceServesOnlyOne(t *testing.T) {
	oneDayOnly := quoteSource{
		name:  "sessiononly",
		fetch: func(context.Context, string, string, int) (*QuoteResp, error) { return nil, nil },
		caps: []quoteCapability{{
			markets:   func(m *quoteMarket) bool { return m.id == "sh" },
			intervals: []quoteInterval{quoteIntervalIntraday},
		}},
	}
	fiveDayOnly := quoteSource{
		name:  "weekonly",
		fetch: func(context.Context, string, string, int) (*QuoteResp, error) { return nil, nil },
		caps: []quoteCapability{{
			markets:   func(m *quoteMarket) bool { return m.id == "hk" },
			intervals: []quoteInterval{quoteIntervalIntraday5D},
		}},
	}
	c := &quoteCache{sources: []quoteSource{oneDayOnly, fiveDayOnly}}
	c.init()
	byName := map[string]quoteSourceInfo{}
	for _, info := range c.describeSources() {
		byName[info.Name] = info
	}
	for _, tc := range []struct {
		source, intraday, intraday5d string
	}{
		{"sessiononly", "sh", ""},
		{"weekonly", "", "hk"},
	} {
		info := byName[tc.source]
		if got := strings.Join(info.Intraday, ","); got != tc.intraday {
			t.Errorf("%s 分时 = %q, want %q — a 5日 grant must not fill the 分时 column", tc.source, got, tc.intraday)
		}
		if got := strings.Join(info.Intraday5D, ","); got != tc.intraday5d {
			t.Errorf("%s 5日 = %q, want %q — a 分时 grant must not fill the 5日 column", tc.source, got, tc.intraday5d)
		}
		// The union column is a WIDER claim on purpose and still names both.
		if got := strings.Join(info.Markets, ","); got != tc.intraday+tc.intraday5d {
			t.Errorf("%s markets = %q, want the union %q", tc.source, got, tc.intraday+tc.intraday5d)
		}
	}
}
