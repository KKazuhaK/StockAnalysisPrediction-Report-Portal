package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// makeZip builds an in-memory zip from path→content.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestParseAppBundle(t *testing.T) {
	raw := makeZip(t, map[string]string{
		"app.json":   `{"id":"hello","name":"Hello","icon":"😀","version":"1.0.0","entry":"index.html","scopes":["query"]}`,
		"index.html": "<h1>hi</h1>",
		"sub/app.js": "console.log(1)",
	})
	app, files, err := parseAppBundle(raw)
	if err != nil {
		t.Fatalf("parseAppBundle: %v", err)
	}
	if app.ID != "hello" || app.Name != "Hello" || app.Entry != "index.html" || app.Icon != "😀" {
		t.Fatalf("app = %+v", app)
	}
	if len(app.Scopes) != 1 || app.Scopes[0] != "query" {
		t.Fatalf("scopes = %v", app.Scopes)
	}
	if _, ok := files["app.json"]; ok {
		t.Fatalf("app.json must not be stored as a servable asset")
	}
	if f, ok := files["index.html"]; !ok || f.Ctype == "" {
		t.Fatalf("index.html missing or has no content type: %+v", f)
	}
	if _, ok := files["sub/app.js"]; !ok {
		t.Fatalf("nested file sub/app.js should be kept")
	}
}

func TestParseAppBundleRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"no manifest":   {"index.html": "x"},
		"missing entry": {"app.json": `{"id":"a","name":"A","entry":"index.html"}`},
		"bad id":        {"app.json": `{"id":"../evil","name":"A","entry":"index.html"}`, "index.html": "x"},
		"zip slip":      {"app.json": `{"id":"a","name":"A","entry":"index.html"}`, "index.html": "x", "../escape.txt": "boom"},
	}
	for name, files := range cases {
		if _, _, err := parseAppBundle(makeZip(t, files)); err == nil {
			t.Fatalf("%s: expected an error, got nil", name)
		}
	}
}

// The token endpoint mints a query-scoped bearer that /api/v1 accepts for reads
// but not for writes.
func TestAppTokenEndpoint(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}, appTok: newAppTokens(30 * 60 * 1e9)}
	if err := s.st.InstallApp(App{ID: "hello", Name: "Hello", Entry: "index.html", Scopes: []string{"query", "ingest"}}, map[string]AppFile{
		"index.html": {Ctype: "text/html", Content: []byte("x")},
	}); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/apps/hello/token", nil)
	req.SetPathValue("id", "hello")
	rec := httptest.NewRecorder()
	s.apiAppToken(rec, req, "user")
	if rec.Code != http.StatusOK {
		t.Fatalf("apiAppToken → %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Token == "" {
		t.Fatal("empty token")
	}
	// ingest was requested but must be filtered out this phase.
	if len(out.Scopes) != 1 || out.Scopes[0] != "query" {
		t.Fatalf("scopes = %v, want [query]", out.Scopes)
	}

	authed := httptest.NewRequest("GET", "/api/v1/reports", nil)
	authed.Header.Set("Authorization", "Bearer "+out.Token)
	if !s.tokenOK(authed, "query") {
		t.Fatal("minted token should pass query scope")
	}
	if s.tokenOK(authed, "ingest") {
		t.Fatal("minted token must NOT pass ingest scope")
	}
}

// A missing app yields 404 from the token endpoint.
func TestAppTokenEndpointUnknownApp(t *testing.T) {
	s := &Server{st: newTestStore(t), appTok: newAppTokens(60 * 1e9)}
	req := httptest.NewRequest("POST", "/api/apps/nope/token", nil)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	s.apiAppToken(rec, req, "user")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown app → %d, want 404", rec.Code)
	}
}
