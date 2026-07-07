package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strings"
	"sync"
)

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
		"name":                        title,
		"short_name":                  shortManifestName(title),
		"description":                 title,
		"id":                          "/",
		"start_url":                   "/",
		"scope":                       "/",
		"display":                     "standalone",
		"background_color":            "#ffffff",
		"theme_color":                 "#1677ff",
		"prefer_related_applications": false,
		"icons":                       pwaIconEntries(s.pwaIconMime()),
	})
}

// pwaIconEntries builds the manifest `icons` array for the served icon's MIME type.
// A raster icon (the default PNG, or an uploaded PNG/JPEG/WebP) is advertised at the
// 192px + 512px sizes browsers require for installability; an SVG is advertised as a
// single scalable "any" icon with its own type (a fixed pixel `sizes` on an SVG fails
// Chrome's install check, which is why the old SVG-as-192x192 manifest was never
// installable).
func pwaIconEntries(mime string) []map[string]string {
	if mime == "image/svg+xml" {
		return []map[string]string{
			{"src": "/pwa-icon", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
		}
	}
	return []map[string]string{
		{"src": "/pwa-icon", "sizes": "192x192", "type": mime, "purpose": "any"},
		{"src": "/pwa-icon", "sizes": "512x512", "type": mime, "purpose": "any"},
		{"src": "/pwa-icon", "sizes": "512x512", "type": mime, "purpose": "maskable"},
	}
}

func shortManifestName(s string) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= 12 {
		return string(rs)
	}
	return string(rs[:12])
}

// pwaIconMime reports the MIME type the /pwa-icon endpoint serves for the current
// settings, so the manifest can advertise it accurately. Mirrors pwaIcon's resolution:
// a data URL's own type, an external URL guessed from its extension, else the generated
// default PNG.
func (s *Server) pwaIconMime() string {
	raw := strings.TrimSpace(s.st.GetSetting("pwa_icon_url", ""))
	if raw == "" {
		raw = strings.TrimSpace(s.st.GetSetting("site_logo_url", ""))
	}
	if raw == "" || !validSiteLogoURL(raw) {
		return "image/png" // generated default
	}
	if mime, _, ok := parseImageDataURL(raw); ok {
		return mime
	}
	switch lower := strings.ToLower(raw); {
	case strings.HasSuffix(lower, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return "image/png" // includes .png and unknown externals (a best-effort hint)
	}
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
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(defaultPWAIconPNG())
}

var (
	pwaIconOnce sync.Once
	pwaIconPNG  []byte
)

// defaultPWAIconPNG returns the bundled brand mark as a 512x512 PNG (a rounded blue
// square with a white check), rendered once and cached. A raster PNG — not the old SVG
// — is what browsers require to treat the app as installable, and it doubles as the
// apple-touch-icon.
func defaultPWAIconPNG() []byte {
	pwaIconOnce.Do(func() {
		pwaIconPNG = renderDefaultPWAIcon(512)
	})
	return pwaIconPNG
}

// renderDefaultPWAIcon draws the mark at size×size, supersampled 2× and box-downscaled
// for anti-aliasing. Geometry is expressed in the 512-unit space the SVG used, so pixels
// map cleanly at any output size. Pure std-lib; no external rasterizer or committed asset.
func renderDefaultPWAIcon(size int) []byte {
	const ss = 2
	big := size * ss
	img := image.NewRGBA(image.Rect(0, 0, big, big))
	blue := color.RGBA{0x16, 0x77, 0xff, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	inv := 512.0 / float64(big) // pixel (device) → 512-unit space
	// The checkmark polyline + end dot, in 512-unit space (matching the former SVG).
	pts := [4][2]float64{{112, 352}, {208, 224}, {288, 304}, {400, 144}}
	const halfStroke = 19.0 // stroke-width 38 → half
	const dotR = 34.0
	for y := 0; y < big; y++ {
		for x := 0; x < big; x++ {
			sx, sy := (float64(x)+0.5)*inv, (float64(y)+0.5)*inv
			if !insideRoundedSquare(sx, sy, 512, 112) {
				continue // transparent outside the rounded square
			}
			px := blue
			if nearPolyline(sx, sy, pts[:], halfStroke) || math.Hypot(sx-400, sy-144) <= dotR {
				px = white
			}
			img.SetRGBA(x, y, px)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, boxDownscale(img, ss))
	return buf.Bytes()
}

// insideRoundedSquare tests a point against a size×size square with corner radius r,
// via the rounded-box signed-distance test.
func insideRoundedSquare(x, y, size, r float64) bool {
	half := size / 2
	qx := math.Max(math.Abs(x-half)-(half-r), 0)
	qy := math.Max(math.Abs(y-half)-(half-r), 0)
	return qx*qx+qy*qy <= r*r
}

// nearPolyline reports whether (x,y) is within halfW of any segment of the polyline
// (round joins/caps, since it's a distance test).
func nearPolyline(x, y float64, pts [][2]float64, halfW float64) bool {
	for i := 0; i+1 < len(pts); i++ {
		if distToSegment(x, y, pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1]) <= halfW {
			return true
		}
	}
	return false
}

func distToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// boxDownscale averages each ss×ss block into one pixel (image.RGBA is premultiplied,
// so averaging blue-vs-transparent edges yields correct anti-aliased alpha).
func boxDownscale(src *image.RGBA, ss int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/ss, b.Dy()/ss
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := ss * ss
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a int
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					c := src.RGBAAt(x*ss+i, y*ss+j)
					r += int(c.R)
					g += int(c.G)
					bl += int(c.B)
					a += int(c.A)
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), uint8(a / n)})
		}
	}
	return dst
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
