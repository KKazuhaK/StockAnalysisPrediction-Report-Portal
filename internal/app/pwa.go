package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

const defaultPWAIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <rect width="512" height="512" rx="112" fill="#1677ff"/>
  <path d="M112 352 L208 224 L288 304 L400 144" fill="none" stroke="#fff" stroke-width="38"
        stroke-linecap="round" stroke-linejoin="round"/>
  <circle cx="400" cy="144" r="34" fill="#fff"/>
</svg>`

func (s *Server) pwaManifest(w http.ResponseWriter, r *http.Request) {
	if !settingBool(s.st.GetSetting("pwa_enabled", ""), true) {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(s.st.GetSetting("site_title", ""))
	if title == "" {
		title = "研报门户"
	}
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(map[string]any{
		"name":             title,
		"short_name":       shortManifestName(title),
		"description":      title,
		"id":               "/",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#ffffff",
		"theme_color":      "#1677ff",
		"prefer_related_applications": false,
		"icons": []map[string]string{
			{"src": "/pwa-icon", "sizes": "192x192", "purpose": "any maskable"},
			{"src": "/pwa-icon", "sizes": "512x512", "purpose": "any maskable"},
		},
	})
}

func shortManifestName(s string) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= 12 {
		return string(rs)
	}
	return string(rs[:12])
}

func (s *Server) pwaIcon(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(s.st.GetSetting("pwa_icon_url", ""))
	if raw == "" {
		raw = strings.TrimSpace(s.st.GetSetting("site_logo_url", ""))
	}
	if raw == "" || !validSiteLogoURL(raw) {
		writeDefaultPWAIcon(w)
		return
	}

	if mime, data, ok := parseImageDataURL(raw); ok {
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
		return
	}
	http.Redirect(w, r, raw, http.StatusFound)
}

func writeDefaultPWAIcon(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(defaultPWAIconSVG))
}

func parseImageDataURL(raw string) (string, []byte, bool) {
	meta, encoded, ok := strings.Cut(raw, ",")
	if !ok {
		return "", nil, false
	}
	lowerMeta := strings.ToLower(meta)
	if !strings.HasPrefix(lowerMeta, "data:image/") || !strings.HasSuffix(lowerMeta, ";base64") {
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, false
	}
	return strings.TrimSuffix(strings.TrimPrefix(lowerMeta, "data:"), ";base64"), data, true
}
