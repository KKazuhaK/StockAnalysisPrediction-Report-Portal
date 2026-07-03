package app

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipMiddleware(t *testing.T) {
	body := strings.Repeat("hello world ", 500) // compressible
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))

	// /api/* + Accept-Encoding gzip → compressed, decodes back to the original
	req := httptest.NewRequest("GET", "/api/reports", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, headers=%v", rec.Header())
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("gzipped body (%d) not smaller than raw (%d)", rec.Body.Len(), len(body))
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	dec, _ := io.ReadAll(gr)
	if string(dec) != body {
		t.Error("decompressed body does not match original")
	}

	// no Accept-Encoding → passthrough, uncompressed
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/api/reports", nil))
	if rec2.Header().Get("Content-Encoding") == "gzip" {
		t.Error("must not gzip when the client does not accept it")
	}
	if rec2.Body.String() != body {
		t.Error("passthrough body altered")
	}

	// already-compressed asset (image) → passthrough even with Accept-Encoding
	reqImg := httptest.NewRequest("GET", "/assets/logo.png", nil)
	reqImg.Header.Set("Accept-Encoding", "gzip")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, reqImg)
	if rec3.Header().Get("Content-Encoding") == "gzip" {
		t.Error("images must not be gzipped")
	}

	reqPWAIcon := httptest.NewRequest("GET", "/pwa-icon", nil)
	reqPWAIcon.Header.Set("Accept-Encoding", "gzip")
	recPWAIcon := httptest.NewRecorder()
	h.ServeHTTP(recPWAIcon, reqPWAIcon)
	if recPWAIcon.Header().Get("Content-Encoding") == "gzip" {
		t.Error("PWA icon endpoint must not be gzipped")
	}

	// static assets with a recognized extension (/assets/*.js etc) are no longer
	// compressed HERE — spaHandler pre-compresses and serves them directly (see
	// spa_test.go); this middleware must leave them untouched to avoid double
	// compression.
	reqAsset := httptest.NewRequest("GET", "/assets/index-abc.js", nil)
	reqAsset.Header.Set("Accept-Encoding", "gzip")
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, reqAsset)
	if rec4.Header().Get("Content-Encoding") == "gzip" {
		t.Error("static assets must be left to spaHandler's own precompressed-gzip serving, not double-compressed here")
	}

	// SPA route fallback (no extension) still compresses here — it's tiny and
	// spaHandler just writes plain index.html bytes for these paths.
	reqRoute := httptest.NewRequest("GET", "/stock/300750", nil)
	reqRoute.Header.Set("Accept-Encoding", "gzip")
	rec5 := httptest.NewRecorder()
	h.ServeHTTP(rec5, reqRoute)
	if rec5.Header().Get("Content-Encoding") != "gzip" {
		t.Error("extensionless SPA routes should still be compressed by this middleware")
	}
}

// A 304 (empty body) must pass through without a gzip body.
func TestGzipSkipsEmptyBody(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	req := httptest.NewRequest("GET", "/api/reports", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("304 must not be gzip-encoded")
	}
	if rec.Body.Len() != 0 {
		t.Error("304 must have an empty body")
	}
}
