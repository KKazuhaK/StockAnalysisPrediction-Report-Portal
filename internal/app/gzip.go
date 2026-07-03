package app

import (
	"compress/gzip"
	"net/http"
	"path"
	"strings"
)

// gzipMiddleware transparently gzip-compresses eligible text responses (the SPA
// bundle, CSS, HTML, and JSON API payloads) when the client accepts it. The
// embedded JS bundle is ~1.6 MB raw / ~0.5 MB gzipped, so this is a ~3x transfer
// win on first load. Already-compressed assets (images, fonts, PDFs) and range /
// empty-body (304) responses are passed through untouched.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || !gzipEligible(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gzip.NewWriter(w)}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

var gzipExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".html": true, ".json": true,
	".svg": true, ".map": true, ".txt": true, ".xml": true,
}

// gzipEligible decides by path whether a response is worth compressing here,
// avoiding already-compressed binaries (images/fonts) and the MD/PDF download
// routes. Real static files with a recognized extension (JS/CSS/...) are
// excluded — spaHandler pre-compresses and serves those directly (see spa.go);
// compressing them again here would double-gzip an already-gzipped body.
func gzipEligible(p string) bool {
	if strings.HasPrefix(p, "/report/") {
		return false
	}
	if p == "/pwa-icon" {
		return false
	}
	if strings.HasPrefix(p, "/api/") {
		return true
	}
	return path.Ext(p) == "" // SPA routes falling back to index.html (text/html)
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	started  bool
	disabled bool // empty-body responses (304/204/1xx): pass through, don't gzip
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if code == http.StatusNotModified || code == http.StatusNoContent || (code >= 100 && code < 200) {
		w.disabled = true
	}
	if !w.disabled {
		w.Header().Del("Content-Length") // length changes after compression
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if w.disabled {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

func (w *gzipResponseWriter) finish() {
	if w.started && !w.disabled {
		w.gz.Close()
	}
}
