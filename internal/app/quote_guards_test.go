package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// quote_guards_test.go —— the guards a second review found were present, correct, and covered by
// nothing: deleting any of them left the whole suite green. Each test below was checked by
// reverting the guard it names and observing it fail, which is the only thing that makes a test
// about a guard worth having.

// ---------- the price ceiling ----------

// quoteCheckSnapshotPrices bounds the four prices the identity check never touches. It was entirely
// hollow: replacing its body with `return nil` broke nothing. It matters most on the Sina path,
// where Change is multiplied by 10,000 to derive a percentage — a multiply that wraps.
func TestQuoteSnapshotPriceCeilingCoversEveryFieldTheIdentityDoesNot(t *testing.T) {
	// last and prevClose are deliberately sane: they are the pair quoteCheckIdentity already bounds,
	// so a test that moved them would pass with this function deleted.
	for _, tc := range []struct {
		field string
		mut   func(s *QuoteSnapshot)
	}{
		{"open", func(s *QuoteSnapshot) { s.Open = quotePriceCeiling + 1 }},
		{"high", func(s *QuoteSnapshot) { s.High = quotePriceCeiling + 1 }},
		{"low", func(s *QuoteSnapshot) { s.Low = quotePriceCeiling + 1 }},
		{"change", func(s *QuoteSnapshot) { s.Change = quotePriceCeiling + 1 }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			snap := QuoteSnapshot{Last: 3335, PrevClose: 3331, Open: 3385, High: 3405, Low: 3307, Change: 4}
			tc.mut(&snap)
			if err := quoteCheckSnapshotPrices(&snap); !errors.Is(err, errQuotePriceBound) {
				t.Fatalf("a %s of %d passed the ceiling: %v", tc.field, quotePriceCeiling+1, err)
			}
		})
	}
	// And the fixture's own values must survive it, or the guard is just an outage.
	ok := QuoteSnapshot{Last: 3335, PrevClose: 3331, Open: 3385, High: 3405, Low: 3307, Change: 4}
	if err := quoteCheckSnapshotPrices(&ok); err != nil {
		t.Fatalf("the captured sh601899 snapshot was refused: %v", err)
	}
}

// End to end, through the real parser: an open past the ceiling must fail the response even though
// last, prevClose and the percentage all agree with each other perfectly.
func TestQuoteCeilingFailsAResponseWhoseIdentityIsPerfect(t *testing.T) {
	doc := tencentDoc(t, fixTencentSH)
	arr := tencentQtArray(t, doc, "sh601899")
	arr[5] = "99999999999999.99" // 今开, far past the ceiling; last/prevClose/pct untouched
	setTencentQtArray(t, doc, "sh601899", arr)
	_, err := parseTencentQuote("sh", "601899", quoteRemarshal(t, doc), 60)
	if !errors.Is(err, errQuotePriceBound) {
		t.Fatalf("got %v, want the price ceiling to refuse it", err)
	}
}

// The ceiling has a job on each side and neither had a test. Too high and the identity's multiply
// by 10^6 wraps int64, which is the bug the ceiling exists to close; too low and it refuses real
// quotes. Shrinking it by three orders of magnitude — the reviewer's mutation — must fail here.
func TestQuotePriceCeilingIsBoundedOnBothSides(t *testing.T) {
	// Upper: every multiply the package performs on a bounded price must stay inside int64.
	for _, factor := range []int64{1_000_000, 10_000} { // the identity, and Sina's percentage
		if quotePriceCeiling > math.MaxInt64/factor {
			t.Errorf("a price at the ceiling times %d overflows int64, which is the wrap the ceiling exists to prevent", factor)
		}
	}
	// Lower: derived from real data rather than asserted. The highest number in any captured
	// fixture, times a margin, is the floor — so shrinking the ceiling toward realistic prices
	// fails here instead of silently narrowing what the portal will serve.
	const margin = 1_000_000
	var highest int64
	for _, f := range []string{fixTencentSH, fixTencentSZ, fixTencentBJ} {
		resp, err := parseTencentQuote(quoteFixtureMarket(f), quoteFixtureCode(f), readQuoteFixture(t, f), 60)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, v := range []int64{resp.Snapshot.High, resp.Snapshot.Last, resp.Snapshot.Open} {
			if v > highest {
				highest = v
			}
		}
	}
	if highest == 0 {
		t.Fatal("read no prices out of the fixtures, so the floor below would be vacuous")
	}
	if floor := highest * margin; quotePriceCeiling < floor {
		t.Errorf("ceiling %d is below %d — %d× the highest price in any captured fixture (%d 分)",
			quotePriceCeiling, floor, margin, highest)
	}
}

func quoteFixtureMarket(f string) string {
	switch {
	case strings.Contains(f, "sz"):
		return "sz"
	case strings.Contains(f, "bj"):
		return "bj"
	}
	return "sh"
}

func quoteFixtureCode(f string) string {
	switch {
	case strings.Contains(f, "sz000001"):
		return "000001"
	case strings.Contains(f, "bj830799"):
		return "830799"
	}
	return "601899"
}

// ---------- 一字板: the boundary the ordering and range checks must NOT reject ----------

// A limit-up day that opens locked has open == high == low == last. Both checks use strict
// comparisons for exactly this reason; turning either into its non-strict form reports the most
// newsworthy day a stock can have as a source failure. Nothing pinned that until now.
func TestQuoteChecksAcceptALockedLimitDay(t *testing.T) {
	locked := QuoteSnapshot{Last: 3664, PrevClose: 3331, Open: 3664, High: 3664, Low: 3664, Change: 333}
	if err := quoteCheckRange(&locked); err != nil {
		t.Errorf("quoteCheckRange refused a locked limit-up day: %v", err)
	}
	if err := quoteCheckSnapshotOrder(&locked); err != nil {
		t.Errorf("quoteCheckSnapshotOrder refused a locked limit-up day: %v", err)
	}
	bars := []QuoteBar{{Date: "2026-09-04", Open: 3664, High: 3664, Low: 3664, Close: 3664, Volume: 100}}
	if err := quoteCheckBars(bars); err != nil {
		t.Errorf("quoteCheckBars refused a locked limit-up candle: %v", err)
	}
	// The touching-the-edge case: a last exactly ON the high, which is every close that finishes at
	// the day's top and is the other value a non-strict comparison would reject.
	edge := QuoteSnapshot{Last: 3405, PrevClose: 3331, Open: 3385, High: 3405, Low: 3307, Change: 74}
	if err := quoteCheckRange(&edge); err != nil {
		t.Errorf("quoteCheckRange refused a close at the day's high: %v", err)
	}
	if err := quoteCheckSnapshotOrder(&edge); err != nil {
		t.Errorf("quoteCheckSnapshotOrder refused a close at the day's high: %v", err)
	}
}

// ---------- 成交额 ----------

// Amount was the one money field with no guard, and the strip that renders it groups digits off the
// absolute value — so a negative reached a reader as a plausible positive.
func TestQuoteTurnoverIsNeverNegative(t *testing.T) {
	t.Run("tencent", func(t *testing.T) {
		doc := tencentDoc(t, fixTencentSH)
		arr := tencentQtArray(t, doc, "sh601899")
		arr[37] = "-593906" // 成交额, 万元
		setTencentQtArray(t, doc, "sh601899", arr)
		_, err := parseTencentQuote("sh", "601899", quoteRemarshal(t, doc), 60)
		if !errors.Is(err, errQuoteAmount) {
			t.Fatalf("got %v, want a negative 成交额 refused", err)
		}
	})
	t.Run("sina", func(t *testing.T) {
		raw := sinaSnapshotWith(t, "sh601899", func(f []string) []string {
			f[9] = "-5939060556.000"
			return f
		})
		_, err := parseSinaSnapshot("sh", "601899", raw)
		if !errors.Is(err, errQuoteAmount) {
			t.Fatalf("got %v, want a negative 成交额 refused", err)
		}
	})
}

// ---------- the deadline on the detached upstream context ----------

// Detaching the load from the leader's request context is what stopped one browser tab navigating
// away from 503-ing everybody else — but detaching also threw away the only deadline that call had.
// The replacement deadline is therefore the entire safety story, and replacing WithTimeout with
// WithCancel left the suite green.
func TestQuoteUpstreamLoadCarriesItsOwnDeadline(t *testing.T) {
	var deadline time.Time
	var hadDeadline bool
	c := &quoteCache{sources: []quoteSource{{
		name: quoteSourceTencent,
		fetch: func(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
			deadline, hadDeadline = ctx.Deadline()
			return &QuoteResp{Symbol: code, Market: market, Source: quoteSourceTencent,
				Snapshot: QuoteSnapshot{Last: 3335, PrevClose: 3331, Session: quoteSessionClose},
				Bars:     []QuoteBar{}}, nil
		},
	}}}
	// A caller with NO deadline of its own: whatever bound the upstream call gets can only have come
	// from the cache, which is the thing under test.
	if _, _, _, err := c.fetch(context.Background(), "sh", "601899", 60); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !hadDeadline {
		t.Fatal("the upstream call ran with no deadline at all: detaching the request context removed " +
			"the caller's, so nothing would stop a hung vendor from pinning the flight open")
	}
	if left := time.Until(deadline); left <= 0 || left > quoteUpstreamTimeout {
		t.Fatalf("upstream deadline is %v away, want (0, %v]", left, quoteUpstreamTimeout)
	}
}

// The leader's own handler is NOT released by its client hanging up — it is released by the deadline
// above. This pins the sentence in quote_cache.go that used to claim the opposite.
func TestQuoteAbandonedLeaderIsReleasedByTheDeadlineNotTheClient(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	c := &quoteCache{sources: []quoteSource{{
		name: quoteSourceTencent,
		fetch: func(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &QuoteResp{Symbol: code, Market: market, Source: quoteSourceTencent,
				Snapshot: QuoteSnapshot{Last: 3335, PrevClose: 3331, Session: quoteSessionClose},
				Bars:     []QuoteBar{}}, nil
		},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.fetch(ctx, "sh", "601899", 60)
		done <- err
	}()
	<-started
	cancel() // the tab navigates away
	select {
	case err := <-done:
		t.Fatalf("the leader returned %v as soon as its client hung up — the load was not detached, "+
			"so every follower waiting on it would have been handed that error as a 503", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the leader failed after its client left: %v", err)
	}
}

var _ = fmt.Sprintf
