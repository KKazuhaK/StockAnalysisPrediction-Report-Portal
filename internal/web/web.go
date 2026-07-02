// Package web embeds the built Vite (React + antd) SPA. The dist/ directory is
// produced by `npm run build` (vite outputs to ../internal/web/dist) and baked
// into the binary via go:embed, so a single binary serves the SPA + the API.
// When not built (only the .gitkeep placeholder present), FS still returns a
// valid fs.FS but index.html is absent, and the server replies with a hint.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the built SPA files (the contents of dist/).
func FS() (fs.FS, error) { return fs.Sub(distFS, "dist") }
