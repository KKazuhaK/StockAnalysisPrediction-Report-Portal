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
	"time"
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
	_, err := u.download(context.Background(), raw, "GeoLite2-City.mmdb")
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

// Extraction is tested directly rather than through a download, because a download now VALIDATES
// what it got and a hand-made fixture is not a real database. Separating them also means each test
// says one thing: this one is about unwrapping, the next about refusing to install rubbish.

func TestExtractsABareDatabase(t *testing.T) {
	var out bytes.Buffer
	if err := extractMMDB(strings.NewReader("raw-database-bytes"), "https://x/y.mmdb?token=T", &out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out.String() != "raw-database-bytes" {
		t.Errorf("got %q", out.String())
	}
}

// MaxMind ships a .tar.gz with the database nested in a dated directory.
func TestExtractsFromTheMaxMindTarball(t *testing.T) {
	var out bytes.Buffer
	src := bytes.NewReader(tarGZ(t, map[string]string{
		"GeoLite2-City_20260801/COPYRIGHT.txt":      "(c)",
		"GeoLite2-City_20260801/GeoLite2-City.mmdb": "database-bytes",
	}))
	if err := extractMMDB(src, "https://x?license_key=K", &out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out.String() != "database-bytes" {
		t.Errorf("got %q — the wrong archive member was taken", out.String())
	}
}

// DB-IP gzips the database directly, with no tar around it. Told apart by the tar magic in the
// DECOMPRESSED stream, not by the file extension, which a redirect can make a liar of.
func TestExtractsFromAPlainGzip(t *testing.T) {
	var gzbuf bytes.Buffer
	zw := gzip.NewWriter(&gzbuf)
	zw.Write([]byte("dbip-database-bytes"))
	zw.Close()

	var out bytes.Buffer
	if err := extractMMDB(&gzbuf, "https://download.db-ip.com/free/x.mmdb.gz", &out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out.String() != "dbip-database-bytes" {
		t.Errorf("got %q", out.String())
	}
}

func TestExtractRejectsAnArchiveWithNoDatabase(t *testing.T) {
	var out bytes.Buffer
	src := bytes.NewReader(tarGZ(t, map[string]string{"README.txt": "nothing here"}))
	err := extractMMDB(src, "https://x?license_key=SECRET", &out)
	if err == nil {
		t.Fatal("an archive with no database was accepted")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the key leaked: %q", err)
	}
}

// The one that matters most about installing: a truncated download or an HTML error page served
// with a 200 must not replace a database that works. Validation happens on the temp file, before
// the swap.
func TestRubbishNeverReplacesTheLiveDatabase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>upstream had a bad day</html>"))
	}))
	defer srv.Close()

	u := newTestUpdater(t)
	live := filepath.Join(u.svc.Dir(), "GeoLite2-City.mmdb")
	if err := os.MkdirAll(u.svc.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("the working database"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := u.download(context.Background(), srv.URL+"/x?license_key=SECRET", "GeoLite2-City.mmdb")
	if err == nil {
		t.Fatal("an HTML error page was installed as a database")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the key leaked: %q", err)
	}
	got, _ := os.ReadFile(live)
	if string(got) != "the working database" {
		t.Errorf("the live database was replaced with rubbish: %q", got)
	}
	// And nothing is left lying around for the picker to find.
	if parts, _ := filepath.Glob(filepath.Join(u.svc.Dir(), "*.part*")); len(parts) != 0 {
		t.Errorf("a temp file survived a failed download: %v", parts)
	}
}

// Every selectable source must be handled by candidateURLs. The two drifting apart would give an
// admin a source that saves and then fails only at download time.
func TestEverySourceIsHandled(t *testing.T) {
	for _, src := range geoSources {
		if !validGeoSource(src) {
			t.Errorf("%q is offered but not valid", src)
		}
		// Supplied with everything any source might need, so the only way to fail is being unhandled.
		_, target, err := candidateURLs(src, "GeoLite2-City", "KEY", "https://example.com/db.mmdb")
		if err != nil {
			t.Errorf("%s: %v", src, err)
		}
		if !strings.HasSuffix(target, ".mmdb") {
			t.Errorf("%s: target %q is not a database filename", src, target)
		}
		if strings.Contains(target, "/") || strings.Contains(target, "..") {
			t.Errorf("%s: target %q could write outside the directory", src, target)
		}
	}
	if _, _, err := candidateURLs("nonsense", "", "", ""); err == nil {
		t.Error("an unknown source was accepted")
	}
}

// A vendor that needs a credential must say so rather than fetching without one and failing oddly.
func TestSourcesThatNeedACredentialSaySo(t *testing.T) {
	for _, src := range []string{"maxmind", "ipinfo"} {
		if _, _, err := candidateURLs(src, "", "", ""); err == nil {
			t.Errorf("%s built a URL with no credential", src)
		}
	}
	// DB-IP needs none, and publishes month-stamped files with no "latest" — so the current month
	// and the previous one are both tried, because early in a month the current may not exist.
	urls, _, err := candidateURLs("dbip", "", "", "")
	if err != nil || len(urls) != 2 {
		t.Fatalf("dbip gave %d urls (%v), want the current month and a fallback", len(urls), err)
	}
	if urls[0] == urls[1] {
		t.Error("the fallback month is the same as the current one")
	}
}

// AddDate(0,-1,0) normalises the 31st back into the same month, which would make the DB-IP
// fallback identical to the URL that just failed.
func TestPreviousMonthIsAlwaysADifferentMonth(t *testing.T) {
	for _, d := range []string{"2026-03-31", "2026-05-31", "2026-01-31", "2026-03-01"} {
		at, _ := time.Parse("2006-01-02", d)
		if prev := prevMonthOf(at); prev.Month() == at.Month() {
			t.Errorf("%s: previous month resolved to the same month", d)
		}
	}
}

// A second click must not start a second download onto the same temp file.
func TestASecondUpdateIsRefusedWhileOneRuns(t *testing.T) {
	u := newTestUpdater(t)
	u.st.SetSetting(setGeoSource, "custom")
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
	if u.State().HasKey {
		t.Error("HasKey is true with nothing configured")
	}
	// The state says whether a source exists and never what it is.
	u.st.SetSetting(setGeoSource, "maxmind")
	u.st.SetSetting(setGeoToken, "SECRET")
	blob := u.State()
	if !blob.HasKey {
		t.Error("HasKey is false with a key configured")
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
