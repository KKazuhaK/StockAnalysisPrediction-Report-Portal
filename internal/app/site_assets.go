package app

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

const maxSiteAssetUploadBytes = 1024 * 1024

var siteAssetExtByType = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
}

func (s *Server) apiSiteAssetUpload(w http.ResponseWriter, r *http.Request, user string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSiteAssetUploadBytes+1024*1024)
	if err := r.ParseMultipartForm(maxSiteAssetUploadBytes + 1024); err != nil {
		jsonError(w, http.StatusBadRequest, "上传文件过大")
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind != "logo" && kind != "pwaIcon" {
		jsonError(w, http.StatusBadRequest, "无效的资源类型")
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "请选择图片文件")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSiteAssetUploadBytes+1))
	if err != nil || len(raw) == 0 {
		jsonError(w, http.StatusBadRequest, "读取图片失败")
		return
	}
	if len(raw) > maxSiteAssetUploadBytes {
		jsonError(w, http.StatusBadRequest, "上传文件过大")
		return
	}
	mime, ext := siteAssetMime(raw, header.Header.Get("Content-Type"), header.Filename)
	if ext == "" {
		jsonError(w, http.StatusBadRequest, "不支持的图片格式")
		return
	}

	if err := os.MkdirAll(s.siteAssetsDir(), 0o755); err != nil {
		jsonError(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	name := siteAssetBaseName(kind) + ext
	dst := filepath.Join(s.siteAssetsDir(), name)
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		jsonError(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	s.removeSiblingSiteAssets(kind, name)
	writeJSON(w, map[string]any{"ok": true, "url": "/site-assets/" + name, "type": mime})
}

func (s *Server) siteAsset(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if name == "." || name == "" || name != r.PathValue("name") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.siteAssetsDir(), name)
	raw, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mime, _ := siteAssetMime(raw, "", name)
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	// Cache the logo/icon (a stable URL whose bytes rarely change) so it doesn't
	// re-download and flicker on every page load. ServeContent revalidates against the
	// file mtime (If-Modified-Since), so a re-upload is picked up once max-age lapses.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(strings.ToLower(name), ".svg") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(raw))
}

func (s *Server) siteAssetsDir() string {
	base := "data"
	if s.cfg != nil && s.cfg.DBPath != "" {
		base = config.DirOf(s.cfg.DBPath)
	}
	return filepath.Join(base, "site-assets")
}

func (s *Server) removeSiblingSiteAssets(kind, keep string) {
	prefix := siteAssetBaseName(kind) + "."
	entries, err := os.ReadDir(s.siteAssetsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == keep || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		_ = os.Remove(filepath.Join(s.siteAssetsDir(), e.Name()))
	}
}

func siteAssetBaseName(kind string) string {
	if kind == "pwaIcon" {
		return "pwa-icon"
	}
	return "logo"
}

func siteAssetMime(raw []byte, declared, filename string) (string, string) {
	detected := strings.ToLower(http.DetectContentType(raw))
	if ext, ok := siteAssetExtByType[detected]; ok {
		return detected, ext
	}
	fileExt := strings.ToLower(filepath.Ext(filename))
	declared = strings.ToLower(strings.TrimSpace(declared))
	if (fileExt == ".svg" || declared == "image/svg+xml") && looksLikeSVG(raw) {
		return "image/svg+xml", ".svg"
	}
	if (fileExt == ".webp" || declared == "image/webp") && looksLikeWEBP(raw) {
		return "image/webp", ".webp"
	}
	return "", ""
}

func looksLikeSVG(raw []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(raw[:min(len(raw), 512)])))
	return strings.HasPrefix(s, "<svg") || strings.Contains(s, "<svg ")
}

func looksLikeWEBP(raw []byte) bool {
	return len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP"
}
