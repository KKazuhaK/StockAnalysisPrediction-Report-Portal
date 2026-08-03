package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Downloading the IP database.
//
// The load-bearing property is that the URL is a CREDENTIAL — MaxMind puts license_key in the
// query, ipinfo puts token — so the two places it must never reach are the log and the error
// shown to whoever pressed the button. The sibling panel has a test for exactly this, and it is
// the one worth copying.

func TestRedactKeepsTheHostAndDropsTheKey(t *testing.T) {
	raw := "https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-City&license_key=SUPER_SECRET&suffix=tar.gz"
	got := redactURL(raw)
	if strings.Contains(got, "SUPER_SECRET") || strings.Contains(got, "license_key") {
		t.Errorf("redactURL leaked the query: %q", got)
	}
	if !strings.Contains(got, "download.maxmind.com") {
		t.Errorf("redactURL dropped the host, so an operator cannot tell what failed: %q", got)
	}

	// ipinfo puts it in ?token=.
	if got := redactURL("https://ipinfo.io/data/ipinfo_lite.mmdb?token=TKN123"); strings.Contains(got, "TKN123") {
		t.Errorf("redactURL leaked the ipinfo token: %q", got)
	}
}

// net/http returns *url.Error, whose Error() prints the whole URL. Wrapping one without rebuilding
// it puts the key straight into the log line.
func TestRedactURLErrRebuildsAUrlError(t *testing.T) {
	raw := "https://download.maxmind.com/app/geoip_download?license_key=SUPER_SECRET"
	err := redactURLErr(raw, &url.Error{Op: "Get", URL: raw, Err: errors.New("i/o timeout")})
	if strings.Contains(err.Error(), "SUPER_SECRET") {
		t.Errorf("the key survived into the error: %q", err)
	}
	if !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("the cause was lost: %q", err)
	}
}

// And the whole download path: every error an admin can trigger must be safe to show.
func TestDownloadErrorsNeverCarryTheKey(t *testing.T) {
	const key = "SUPER_SECRET_KEY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A vendor's rejection page routinely echoes the key back; only the STATUS may be used.
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid license_key=" + key))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	raw := srv.URL + "/db?license_key=" + key
	_, err := u.download(context.Background(), raw)
	if err == nil {
		t.Fatal("a 401 was treated as success")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the key reached the admin's error box: %q", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error does not say what the vendor answered: %q", err)
	}
}

func TestDownloadsABareMMDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-really-a-database-but-bytes"))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	name, err := u.download(context.Background(), srv.URL+"/data/ipinfo_lite.mmdb?token=TKN")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if name != "ipinfo_lite.mmdb" {
		t.Errorf("wrote %q, want the name from the URL path", name)
	}
	// The credential must not end up in a filename either — those show up in logs and listings.
	if strings.Contains(name, "TKN") || strings.Contains(name, "?") {
		t.Errorf("the query reached the filename: %q", name)
	}
	if _, err := os.Stat(filepath.Join(u.svc.Dir(), name)); err != nil {
		t.Errorf("the file is not where the reader looks: %v", err)
	}
	// And no .part is left behind on success.
	if parts, _ := filepath.Glob(filepath.Join(u.svc.Dir(), "*.part")); len(parts) != 0 {
		t.Errorf("a temp file survived a successful download: %v", parts)
	}
}

// MaxMind ships a .tar.gz with the database nested in a dated directory.
func TestUnpacksTheMaxMindTarball(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarGZ(t, map[string]string{
			"GeoLite2-City_20260801/COPYRIGHT.txt":      "(c)",
			"GeoLite2-City_20260801/GeoLite2-City.mmdb": "database-bytes",
		}))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	name, err := u.download(context.Background(), srv.URL+"/geoip_download?license_key=K&suffix=tar.gz")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if name != "GeoLite2-City.mmdb" {
		t.Fatalf("wrote %q, want the .mmdb from inside the archive", name)
	}
	got, err := os.ReadFile(filepath.Join(u.svc.Dir(), name))
	if err != nil || string(got) != "database-bytes" {
		t.Errorf("contents = %q (%v); the wrong archive member was written", got, err)
	}
}

// A tar entry's path is attacker-influenced, and "../" is how an archive writes outside its
// directory. Only the base name is ever used.
func TestArchivePathsCannotEscapeTheDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarGZ(t, map[string]string{"../../../../tmp/evil.mmdb": "pwned"}))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	name, err := u.download(context.Background(), srv.URL+"/x?license_key=K")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		t.Fatalf("the archive's path was used verbatim: %q", name)
	}
	if _, err := os.Stat(filepath.Join(u.svc.Dir(), "evil.mmdb")); err != nil {
		t.Errorf("the file did not land inside the geoip dir: %v", err)
	}
}

func TestArchiveWithNoDatabaseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarGZ(t, map[string]string{"README.txt": "nothing here"}))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	if _, err := u.download(context.Background(), srv.URL+"/x?license_key=SECRET"); err == nil {
		t.Fatal("an archive with no database was accepted")
	} else if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the key leaked: %q", err)
	}
}

// A second click must not start a second download onto the same temp file.
func TestASecondUpdateIsRefusedWhileOneRuns(t *testing.T) {
	u := newTestUpdater(t)
	u.st.SetSetting(setGeoURL, "https://example.invalid/db.mmdb")
	u.mu.Lock()
	u.updating = true
	u.mu.Unlock()
	if err := u.Start(); err == nil {
		t.Fatal("a concurrent update was allowed")
	}
	if !u.State().Updating {
		t.Error("the state does not report the run in progress")
	}
}

func TestStartRefusesWithNoURLConfigured(t *testing.T) {
	u := newTestUpdater(t)
	if err := u.Start(); err == nil {
		t.Fatal("started an update with no source")
	}
	if u.State().HasURL {
		t.Error("HasURL is true with nothing configured")
	}
	// The state says whether a source exists and never what it is.
	u.st.SetSetting(setGeoURL, "https://download.maxmind.com/x?license_key=SECRET")
	blob := u.State()
	if !blob.HasURL {
		t.Error("HasURL is false with a source configured")
	}
	if strings.Contains(blob.LastErr+blob.LastFile, "SECRET") {
		t.Error("the state carries the credential")
	}
}

func newTestUpdater(t *testing.T) *geoUpdater {
	t.Helper()
	s := tenancyServer(t)
	s.geo = newGeoService(t.TempDir())
	u := newGeoUpdater(s.geo, s.st, func() *safeClient {
		c := newSafeClient(true) // the httptest server is on loopback
		c.allowInsecureForTest = true
		return c
	})
	return u
}

func tarGZ(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
