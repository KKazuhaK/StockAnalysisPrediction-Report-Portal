package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// writeTestConfig writes a throwaway config pointing at a sqlite database inside the test's temp
// dir, so CLI entry points (which take a config path) never touch the repository's data/.
func writeTestConfig(t *testing.T, dbPath string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("listen: \":8799\"\nsecret_key: %q\ndb_driver: \"sqlite\"\ndb_path: %q\n",
		strings.Repeat("a", 64), dbPath)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// randPassword must respect its length and never emit easily confused characters.
func TestRandPasswordAlphabet(t *testing.T) {
	const confusing = "0O1lI"
	for _, n := range []int{8, 16, 32} {
		pw := randPassword(n)
		if len(pw) != n {
			t.Fatalf("randPassword(%d) length = %d", n, len(pw))
		}
		for _, c := range pw {
			if strings.ContainsRune(confusing, c) {
				t.Fatalf("randPassword produced %q (confusing character) in %q", c, pw)
			}
		}
	}
}

// handleVersion answers the signed-in app's build identity from the ldflags-injected package vars.
func TestHandleVersionReportsBuildIdentity(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Server{}
	s.handleVersion(rec, httptest.NewRequest("GET", "/api/version", nil), "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("handleVersion → %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] == "" || out["commit"] == "" || out["buildDate"] == "" {
		t.Fatalf("version payload = %v", out)
	}
}

// requireUser redirects an anonymous browser to /login and hands the resolved session to the
// wrapped handler otherwise.
func TestRequireUserRedirectsAndPassesIdentity(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	var seen string
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request, user string) {
		seen = user
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous → %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	if seen != "" {
		t.Fatalf("handler ran for an anonymous request as %q", seen)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign("alice")})
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || seen != "alice" {
		t.Fatalf("signed in → %d as %q", rec.Code, seen)
	}
}

// apiSymbols serves the omnibox autocomplete to a query token or a browser session, and refuses
// anonymous callers.
func TestApiSymbolsAuthAndPayload(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "test-secret"}, names: LoadNames(t.TempDir(), st)}
	if err := st.CreateToken("query-token", "autocomplete", "query", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertReport(Rep{Title: "r1", Symbol: "600519", Name: "Moutai", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertReport(Rep{Title: "r2", Symbol: "600519", Name: "Moutai", RType: "事件监测", Kind: "重组决策", Date: "2026-07-02"}); err != nil {
		t.Fatal(err)
	}

	// Anonymous → 401.
	rec := httptest.NewRecorder()
	s.apiSymbols(rec, httptest.NewRequest("GET", "/api/symbols?q=6005", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous apiSymbols → %d, want 401", rec.Code)
	}

	req := httptest.NewRequest("GET", "/api/symbols?q=6005&limit=5", nil)
	req.Header.Set("Authorization", "Bearer query-token")
	rec = httptest.NewRecorder()
	s.apiSymbols(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apiSymbols → %d", rec.Code)
	}
	var out struct {
		Count   int `json:"count"`
		Symbols []struct {
			Symbol string `json:"symbol"`
			Name   string `json:"name"`
			Count  int    `json:"count"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || len(out.Symbols) != 1 || out.Symbols[0].Symbol != "600519" || out.Symbols[0].Count != 2 {
		t.Fatalf("symbols payload = %+v", out)
	}

	// A query that matches nothing still answers, with an empty list.
	req = httptest.NewRequest("GET", "/api/symbols?q=zzzz", nil)
	req.Header.Set("Authorization", "Bearer query-token")
	rec = httptest.NewRecorder()
	s.apiSymbols(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("no-match payload = %d %s", rec.Code, rec.Body.String())
	}
}

// AddUser is the lockout fallback: it must enforce the password policy up front and otherwise
// create/overwrite the account in the database the config points at.
func TestAddUserCLI(t *testing.T) {
	cfgPath := writeTestConfig(t, filepath.Join(t.TempDir(), "portal.db"))

	// A weak password fails before any config/database is created.
	weakPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddUser(weakPath, "admin", "short", true); err == nil {
		t.Fatal("AddUser accepted a short password")
	}
	if _, err := os.Stat(weakPath); !os.IsNotExist(err) {
		t.Fatalf("weak password still touched the config: %v", err)
	}

	if err := AddUser(cfgPath, "Admin", "correct-horse-battery", true); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u := st.GetUser("admin") // folded, not stored as the typed "Admin"
	if u == nil || u.Role != "admin" {
		t.Fatalf("created user = %+v", u)
	}
	// Re-running as a plain user updates the role rather than creating a second account.
	if err := AddUser(cfgPath, "admin", "correct-horse-battery", false); err != nil {
		t.Fatal(err)
	}
	if u = st.GetUser("admin"); u == nil || u.Role != "user" {
		t.Fatalf("updated user = %+v", u)
	}
	if n := st.CountUsers(); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

// RecomputeKinds re-derives stored report kinds from the current taxonomy rules.
func TestRecomputeKindsCLI(t *testing.T) {
	cfgPath := writeTestConfig(t, filepath.Join(t.TempDir(), "portal.db"))
	if err := AddUser(cfgPath, "admin", "correct-horse-battery", true); err != nil {
		t.Fatal(err)
	}
	c, _ := config.EnsureConfig(cfgPath)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertReport(Rep{Title: "r", Symbol: "600519", RType: "事件监测", Kind: "未分类", Date: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n, err := RecomputeKinds(cfgPath)
	if err != nil {
		t.Fatalf("RecomputeKinds: %v", err)
	}
	if n != 1 {
		t.Fatalf("RecomputeKinds updated %d rows, want 1", n)
	}
	st, err = OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var kind string
	st.queryRow("SELECT kind FROM reports WHERE rtype='事件监测'").Scan(&kind)
	if kind != "重组决策" {
		t.Fatalf("kind after recompute = %q, want 重组决策", kind)
	}
}

// FreezeReportNames snapshots current stock names onto un-named reports exactly once.
func TestFreezeReportNamesCLI(t *testing.T) {
	cfgPath := writeTestConfig(t, filepath.Join(t.TempDir(), "portal.db"))
	if err := AddUser(cfgPath, "admin", "correct-horse-battery", true); err != nil {
		t.Fatal(err)
	}
	c, _ := config.EnsureConfig(cfgPath)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec("INSERT INTO stocks(code,name,updated_at) VALUES(?,?,?)", "600519", "Frozen Co", nowStr()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertReport(Rep{Title: "r", Symbol: "600519", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n, err := FreezeReportNames(cfgPath)
	if err != nil {
		t.Fatalf("FreezeReportNames: %v", err)
	}
	if n != 1 {
		t.Fatalf("frozen %d rows, want 1", n)
	}
	if n, err = FreezeReportNames(cfgPath); err != nil || n != 0 {
		t.Fatalf("second freeze = %d, %v; want 0 (idempotent)", n, err)
	}
	st, err = OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var name string
	st.queryRow("SELECT name FROM reports WHERE rtype='投资决策'").Scan(&name)
	if name != "Frozen Co" {
		t.Fatalf("report name = %q, want the frozen snapshot", name)
	}
}
