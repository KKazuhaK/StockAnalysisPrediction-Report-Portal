package app

// quote_cache.go —— what stands between a page view and the vendors.
//
// A quote is the one thing in this portal that a browser asks for on every visit to every stock
// page, and it is served by two free endpoints that owe us nothing. Three separate failure modes
// follow from that, and this file is one answer to each:
//
//   - repetition — ten people opening the same stock in the same minute is ten identical calls, so
//     there is an LRU in front of them, expiring on a TTL the VENDOR's own session field decides;
//   - the thundering herd — those ten arriving in the same MILLISECOND all miss the LRU together,
//     so a hand-rolled single-flight collapses them into one upstream call and hands the one answer
//     to all ten;
//   - amplification — one caller walking five thousand codes would otherwise become five thousand
//     concurrent requests aimed at Tencent from this server's address, so a buffered channel caps
//     how many can be in flight at once no matter how many browsers are waiting.
//
// The single-flight is a mutex, a map and a done channel — the same hand-rolled shape as the LRU
// in mermaid_pdf.go. golang.org/x/sync IS resolvable (go.mod carries v0.22.0 as an indirect
// dependency of something else), so this is not a case of the package being unavailable: promoting
// an indirect dependency to a direct one is still a dependency this repository would then own, for
// about forty lines it can write itself.

import (
	"container/list"
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	// This cache is its OWN instance with its OWN bounds, and deliberately not the mermaidChartCache
	// next door. That one is keyed by user, source and theme and holds the rendered SVGs a report's
	// PDF export needs; it is process-wide at 512 entries and 32 MiB, and one entry per symbol per
	// range per refresh would walk somebody's warmed report charts straight out of it minutes before
	// they pressed Export. Two caches with different lifetimes and different eviction pressure do
	// not belong in one LRU however similar their code looks.
	//
	// Both bounds are live, which is the point of having two. A 1m entry is roughly 1.9 KB and a 1y
	// entry roughly 19 KB, so a portal whose users mostly look at short ranges is bounded by the
	// entry count (256 short entries is under 500 KB), while one that mostly asks for 1y hits the
	// byte ceiling at around 220 entries. A count alone would let the second case cost 5 MB; a byte
	// budget alone would let the first case hold tens of thousands of map keys.
	quoteCacheMaxEntries = 256
	quoteCacheMaxBytes   = 4 << 20

	// The TTL is chosen from the vendor's own market-session field rather than from a trading
	// calendar of our own. A hand-written calendar is wrong on exactly the days it matters: the
	// Spring Festival and National Day weeks move every year, half-day sessions exist, an
	// unscheduled closure is not on anybody's calendar in advance, and the exchange keeps publishing
	// its own late-afternoon batch (settlement figures, 龙虎榜) well after 15:00 — so a calendar that
	// says "closed at 15:00" pins a five-minute-stale snapshot over data that is still moving.
	// Tencent already answers with the state it believes the market is in, and it is never wrong
	// about its own feed. Deriving costs one array lookup and never needs a yearly maintenance PR.
	quoteTTLOpen = 30 * time.Second
	// Not "open" — closed, or a source that does not publish a session at all — takes the long TTL.
	// The unknown case is Sina, which carries no market-state field, and it is only ever reached
	// when Tencent has already failed: being gentle with the one vendor still answering is the right
	// bias precisely then, and the alternative (guess from this server's wall clock) is the
	// substitution the whole feature avoids, because a server whose zone has drifted would
	// confidently poll a closed market every thirty seconds forever.
	quoteTTLClosed = 5 * time.Minute

	// quoteUpstreamConcurrency is the ceiling on simultaneous vendor conversations for the whole
	// process. Four is chosen to be obviously polite rather than tuned: a page view needs one, and
	// anything that needs more than four at once is somebody enumerating the market, which is
	// exactly the traffic this portal must not relay.
	quoteUpstreamConcurrency = 4

	// quoteUpstreamTimeout is the whole failover's budget on the detached context the leader's load
	// runs under. Failover is two vendors at quoteFetchTimeout each plus however long it waits for a
	// slot on the ceiling above, and it exists because a load that no caller can cancel any more
	// still has to end: this is the deadline that replaces the one the leader's request carried.
	quoteUpstreamTimeout = 30 * time.Second

	// The size accounting is an ESTIMATE, and only has to be one: its job is to keep an unbounded
	// map from existing, not to predict the allocator. quoteBarBytes covers a QuoteBar's five int64s
	// plus its string header, and quoteRespBytes the surrounding struct, its map key and its list
	// element.
	quoteBarBytes  = 64
	quoteRespBytes = 256
)

// errQuoteNoSources is what a cache with an empty source list returns, so the handler's 503 path is
// reached rather than a nil response escaping as a success.
var errQuoteNoSources = errors.New("quote: no vendor sources configured")

// quoteFetchFunc is the shape both vendor fetchers in quote.go already have.
type quoteFetchFunc func(ctx context.Context, market, code string, bars int) (*QuoteResp, error)

// quoteSource is one vendor, named so the health counters and QuoteResp.Source agree without a
// second table mapping one to the other.
type quoteSource struct {
	name  string
	fetch quoteFetchFunc
}

// defaultQuoteSources is the failover order. Tencent first because one call returns history,
// snapshot and session state together and therefore cannot contradict itself about which instant it
// is describing; Sina second because it costs two calls and publishes no percentage of its own.
func defaultQuoteSources() []quoteSource {
	return []quoteSource{
		{name: quoteSourceTencent, fetch: fetchTencentQuote},
		{name: quoteSourceSina, fetch: fetchSinaQuote},
	}
}

// QuoteSourceHealth is one vendor's recent record, for an operator asking the question a log cannot
// answer at a glance: is this source down, or did it merely blip? Consecutive failures is the field
// that separates the two, which is why it is a counter and not a boolean.
type QuoteSourceHealth struct {
	Source      string    `json:"source"`
	LastSuccess time.Time `json:"lastSuccess"`
	LastError   string    `json:"lastError"`
	LastErrorAt time.Time `json:"lastErrorAt"`
	Failures    int       `json:"consecutiveFailures"`
}

type quoteCacheEntry struct {
	key     string
	resp    *QuoteResp
	expires time.Time
	size    int
}

// quoteFlight is one in-flight upstream load. Everyone who arrives for the same key while it is
// running waits on done and shares its result, which is the whole of the single-flight.
type quoteFlight struct {
	done chan struct{}
	resp *QuoteResp
	err  error
}

type quoteCache struct {
	once sync.Once

	mu      sync.Mutex // guards entries, lru, bytes and health
	entries map[string]*list.Element
	lru     list.List
	bytes   int
	health  map[string]*QuoteSourceHealth

	flightMu sync.Mutex // guards flights; separate from mu so a waiter never blocks a cache read
	flights  map[string]*quoteFlight

	sem chan struct{} // the upstream concurrency ceiling

	// sources and now are seams, set before first use. A test substitutes a counting stub here
	// rather than reaching for a package-level var, so two tests running concurrently under -race
	// cannot overwrite each other's fetcher.
	sources []quoteSource
	now     func() time.Time
}

// init fills in whatever the zero value lacks. The cache is a value field on Server, exactly like
// mermaidChartCache, so there is no constructor to do this in; sync.Once keeps the check to an
// atomic load on every call after the first. A seam already assigned by a test survives, which is
// what lets a test set sources on a zero-value Server.
func (c *quoteCache) init() {
	c.once.Do(func() {
		c.entries = make(map[string]*list.Element)
		c.flights = make(map[string]*quoteFlight)
		c.health = make(map[string]*QuoteSourceHealth)
		c.sem = make(chan struct{}, quoteUpstreamConcurrency)
		if c.sources == nil {
			c.sources = defaultQuoteSources()
		}
		if c.now == nil {
			c.now = time.Now
		}
	})
}

// quoteCacheKey identifies one cached answer. The bar count is part of it because it is part of the
// answer: the 1y response is not the 3m response with more rows appended, it is a different vendor
// call, and keying on the symbol alone would serve whichever range happened to be fetched first.
func quoteCacheKey(market, code string, bars int) string {
	return market + code + ":" + strconv.Itoa(bars)
}

// quoteEntrySize estimates one entry's footprint. See quoteBarBytes for why an estimate is enough.
func quoteEntrySize(resp *QuoteResp) int {
	if resp == nil {
		return quoteRespBytes
	}
	n := quoteRespBytes +
		len(resp.Symbol) + len(resp.Name) + len(resp.Market) + len(resp.Source) +
		len(resp.BarsSource) + len(resp.BarsUnavailable) +
		len(resp.Snapshot.ChangePct) + len(resp.Snapshot.AsOf) + len(resp.Snapshot.Session)
	for _, b := range resp.Bars {
		n += quoteBarBytes + len(b.Date)
	}
	return n
}

// quoteTTLFor picks the TTL from the vendor's own session state. See quoteTTLOpen for why it is
// derived rather than looked up in a calendar.
func quoteTTLFor(resp *QuoteResp) time.Duration {
	if resp != nil && resp.Snapshot.Session == quoteSessionOpen {
		return quoteTTLOpen
	}
	return quoteTTLClosed
}

// get returns a live entry and HOW LONG it stays live. The remaining lifetime, not the full TTL, is
// what the handler puts in Cache-Control: a browser told max-age=300 by a response that the server
// itself will drop in four seconds would hold a stale price for nearly five minutes longer than the
// server ever intended to.
func (c *quoteCache) get(key string) (*QuoteResp, time.Duration, bool) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return nil, 0, false
	}
	entry := element.Value.(*quoteCacheEntry)
	left := entry.expires.Sub(c.now())
	if left <= 0 {
		// Dropped rather than merely reported as a miss, so an expired symbol nobody asks for again
		// stops occupying the byte budget the moment anyone notices it has expired.
		c.removeLocked(element)
		return nil, 0, false
	}
	c.lru.MoveToFront(element)
	return entry.resp, left, true
}

func (c *quoteCache) put(key string, resp *QuoteResp, ttl time.Duration) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	size := quoteEntrySize(resp)
	expires := c.now().Add(ttl)
	if old := c.entries[key]; old != nil {
		entry := old.Value.(*quoteCacheEntry)
		c.bytes -= entry.size
		entry.resp, entry.expires, entry.size = resp, expires, size
		c.bytes += size
		c.lru.MoveToFront(old)
	} else {
		entry := &quoteCacheEntry{key: key, resp: resp, expires: expires, size: size}
		c.entries[key] = c.lru.PushFront(entry)
		c.bytes += size
	}
	for len(c.entries) > quoteCacheMaxEntries || c.bytes > quoteCacheMaxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.removeLocked(oldest)
	}
}

// removeLocked drops one element; the caller holds mu.
func (c *quoteCache) removeLocked(element *list.Element) {
	entry := element.Value.(*quoteCacheEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.size
	c.lru.Remove(element)
}

// acquire takes a slot on the upstream ceiling, or gives up when the context it is handed is done.
// That context is the load's own — fetch detaches it from any single caller, so what bounds the
// wait here is quoteUpstreamTimeout and not one browser tab's lifetime. Waiting on it at all is
// what keeps a queue that outlives its own deadline from holding places in line behind the four
// conversations actually in progress.
func (c *quoteCache) acquire(ctx context.Context) (func(), error) {
	c.init()
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// noteSuccess and noteFailure keep the per-source counters a later admin panel would read. There is
// deliberately no HTTP surface for them yet: the counters are cheap and the panel is not this
// change, and adding the endpoint now would ship an untested admin route nobody has asked for.
func (c *quoteCache) noteSuccess(source string) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.healthLocked(source)
	h.LastSuccess = c.now()
	h.Failures = 0
}

func (c *quoteCache) noteFailure(source string, err error) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.healthLocked(source)
	h.LastErrorAt = c.now()
	if err != nil {
		h.LastError = err.Error()
	}
	h.Failures++
}

func (c *quoteCache) healthLocked(source string) *QuoteSourceHealth {
	h := c.health[source]
	if h == nil {
		h = &QuoteSourceHealth{Source: source}
		c.health[source] = h
	}
	return h
}

// snapshotHealth copies the counters out. Copies, not pointers: whatever reads this must not be able
// to hold a reference into state the fetch path keeps mutating under the lock.
func (c *quoteCache) snapshotHealth() []QuoteSourceHealth {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]QuoteSourceHealth, 0, len(c.health))
	for _, h := range c.health {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// load runs the failover, holding one slot on the upstream ceiling for the WHOLE sequence rather
// than re-taking it per vendor. Failover is one logical upstream operation, and releasing between
// Tencent and Sina would let a burst of failing symbols double the fan-out at exactly the moment the
// vendors are least able to absorb it.
//
// A drift-gate failure arrives here as an ordinary error from the fetcher, which is the point: the
// gate is not advisory, and a source that answered with a shape we cannot trust has failed as
// completely as one that did not answer at all.
func (c *quoteCache) load(ctx context.Context, market, code string, bars int) (*QuoteResp, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	var firstErr error
	for _, src := range c.sources {
		resp, err := src.fetch(ctx, market, code, bars)
		if err != nil {
			// A failure that arrives because THIS side stopped waiting is not the vendor's, and it
			// must not land in the health counters: those are what an operator reads to decide
			// whether a source is down. The detach below means an abandoned browser tab no longer
			// reaches here at all, so this is the belt to that braces: it still covers a caller that
			// hands in an already-cancelled context, and it keeps the counters honest if the detach is
			// ever removed — which is exactly how the bug it guards arrived. The context's own
			// state decides it rather than the error's text, because a vendor that blows through
			// this feature's own eight-second budget also fails with context.DeadlineExceeded and
			// that one IS the vendor's.
			if ctx.Err() == nil {
				c.noteFailure(src.name, err)
			}
			if firstErr == nil {
				// The FIRST error, not the last: the primary's reason is the one worth reporting,
				// and the fallback's failure is usually the less interesting "and that one too".
				firstErr = err
			}
			continue
		}
		c.noteSuccess(src.name)
		return resp, nil
	}
	if firstErr == nil {
		return nil, errQuoteNoSources
	}
	return nil, firstErr
}

// fetch is the whole path a handler takes: cache, then single-flight, then load. It reports whether
// the answer cost an upstream call and how long the answer stays fresh.
//
// The returned *QuoteResp is SHARED with every other caller holding the same cache entry. Nothing
// downstream may mutate it; the handler copies the struct before stamping Cached on it.
func (c *quoteCache) fetch(ctx context.Context, market, code string, bars int) (*QuoteResp, bool, time.Duration, error) {
	c.init()
	key := quoteCacheKey(market, code, bars)
	if resp, left, ok := c.get(key); ok {
		return resp, true, left, nil
	}

	c.flightMu.Lock()
	if fl := c.flights[key]; fl != nil {
		c.flightMu.Unlock()
		select {
		case <-fl.done:
		case <-ctx.Done():
			// This caller gave up; the leader carries on, because the other waiters still want the
			// answer and the upstream call is already paid for.
			return nil, false, 0, ctx.Err()
		}
		if fl.err != nil {
			return nil, false, 0, fl.err
		}
		// A follower is served the leader's answer, which is in the LRU by now, and reports it as
		// cached. The flag answers "did this response cost an upstream call", which is the question
		// both the 缓存 chip and Cache-Control actually need answered — a follower made none.
		return fl.resp, true, quoteTTLFor(fl.resp), nil
	}
	fl := &quoteFlight{done: make(chan struct{})}
	c.flights[key] = fl
	c.flightMu.Unlock()

	// Re-checked under the flight: a request that arrived while the PREVIOUS flight was between
	// storing its result and deleting its map entry would otherwise start a second identical call
	// against a cache that already has the answer.
	if resp, left, ok := c.get(key); ok {
		fl.resp = resp
		c.endFlight(key, fl)
		return resp, true, left, nil
	}

	// The upstream call runs on a context DETACHED from the leader's request. The leader is only
	// whichever tab happened to arrive first, and Go cancels a request's context the moment that tab
	// navigates away — which used to cancel the shared load, hand every follower still waiting a
	// context.Canceled that the handler turns into 503 quote_unavailable, and record a client
	// disconnect against the vendor's health as though the vendor had gone down. A FOLLOWER still
	// holds its own context and can still give up on the select above; the LEADER cannot, because it
	// calls load synchronously — so an abandoned leader's handler goroutine stays alive until the
	// load finishes or quoteUpstreamTimeout fires. That is the price of the detach, and it is
	// bounded by that timeout rather than by anything the client does.
	loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), quoteUpstreamTimeout)
	defer cancelLoad()

	ttl := time.Duration(0)
	fl.resp, fl.err = c.load(loadCtx, market, code, bars)
	if fl.err == nil {
		ttl = quoteTTLFor(fl.resp)
		c.put(key, fl.resp, ttl)
	}
	resp, err := fl.resp, fl.err
	c.endFlight(key, fl)
	if err != nil {
		return nil, false, 0, err
	}
	return resp, false, ttl, nil
}

// endFlight publishes the leader's result to its waiters. The map entry goes first and the channel
// closes second, so a goroutine that misses the cache after this point starts a NEW flight instead
// of joining one that has already answered.
func (c *quoteCache) endFlight(key string, fl *quoteFlight) {
	c.flightMu.Lock()
	if c.flights[key] == fl {
		delete(c.flights, key)
	}
	c.flightMu.Unlock()
	close(fl.done)
}

// QuoteHealth reports each vendor's recent record. It is the accessor an admin panel will read; the
// panel itself is not part of this change.
func (s *Server) QuoteHealth() []QuoteSourceHealth { return s.quotes.snapshotHealth() }
