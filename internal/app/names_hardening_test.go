package app

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// marketPrefix is the URL-construction boundary, so it is the thing that has to refuse a symbol a
// URL cannot carry. Six ASCII digits and a known board byte, or "".
func TestMarketPrefixRejectsNonDigitSymbols(t *testing.T) {
	cases := []struct{ code, want string }{
		{"600000", "sh"},
		{"601899", "sh"},
		{"000001", "sz"},
		{"200011", "sz"},
		{"300750", "sz"},
		{"430047", "bj"},
		{"830799", "bj"},
		{"920008", "bj"},
		{"700000", ""},    // six digits, but no board starts with 7
		{"", ""},          // thematic reports post an empty symbol on purpose
		{"60000", ""},     // short
		{"6000000", ""},   // long
		{"6ABCDE", ""},    // letters
		{"600 00", ""},    // an interior space survives the handler's TrimSpace
		{"600\x0000", ""}, // NUL — the confirmed crash input
		{"6000\n0", ""},   // newline
		{"6\x7f0000", ""}, // DEL
		{"紫金矿业行情", ""},    // six runes, eighteen bytes
	}
	for _, c := range cases {
		if got := marketPrefix(c.code); got != c.want {
			t.Errorf("marketPrefix(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

// The confirmed process crash. A six-byte symbol whose first byte is a digit but which carries a
// control byte used to reach http.NewRequest, which returns (nil, err) for a control character in a
// URL, and the next line dereferenced that nil request — from the bare goroutine in Names.Resolve,
// past the only recover in the tree, so it took the whole binary with it. marketPrefix must now
// reject it before any request is built at all: an empty vendor log is the assertion that nothing
// was fetched.
func TestFetchOneNameRejectsControlCharSymbol(t *testing.T) {
	restoreDelay := nameRetryDelay
	nameRetryDelay = 0
	defer func() { nameRetryDelay = restoreDelay }()

	// Move the log limiter's clock well past anything an earlier test recorded, so a line emitted
	// here is certain to be printed rather than suppressed as a duplicate.
	restoreNow := vendorNow
	vendorNow = func() time.Time { return time.Now().Add(24 * time.Hour) }
	defer func() { vendorNow = restoreNow }()

	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("FetchOneName panicked on a control-character symbol: %v", r)
		}
	}()

	if got := FetchOneName("600\x0000"); got != "" {
		t.Fatalf("FetchOneName(NUL symbol) = %q, want empty", got)
	}
	if strings.Contains(logged.String(), "stock-name fetch failed") {
		t.Fatalf("a vendor request was attempted for a symbol that cannot be a URL: %s", logged.String())
	}
}

// A 403 interstitial must never look like a successful fetch that simply parsed to nothing: the
// empty string is what a connection failure produces, so conflating the two makes a wholly blocked
// deployment look healthy and reduces the two-source failover to decoration.
func TestVendorGetRefusesNon2xx(t *testing.T) {
	const interstitial = `<html><head><title>403 Forbidden</title></head><body><h1>访问被拒绝</h1></body></html>`
	var gotAgent, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent, gotReferer = r.Header.Get("User-Agent"), r.Header.Get("Referer")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(interstitial))
	}))
	defer srv.Close()

	const referer = "https://finance.sina.com.cn/"
	body, err := vendorGet(context.Background(), srv.URL+"/list=sh601899", referer, maxNameBodyBytes)
	if err == nil {
		t.Fatalf("403 returned no error; body=%q", body)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error %q does not name the status", err)
	}
	if body != nil {
		t.Errorf("403 body handed back to the caller: %q", body)
	}
	// Sina 403s a request that arrives without a Referer, which is why this path sets one.
	if gotAgent == "" || gotReferer != referer {
		t.Errorf("request headers: User-Agent=%q Referer=%q, want both set", gotAgent, gotReferer)
	}

	if s, err := vendorGetGBK(context.Background(), srv.URL+"/list=sh601899", referer, maxNameBodyBytes); err == nil || s != "" {
		t.Errorf("vendorGetGBK on 403 = %q, err=%v; want empty text and an error", s, err)
	}
}

// The body is read through a LimitReader, so a vendor that answers with something enormous costs a
// fixed number of bytes rather than the whole of it. The 80-page name-table loop runs in a
// small-memory container.
func TestVendorGetTruncatesOversizedBody(t *testing.T) {
	const served = 512 << 10
	const limit = 1 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), served))
	}))
	defer srv.Close()

	body, err := vendorGet(context.Background(), srv.URL, "", limit)
	if err != nil {
		t.Fatalf("vendorGet: %v", err)
	}
	if len(body) != limit {
		t.Fatalf("read %d bytes of a %d-byte body, want the %d-byte limit", len(body), served, limit)
	}
}

// gbkFixtureServer serves the captured Sina snapshot re-encoded to GBK — the wire form the real
// endpoint sends — and returns the server plus the UTF-8 text it must decode back to.
func gbkFixtureServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	utf8Body, err := os.ReadFile("testdata/quote/sina_snapshot_sh601899.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	gbkBody, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), utf8Body)
	if err != nil {
		t.Fatalf("re-encode fixture to GBK: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=GBK")
		_, _ = w.Write(gbkBody)
	}))
	t.Cleanup(srv.Close)
	return srv, string(utf8Body)
}

// A 200 with a real captured body still comes back whole and correctly decoded, and the shipped
// Sina parser still reads the company name out of it.
func TestVendorGetGBKDecodesCapturedSnapshot(t *testing.T) {
	srv, want := gbkFixtureServer(t)

	got, err := vendorGetGBK(context.Background(), srv.URL+"/list=sh601899", "https://finance.sina.com.cn/", maxNameBodyBytes)
	if err != nil {
		t.Fatalf("vendorGetGBK: %v", err)
	}
	if got != want {
		t.Fatalf("decoded %q, want the captured fixture %q", got, want)
	}
	if name := aShareNameSources("sh", "601899")[1].parse(got); name != "紫金矿业" {
		t.Fatalf("Sina parser read %q out of the fixture, want 紫金矿业", name)
	}
}

// End to end over the exact call chain FetchOneName uses — fetchNameWithRetry -> httpGetGBK ->
// vendorGetGBK -> vendorGet — so the rewritten httpGetGBK is proven against a real body and not
// only against its own unit.
func TestFetchNameEndToEndOverVendorGet(t *testing.T) {
	srv, _ := gbkFixtureServer(t)
	restore := nameRetryDelay
	nameRetryDelay = 0
	defer func() { nameRetryDelay = restore }()

	src := aShareNameSources("sh", "601899")[1]
	src.url = srv.URL + "/list=sh601899"
	if got := fetchNameWithRetry([]nameSource{src}, httpGetGBK); got != "紫金矿业" {
		t.Fatalf("fetchNameWithRetry = %q, want 紫金矿业", got)
	}
}

// One line per host per minute: a vendor outage on a per-page-view fetch has to leave evidence
// without filling the disk with the same sentence.
func TestVendorLogRateLimit(t *testing.T) {
	restoreNow := vendorNow
	base := time.Now().Add(48 * time.Hour)
	now := base
	vendorNow = func() time.Time { return now }
	defer func() { vendorNow = restoreNow }()

	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	for i := 0; i < 3; i++ {
		vendorLogf("https://qt.gtimg.cn/q=sh601899", "boom %d", i)
	}
	if n := strings.Count(logged.String(), "qt.gtimg.cn"); n != 1 {
		t.Fatalf("logged %d lines for one host inside the interval, want 1: %s", n, logged.String())
	}

	// a different host is its own bucket
	vendorLogf("https://hq.sinajs.cn/list=sh601899", "boom")
	if n := strings.Count(logged.String(), "hq.sinajs.cn"); n != 1 {
		t.Fatalf("second host logged %d lines, want 1: %s", n, logged.String())
	}

	// once the interval has rolled over the first host may speak again
	now = base.Add(vendorLogInterval + time.Second)
	vendorLogf("https://qt.gtimg.cn/q=sh601899", "boom again")
	if n := strings.Count(logged.String(), "qt.gtimg.cn"); n != 2 {
		t.Fatalf("logged %d lines after the interval rolled over, want 2: %s", n, logged.String())
	}
}

// The INNER half of the same crash. TestFetchOneNameRejectsControlCharSymbol proves the outer guard
// — marketPrefix refuses the symbol before a URL is ever built — but the fix the comment in
// vendorGet describes is the error return from http.NewRequestWithContext, and nothing exercised it:
// reverting that line to `req, _ :=` left the whole suite green while leaving a nil request to be
// dereferenced by the next caller that reaches vendorGet without going through marketPrefix.
func TestVendorGetRefusesAURLTheRequestBuilderRejects(t *testing.T) {
	body, err := vendorGet(context.Background(), "https://hq.sinajs.cn/list=sh600\x0000", "", 1024)
	if err == nil {
		t.Fatalf("a control character in a URL returned no error; body=%q", body)
	}
	if body != nil {
		t.Errorf("a request that was never built handed back a body: %q", body)
	}
	// And the GBK wrapper is not a second place with the same hole.
	if s, err := vendorGetGBK(context.Background(), "https://hq.sinajs.cn/list=sh600\x0000", "", 1024); err == nil || s != "" {
		t.Errorf("vendorGetGBK on an unbuildable URL = %q, err=%v; want empty text and an error", s, err)
	}
}
