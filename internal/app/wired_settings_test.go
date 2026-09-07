package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Settings that were read and never written.
//
// Each of these had a reader, a default and a comment describing the choice an operator could make —
// and nothing anywhere able to make it. A setting in that state is not configuration, it is a
// constant with a misleading name, and the only way to notice is to go looking for the writer.

func wiredFixture(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	return &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		names: LoadNames(t.TempDir(), st)}
}

// The authenticator entry an enrolment lands under. It asked for `site_name`, a key nothing in the
// repository has ever written, so the branding an operator set was silently ignored.
func TestTheAuthenticatorLabelFollowsTheSiteTitle(t *testing.T) {
	s := wiredFixture(t)
	if got := s.totpIssuer(); got != "Report Portal" {
		t.Fatalf("unbranded portal issued %q", got)
	}
	// site_title is the key the branding form actually writes — the same one the shell, the PWA
	// manifest and the email sender read.
	s.st.SetSetting("site_title", "华东研究门户")
	if got := s.totpIssuer(); got != "华东研究门户" {
		t.Fatalf("issuer = %q, want the configured site title", got)
	}
}

func TestTheConcurrencyCeilingCanBeSetAndIsWhatClamps(t *testing.T) {
	s := wiredFixture(t)
	// The shipped ceiling, and the run form's own maximum before this was writable.
	if got := s.batchMaxConcurrency(); got != 10 {
		t.Fatalf("default ceiling = %d, want 10", got)
	}
	if got := s.clampConcurrency(20); got != 10 {
		t.Fatalf("a request for 20 clamped to %d — the console was offering twice this", got)
	}

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiBatchConfigSave(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), "admin")
		return rec
	}
	if rec := post(`{"max_concurrency":25}`); rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.batchMaxConcurrency(); got != 25 {
		t.Fatalf("ceiling = %d after saving 25", got)
	}
	if got := s.clampConcurrency(20); got != 20 {
		t.Fatalf("a request for 20 still clamped to %d", got)
	}
	// The console reads back what it will actually get, so the picker cannot offer more.
	rec := httptest.NewRecorder()
	s.apiBatchConfigGet(rec, httptest.NewRequest(http.MethodGet, "/x", nil), "admin")
	var cfg map[string]any
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg["max_concurrency"] != float64(25) {
		t.Fatalf("config reports max_concurrency=%v", cfg["max_concurrency"])
	}
	// Out of range is refused rather than clamped, and leaves the working value alone.
	if rec := post(`{"max_concurrency":100000}`); rec.Code != http.StatusOK {
		t.Fatalf("an out-of-range save should be ignored, not error: %d", rec.Code)
	}
	if got := s.batchMaxConcurrency(); got != 25 {
		t.Fatalf("out-of-range save changed the ceiling to %d", got)
	}
}

// The SSRF escape hatch safefetch's own comment calls "a setting". It had no writer, so a portal
// whose IdP is genuinely on the intranet could not be configured at all.
func TestTheSSOPrivateAddressSwitchIsReachableAndFailsClosed(t *testing.T) {
	s := wiredFixture(t)
	if s.ssoAllowPrivate() {
		t.Fatal("private addresses are allowed on a fresh portal")
	}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiAdminSSOAllowPrivate(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), "admin")
		return rec
	}
	if rec := post(`{"allow":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !s.ssoAllowPrivate() {
		t.Fatal("the switch did not take")
	}
	if rec := post(`{"allow":false}`); rec.Code != http.StatusOK || s.ssoAllowPrivate() {
		t.Fatalf("could not turn it back off: status=%d", rec.Code)
	}
	// Anything but an explicit "1" is off, so a hand-edited or half-written value fails closed
	// rather than opening the guard.
	for _, v := range []string{"", "true", "yes", "0", "2"} {
		s.st.SetSetting(setSSOAllowPrivate, v)
		if s.ssoAllowPrivate() {
			t.Fatalf("a stored %q opened the guard", v)
		}
	}
}

// The market index the constant has promised was repointable since the market shipped.
func TestTheAppMarketIndexCanBeRepointedAndReset(t *testing.T) {
	s := wiredFixture(t)
	if got := s.appMarketIndexURL(); got != defaultAppMarketIndexURL {
		t.Fatalf("default index = %q", got)
	}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiAppMarketIndexSave(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), "admin")
		return rec
	}
	if rec := post(`{"index_url":"https://apps.example.test/index.json"}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.appMarketIndexURL(); got != "https://apps.example.test/index.json" {
		t.Fatalf("index = %q", got)
	}
	// Empty returns to the built-in index rather than storing a blank, so undo is expressible
	// without knowing the default URL by heart.
	if rec := post(`{"index_url":"  "}`); rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d", rec.Code)
	}
	if got := s.appMarketIndexURL(); got != defaultAppMarketIndexURL {
		t.Fatalf("index after reset = %q", got)
	}
	// The saved URL goes through the same guard the fetch uses, so a stored value cannot be a probe
	// of the host's own network.
	rec := post(`{"index_url":"http://127.0.0.1:9/index.json"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "app_market_url_refused") {
		t.Fatalf("a private index URL was accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.appMarketIndexURL(); got != defaultAppMarketIndexURL {
		t.Fatalf("a refused save still changed the index to %q", got)
	}
}

// Every jsonErrorCode the server can answer with has an err.<code> string in every bundle.
//
// The SPA renders the CODE through t() and falls back to the server's message, which is written
// before the browser's language is known and is therefore always Chinese. So a missing string does
// not break anything — it just hands an English or Traditional admin a sentence in Simplified
// Chinese, and nothing fails until somebody looks. Same shape as the audit-vocabulary check, and
// derived the same way: from the server side, which is the source of truth.
func TestEveryErrorCodeIsTranslatedInEveryLanguage(t *testing.T) {
	codes := map[string]string{} // code -> the file it is answered from
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	re := regexp.MustCompile(`jsonErrorCode\(\s*w\s*,\s*[^,]+,\s*"([a-z0-9_]+)"`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			codes[m[1]] = e.Name()
		}
	}
	// A scan that finds nothing would pass silently, which is the failure this whole test is about.
	if len(codes) < 40 {
		t.Fatalf("found only %d error codes — the scan is broken, not the locales", len(codes))
	}

	dir := filepath.Join("..", "..", "web", "src", "locales")
	bundles, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("locale bundles not present: %v", err)
	}
	var checked int
	for _, b := range bundles {
		if b.IsDir() || !strings.HasSuffix(b.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, b.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", b.Name(), err)
		}
		var strs map[string]string
		if err := json.Unmarshal(raw, &strs); err != nil {
			t.Fatalf("parse %s: %v", b.Name(), err)
		}
		checked++
		for code, from := range codes {
			if strings.TrimSpace(strs["err."+code]) == "" {
				t.Errorf("%s has no \"err.%s\" (answered from %s) — that refusal reaches the reader in the server's own language",
					b.Name(), code, from)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no locale bundles were checked")
	}
}
