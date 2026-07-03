package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestSiteAssetUploadStoresFileAndReturnsPath(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{DBPath: filepath.Join(t.TempDir(), "portal.db")}

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	rec := uploadSiteAsset(t, s, "logo", "logo.png", png)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	url, _ := m["url"].(string)
	if url != "/site-assets/logo.png" {
		t.Fatalf("url=%q, want /site-assets/logo.png", url)
	}
	if strings.HasPrefix(url, "data:") {
		t.Fatalf("upload returned data URL: %q", url)
	}

	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("name", "logo.png")
	rec = httptest.NewRecorder()
	s.siteAsset(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("asset status=%d type=%q bytes=%d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
}

func TestSiteAssetUploadRejectsDisguisedImage(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{DBPath: filepath.Join(t.TempDir(), "portal.db")}

	rec := uploadSiteAsset(t, s, "logo", "logo.png", []byte("not really an image"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("upload status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestSiteAssetUploadReplacesPreviousKindFiles(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{DBPath: filepath.Join(t.TempDir(), "portal.db")}

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if rec := uploadSiteAsset(t, s, "logo", "logo.png", png); rec.Code != http.StatusOK {
		t.Fatalf("png upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"></svg>`)
	rec := uploadSiteAsset(t, s, "logo", "logo.svg", svg)
	if rec.Code != http.StatusOK {
		t.Fatalf("svg upload status=%d body=%s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(s.siteAssetsDir(), "logo.png")); !os.IsNotExist(err) {
		t.Fatalf("old logo.png still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.siteAssetsDir(), "logo.svg")); err != nil {
		t.Fatalf("new logo.svg missing: %v", err)
	}
}

func uploadSiteAsset(t *testing.T, s *Server, kind, filename string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("kind", kind); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/admin/site-asset", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.apiSiteAssetUpload(rec, req, "admin")
	return rec
}
