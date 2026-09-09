package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func seedFavoriteUser(t *testing.T, st *Store, name string) {
	t.Helper()
	if err := st.UpsertUser(User{Username: name, PasswordHash: "h", Role: "user", Active: true}); err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
}

func TestStockFavoritesAreOrderedIdempotentAndUserScoped(t *testing.T) {
	st := newTestStore(t)
	seedFavoriteUser(t, st, "alice")
	seedFavoriteUser(t, st, "bob")

	first, created, err := st.AddStockFavorite("alice", "sh", "600519")
	if err != nil || !created || first.Ord != 0 {
		t.Fatalf("first add = %+v created=%v err=%v", first, created, err)
	}
	if again, created, err := st.AddStockFavorite("alice", "sh", "600519"); err != nil || created || again != first {
		t.Fatalf("idempotent add = %+v created=%v err=%v, want %+v/false", again, created, err, first)
	}
	if second, created, err := st.AddStockFavorite("alice", "us", "AAPL"); err != nil || !created || second.Ord != 1 {
		t.Fatalf("second add = %+v created=%v err=%v", second, created, err)
	}
	if _, _, err := st.AddStockFavorite("bob", "hk", "00700"); err != nil {
		t.Fatal(err)
	}

	got := st.StockFavorites("alice")
	if len(got) != 2 || got[0].Key() != "sh600519" || got[1].Key() != "usAAPL" {
		t.Fatalf("alice favorites = %+v", got)
	}
	if bob := st.StockFavorites("bob"); len(bob) != 1 || bob[0].Key() != "hk00700" {
		t.Fatalf("bob favorites = %+v", bob)
	}
	if err := st.RemoveStockFavorite("alice", "sh", "600519"); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveStockFavorite("alice", "sh", "600519"); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
	if got := st.StockFavorites("alice"); len(got) != 1 || got[0].Key() != "usAAPL" {
		t.Fatalf("after remove = %+v", got)
	}
}

func TestStockFavoriteLimitAndAtomicReorder(t *testing.T) {
	st := newTestStore(t)
	seedFavoriteUser(t, st, "alice")
	for i := 0; i < stockFavoriteLimit; i++ {
		if _, err := st.exec(`INSERT INTO user_stock_favorites(username,market,symbol,ord,created_at)
			VALUES(?,?,?,?,?)`, "alice", "us", "S"+itoa(int64(i)), i, "2026-09-08T12:00:00Z"); err != nil {
			t.Fatalf("seed favorite %d: %v", i, err)
		}
	}
	if _, _, err := st.AddStockFavorite("alice", "us", "OVER"); !errors.Is(err, errStockFavoriteLimit) {
		t.Fatalf("over-limit add err = %v", err)
	}

	if _, err := st.exec("DELETE FROM user_stock_favorites WHERE username=?", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []StockFavorite{{Market: "sh", Symbol: "600519"}, {Market: "hk", Symbol: "00700"}, {Market: "us", Symbol: "AAPL"}} {
		if _, _, err := st.AddStockFavorite("alice", item.Market, item.Symbol); err != nil {
			t.Fatal(err)
		}
	}
	want := []FavoriteIdentity{{Market: "us", Symbol: "AAPL"}, {Market: "sh", Symbol: "600519"}, {Market: "hk", Symbol: "00700"}}
	if err := st.ReorderStockFavorites("alice", want); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got := st.StockFavorites("alice")
	for i := range want {
		if got[i].Market != want[i].Market || got[i].Symbol != want[i].Symbol || got[i].Ord != i {
			t.Fatalf("reordered favorites = %+v", got)
		}
	}
	stale := want[:2]
	if err := st.ReorderStockFavorites("alice", stale); !errors.Is(err, errStockFavoriteSetChanged) {
		t.Fatalf("stale reorder err = %v", err)
	}
	if after := st.StockFavorites("alice"); len(after) != 3 || after[0].Key() != "usAAPL" {
		t.Fatalf("failed reorder changed rows: %+v", after)
	}
}

func TestDeletingAUserSweepsStockFavorites(t *testing.T) {
	st := newTestStore(t)
	seedFavoriteUser(t, st, "alice")
	if _, _, err := st.AddStockFavorite("alice", "sh", "600519"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	seedFavoriteUser(t, st, "alice")
	if got := st.StockFavorites("alice"); len(got) != 0 {
		t.Fatalf("reused username inherited favorites: %+v", got)
	}
}

func TestLatestReportsForFavoritesHonorsReportScopeAndSkipsInternalRows(t *testing.T) {
	st := newTestStore(t)
	st.SaveVersion(ReportVersion{Name: "private", Ord: 0, Visibility: VisibilityAll})
	st.SaveVersion(ReportVersion{Name: "public", Ord: 1, Visibility: VisibilityAll})
	_, _, _ = st.UpsertReport(Rep{Symbol: "600519", Date: "2026-09-08", RType: "公司基础快照", Title: "cache", Version: "public"})
	_, _, _ = st.UpsertReport(Rep{Symbol: "600519", Date: "2026-09-07", RType: "Research", Title: "hidden", Version: "private"})
	visibleID, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-09-06", RType: "Research", Title: "visible", Version: "public", Name: "Moutai"})

	sc := &ownerScope{versAll: []string{"public"}}
	got, err := st.LatestReportsForFavoriteSymbols([]string{"600519"}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got["600519"]; !ok || r.ID != visibleID || r.Title != "visible" {
		t.Fatalf("latest scoped report = %+v", got)
	}
}

func favoriteServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	seedFavoriteUser(t, st, "alice")
	seedFavoriteUser(t, st, "bob")
	return &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
}

func favoriteMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/favorites", s.requireUserJSON(s.apiFavorites))
	mux.HandleFunc("PUT /api/favorites/order", s.requireUserJSON(s.apiFavoriteReorder))
	mux.HandleFunc("PUT /api/favorites/{market}/{symbol}", s.requireUserJSON(s.apiFavoriteAdd))
	mux.HandleFunc("DELETE /api/favorites/{market}/{symbol}", s.requireUserJSON(s.apiFavoriteDelete))
	return mux
}

func favoriteRequest(t *testing.T, s *Server, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign(user)})
	}
	rec := httptest.NewRecorder()
	favoriteMux(s).ServeHTTP(rec, req)
	return rec
}

func favoriteBody(t *testing.T, rec *httptest.ResponseRecorder) favoriteListResponse {
	t.Helper()
	var out favoriteListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode favorites: %v (body %s)", err, rec.Body.String())
	}
	return out
}

func TestFavoriteAPIUsesTheSessionOwnerAndCanonicalIdentity(t *testing.T) {
	s := favoriteServer(t)
	added := favoriteRequest(t, s, "alice", http.MethodPut, "/api/favorites/us/aapl", "")
	if added.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body.String())
	}
	listed := favoriteBody(t, favoriteRequest(t, s, "alice", http.MethodGet, "/api/favorites", ""))
	if len(listed.Items) != 1 || listed.Items[0].Market != "us" || listed.Items[0].Symbol != "AAPL" || listed.Items[0].Key != "usAAPL" {
		t.Fatalf("alice list = %+v", listed)
	}
	if bob := favoriteBody(t, favoriteRequest(t, s, "bob", http.MethodGet, "/api/favorites", "")); len(bob.Items) != 0 {
		t.Fatalf("bob saw alice favorites: %+v", bob)
	}

	bad := favoriteRequest(t, s, "alice", http.MethodPut, "/api/favorites/moon/ABC", "")
	if bad.Code != http.StatusBadRequest || quoteErrCode(t, bad) != "favorite_bad_symbol" {
		t.Fatalf("bad symbol status/code=%d/%q body=%s", bad.Code, quoteErrCode(t, bad), bad.Body.String())
	}
	unauth := favoriteRequest(t, s, "", http.MethodGet, "/api/favorites", "")
	if unauth.Code != http.StatusUnauthorized || quoteErrCode(t, unauth) != "session_expired" {
		t.Fatalf("unauth status/code=%d/%q", unauth.Code, quoteErrCode(t, unauth))
	}
}

func TestFavoriteAPIAddReturnsTheVisibleReportSummary(t *testing.T) {
	s := favoriteServer(t)
	if _, _, err := s.st.UpsertReport(Rep{Symbol: "600519", Name: "Moutai", Date: "2026-09-08",
		RType: "Research", Title: "Decision", MD: "body"}); err != nil {
		t.Fatal(err)
	}
	rec := favoriteRequest(t, s, "alice", http.MethodPut, "/api/favorites/sh/600519", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", rec.Code, rec.Body.String())
	}
	var item favoriteItemView
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Key != "sh600519" || item.Report == nil || item.Report.Title != "Moutai Decision" {
		t.Fatalf("add response = %+v report=%+v", item, item.Report)
	}
}

func TestFavoriteAPIReorderAndDeleteAreSafe(t *testing.T) {
	s := favoriteServer(t)
	for _, path := range []string{"/api/favorites/sh/600519", "/api/favorites/hk/00700"} {
		if rec := favoriteRequest(t, s, "alice", http.MethodPut, path, ""); rec.Code != http.StatusOK {
			t.Fatalf("add %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	body := `{"items":[{"market":"hk","symbol":"00700"},{"market":"sh","symbol":"600519"}]}`
	if rec := favoriteRequest(t, s, "alice", http.MethodPut, "/api/favorites/order", body); rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body.String())
	}
	if got := favoriteBody(t, favoriteRequest(t, s, "alice", http.MethodGet, "/api/favorites", "")); got.Items[0].Key != "hk00700" {
		t.Fatalf("order = %+v", got.Items)
	}
	stale := `{"items":[{"market":"hk","symbol":"00700"}]}`
	rec := favoriteRequest(t, s, "alice", http.MethodPut, "/api/favorites/order", stale)
	if rec.Code != http.StatusConflict || quoteErrCode(t, rec) != "favorite_set_changed" {
		t.Fatalf("stale reorder status/code=%d/%q body=%s", rec.Code, quoteErrCode(t, rec), rec.Body.String())
	}
	for i := 0; i < 2; i++ {
		if rec := favoriteRequest(t, s, "alice", http.MethodDelete, "/api/favorites/hk/00700", ""); rec.Code != http.StatusOK {
			t.Fatalf("delete %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
}
