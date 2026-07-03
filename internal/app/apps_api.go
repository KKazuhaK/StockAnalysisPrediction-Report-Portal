package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// HTTP surface for the downloadable iframe-app platform (docs/adr/0003-downloadable-apps.md).
//
//   - install/uninstall are admin-only (a bundle is untrusted third-party code);
//   - listing + opening (token mint) is available to any logged-in user;
//   - assets are served publicly like the SPA's own bundle — they carry no secrets,
//     the security boundary is the sandboxed iframe + the scoped bridge token.

// appTokenTTL bounds how long a minted bridge token stays valid.
const appTokenTTL = 30 * time.Minute

// Bundle size guards (defend against zip bombs on an admin upload).
const (
	maxBundleUpload = 8 << 20  // compressed upload cap
	maxBundleTotal  = 16 << 20 // total uncompressed cap
	maxBundleFiles  = 200
)

var appIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// appManifest is the app.json inside a bundle.
type appManifest struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon"`
	Version string   `json:"version"`
	Entry   string   `json:"entry"`
	Scopes  []string `json:"scopes"`
}

// appJSON is the wire shape returned to the SPA (never includes file bytes).
func appJSON(a App) map[string]any {
	return map[string]any{
		"id": a.ID, "name": a.Name, "icon": a.Icon, "version": a.Version,
		"entry": a.Entry, "scopes": a.Scopes,
	}
}

// apiApps lists installed apps for the Apps hub. Any logged-in user.
func (s *Server) apiApps(w http.ResponseWriter, r *http.Request, user string) {
	apps := s.st.ListApps()
	out := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		out = append(out, appJSON(a))
	}
	writeJSON(w, map[string]any{"apps": out})
}

// apiAppInstall installs (or replaces) an app from an uploaded .zip bundle. Admin-only.
func (s *Server) apiAppInstall(w http.ResponseWriter, r *http.Request, user string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleUpload)
	if err := r.ParseMultipartForm(maxBundleUpload); err != nil {
		jsonError(w, http.StatusBadRequest, "upload too large or malformed")
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "missing 'bundle' file")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBundleUpload+1))
	if err != nil || len(raw) > maxBundleUpload {
		jsonError(w, http.StatusBadRequest, "bundle read failed or too large")
		return
	}
	app, files, err := parseAppBundle(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid bundle: "+err.Error())
		return
	}
	if err := s.st.InstallApp(app, files); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("app installed: %s (%s) by %s, %d files", app.ID, app.Version, user, len(files))
	writeJSON(w, map[string]any{"ok": true, "app": appJSON(app)})
}

// apiAppDelete uninstalls an app and its files. Admin-only.
func (s *Server) apiAppDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := r.PathValue("id")
	if err := s.st.DeleteApp(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiAppToken mints a short-lived, scoped bearer token for an app's /api/v1 bridge.
// The trusted host page holds it and calls the API on the sandboxed iframe's behalf;
// the token itself is never handed to the iframe. Any logged-in user may open an app.
func (s *Server) apiAppToken(w http.ResponseWriter, r *http.Request, user string) {
	app, ok := s.st.GetApp(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "app not found")
		return
	}
	scopes := grantableScopes(app.Scopes)
	token := s.appTok.mint(scopes, time.Now())
	writeJSON(w, map[string]any{
		"app": appJSON(app), "token": token, "scopes": scopes, "expires_in": int(appTokenTTL / time.Second),
	})
}

// appAssets serves an app's static files at /app-assets/{id}/{path...}. Public
// (like the SPA bundle); no-cache so a re-install is picked up immediately.
func (s *Server) appAssets(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := cleanAssetPath(r.PathValue("path"))
	if rel == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ctype, content, ok := s.st.AppFile(id, rel)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Force the browser to sandbox this document to a unique (null) origin even if
	// it is opened as a top-level page, not just inside the host's iframe. Without
	// this, a malicious installed app's HTML fetched directly at /app-assets/{id}/…
	// would run on the portal's own origin and could ride the admin's session
	// cookie to /api/*. The CSP sandbox neutralises that: a null origin can't send
	// the (SameSite=Lax) session cookie cross-site. allow-scripts keeps the app
	// functional; we deliberately omit allow-same-origin.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	w.Write(content)
}

// cleanAssetPath normalises a request path and rejects traversal/absolute paths.
func cleanAssetPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || path.IsAbs(c) {
		return ""
	}
	return c
}

// parseAppBundle reads a .zip bundle: app.json (the manifest) plus the app's files.
// It validates the id/entry and rejects path traversal, oversize, and too many files.
func parseAppBundle(raw []byte) (App, map[string]AppFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return App{}, nil, fmt.Errorf("not a zip: %w", err)
	}
	var manifestRaw []byte
	files := map[string]AppFile{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := cleanAssetPath(f.Name)
		if name == "" {
			return App{}, nil, fmt.Errorf("unsafe path in bundle: %q", f.Name)
		}
		if len(files)+1 > maxBundleFiles {
			return App{}, nil, fmt.Errorf("too many files (max %d)", maxBundleFiles)
		}
		rc, err := f.Open()
		if err != nil {
			return App{}, nil, err
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxBundleTotal-total+1))
		rc.Close()
		if err != nil {
			return App{}, nil, err
		}
		total += int64(len(content))
		if total > maxBundleTotal {
			return App{}, nil, fmt.Errorf("bundle too large uncompressed (max %d bytes)", maxBundleTotal)
		}
		if name == "app.json" {
			manifestRaw = content
			continue // the manifest is metadata, not a servable asset
		}
		files[name] = AppFile{Ctype: contentTypeFor(name), Content: content}
	}
	if manifestRaw == nil {
		return App{}, nil, fmt.Errorf("bundle has no app.json manifest")
	}
	var m appManifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return App{}, nil, fmt.Errorf("app.json is not valid JSON: %w", err)
	}
	if !appIDRe.MatchString(m.ID) {
		return App{}, nil, fmt.Errorf("app id %q is invalid (use a-z 0-9 _ -)", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return App{}, nil, fmt.Errorf("app.json: name is required")
	}
	entry := m.Entry
	if entry == "" {
		entry = "index.html"
	}
	entry = cleanAssetPath(entry)
	if entry == "" {
		return App{}, nil, fmt.Errorf("app.json: entry path is invalid")
	}
	if _, ok := files[entry]; !ok {
		return App{}, nil, fmt.Errorf("entry %q is not present in the bundle", entry)
	}
	app := App{ID: m.ID, Name: m.Name, Icon: m.Icon, Version: m.Version, Entry: entry, Scopes: splitCSV(strings.Join(m.Scopes, ","))}
	return app, files, nil
}

// contentTypeFor infers a content type from a file extension, defaulting to a
// safe octet-stream so unknown types are downloaded, not executed.
func contentTypeFor(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
