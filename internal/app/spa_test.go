package app

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func testDist() fstest.MapFS {
	big := strings.Repeat("console.log('hi');", 20000) // compressible, large enough to matter
	return fstest.MapFS{
		"index.html":    {Data: []byte("<html>spa shell</html>")},
		"assets/app.js": {Data: []byte(big)},
		"favicon.svg":   {Data: []byte("<svg></svg>")},
	}
}

// Large static assets under /assets/ are pre-compressed once at startup and served
// directly (skipping per-request gzip CPU work), with an accurate Content-Length and
// the correct Content-Type (lost otherwise, since we bypass http.FileServer's sniffing).
func TestSpaServesPrecompressedGzipForEligibleAsset(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	wantLen := strconv.Itoa(rec.Body.Len())
	if cl := rec.Header().Get("Content-Length"); cl != wantLen {
		t.Errorf("Content-Length = %q, want %q (actual body size)", cl, wantLen)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable (hashed asset)", cc)
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	dec, _ := io.ReadAll(gr)
	want := strings.Repeat("console.log('hi');", 20000)
	if string(dec) != want {
		t.Error("decompressed body does not match the original asset content")
	}
}

// A client that doesn't advertise gzip support gets the plain bytes.
func TestSpaServesPlainWhenClientDoesNotAcceptGzip(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("must not gzip when the client does not accept it")
	}
	want := strings.Repeat("console.log('hi');", 20000)
	if rec.Body.String() != want {
		t.Error("plain body does not match the original asset content")
	}
}

// A Range request must fall back to plain (whole-file precompressed gzip breaks
// byte-range semantics), so http.FileServer's Range handling stays correct.
func TestSpaSkipsPrecompressedOnRangeRequest(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("must not serve precompressed gzip for a Range request")
	}
}

// Unknown app routes (client-side React Router paths) fall back to index.html, unchanged.
func TestSpaFallbackToIndexForUnknownRoute(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")
	req := httptest.NewRequest("GET", "/stock/300750", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "<html>spa shell</html>" {
		t.Error("fallback body does not match index.html")
	}
}

// /sw.js is served with the build version stamped into its cache name (so every deploy
// ships a fresh SW) and marked no-cache. Non-token characters in the version are stripped.
func TestSpaInjectsServiceWorkerVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html></html>")},
		"sw.js":      {Data: []byte("const CACHE_NAME = 'rp-cache-__RP_SW_VERSION__'")},
	}
	h := spaHandlerFS(fsys, nil, "abc123!!")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))

	body := rec.Body.String()
	if strings.Contains(body, "__RP_SW_VERSION__") {
		t.Fatalf("placeholder not replaced: %q", body)
	}
	if !strings.Contains(body, "rp-cache-abc123'") { // '!!' sanitized away
		t.Fatalf("version not injected/sanitized: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

// The shell's default title + favicon are replaced with the configured branding, so the
// first paint isn't the default (no flash, and the default favicon isn't fetched).
func TestSpaInjectsBranding(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte(`<head><title>` + defaultSiteTitle + `</title>` +
			`<link rel="icon" type="image/svg+xml" href="/favicon.svg" />` +
			`<link rel="apple-touch-icon" href="/pwa-icon" /></head>` +
			`<body><div id="boot-splash"><!--RP_BOOT_LOGO_START--><svg>default</svg><!--RP_BOOT_LOGO_END--></div></body>`)},
	}
	brand := func() (string, string) { return "MyPortal", "/site-assets/logo.png" }
	h := spaHandlerFS(fsys, brand, "test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/anything", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "<title>MyPortal</title>") {
		t.Errorf("title not injected: %q", body)
	}
	if strings.Contains(body, "/favicon.svg") {
		t.Errorf("default favicon still present after a custom logo was set: %q", body)
	}
	if !strings.Contains(body, `href="/site-assets/logo.png"`) {
		t.Errorf("custom logo favicon not injected: %q", body)
	}
	// The boot splash shows the site logo, not the default mark.
	if strings.Contains(body, "<svg>default</svg>") {
		t.Errorf("default boot-splash mark still present: %q", body)
	}
	if !strings.Contains(body, `<img src="/site-assets/logo.png" alt="" />`) {
		t.Errorf("boot-splash logo not injected: %q", body)
	}
	// apple-touch-icon points at the logo (one cacheable URL), not /pwa-icon.
	if strings.Contains(body, `href="/pwa-icon"`) {
		t.Errorf("apple-touch-icon still points at /pwa-icon (extra 302): %q", body)
	}
	if !strings.Contains(body, `<link rel="apple-touch-icon" href="/site-assets/logo.png" />`) {
		t.Errorf("apple-touch-icon not pointed at the logo: %q", body)
	}
}

// A non-hashed static file (no /assets/ prefix) still gets precompressed-gzip
// serving (by extension), but must NOT get the immutable long-cache header.
func TestSpaPrecompressedAssetOutsideAssetsDirHasNoImmutableCache(t *testing.T) {
	h := spaHandlerFS(testDist(), nil, "test")
	req := httptest.NewRequest("GET", "/favicon.svg", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, favicon.svg is not content-hashed so must not be immutable", cc)
	}
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	dec, _ := io.ReadAll(gr)
	if string(dec) != "<svg></svg>" {
		t.Error("decompressed favicon does not match original")
	}
}
