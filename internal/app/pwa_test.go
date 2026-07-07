package app

import (
	"bytes"
	"image/png"
	"testing"
)

// The default PWA icon must be a real 512x512 PNG (browsers reject an SVG advertised at
// fixed pixel sizes, which is why the app was not installable).
func TestDefaultPWAIconIsPNG(t *testing.T) {
	b := defaultPWAIconPNG()
	if len(b) == 0 {
		t.Fatal("empty icon")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if got := img.Bounds(); got.Dx() != 512 || got.Dy() != 512 {
		t.Fatalf("icon size = %dx%d, want 512x512", got.Dx(), got.Dy())
	}
}

// The manifest advertises raster icons at the 192/512 sizes browsers require, and an SVG
// only as a scalable "any" icon.
func TestPWAIconEntries(t *testing.T) {
	raster := pwaIconEntries("image/png")
	has192, has512 := false, false
	for _, e := range raster {
		if e["type"] != "image/png" {
			t.Fatalf("raster entry type = %q, want image/png", e["type"])
		}
		if e["sizes"] == "192x192" {
			has192 = true
		}
		if e["sizes"] == "512x512" {
			has512 = true
		}
	}
	if !has192 || !has512 {
		t.Fatalf("raster manifest missing 192/512 icons: %+v", raster)
	}

	svg := pwaIconEntries("image/svg+xml")
	if len(svg) != 1 || svg[0]["sizes"] != "any" || svg[0]["type"] != "image/svg+xml" {
		t.Fatalf("svg manifest = %+v, want a single any/svg entry", svg)
	}
}
