package app

import (
	"bytes"
	"compress/gzip"
	"html"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/version"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/web"
)

// The title baked into the built index.html; replaced at serve time with the configured
// site title so the tab never flashes the default before the SPA boots.
const defaultSiteTitle = "研报门户"

// spaBranding supplies the per-request site title and favicon href injected into the shell.
type spaBranding func() (title, faviconHref string)

// distContentType maps embedded-asset extensions to their MIME type. Needed when
// serving a precompressed asset directly (bypassing http.FileServer's sniffing).
var distContentType = map[string]string{
	".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8",
	".json": "application/json", ".svg": "image/svg+xml", ".map": "application/json",
	".txt": "text/plain; charset=utf-8", ".xml": "application/xml",
}

// spaHandler serves the embedded SPA: real files (JS/CSS/img) as-is, and
// index.html for every unknown path so the React router can handle deep links
// (e.g. /stock/300750, /manage/...).
func (s *Server) spaHandler() http.HandlerFunc {
	sub, err := web.FS()
	if err != nil {
		log.Printf("web dist embed: %v", err)
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web assets unavailable", http.StatusInternalServerError)
		}
	}
	// Inject the configured site title + logo into the shell so the first paint is already
	// branded (no flash of the default title / default favicon).
	brand := func() (string, string) {
		title := strings.TrimSpace(s.st.GetSetting("site_title", ""))
		if title == "" {
			title = defaultSiteTitle
		}
		return title, strings.TrimSpace(s.st.GetSetting("site_logo_url", ""))
	}
	return spaHandlerFS(sub, brand, version.Commit)
}

// spaHandlerFS builds the handler over a given filesystem (injectable for tests).
// Every gzip-eligible file is pre-compressed once here (the embedded dist is a
// couple MB and immutable for the process lifetime) instead of on every request —
// the ~1.2 MB antd vendor chunk alone previously cost several hundred ms of CPU to
// re-gzip per request, directly adding to that asset's time-to-first-byte.
func spaHandlerFS(sub fs.FS, brand spaBranding, swVersion string) http.HandlerFunc {
	index, ierr := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))
	gzipped := precompressAssets(sub)
	// Stamp the build version into the service worker's cache name so every deploy ships a
	// fresh SW whose activate purges the old cache — no stale shell can survive a deploy.
	swRaw, _ := fs.ReadFile(sub, "sw.js")
	swJS := bytes.ReplaceAll(swRaw, []byte("__RP_SW_VERSION__"), []byte(swCacheTag(swVersion)))
	return func(w http.ResponseWriter, r *http.Request) {
		if ierr != nil { // frontend not built yet
			http.Error(w, "frontend not built — run: cd web && npm ci && npm run build", http.StatusServiceUnavailable)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// The service worker is served with its version injected and never cached itself,
		// so the browser always picks up a new SW on the next visit after a deploy.
		if clean == "sw.js" && len(swJS) > 0 {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Service-Worker-Allowed", "/")
			w.Write(swJS)
			return
		}
		if clean != "" && clean != "index.html" { // serve a real static file if present
			if f, e := sub.Open(clean); e == nil {
				f.Close()
				// Vite asset filenames are content-hashed, so they can be cached
				// forever — this makes a refresh serve them from disk with no network.
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				if gz, ok := gzipped[clean]; ok && r.Header.Get("Range") == "" && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
					w.Header().Set("Content-Type", distContentTypeFor(clean))
					w.Header().Set("Content-Encoding", "gzip")
					w.Header().Set("Vary", "Accept-Encoding")
					w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
					w.Write(gz)
					return
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// fallback: SPA route → index.html, branded so the first paint isn't the default
		// title/favicon (hashed asset names make caching safe).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(brandIndex(index, brand))
	}
}

// precompressAssets gzip-compresses (best level) every file under sub whose
// extension is worth compressing (gzipExt, shared with gzip.go), keyed by its
// path relative to the filesystem root — matching the `clean` request-path key
// used at serve time.
func precompressAssets(sub fs.FS) map[string][]byte {
	out := map[string][]byte{}
	fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !gzipExt[path.Ext(p)] {
			return nil
		}
		raw, rerr := fs.ReadFile(sub, p)
		if rerr != nil {
			return nil
		}
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		gz.Write(raw)
		gz.Close()
		out[p] = buf.Bytes()
		return nil
	})
	return out
}

func distContentTypeFor(p string) string {
	if ct, ok := distContentType[path.Ext(p)]; ok {
		return ct
	}
	return "application/octet-stream"
}

// swCacheTag reduces the build version to a safe token for the SW cache name, so a deploy
// (new commit) yields a new cache. Falls back to "dev" for an unstamped/dev build.
func swCacheTag(v string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(v) {
		if r == '-' || r == '.' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

// brandIndex rewrites the shell's default title + favicon with the configured ones, so the
// tab and icon are correct on the very first paint (before the SPA reads /api/site). Both
// replacements are no-ops when unset or already the default, and never mutate `index`.
func brandIndex(index []byte, brand spaBranding) []byte {
	if brand == nil {
		return index
	}
	title, favicon := brand()
	out := index
	if title != "" && title != defaultSiteTitle {
		out = bytes.Replace(out,
			[]byte("<title>"+defaultSiteTitle+"</title>"),
			[]byte("<title>"+html.EscapeString(title)+"</title>"), 1)
	}
	if favicon != "" {
		out = bytes.Replace(out,
			[]byte(`<link rel="icon" type="image/svg+xml" href="/favicon.svg" />`),
			[]byte(`<link rel="icon" href="`+html.EscapeString(favicon)+`" />`), 1)
	}
	return out
}
