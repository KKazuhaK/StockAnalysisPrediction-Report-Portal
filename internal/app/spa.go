package app

import (
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/web"
)

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
	index, ierr := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))
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
