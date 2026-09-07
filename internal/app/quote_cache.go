package app

// quote_cache.go —— what stands between a page view and the vendors.
//
// A quote is the one thing in this portal that a browser asks for on every visit to every stock
// page, and it is served by two free endpoints that owe us nothing. Three separate failure modes
// follow from that, and this file is one answer to each:
//
//   - repetition — ten people opening the same stock in the same minute is ten identical calls, so
//     there is an LRU in front of them, expiring on a TTL the VENDOR's own session field selects
//     between (open or closed) and an operator sets the length of (quote_admin_api.go);
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
	"strings"
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

	// WHICH of the two TTLs applies is chosen from the vendor's own market-session field rather than
	// from a trading calendar of our own, and that part is not configurable. A hand-written calendar
	// is wrong on exactly the days it matters: the Spring Festival and National Day weeks move every
	// year, half-day sessions exist, an unscheduled closure is not on anybody's calendar in advance,
	// and the exchange keeps publishing its own late-afternoon batch (settlement figures, 龙虎榜)
	// well after 15:00 — so a calendar that says "closed at 15:00" pins a five-minute-stale snapshot
	// over data that is still moving. Tencent already answers with the state it believes the market
	// is in, and it is never wrong about its own feed.
	//
	// HOW LONG each one lasts is a load-versus-freshness trade that belongs to whoever runs the
	// portal, so these two are the shipped defaults behind quote_ttl_open_secs and
	// quote_ttl_closed_secs rather than the only answers. Nothing changes until an admin saves
	// something else: quoteConfigDefault reads exactly these.
	quoteTTLOpen = 30 * time.Second
	// Not "open" — closed, or a source that does not publish a session at all — takes the long TTL.
	// The unknown case is Sina, which carries no market-state field, and it is only ever reached
	// when Tencent has already failed: being gentle with the one vendor still answering is the right
	// bias precisely then, and the alternative (guess from this server's wall clock) is the
	// substitution the whole feature avoids, because a server whose zone has drifted would
	// confidently poll a closed market every thirty seconds forever.
	quoteTTLClosed = 5 * time.Minute

	// The floors under those two, and they are Go consts rather than two more settings for the
	// reason ADR 0017 gives about the retention floors: a floor that is itself configurable is not a
	// floor. Both are applied on SAVE and again on READ, so a value written by an older build, by a
	// restored backup or by hand into meta cannot take the portal below them — the same discipline
	// cleanupConfigLoad applies to the retention days.
	//
	// The number they protect is not this portal's, it is the vendors'. Five seconds of open-market
	// TTL is already one call per symbol per five seconds per portal against two free endpoints that
	// owe us nothing, and the address that gets rate-limited for it is ours.
	quoteTTLOpenFloor   = 5 * time.Second
	quoteTTLClosedFloor = 30 * time.Second

	// And the ceiling over both, applied at the same two sites. It is ONE number where the floors
	// are two because the reason does not vary with the market state: a cached price that outlives
	// the session it was fetched in is indistinguishable from a broken feed. The reading page shows
	// a number that will never change again, with only the 缓存 chip as a hint, and the only way out
	// is the clear-cache button or noticing the value in the form.
	//
	// The large-positive side is the one nothing else catches: 2000000000 typed into the closed TTL
	// (or carried in by a restored backup) is time.Duration(2e9)*time.Second = 2e18ns, comfortably
	// UNDER the int64 wrap and therefore a perfectly valid Duration — every symbol pinned in the LRU
	// for about sixty-three years.
	//
	// A day is deliberately the generous end of "no longer than a session": long enough that an
	// operator shedding vendor load across a weekend is not fighting it, short enough that a value
	// nobody meant expires by itself.
	quoteTTLCeiling = 24 * time.Hour

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

// errQuoteNoSources is what load returns when it called nobody at all — an empty source list, an
// order that names none of this build's sources, or a market none of the enabled ones covers. It
// exists so the handler's 503 path is reached rather than a nil response escaping as a success.
var errQuoteNoSources = errors.New("quote: no vendor sources configured")

// quoteFetchFunc is the shape both vendor fetchers in quote.go already have.
type quoteFetchFunc func(ctx context.Context, market, code string, bars int) (*QuoteResp, error)

// quoteSource is one vendor, named so the health counters and QuoteResp.Source agree without a
// second table mapping one to the other.
type quoteSource struct {
	name  string
	fetch quoteFetchFunc
	// serves reports whether this source's parser covers a market. It is a PREDICATE over the
	// market table rather than a list of ids, because the fact it answers already lives in
	// quoteMarkets — quoteSinaTarget refuses hk and us on the `sina` column — and a second list
	// here would be free to disagree with the one that actually decides. The admin panel prints
	// what this predicate says, so a source cannot advertise a market its fetcher then refuses.
	//
	// nil means "no restriction recorded", which is what a test that wires a bare stub gets.
	serves func(*quoteMarket) bool
}

// covers is serves with the nil case spelled out: an unrestricted source is asked for every market.
func (src quoteSource) covers(m *quoteMarket) bool {
	return src.serves == nil || m == nil || src.serves(m)
}

// marketIDs is the answer to "which markets can this source serve", asked of every row in the market
// table so that adding a market cannot leave a stale answer behind here.
func (src quoteSource) marketIDs() []string {
	out := make([]string, 0, len(quoteMarkets))
	for id, m := range quoteMarkets {
		if src.covers(m) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// defaultQuoteSources is the shipped failover order. Tencent first because one call returns history,
// snapshot and session state together and therefore cannot contradict itself about which instant it
// is describing; Sina second because it costs two calls and publishes no percentage of its own.
//
// It is also the list quote_source_order is validated against: a name no source here answers to is
// refused at the save rather than stored and silently skipped.
func defaultQuoteSources() []quoteSource {
	return []quoteSource{
		// fqkline answers all five markets — every fixture in testdata/quote/ came out of it.
		{name: quoteSourceTencent, fetch: fetchTencentQuote, serves: func(*quoteMarket) bool { return true }},
		{name: quoteSourceSina, fetch: fetchSinaQuote, serves: func(m *quoteMarket) bool { return m.sina }},
	}
}

// quoteSourceNames is every source compiled into this build, in the shipped order. Derived from
// defaultQuoteSources rather than written out again, so a source added there cannot be one the
// order setting refuses to name.
func quoteSourceNames() []string {
	srcs := defaultQuoteSources()
	out := make([]string, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, src.name)
	}
	return out
}

// ---------- the operator's three choices ----------

const (
	// A comma-separated source order. A source ABSENT from it is off — there is no separate
	// enable flag, because two ways to disable a source is one way for the two to disagree.
	setQuoteSourceOrder = "quote_source_order"
	// The two TTLs, in seconds, clamped into [quoteTTLOpenFloor / quoteTTLClosedFloor,
	// quoteTTLCeiling].
	setQuoteTTLOpenSecs   = "quote_ttl_open_secs"
	setQuoteTTLClosedSecs = "quote_ttl_closed_secs"
)

// quoteConfig is what an admin can change about the fetch path. Every field's zero value is refused
// by orDefaults below rather than obeyed: an empty order would mean "every source off" and a zero
// TTL would mean "expire on arrival", and neither is a thing anybody configured.
type quoteConfig struct {
	Order     []string // source names, in the order to try
	TTLOpen   time.Duration
	TTLClosed time.Duration
}

// quoteConfigDefault is what the portal did before any of this was configurable, and it is what an
// unconfigured portal still does. The whole point of the three settings is that this function is
// the answer until somebody saves something else.
func quoteConfigDefault() quoteConfig {
	return quoteConfig{Order: quoteSourceNames(), TTLOpen: quoteTTLOpen, TTLClosed: quoteTTLClosed}
}

// orDefaults fills in whatever a caller left unset. It exists because quoteConfig travels as a
// value: a zero one reaching load would disable every source, which is the one failure mode that
// looks like "the vendors are down" rather than like a bug here.
func (cfg quoteConfig) orDefaults() quoteConfig {
	def := quoteConfigDefault()
	if len(cfg.Order) == 0 {
		cfg.Order = def.Order
	}
	if cfg.TTLOpen <= 0 {
		cfg.TTLOpen = def.TTLOpen
	}
	if cfg.TTLClosed <= 0 {
		cfg.TTLClosed = def.TTLClosed
	}
	return cfg
}

// ttlFor picks which of the two TTLs this response gets. See quoteTTLOpen for why the choice is the
// vendor's session field and not a calendar.
func (cfg quoteConfig) ttlFor(resp *QuoteResp) time.Duration {
	cfg = cfg.orDefaults()
	if resp != nil && resp.Snapshot.Session == quoteSessionOpen {
		return cfg.TTLOpen
	}
	return cfg.TTLClosed
}

// quoteParseOrder splits a stored or submitted order into source names. It returns the KNOWN names,
// in the order given and without repeats, plus the first name this build has no source for.
//
// The two answers have different audiences, which is why they are separate return values rather than
// one error: a save refuses on `unknown` (an admin typing a vendor that does not exist should be
// told, not quietly given something else), while a READ drops the unknown name and keeps the rest —
// a portal restored from a backup written by a build with a third source must go on serving quotes
// from the two it does have, and falling back to the shipped default there would silently re-enable
// a source the operator had switched off.
func quoteParseOrder(raw string) (order []string, unknown string) {
	known := map[string]bool{}
	for _, name := range quoteSourceNames() {
		known[name] = true
	}
	seen := map[string]bool{}
	for _, field := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(field))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !known[name] {
			if unknown == "" {
				unknown = name
			}
			continue
		}
		order = append(order, name)
	}
	return order, unknown
}

// quoteConfigLoad reads the three settings and clamps on READ as well as on save, so a value that
// reached meta some other way — an older build, a hand edit, a restore — cannot put the portal
// below a floor or above the ceiling. Same shape and same reason as cleanupConfigLoad (ADR 0017).
func (s *Server) quoteConfigLoad() quoteConfig {
	cfg := quoteConfigDefault()
	// Total by construction, like settingInt: these getters sit on the request path and have to
	// answer for a Server with no store to ask. No configuration available reads as the shipped
	// behaviour, which is what the default is.
	if s.st == nil {
		return cfg
	}
	// An order with nothing recognisable left in it — blank, or naming only sources this build does
	// not have — keeps the shipped one. The alternative is a portal that answers every quote with a
	// 503 until somebody thinks to look in meta.
	if order, _ := quoteParseOrder(s.st.GetSetting(setQuoteSourceOrder, "")); len(order) > 0 {
		cfg.Order = order
	}
	// The fallback is cfg's own field, which quoteConfigDefault has just filled in — not the constant
	// again. Naming the constant here would be a second statement of the shipped default, free to
	// disagree with the first: a mutation that changed quoteConfigDefault's TTL left this path
	// serving the old one, and every test still passed.
	cfg.TTLOpen = quoteTTLSetting(s.st, setQuoteTTLOpenSecs, cfg.TTLOpen, quoteTTLOpenFloor)
	cfg.TTLClosed = quoteTTLSetting(s.st, setQuoteTTLClosedSecs, cfg.TTLClosed, quoteTTLClosedFloor)
	return cfg
}

// quoteTTLSetting reads one TTL in seconds. A value outside the range is CLAMPED to the nearer end,
// not replaced by the default the way settingInt would: an operator who asked for a two-second TTL
// wants the shortest one they are allowed to have, and handing them the five-minute default instead
// is further from what they asked for than the floor is. Only an unreadable value — absent, blank,
// not a number — falls back to the default, because that is not a request at all.
func quoteTTLSetting(st *Store, key string, def, floor time.Duration) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(st.GetSetting(key, "")))
	if err != nil {
		return def
	}
	return quoteClampTTL(time.Duration(n)*time.Second, floor)
}

// quoteClampTTL applies the floor and the ceiling, and it is handed a Duration rather than a seconds
// count so that the multiply is already done when the comparison happens. A seconds count big enough
// to overflow int64 nanoseconds wraps, and a wrapped value can come out negative — an entry that
// expires before it is stored, turning every page view into a vendor call. Comparing the seconds
// first and multiplying afterwards would let exactly that through; this way it lands on the floor.
// A wrap that lands large-positive is not distinguishable here from a number somebody meant, which
// is the other half of what the ceiling is for: whatever the multiply produced, what comes out of
// this function is inside the range.
//
// The floor is a parameter and the ceiling is not, because the floor differs by market state — it
// protects the vendors, and open and closed cost them differently — while the ceiling protects the
// READER, who cannot tell a day-old cache from a dead feed either way.
func quoteClampTTL(d, floor time.Duration) time.Duration {
	if d < floor {
		return floor
	}
	if d > quoteTTLCeiling {
		return quoteTTLCeiling
	}
	return d
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

// noteSuccess and noteFailure keep the per-source counters the admin panel reads (quote_admin_api.go).
// They are counters rather than a boolean because the question an operator actually has is "is this
// source down, or did it blip", and only a streak can answer it.
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

// ordered is the source list the operator asked for: the sources this build has, in cfg's order,
// and only those cfg names. A source absent from the order is off — that is the whole of the
// enable/disable mechanism, and it is why there is no second flag to contradict it.
//
// A name in the order that this build has no source for is skipped rather than treated as a failure.
// The save refuses such a name and the read drops it (quoteParseOrder), so reaching here means the
// setting outlived the source it named, and a missing vendor is not a vendor that answered badly.
func (c *quoteCache) ordered(cfg quoteConfig) []quoteSource {
	byName := make(map[string]quoteSource, len(c.sources))
	for _, src := range c.sources {
		byName[src.name] = src
	}
	out := make([]quoteSource, 0, len(cfg.Order))
	for _, name := range cfg.Order {
		if src, ok := byName[name]; ok {
			out = append(out, src)
		}
	}
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
func (c *quoteCache) load(ctx context.Context, cfg quoteConfig, market, code string, bars int) (*QuoteResp, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	m := quoteMarkets[market]
	var firstErr error
	for _, src := range c.ordered(cfg) {
		// A source whose parser does not cover this market is not called, and above all is not
		// blamed. Asking Sina for usAAPL returns "sina has no A-share-shaped line for the us
		// market" — a fact about our own code — and counting that as a vendor failure puts a
		// permanent red streak on the fallback in the admin panel for every US symbol somebody
		// looks up while Tencent is having a bad afternoon.
		if !src.covers(m) {
			continue
		}
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

// fetchUnder is the whole path a handler takes: cache, then single-flight, then load. It reports
// whether the answer cost an upstream call and how long the answer stays fresh.
//
// The config arrives as an argument rather than as a field on the cache because it is read from the
// store per request: an admin who shortens the TTL or reorders the sources gets that on the next
// page view, not at the next restart. What it does NOT reach back into is the entries already
// stored — those keep the expiry they were written with, which is what the panel's clear button is
// for.
//
// The returned *QuoteResp is SHARED with every other caller holding the same cache entry. Nothing
// downstream may mutate it; the handler copies the struct before stamping Cached on it.
func (c *quoteCache) fetchUnder(ctx context.Context, cfg quoteConfig, market, code string, bars int) (*QuoteResp, bool, time.Duration, error) {
	c.init()
	cfg = cfg.orDefaults()
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
		return fl.resp, true, cfg.ttlFor(fl.resp), nil
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
	fl.resp, fl.err = c.load(loadCtx, cfg, market, code, bars)
	if fl.err == nil {
		ttl = cfg.ttlFor(fl.resp)
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

// QuoteHealth reports each vendor's recent record — the counters the 管理 → 行情 panel reads
// (quote_admin_api.go).
func (s *Server) QuoteHealth() []QuoteSourceHealth { return s.quotes.snapshotHealth() }

// ---------- what the admin panel asks the cache ----------

// quoteCacheStats is the occupancy half of that panel, in BOTH units rather than one: the entry
// count is what binds when readers ask for short ranges and the byte budget is what binds when they
// ask for 1y, so a single number would be silent about whichever of quoteCacheMaxEntries and
// quoteCacheMaxBytes is actually doing the work today.
type quoteCacheStats struct {
	Entries int
	Bytes   int
}

func (c *quoteCache) stats() quoteCacheStats {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statsLocked()
}

// statsLocked is the same reading for a caller that already holds mu — clear needs it before it
// empties the map, and taking the lock twice there would let a fetch land in between and be
// reported as dropped when it was not.
func (c *quoteCache) statsLocked() quoteCacheStats {
	return quoteCacheStats{Entries: len(c.entries), Bytes: c.bytes}
}

// clear drops every entry and returns what it dropped. The count is not on the wire — the panel
// reads the fresh occupancy back instead — but it is what makes "the button emptied the cache" an
// assertion rather than an impression.
//
// The health counters SURVIVE it. They are the record of what the vendors have been doing, not
// cached data, and an operator who clears the cache to test a source would otherwise erase the
// evidence they were about to read. The in-flight single-flights survive too: a leader mid-call
// still stores its answer afterwards, which is correct — that answer was fetched fresh.
func (c *quoteCache) clear() quoteCacheStats {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := c.statsLocked()
	c.entries = make(map[string]*list.Element)
	c.lru.Init()
	c.bytes = 0
	return dropped
}

// quoteSourceInfo is one compiled-in source as the panel sees it, before the operator's order and
// the health counters are laid over it.
type quoteSourceInfo struct {
	Name    string
	Markets []string
}

// describeSources reports the sources this cache actually holds, not the shipped list: a build whose
// source table has been swapped (a test) must describe what it will really call, or the panel is a
// picture of a different program.
func (c *quoteCache) describeSources() []quoteSourceInfo {
	c.init()
	out := make([]quoteSourceInfo, 0, len(c.sources))
	for _, src := range c.sources {
		out = append(out, quoteSourceInfo{Name: src.name, Markets: src.marketIDs()})
	}
	return out
}
