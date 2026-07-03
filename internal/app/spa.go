package app

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/web"
)

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
	return spaHandlerFS(sub)
}

// spaHandlerFS builds the handler over a given filesystem (injectable for tests).
// Every gzip-eligible file is pre-compressed once here (the embedded dist is a
// couple MB and immutable for the process lifetime) instead of on every request —
// the ~1.2 MB antd vendor chunk alone previously cost several hundred ms of CPU to
// re-gzip per request, directly adding to that asset's time-to-first-byte.
func spaHandlerFS(sub fs.FS) http.HandlerFunc {
	index, ierr := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))
	gzipped := precompressAssets(sub)
	return func(w http.ResponseWriter, r *http.Request) {
		if ierr != nil { // frontend not built yet
			http.Error(w, "frontend not built — run: cd web && npm ci && npm run build", http.StatusServiceUnavailable)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
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
		// fallback: SPA route → index.html (hashed asset names make caching safe)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
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
