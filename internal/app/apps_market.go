package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The app market: an admin browses a GitHub-hosted index of downloadable apps and
// one-click installs a bundle from it. It mirrors the batch plugin fetcher, but the
// unit is an app `.zip` (manifest + assets) rather than a single JSON manifest, so
// install runs the same parseAppBundle + InstallApp path as a manual upload.
// See docs/adr/0003-downloadable-apps.md (phase 2).

// defaultAppMarketIndexURL points at this repo's own apps/index.json so the market
// works out of the box; an admin can repoint it via the app_market_index_url setting.
const defaultAppMarketIndexURL = "https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/main/apps/index.json"

// appMarketEntry is one app listed in the market index. `path` is resolved relative
// to the index URL; an absolute `url` (if set) overrides it.
type appMarketEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
	Path        string   `json:"path"` // bundle path relative to the index URL
	URL         string   `json:"url"`  // absolute bundle URL (overrides Path when set)
}

const setAppMarketIndexURL = "app_market_index_url"

func (s *Server) appMarketIndexURL() string {
	if u := strings.TrimSpace(s.st.GetSetting(setAppMarketIndexURL, "")); u != "" {
		return u
	}
	return defaultAppMarketIndexURL
}

// apiAppMarketIndexSave repoints the market at another index, which the constant above has promised
// an admin could do since the market shipped — with nothing anywhere able to write the setting.
//
// An empty value returns to the built-in index rather than storing a blank, so "undo" is expressible
// without knowing the default URL by heart. The URL goes through the same SSRF guard the fetch
// itself uses, so a saved value cannot be a probe of the host's own network.
func (s *Server) apiAppMarketIndexSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IndexURL *string `json:"index_url"`
	}
	if err := readJSON(r, &in); err != nil || in.IndexURL == nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	next := strings.TrimSpace(*in.IndexURL)
	if next != "" {
		if err := newSafeClient(false).checkURL(next); err != nil {
			jsonErrorCode(w, http.StatusBadRequest, "app_market_url_refused", "该地址不可用："+err.Error())
			return
		}
	}
	s.st.SetSetting(setAppMarketIndexURL, next)
	s.recordChange(r, user, AuditPolicyChange, "app_market_index", "",
		map[string]any{"index_url": firstNonEmpty(next, defaultAppMarketIndexURL)})
	writeJSON(w, map[string]any{"ok": true, "index_url": s.appMarketIndexURL()})
}

// fetchURL downloads up to `limit` bytes from rawURL over an HTTP GET. The index is
// tiny; a bundle can be as large as an admin upload, so the cap is passed in.
func fetchURL(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response too large (max %d bytes)", limit)
	}
	return raw, nil
}

func (s *Server) fetchAppMarket(ctx context.Context) ([]appMarketEntry, error) {
	raw, err := fetchURL(ctx, s.appMarketIndexURL(), 1<<20)
	if err != nil {
		return nil, err
	}
	var idx struct {
		Apps []appMarketEntry `json:"apps"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index is not valid JSON: %w", err)
	}
	return idx.Apps, nil
}

// apiAppMarket lists the market index, tagging each entry with whether it is already
// installed. Admin-only.
func (s *Server) apiAppMarket(w http.ResponseWriter, r *http.Request, user string) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	entries, err := s.fetchAppMarket(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch market failed: "+err.Error())
		return
	}
	installed := map[string]bool{}
	for _, a := range s.st.ListApps() {
		installed[a.ID] = true
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"id": e.ID, "name": e.Name, "icon": e.Icon, "version": e.Version,
			"description": e.Description, "scopes": e.Scopes, "installed": installed[e.ID],
		})
	}
	writeJSON(w, map[string]any{"index_url": s.appMarketIndexURL(), "apps": out})
}

// apiAppMarketInstall downloads one bundle from the configured market and installs
// it through the same validated path as a manual upload. Admin-only.
func (s *Server) apiAppMarketInstall(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &in); err != nil || in.ID == "" {
		jsonError(w, http.StatusBadRequest, "id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	entries, err := s.fetchAppMarket(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch market failed: "+err.Error())
		return
	}
	var entry *appMarketEntry
	for i := range entries {
		if entries[i].ID == in.ID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		jsonError(w, http.StatusNotFound, "app not found in market")
		return
	}
	bundleURL := entry.URL
	if bundleURL == "" {
		base, err := url.Parse(s.appMarketIndexURL())
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "bad market index url")
			return
		}
		ref, err := url.Parse(entry.Path)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "bad bundle path")
			return
		}
		bundleURL = base.ResolveReference(ref).String()
	}
	raw, err := fetchURL(ctx, bundleURL, maxBundleUpload)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch bundle failed: "+err.Error())
		return
	}
	app, files, err := parseAppBundle(raw)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "invalid bundle: "+err.Error())
		return
	}
	if err := s.st.InstallApp(app, files); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("app installed from market: %s (%s) by %s, %d files", app.ID, app.Version, user, len(files))
	writeJSON(w, map[string]any{"ok": true, "app": appJSON(app)})
}
