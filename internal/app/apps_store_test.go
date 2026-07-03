package app

import (
	"bytes"
	"testing"
)

// Installing an app persists its manifest and every file; ListApps/GetApp surface
// the manifest (scopes parsed), AppFile returns the bytes + content type, and a
// re-install fully replaces the previous files (no stale leftovers).
func TestInstallListDeleteApp(t *testing.T) {
	st := newTestStore(t)

	app := App{ID: "hello", Name: "Hello", Icon: "😀", Version: "1.0.0", Entry: "index.html", Scopes: []string{"query"}}
	files := map[string]AppFile{
		"index.html": {Ctype: "text/html; charset=utf-8", Content: []byte("<h1>hi</h1>")},
		"app.js":     {Ctype: "text/javascript; charset=utf-8", Content: []byte("console.log(1)")},
	}
	if err := st.InstallApp(app, files); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}

	got := st.ListApps()
	if len(got) != 1 {
		t.Fatalf("ListApps len = %d, want 1", len(got))
	}
	if got[0].ID != "hello" || got[0].Name != "Hello" || got[0].Entry != "index.html" || got[0].Icon != "😀" {
		t.Fatalf("ListApps[0] = %+v", got[0])
	}
	if len(got[0].Scopes) != 1 || got[0].Scopes[0] != "query" {
		t.Fatalf("scopes = %v, want [query]", got[0].Scopes)
	}

	if _, ok := st.GetApp("hello"); !ok {
		t.Fatalf("GetApp(hello) not found")
	}
	if _, ok := st.GetApp("nope"); ok {
		t.Fatalf("GetApp(nope) should be missing")
	}

	ctype, content, ok := st.AppFile("hello", "index.html")
	if !ok || !bytes.Equal(content, []byte("<h1>hi</h1>")) || ctype != "text/html; charset=utf-8" {
		t.Fatalf("AppFile index.html = %q %q %v", ctype, content, ok)
	}
	if _, _, ok := st.AppFile("hello", "missing.css"); ok {
		t.Fatalf("AppFile missing.css should be absent")
	}

	// Re-install with a different file set: the old app.js must be gone.
	app2 := App{ID: "hello", Name: "Hello v2", Version: "2.0.0", Entry: "index.html", Scopes: []string{"query"}}
	if err := st.InstallApp(app2, map[string]AppFile{
		"index.html": {Ctype: "text/html; charset=utf-8", Content: []byte("<h1>v2</h1>")},
	}); err != nil {
		t.Fatalf("re-InstallApp: %v", err)
	}
	if g, _ := st.GetApp("hello"); g.Name != "Hello v2" || g.Version != "2.0.0" {
		t.Fatalf("after re-install GetApp = %+v", g)
	}
	if _, _, ok := st.AppFile("hello", "app.js"); ok {
		t.Fatalf("stale app.js survived re-install")
	}
	if _, content, _ := st.AppFile("hello", "index.html"); !bytes.Equal(content, []byte("<h1>v2</h1>")) {
		t.Fatalf("index.html not replaced on re-install")
	}

	if err := st.DeleteApp("hello"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if len(st.ListApps()) != 0 {
		t.Fatalf("app still listed after delete")
	}
	if _, _, ok := st.AppFile("hello", "index.html"); ok {
		t.Fatalf("app files survived delete")
	}
}
