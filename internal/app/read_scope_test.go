package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// readScopeFixture builds two restricted OUs (A, B) and, for symbol 600000: A's own report, an
// internal (NULL-owner) report today, an internal report yesterday, and B's report today; plus a
// B-only symbol and a B-only subtype. Returns the store, a Server, the restricted user in OU A, and
// the report ids. It is the shared setup for the P2 owner-scope read tests (ADR 0022 R1).
type scopeIDs struct{ own, intToday, intOld, otherOU, bOnlySym, bOnlyType int64 }

func readScopeFixture(t *testing.T) (*Store, *Server, string, int64, scopeIDs) {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}, names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" } // no network in tests
	root := st.EnsureDefaultGroup()

	ouA, _ := st.CreateUserGroup("ext-A", "", 0)
	st.SetGroupParent(ouA, root)
	st.SetGroupRestricted(ouA, true)
	ouB, _ := st.CreateUserGroup("ext-B", "", 0)
	st.SetGroupParent(ouB, root)
	st.SetGroupRestricted(ouB, true)

	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", ouA)
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"}) // internal (Default group)

	today := time.Now().Format("2006-01-02")
	yest := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	mk := func(sym, date, rtype, title string, owner int64) int64 {
		id, _, err := st.UpsertReport(Rep{Symbol: sym, Date: date, RType: rtype, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		if owner != 0 {
			if _, err := st.StampReportOwner(id, owner); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	ids := scopeIDs{
		own:       mk("600000", today, "val", "own", ouA),
		intToday:  mk("600000", today, "val", "int-today", 0),
		intOld:    mk("600000", yest, "val", "int-old", 0),
		otherOU:   mk("600000", today, "val", "other", ouB),
		bOnlySym:  mk("000001", today, "val", "b-only-symbol", ouB),
		bOnlyType: mk("600000", today, "secret-type", "b-only-type", ouB),
	}
	return st, s, "ext", ouA, ids
}

func TestViewerScopeGating(t *testing.T) {
	st, s, _, ouA, _ := readScopeFixture(t)
	// Restricted external user → non-nil scope pinned to their OU.
	sc := s.viewerScope("ext")
	if sc == nil || sc.myOU != ouA {
		t.Fatalf("restricted viewerScope = %+v, want myOU=%d", sc, ouA)
	}
	// Internal user, admin, and anonymous → nil (no predicate, unchanged behavior).
	if s.viewerScope("staff") != nil {
		t.Error("internal user must get a nil scope")
	}
	st.UpsertUser(User{Username: "boss", PasswordHash: "h", Role: "admin"})
	st.SetPrimaryGroup("boss", ouA) // admin inside a restricted OU is still exempt
	if s.viewerScope("boss") != nil {
		t.Error("admin must be exempt (nil scope) even inside a restricted OU")
	}
	if s.viewerScope("") != nil {
		t.Error("anonymous must get a nil scope")
	}
}

func TestNewBySymbolScoped(t *testing.T) {
	st, s, _, _, ids := readScopeFixture(t)
	sc := s.viewerScope("ext")

	got := map[int64]bool{}
	rows, err := st.NewBySymbol("600000", sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		got[r.ID] = true
	}
	// Visible: own OU + today's internal. Hidden: yesterday's internal, other OU, other OU's subtype.
	if !got[ids.own] || !got[ids.intToday] {
		t.Errorf("restricted view missing own/internal-today: %v", got)
	}
	if got[ids.intOld] || got[ids.otherOU] || got[ids.bOnlyType] {
		t.Errorf("restricted view leaked hidden rows: %v", got)
	}
	// Unscoped (internal) sees everything on that symbol (own, int-today, int-old, other, b-type = 5).
	all, _ := st.NewBySymbol("600000", nil)
	if len(all) != 5 {
		t.Errorf("unscoped NewBySymbol = %d rows, want 5", len(all))
	}
}

func TestGetNewScopedFailsClosed(t *testing.T) {
	_, s, _, _, ids := readScopeFixture(t)
	st := s.st
	sc := s.viewerScope("ext")

	if r, _ := st.GetNew(ids.otherOU, sc); r != nil {
		t.Error("another OU's report must be invisible (id-enumeration fail-closed)")
	}
	if r, _ := st.GetNew(ids.intOld, sc); r != nil {
		t.Error("yesterday's internal report must be invisible to a restricted viewer")
	}
	if r, _ := st.GetNew(ids.own, sc); r == nil {
		t.Error("own-OU report must be visible")
	}
	if r, _ := st.GetNew(ids.intToday, sc); r == nil {
		t.Error("today's internal report must be visible (same-day pool)")
	}
	// The Server chokepoint loadRep(user,id) enforces the same for every by-id read path.
	if s.loadRep("ext", ids.otherOU) != nil {
		t.Error("loadRep must fail-closed for a restricted viewer on another OU's id")
	}
	if s.loadRep("staff", ids.otherOU) == nil {
		t.Error("loadRep must return the report for an internal viewer")
	}
}

func TestListSymbolsScoped(t *testing.T) {
	_, s, _, _, ids := readScopeFixture(t)
	st := s.st
	sc := s.viewerScope("ext")
	_ = ids

	find := func(list []SymbolInfo, sym string) *SymbolInfo {
		for i := range list {
			if list[i].Symbol == sym {
				return &list[i]
			}
		}
		return nil
	}
	scoped := st.ListSymbols("", 0, sc)
	if si := find(scoped, "600000"); si == nil || si.Count != 2 {
		t.Errorf("scoped 600000 = %+v, want count 2 (own + internal-today)", si)
	}
	if find(scoped, "000001") != nil {
		t.Error("a symbol owned only by another OU must not appear in the omnibox")
	}
	// Unscoped sees both symbols.
	if find(st.ListSymbols("", 0, nil), "000001") == nil {
		t.Error("unscoped ListSymbols must include 000001")
	}
}

func TestNewTypesKindsScoped(t *testing.T) {
	_, s, _, _, _ := readScopeFixture(t)
	st := s.st
	sc := s.viewerScope("ext")
	has := func(xs []string, v string) bool {
		for _, x := range xs {
			if x == v {
				return true
			}
		}
		return false
	}
	if !has(st.NewTypes(sc), "val") {
		t.Error("scoped NewTypes must include the viewer's own subtype")
	}
	if has(st.NewTypes(sc), "secret-type") {
		t.Error("scoped NewTypes must not reveal another OU's subtype")
	}
	if !has(st.NewTypes(nil), "secret-type") {
		t.Error("unscoped NewTypes must include every subtype")
	}
}

// TestV1ReadSurfaceScopedForRestrictedSession is the regression test for the adversarial-review
// finding: canQuery admits a browser cookie session, so the /api/v1 read surface must be owner-scoped
// for a restricted viewer's own session — while a machine Bearer(query) token stays unscoped. Without
// the fix a restricted user reads any OU's report by id enumeration or bulk query via /api/v1.
func TestV1ReadSurfaceScopedForRestrictedSession(t *testing.T) {
	st, s, _, _, ids := readScopeFixture(t)
	st.CreateToken("machine-tok", "test", "query", "") // machine query token → unscoped
	cookie := s.sign("ext")                            // restricted viewer's browser session

	// v1GetReport {id}: restricted session fails closed on another OU's id, sees its own.
	getReport := func(id int64, cookieVal, bearer string) int {
		req := httptest.NewRequest("GET", "/api/v1/reports/"+strconv.FormatInt(id, 10), nil)
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		if cookieVal != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieVal})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		s.v1GetReport(rec, req)
		return rec.Code
	}
	if code := getReport(ids.otherOU, cookie, ""); code != http.StatusNotFound {
		t.Errorf("restricted session reading another OU's report by id → %d, want 404 (the leak)", code)
	}
	if code := getReport(ids.own, cookie, ""); code != http.StatusOK {
		t.Errorf("restricted session reading its OWN report → %d, want 200", code)
	}
	if code := getReport(ids.otherOU, "", "machine-tok"); code != http.StatusOK {
		t.Errorf("machine Bearer token must stay unscoped (200), got %d", code)
	}

	// v1QueryReports: restricted session gets only its visible rows; machine token gets all.
	queryCount := func(cookieVal, bearer string) int {
		req := httptest.NewRequest("GET", "/api/v1/reports?symbol=600000&with_body=1", nil)
		if cookieVal != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieVal})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		s.v1QueryReports(rec, req)
		var resp struct {
			Items []map[string]any `json:"items"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return len(resp.Items)
	}
	if n := queryCount(cookie, ""); n != 2 {
		t.Errorf("restricted v1 query for 600000 → %d items, want 2 (own + internal-today)", n)
	}
	if n := queryCount("", "machine-tok"); n != 5 {
		t.Errorf("machine v1 query for 600000 → %d items, want 5 (unscoped)", n)
	}
	_ = ids
}
