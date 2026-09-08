package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// quote_cache_claims_test.go —— three promises the cache layer makes in comments and that a review
// proved nothing held: each was removed and the whole suite stayed green. They are cheap to state
// and expensive to lose, because every one of them fails as a wrong number rather than as an error.

// quoteCacheKey drops the bar count for a snapshot. The comment used to justify that with card-to-
// page warming, which is actually snapshotFor's job; what it really buys is that on a market whose
// reading page can only ever get a snapshot — us and bj, whose daily ranges degrade — the card and
// the page land on ONE key instead of two entries holding the same price.
func TestQuoteSnapshotKeyIsTheSameKeyWhateverBarCountWasAsked(t *testing.T) {
	page := quoteCacheKey("us", "AAPL", 66, quoteIntervalSnapshot, quoteWindow{}) // a reading page at 3个月
	card := quoteCacheKey("us", "AAPL", 0, quoteIntervalSnapshot, quoteWindow{})  // a home card, which asks for none
	if page != card {
		t.Errorf("a US card and a US reading page take two cache slots for one price:\n  page %q\n  card %q", page, card)
	}
	// And the normalisation must not leak into an interval that DOES have a series, or 分时 and 1年
	// would share a slot and whichever was asked for first would be drawn under both labels.
	if a, b := quoteCacheKey("sh", "601899", 66, quoteIntervalDaily, quoteWindow{}), quoteCacheKey("sh", "601899", 250, quoteIntervalDaily, quoteWindow{}); a == b {
		t.Errorf("two daily ranges share a cache slot: %q", a)
	}
	if a, b := quoteCacheKey("sh", "601899", 0, quoteIntervalDaily, quoteWindow{}), quoteCacheKey("sh", "601899", 0, quoteIntervalSnapshot, quoteWindow{}); a == b {
		t.Errorf("a daily answer and a snapshot share a cache slot: %q", a)
	}
}

// snapshotFor takes the FRESHEST matching entry, not the first the map hands it. Go randomises map
// iteration, so without the tie-break the 缓存 chip and the Cache-Control age become nondeterministic
// per request for any symbol with more than one live entry — which is every symbol a reader has
// looked at on two ranges.
func TestSnapshotForTakesTheFreshestEntryAndNotWhicheverTheMapOffersFirst(t *testing.T) {
	c := &quoteCache{}
	c.init()
	base := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return base }
	stale := &QuoteResp{Symbol: "601899", Snapshot: QuoteSnapshot{Last: 1000}}
	fresh := &QuoteResp{Symbol: "601899", Snapshot: QuoteSnapshot{Last: 2000}}
	// Same symbol, three live entries under different ranges. Only the last-expiring one may win.
	c.put(quoteCacheKey("sh", "601899", 66, quoteIntervalDaily, quoteWindow{}), stale, 1*time.Minute)
	c.put(quoteCacheKey("sh", "601899", 250, quoteIntervalDaily, quoteWindow{}), fresh, 9*time.Minute)
	c.put(quoteCacheKey("sh", "601899", 22, quoteIntervalDaily, quoteWindow{}), stale, 2*time.Minute)
	// Run it enough times that a map-order win would show: 200 draws over three keys.
	for i := 0; i < 200; i++ {
		got, ok := c.snapshotFor("sh", "601899")
		if !ok {
			t.Fatal("nothing found for a symbol with three live entries")
		}
		if got.Snapshot.Last != 2000 {
			t.Fatalf("draw %d returned the entry expiring at %v, not the freshest one", i, base.Add(9*time.Minute))
		}
	}
	// An expired entry is not a candidate however fresh it once was.
	c.now = func() time.Time { return base.Add(10 * time.Minute) }
	if _, ok := c.snapshotFor("sh", "601899"); ok {
		t.Error("an all-expired symbol still answered")
	}
}

// A batch may span markets no single source covers. loadBatchUncached splits the targets per source
// and gives each vendor only the symbols it declared — so a source is never blamed for a market it
// never claimed. With the shipped sources this branch never fires (Tencent declares every market for
// a snapshot), which is exactly why it needs a stub to be exercised at all.
func TestQuoteBatchGivesEachVendorOnlyTheMarketsItDeclared(t *testing.T) {
	asked := map[string][]string{}
	source := func(name string, ids ...string) quoteSource {
		markets := map[string]bool{}
		for _, id := range ids {
			markets[id] = true
		}
		return quoteSource{
			name: name,
			caps: []quoteCapability{{
				markets:   func(m *quoteMarket) bool { return m != nil && markets[m.id] },
				intervals: []quoteInterval{quoteIntervalSnapshot},
			}},
			fetchBatch: func(_ context.Context, targets []quoteTarget) (map[string]*QuoteResp, error) {
				out := map[string]*QuoteResp{}
				for _, tg := range targets {
					asked[name] = append(asked[name], tg.Symbol)
					if !markets[tg.Market.id] {
						// What a real vendor does with a code it has never heard of: no line for it.
						continue
					}
					out[tg.Symbol] = &QuoteResp{
						Symbol: tg.Code, Market: tg.Market.id,
						Snapshot: QuoteSnapshot{Last: 100, PrevClose: 100, Session: quoteSessionClose},
						Bars:     []QuoteBar{},
					}
				}
				if len(out) == 0 {
					// And a body carrying no line for ANY symbol asked about is an error from the
					// parser, not an empty success — the rule fetchTencentBatch follows.
					return nil, errors.New("no line for any symbol asked about")
				}
				return out, nil
			},
		}
	}
	c := &quoteCache{sources: []quoteSource{source("cn", "sh", "sz"), source("intl", "hk", "us")}}
	c.init()
	cfg := quoteConfig{Order: []string{"cn", "intl"}}

	targets := []quoteTarget{}
	for _, in := range []string{"601899", "000001", "00700", "AAPL"} {
		tg, err := quoteResolve(in)
		if err != nil {
			t.Fatalf("resolve %s: %v", in, err)
		}
		targets = append(targets, tg)
	}
	got := c.loadBatchUncached(context.Background(), cfg, targets)

	if len(got) != 4 {
		t.Fatalf("a batch spanning four markets answered %d symbols, want 4", len(got))
	}
	for name, want := range map[string][]string{
		"cn":   {"sh601899", "sz000001"},
		"intl": {"hk00700", "usAAPL"},
	} {
		if len(asked[name]) != len(want) {
			t.Errorf("%s was asked about %v, want exactly %v", name, asked[name], want)
			continue
		}
		seen := map[string]bool{}
		for _, s := range asked[name] {
			seen[s] = true
		}
		for _, w := range want {
			if !seen[w] {
				t.Errorf("%s was not asked about %s (got %v)", name, w, asked[name])
			}
		}
	}
	// The point of the split: neither vendor ever saw the other's markets, so neither can be blamed
	// for one. A single failing vendor would otherwise carry a market it never claimed.
	for _, bad := range []string{"hk00700", "usAAPL"} {
		for _, s := range asked["cn"] {
			if s == bad {
				t.Errorf("the mainland source was asked about %s, a market it never declared", bad)
			}
		}
	}
	// Both vendors answered everything they were handed, so neither may carry a failure. This is the
	// half of the split an operator reads: without it, the source that does not cover Hong Kong would
	// be handed 00700, refuse it, and climb a consecutive-failure streak for a market it never claimed.
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, h := range c.health {
		if h.Failures != 0 || h.LastError != "" {
			t.Errorf("%s recorded %d failure(s) (%q) for a batch it answered in full", name, h.Failures, h.LastError)
		}
	}
}

// ttlFor asks the INTERVAL first and the session second. The end-to-end test that names this ordering
// cannot actually see it: its captured 分时 body says SH_close, and a closed market takes the intraday
// TTL under either ordering. The case that separates them is the one the ordering was written for —
// an OPEN market on a one-minute chart, where asking the session first hands back the 30-second
// open-market TTL for a series whose points arrive once a minute.
func TestIntradayTTLWinsOverTheSessionOnAnOpenMarket(t *testing.T) {
	open := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionOpen}}
	closed := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}
	cfg := quoteConfig{}
	for _, iv := range []quoteInterval{quoteIntervalIntraday, quoteIntervalIntraday5D} {
		for label, resp := range map[string]*QuoteResp{"open": open, "close": closed, "no snapshot": nil} {
			if got := cfg.ttlFor(resp, iv); got != quoteTTLIntraday {
				t.Errorf("%s on session %q was cached for %v, want %v", iv, label, got, quoteTTLIntraday)
			}
		}
	}
	// And the ordering must not swallow the session where the session is still the right question.
	if got := cfg.ttlFor(open, quoteIntervalDaily); got != quoteTTLOpen {
		t.Errorf("a daily answer on an open market was cached for %v, want %v", got, quoteTTLOpen)
	}
	if got := cfg.ttlFor(open, quoteIntervalSnapshot); got != quoteTTLOpen {
		t.Errorf("a snapshot on an open market was cached for %v, want %v", got, quoteTTLOpen)
	}
}
