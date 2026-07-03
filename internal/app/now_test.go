package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// GET /api/v1/now is the portal's authoritative clock: a UTC instant plus the
// civil date in the configured panel timezone. Dify anchors "today" here so a
// UTC-only sandbox clock can't mis-date a China-market report by a day.
func TestV1Now(t *testing.T) {
	s := seedDedupServer(t) // provides tok-query + tok-ingest

	call := func(token string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/now", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.v1Now(rec, req)
		var m map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				t.Fatalf("not JSON: %q", rec.Body.String())
			}
		}
		return rec, m
	}

	// scope: reads need the query scope; ingest-only and anon are rejected
	if rec, _ := call("tok-query"); rec.Code != http.StatusOK {
		t.Fatalf("query token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec, _ := call("tok-ingest"); rec.Code != http.StatusUnauthorized {
		t.Errorf("ingest token on /now: status=%d, want 401", rec.Code)
	}
	if rec, _ := call(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anon on /now: status=%d, want 401", rec.Code)
	}

	// default (no setting): utc is RFC3339 Z, date is a valid YYYY-MM-DD
	_, m := call("tok-query")
	if m["ok"] != true {
		t.Fatalf("ok=%v", m["ok"])
	}
	utc, _ := m["utc"].(string)
	if ts, err := time.Parse(time.RFC3339, utc); err != nil || utc == "" || utc[len(utc)-1] != 'Z' {
		t.Errorf("utc=%q not RFC3339 Z (%v)", utc, err)
	} else if time.Since(ts) > time.Minute {
		t.Errorf("utc=%q not ~now", utc)
	}
	if d, _ := m["date"].(string); !validReportDate(d) {
		t.Errorf("date=%q not YYYY-MM-DD", m["date"])
	}

	// timezone=UTC → date/datetime resolve in UTC
	s.st.SetSetting("timezone", "UTC")
	_, m = call("tok-query")
	if m["tz"] != "UTC" {
		t.Errorf("tz=%v, want UTC", m["tz"])
	}
	if want := time.Now().UTC().Format("2006-01-02"); m["date"] != want {
		t.Errorf("date=%v, want %v (UTC)", m["date"], want)
	}

	// timezone=Asia/Shanghai → civil date resolves in CST (the business zone)
	s.st.SetSetting("timezone", "Asia/Shanghai")
	_, m = call("tok-query")
	if m["tz"] != "Asia/Shanghai" {
		t.Errorf("tz=%v, want Asia/Shanghai", m["tz"])
	}
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		if want := time.Now().In(loc).Format("2006-01-02"); m["date"] != want {
			t.Errorf("date=%v, want %v (Shanghai)", m["date"], want)
		}
	}

	// invalid tz still returns 200 with a valid date (falls back to system)
	s.st.SetSetting("timezone", "Not/AZone")
	if rec, m2 := call("tok-query"); rec.Code != http.StatusOK || !validReportDate(m2["date"].(string)) {
		t.Errorf("invalid tz should fall back: status=%d date=%v", rec.Code, m2["date"])
	}
}

// The panel timezone is admin-editable via /api/admin/settings: valid IANA zones
// persist, invalid ones are rejected without clobbering, and an omitted field is
// left untouched (so saving legacy-import settings never wipes the timezone).
func TestTimezoneSetting(t *testing.T) {
	s := newV1Server(t)
	save := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/admin/settings", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		s.apiSettingsSave(rec, req, "admin")
		return rec
	}

	if rec := save(`{"Timezone":"Asia/Tokyo"}`); rec.Code != http.StatusOK {
		t.Fatalf("save valid tz: %d body=%s", rec.Code, rec.Body.String())
	}
	if s.st.GetSetting("timezone", "") != "Asia/Tokyo" || s.panelLocation().String() != "Asia/Tokyo" {
		t.Errorf("tz not persisted/applied: %q", s.st.GetSetting("timezone", ""))
	}

	// invalid zone → 400, existing value preserved
	if rec := save(`{"Timezone":"Nope/Zone"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid tz: status=%d, want 400", rec.Code)
	}
	if s.st.GetSetting("timezone", "") != "Asia/Tokyo" {
		t.Errorf("invalid save clobbered tz to %q", s.st.GetSetting("timezone", ""))
	}

	// omitted Timezone field → tz untouched (legacy settings save path); and the
	// legacy field it did send is applied.
	if rec := save(`{"OldBase":"http://x"}`); rec.Code != http.StatusOK {
		t.Fatalf("legacy save: %d", rec.Code)
	}
	if s.st.GetSetting("timezone", "") != "Asia/Tokyo" {
		t.Errorf("omitted Timezone wiped the setting to %q", s.st.GetSetting("timezone", ""))
	}
	if s.st.GetSetting("old_base", "") != "http://x" {
		t.Errorf("oldBase not applied: %q", s.st.GetSetting("old_base", ""))
	}

	// converse: a timezone-only save must not wipe the legacy creds
	if rec := save(`{"Timezone":"Asia/Shanghai"}`); rec.Code != http.StatusOK {
		t.Fatalf("tz-only save: %d", rec.Code)
	}
	if s.st.GetSetting("old_base", "") != "http://x" {
		t.Errorf("tz-only save wiped old_base to %q", s.st.GetSetting("old_base", ""))
	}

	// explicit empty → clears to system default
	if rec := save(`{"Timezone":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear tz: %d", rec.Code)
	}
	if s.panelLocation() != time.Local {
		t.Errorf("cleared tz should fall back to system, got %v", s.panelLocation())
	}

	// GET exposes the current timezone for the UI
	grec := httptest.NewRecorder()
	s.apiAdminSettings(grec, httptest.NewRequest("GET", "/api/admin/settings", nil), "admin")
	var m map[string]any
	json.Unmarshal(grec.Body.Bytes(), &m)
	if _, ok := m["timezone"]; !ok {
		t.Errorf("admin settings must include timezone; got %v", m)
	}
}

func TestSiteSettings(t *testing.T) {
	s := newV1Server(t)
	save := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/admin/settings", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		s.apiSettingsSave(rec, req, "admin")
		return rec
	}
	get := func(h func(http.ResponseWriter, *http.Request), path string) map[string]any {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s not JSON: %v", path, err)
		}
		return m
	}

	pub := get(s.apiSite, "/api/site")
	if pub["siteTitle"] != "" || pub["siteLogoUrl"] != "" {
		t.Fatalf("default site settings = %v, want empty overrides", pub)
	}

	if rec := save(`{"siteTitle":" 智研平台 ","siteLogoUrl":"/brand/logo.png"}`); rec.Code != http.StatusOK {
		t.Fatalf("save site settings: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("site_title", ""); got != "智研平台" {
		t.Errorf("site_title=%q", got)
	}
	if got := s.st.GetSetting("site_logo_url", ""); got != "/brand/logo.png" {
		t.Errorf("site_logo_url=%q", got)
	}

	admin := get(func(w http.ResponseWriter, r *http.Request) { s.apiAdminSettings(w, r, "admin") }, "/api/admin/settings")
	if admin["siteTitle"] != "智研平台" || admin["siteLogoUrl"] != "/brand/logo.png" {
		t.Errorf("admin settings missing site fields: %v", admin)
	}
	pub = get(s.apiSite, "/api/site")
	if pub["siteTitle"] != "智研平台" || pub["siteLogoUrl"] != "/brand/logo.png" {
		t.Errorf("public site settings = %v", pub)
	}

	if rec := save(`{"siteTitle":"不应保存","siteLogoUrl":"javascript:alert(1)"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid logo status=%d, want 400", rec.Code)
	}
	if got := s.st.GetSetting("site_title", ""); got != "智研平台" {
		t.Errorf("invalid save half-applied title: %q", got)
	}

	if rec := save(`{"siteTitle":"","siteLogoUrl":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear site settings: %d body=%s", rec.Code, rec.Body.String())
	}
	if s.st.GetSetting("site_title", "x") != "" || s.st.GetSetting("site_logo_url", "x") != "" {
		t.Errorf("clear did not empty site settings")
	}
}

// panelLocation resolves the configured panel tz, falling back to the system
// zone (time.Local) when unset or invalid.
func TestPanelLocation(t *testing.T) {
	s := newV1Server(t)
	if s.panelLocation() != time.Local {
		t.Errorf("default panelLocation = %v, want time.Local", s.panelLocation())
	}
	s.st.SetSetting("timezone", "UTC")
	if s.panelLocation().String() != "UTC" {
		t.Errorf("panelLocation = %v, want UTC", s.panelLocation())
	}
	s.st.SetSetting("timezone", "garbage/zone")
	if s.panelLocation() != time.Local {
		t.Errorf("invalid tz should fall back to time.Local, got %v", s.panelLocation())
	}
}
