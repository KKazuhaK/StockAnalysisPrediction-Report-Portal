package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// marketAppServer stands in for the GitHub raw host: it serves an app-market
// index and a real .zip bundle at fixed paths.
func marketAppServer(t *testing.T) *httptest.Server {
	t.Helper()
	zip := makeZip(t, map[string]string{
		"app.json":   `{"id":"demo","name":"Demo","icon":"🧪","version":"2.0.0","entry":"index.html","scopes":["query"]}`,
		"index.html": "<h1>demo</h1>",
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			idx := `{"apps":[{"id":"demo","name":"Demo","icon":"🧪","version":"2.0.0","description":"a demo","scopes":["query"],"path":"demo.zip"}]}`
			w.Write([]byte(idx))
		case "/demo.zip":
			w.Header().Set("Content-Type", "application/zip")
			w.Write(zip)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func appMarketServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}, appTok: newAppTokens(30 * 60 * 1e9)}
}

// The market list reports each entry and flips `installed` once installed.
func TestAppMarketList(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	rec := httptest.NewRecorder()
	srv.apiAppMarket(rec, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("apiAppMarket → %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		IndexURL string `json:"index_url"`
		Apps     []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Version   string   `json:"version"`
			Scopes    []string `json:"scopes"`
			Installed bool     `json:"installed"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Apps) != 1 || out.Apps[0].ID != "demo" || out.Apps[0].Version != "2.0.0" {
		t.Fatalf("apps = %+v", out.Apps)
	}
	if out.Apps[0].Installed {
		t.Fatal("demo should not be installed yet")
	}
	if len(out.Apps[0].Scopes) != 1 || out.Apps[0].Scopes[0] != "query" {
		t.Fatalf("scopes = %v", out.Apps[0].Scopes)
	}

	// Install it, then the list should mark it installed.
	post(t, srv.apiAppMarketInstall, `{"id":"demo"}`)
	rec2 := httptest.NewRecorder()
	srv.apiAppMarket(rec2, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	json.Unmarshal(rec2.Body.Bytes(), &out)
	if !out.Apps[0].Installed {
		t.Fatal("demo should be installed after market install")
	}
}

// Installing from the market downloads the bundle and stores the app + its files.
func TestAppMarketInstall(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	post(t, srv.apiAppMarketInstall, `{"id":"demo"}`)

	a, ok := srv.st.GetApp("demo")
	if !ok {
		t.Fatal("app not installed from market")
	}
	if a.Version != "2.0.0" || a.Icon != "🧪" || a.Entry != "index.html" {
		t.Fatalf("installed app = %+v", a)
	}
	if _, _, ok := srv.st.AppFile("demo", "index.html"); !ok {
		t.Fatal("index.html asset missing after market install")
	}
}

// An unknown id yields 404 from the market install endpoint.
func TestAppMarketInstallUnknown(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	rec := httptest.NewRecorder()
	srv.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"id":"nope"}`)), "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id → %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
