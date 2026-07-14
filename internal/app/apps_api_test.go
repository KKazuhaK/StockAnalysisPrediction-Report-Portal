package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// multipartBundle builds a multipart/form-data request carrying a "bundle" file.
func multipartBundle(t *testing.T, url string, raw []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("bundle", "app.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	fw.Write(raw)
	mw.Close()
	req := httptest.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

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

// The token endpoint mints a bearer carrying exactly the app's declared, supported
// scopes; /api/v1 then accepts it for those scopes (phase 2: query + ingest).
func TestAppTokenEndpoint(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}, appTok: newAppTokens(30 * 60 * 1e9)}
	if err := s.st.InstallApp(App{ID: "hello", Name: "Hello", Entry: "index.html", Scopes: []string{"query", "ingest"}}, map[string]AppFile{
		"index.html": {Ctype: "text/html", Content: []byte("x")},
	}); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}

	mint := func(user string) []string {
		req := httptest.NewRequest("POST", "/api/apps/hello/token", nil)
		req.SetPathValue("id", "hello")
		rec := httptest.NewRecorder()
		s.apiAppToken(rec, req, user)
		if rec.Code != http.StatusOK {
			t.Fatalf("apiAppToken(%s) → %d: %s", user, rec.Code, rec.Body.String())
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
		return out.Scopes
	}

	// A non-admin caller is downgraded to read-only: even though the app declares ingest, a read-only
	// user must not be able to mint a write token and bypass the sandbox (security fix).
	if sc := mint("user"); len(sc) != 1 || !contains(sc, "query") {
		t.Fatalf("non-admin scopes = %v, want [query]", sc)
	}

	// An admin gets both declared+supported scopes, and /api/v1 accepts the token for each.
	if err := s.st.UpsertUser(User{Username: "boss", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if sc := mint("boss"); len(sc) != 2 || !contains(sc, "query") || !contains(sc, "ingest") {
		t.Fatalf("admin scopes = %v, want [query ingest]", sc)
	}
	// Re-mint an admin token and confirm /api/v1 honors both scopes.
	req := httptest.NewRequest("POST", "/api/apps/hello/token", nil)
	req.SetPathValue("id", "hello")
	rec := httptest.NewRecorder()
	s.apiAppToken(rec, req, "boss")
	var out struct {
		Token string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	authed := httptest.NewRequest("GET", "/api/v1/reports", nil)
	authed.Header.Set("Authorization", "Bearer "+out.Token)
	if !s.tokenOK(authed, "query") || !s.tokenOK(authed, "ingest") {
		t.Fatal("admin-minted token should pass both query and ingest scopes")
	}
}

// An app that declares only read access is never granted a write scope, even if a
// buggy caller asks for ingest downstream.
func TestAppTokenEndpointReadOnlyApp(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}, appTok: newAppTokens(30 * 60 * 1e9)}
	if err := s.st.InstallApp(App{ID: "ro", Name: "RO", Entry: "index.html", Scopes: []string{"query"}}, map[string]AppFile{
		"index.html": {Ctype: "text/html", Content: []byte("x")},
	}); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/apps/ro/token", nil)
	req.SetPathValue("id", "ro")
	rec := httptest.NewRecorder()
	s.apiAppToken(rec, req, "user")
	var out struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Scopes) != 1 || out.Scopes[0] != "query" {
		t.Fatalf("scopes = %v, want [query]", out.Scopes)
	}
	authed := httptest.NewRequest("POST", "/api/v1/reports", nil)
	authed.Header.Set("Authorization", "Bearer "+out.Token)
	if s.tokenOK(authed, "ingest") {
		t.Fatal("a query-only app must NOT pass ingest scope")
	}
}

// install?preview=1 parses the bundle and returns its manifest (scopes) WITHOUT
// persisting — it drives the install-time permission prompt for manual uploads.
func TestAppInstallPreview(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}}
	raw := makeZip(t, map[string]string{
		"app.json":   `{"id":"prev","name":"Prev","version":"1.0.0","entry":"index.html","scopes":["query","ingest"]}`,
		"index.html": "<h1>x</h1>",
	})

	rec := httptest.NewRecorder()
	s.apiAppInstall(rec, multipartBundle(t, "/api/admin/apps/install?preview=1", raw), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview → %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Preview bool `json:"preview"`
		App     struct {
			ID     string   `json:"id"`
			Scopes []string `json:"scopes"`
		} `json:"app"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Preview || out.App.ID != "prev" {
		t.Fatalf("preview response = %s", rec.Body.String())
	}
	if len(out.App.Scopes) != 2 || !contains(out.App.Scopes, "ingest") {
		t.Fatalf("scopes = %v, want query+ingest", out.App.Scopes)
	}
	if _, ok := s.st.GetApp("prev"); ok {
		t.Fatal("preview must NOT persist the app")
	}

	// A real install (no preview) does persist.
	rec2 := httptest.NewRecorder()
	s.apiAppInstall(rec2, multipartBundle(t, "/api/admin/apps/install", raw), "admin")
	if rec2.Code != http.StatusOK {
		t.Fatalf("install → %d: %s", rec2.Code, rec2.Body.String())
	}
	if _, ok := s.st.GetApp("prev"); !ok {
		t.Fatal("install should persist the app")
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
