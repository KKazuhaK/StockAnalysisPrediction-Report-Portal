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
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(body))
	}))

	// eligible path + Accept-Encoding gzip → compressed, decodes back to the original
	req := httptest.NewRequest("GET", "/assets/index-abc.js", nil)
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
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/assets/x.js", nil))
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
}

// A 304 (empty body) must pass through without a gzip body.
func TestGzipSkipsEmptyBody(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	req := httptest.NewRequest("GET", "/assets/x.js", nil)
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
