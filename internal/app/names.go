package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed names.json
var embeddedNames []byte

// nameFetchTimeout caps how long ingest waits for a live name fetch before falling back
// to the last known name; a slow fetch keeps running and updates the cache in the background.
const nameFetchTimeout = 5 * time.Second

// Names maps stock code to name. Embedded seed (common/demo tickers) + runtime full table (data/names.json).
type Names struct {
	mu    sync.RWMutex
	m     map[string]string
	dir   string
	st    *Store                   // syncs names into the stocks table (used for searching by name)
	fetch func(code string) string // live single-name fetch (injectable for tests); defaults to FetchOneName
}

func LoadNames(dataDir string, st *Store) *Names {
	n := &Names{m: map[string]string{}, dir: dataDir, st: st}
	var seed map[string]string
	_ = json.Unmarshal(embeddedNames, &seed)
	for k, v := range seed {
		n.m[k] = v
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "names.json")); err == nil {
		var ext map[string]string
		if json.Unmarshal(b, &ext) == nil {
			for k, v := range ext {
				n.m[k] = v
			}
		}
	}
	if st != nil {
		for k, v := range st.AllStockNames() { // merge in the stocks table (including previously fallback-fetched entries, so they survive restarts)
			n.m[k] = v
		}
		go st.SyncStocks(n.All()) // reverse-sync entries from seed/json that the stocks table doesn't have yet
	}
	return n
}

// All returns a copy of the names table.
func (n *Names) All() map[string]string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	cp := make(map[string]string, len(n.m))
	for k, v := range n.m {
		cp[k] = v
	}
	return cp
}

func (n *Names) Get(code string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.m[code]
}

func (n *Names) count() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.m)
}

func (n *Names) merge(ext map[string]string) {
	n.mu.Lock()
	for k, v := range ext {
		n.m[k] = v
	}
	n.mu.Unlock()
}

// ensureFull best-effort fetches the full table once in the background if it's not present locally (auto-completes when the production machine has good network).
func (n *Names) ensureFull() {
	if _, err := os.Stat(filepath.Join(n.dir, "names.json")); err == nil {
		return // full table already exists
	}
	go func() {
		m, err := FetchAShareNames()
		if err != nil || len(m) < 3000 {
			log.Printf("stock-name auto-fetch skipped (%v, %d so far); run `report-portal fetchnames` later", err, len(m))
			return
		}
		n.merge(m)
		_ = n.save(m)
		if n.st != nil {
			n.st.SyncStocks(m) // sync into the stocks table for searching by name
		}
		log.Printf("stock names fetched: %d", n.count())
	}()
}

func (n *Names) save(m map[string]string) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(n.dir, "names.json"), b, 0o644)
}

// FetchNamesToFile fetches the full table and writes it to <dir>/names.json (used by the fetchnames subcommand).
func FetchNamesToFile(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	m, err := FetchAShareNames()
	if err != nil && len(m) == 0 {
		return 0, err
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "names.json"), b, 0o644); err != nil {
		return len(m), err
	}
	return len(m), nil
}

const (
	// One page of the eastmoney table (pz=100, two fields per row) measures a few KB, so this
	// ceiling is some two hundred times the real thing and can only ever truncate a response that
	// is not the API's. It is here because this loop runs up to 80 times inside a small-memory
	// container and used to io.ReadAll whatever arrived, with no ceiling anywhere.
	maxNamePageBytes = 1 << 20
	// nameTablePageTimeout is the per-page budget the dedicated client this replaced carried as
	// its client timeout; the whole table is ~40 pages of a paced loop, so it is generous on purpose.
	nameTablePageTimeout = 25 * time.Second
)

// FetchAShareNames fetches all A-share code->name pairs from eastmoney via pagination.
// Note: uses push2.eastmoney.com (the 82.push2 subdomain is blocked on some networks); diff supports both object and array formats; retries on per-page failure and rotates hosts.
// A non-2xx page now counts as a failed attempt (retry, then rotate host) rather than being handed
// to the parser: mergeDiff over an error page returns 0, and the loop below reads a 0 as "we are
// past the last page" and stops fetching the table at all — so one 403 used to silently truncate
// the whole name table down to whatever had arrived so far.
func FetchAShareNames() (map[string]string, error) {
	const fs = "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"
	hosts := []string{"push2.eastmoney.com", "push2delay.eastmoney.com", "82.push2.eastmoney.com"}
	m := map[string]string{}
	for pn := 1; pn <= 80; pn++ {
		ok, got := false, 0
		for attempt := 0; attempt < 4; attempt++ {
			host := hosts[attempt%len(hosts)]
			u := fmt.Sprintf("https://%s/api/qt/clist/get?pn=%d&pz=100&po=1&np=1"+
				"&fltt=2&invt=2&fid=f12&fs=%s&fields=f12,f14", host, pn, fs)
			ctx, cancel := context.WithTimeout(context.Background(), nameTablePageTimeout)
			body, err := vendorGet(ctx, u, "https://quote.eastmoney.com/", maxNamePageBytes)
			cancel()
			if err != nil {
				vendorLogf(u, "stock-name table page %d failed: %v", pn, err)
				time.Sleep(time.Duration(attempt+1) * 800 * time.Millisecond)
				continue
			}
			ok, got = true, mergeDiff(body, m)
			break
		}
		if !ok || got == 0 { // repeated failures, or reached the last page
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if len(m) == 0 {
		return m, fmt.Errorf("no stock names fetched (eastmoney may be unreachable)")
	}
	return m, nil
}

// cleanName strips whitespace eastmoney sometimes pads Chinese company names with
// (leading/trailing/internal, e.g. "柳    工" for 柳工) — a real name never contains any.
func cleanName(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// mergeDiff parses data.diff from the clist response (supporting both object {"0":{…}} and array [{…}] forms), merges into m, and returns the number of entries parsed.
func mergeDiff(body []byte, m map[string]string) int {
	var raw struct {
		Data *struct {
			Diff json.RawMessage `json:"diff"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &raw) != nil || raw.Data == nil || len(raw.Data.Diff) == 0 {
		return 0
	}
	var items []map[string]any
	if json.Unmarshal(raw.Data.Diff, &items) != nil {
		var obj map[string]map[string]any
		if json.Unmarshal(raw.Data.Diff, &obj) != nil {
			return 0
		}
		for _, v := range obj {
			items = append(items, v)
		}
	}
	n := 0
	for _, d := range items {
		code, _ := d["f12"].(string)
		name, _ := d["f14"].(string)
		if code != "" && name != "" {
			m[code] = cleanName(name)
			n++
		}
	}
	return n
}

// marketPrefix infers the Tencent/Sina quote prefix (6->Shanghai, 0/2/3->Shenzhen, 4/8/9->Beijing),
// and returns "" for anything that is not exactly six ASCII digits.
//
// The all-digits check belongs HERE, and not at the API edge, because this function is the
// URL-construction boundary: everything downstream interpolates the code straight into a vendor URL,
// and a URL containing a control character is one http.NewRequest refuses — a six-byte symbol with
// a NUL in it is what used to reach that call and crash the process on the nil request it returned.
// Rejecting non-digits in apiv1.go instead would be wrong on its own terms: openapi.json documents
// symbol as free text, and a thematic report legitimately posts an empty one.
func marketPrefix(code string) string {
	if len(code) != 6 { // bytes, not runes: a six-character Chinese string is 18 bytes and fails here
		return ""
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return ""
		}
	}
	switch code[0] {
	case '6':
		return "sh"
	case '0', '2', '3':
		return "sz"
	case '4', '8', '9':
		return "bj"
	}
	return ""
}

const (
	// A realtime quote line is a few hundred bytes; anything approaching this ceiling is an
	// interstitial or a redirect page, not a quote.
	maxNameBodyBytes = 64 << 10
	// nameSourceTimeout is the per-request budget the inline client this replaced carried.
	nameSourceTimeout = 8 * time.Second
)

// httpGetGBK fetches a GBK-encoded quote endpoint and returns UTF-8 text, or "" on any failure.
// The signature stays string-only because the empty string IS this path's contract — fetchNameWithRetry
// reads it as "this source had nothing, try the next" — but the error is no longer thrown away
// silently: it goes to the rate-limited vendor log, so a vendor that has started refusing us leaves
// a trace instead of looking indistinguishable from a stock that has no name.
func httpGetGBK(url, referer string) string {
	ctx, cancel := context.WithTimeout(context.Background(), nameSourceTimeout)
	defer cancel()
	body, err := vendorGetGBK(ctx, url, referer, maxNameBodyBytes)
	if err != nil {
		vendorLogf(url, "stock-name fetch failed: %v", err)
		return ""
	}
	return body
}

// Per-source retry policy for the single-name live fetch. Each source is tried up to
// nameFetchAttempts times (short pause between same-source retries) before falling over to
// the next source. nameRetryDelay is a var so tests can zero it.
const nameFetchAttempts = 2

var nameRetryDelay = 300 * time.Millisecond

// nameSource is one live quote endpoint plus the parser that extracts the stock name from its body.
type nameSource struct {
	url, referer string
	parse        func(body string) string
}

// aShareNameSources builds the ordered live sources for a code: Tencent then Sina (both GBK).
func aShareNameSources(pre, code string) []nameSource {
	return []nameSource{
		{ // Tencent: v_sh601899="1~紫金矿业~601899~...
			url: "https://qt.gtimg.cn/q=" + pre + code, referer: "https://gu.qq.com/",
			parse: func(s string) string {
				if strings.Count(s, "~") < 2 {
					return ""
				}
				return strings.TrimSpace(strings.Split(s, "~")[1])
			},
		},
		{ // Sina: var hq_str_sz000001="平安银行,10.05,...
			url: "https://hq.sinajs.cn/list=" + pre + code, referer: "https://finance.sina.com.cn/",
			parse: func(s string) string {
				i := strings.Index(s, `"`)
				if i < 0 {
					return ""
				}
				rest := s[i+1:]
				if j := strings.Index(rest, ","); j > 0 {
					return strings.TrimSpace(rest[:j])
				}
				return ""
			},
		},
	}
}

// fetchNameWithRetry tries each source up to nameFetchAttempts times (pausing nameRetryDelay
// between same-source retries), returning the first parsed non-empty name; "" if all fail.
func fetchNameWithRetry(sources []nameSource, get func(url, referer string) string) string {
	for _, src := range sources {
		for attempt := 0; attempt < nameFetchAttempts; attempt++ {
			if attempt > 0 {
				time.Sleep(nameRetryDelay)
			}
			if name := src.parse(get(src.url, src.referer)); name != "" {
				return name
			}
		}
	}
	return ""
}

// FetchOneName fetches a single stock's current name live (Tencent -> Sina, both GBK
// real-time quote endpoints), each source retried per the policy above. Used at ingest and
// as a fallback when the eastmoney batch misses or fails.
func FetchOneName(code string) string {
	pre := marketPrefix(code)
	if pre == "" {
		return ""
	}
	return fetchNameWithRetry(aShareNameSources(pre, code), httpGetGBK)
}

func (n *Names) fetchOne(code string) string {
	if n.fetch != nil {
		return n.fetch(code)
	}
	return FetchOneName(code)
}

// Resolve returns the name to freeze onto a report at ingest time. Whenever the payload
// carries no name we re-fetch the current live name (names can change on rename / backdoor
// listing, so a rename is reflected on the very next report while earlier reports keep their
// own frozen name). The fetch is bounded by nameFetchTimeout; on a slow/failed fetch it falls
// back to the last known name and lets the fetch finish updating the cache in the background.
func (n *Names) Resolve(code string) string {
	if code == "" {
		return ""
	}
	ch := make(chan string, 1)
	go func() {
		name := n.fetchOne(code)
		if name != "" {
			n.merge(map[string]string{code: name})
			if n.st != nil {
				n.st.SyncStocks(map[string]string{code: name})
			}
		}
		ch <- name
	}()
	select {
	case name := <-ch:
		if name != "" {
			return name
		}
	case <-time.After(nameFetchTimeout):
		// too slow; snapshot the last known name and let the fetch finish in the background
	}
	return n.Get(code)
}
