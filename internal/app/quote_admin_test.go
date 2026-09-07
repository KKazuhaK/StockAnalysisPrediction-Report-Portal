package app

// quote_admin_test.go —— the 管理 → 行情 panel.
//
// Two things here are deliberately not tested by reading a setting back. "The order disables a
// source" and "the clear button empties the cache" are both claims about what the FETCH PATH does,
// and a save that stored the value while the fetch ignored it would satisfy every read-back
// assertion ever written. Both are asserted with a counting stub instead: the number of upstream
// calls is the only witness that cannot agree with the code by construction.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------- harness ----------

// quoteAdminServer is quoteServer plus an administrator, so every test here has both a principal the
// panel is for and one it is not.
func quoteAdminServer(t *testing.T) *Server {
	t.Helper()
	s := quoteServer(t)
	s.st.UpsertUser(User{Username: "root", PasswordHash: "h", Role: "admin"})
	return s
}

// quoteAdminMux registers the three routes exactly as server.go does, so these tests go through
// requireAdminJSON rather than calling the handlers directly — the gate is half of what is under
// test.
func quoteAdminMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/quote", s.requireAdminJSON(s.apiAdminQuotes))
	mux.HandleFunc("POST /api/admin/quote", s.requireAdminJSON(s.apiAdminQuotesSave))
	mux.HandleFunc("POST /api/admin/quote/cache/clear", s.requireAdminJSON(s.apiAdminQuotesCacheClear))
	return mux
}

// quoteAdminDo makes one request as `user`, or anonymously when user is "".
func quoteAdminDo(t *testing.T, s *Server, method, path, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign(user)})
	}
	rec := httptest.NewRecorder()
	quoteAdminMux(s).ServeHTTP(rec, r)
	return rec
}

// quoteAdminView is the panel payload. Decoded into a struct rather than a map so that a key this
// change renames is a compile-time-shaped failure here rather than a silently absent assertion.
type quoteAdminView struct {
	Sources        []quoteSourceStatus `json:"sources"`
	CacheEntries   int                 `json:"cacheEntries"`
	CacheBytes     int                 `json:"cacheBytes"`
	TTLOpen        int                 `json:"ttlOpenSecs"`
	TTLClosed      int                 `json:"ttlClosedSecs"`
	TTLOpenFloor   int                 `json:"ttlOpenFloor"`
	TTLClosedFloor int                 `json:"ttlClosedFloor"`
}

func quoteAdminGet(t *testing.T, s *Server) quoteAdminView {
	t.Helper()
	rec := quoteAdminDo(t, s, http.MethodGet, "/api/admin/quote", "root", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/quote → %d (%s)", rec.Code, rec.Body.String())
	}
	var out quoteAdminView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode panel: %v (body %s)", err, rec.Body.String())
	}
	return out
}

func quoteAdminSave(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	return quoteAdminDo(t, s, http.MethodPost, "/api/admin/quote", "root", body)
}

// quoteAdminRow finds one source's row, failing rather than returning a zero value — an absent row
// would otherwise make every field assertion below pass against zeros.
func quoteAdminRow(t *testing.T, view quoteAdminView, name string) quoteSourceStatus {
	t.Helper()
	for _, row := range view.Sources {
		if row.Source == name {
			return row
		}
	}
	t.Fatalf("no %q row in the panel: %+v", name, view.Sources)
	return quoteSourceStatus{}
}

// ---------- the gate ----------

// Every one of the three is admin-only, and the two that WRITE have to refuse a reader without
// writing: a 403 that still saved would be the worst of both answers.
func TestQuoteAdminRoutesAreAdminOnly(t *testing.T) {
	s := quoteAdminServer(t)
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSH)), sinaStub(t))

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/quote", ""},
		{http.MethodPost, "/api/admin/quote", `{"order":"sina","ttlOpenSecs":111}`},
		{http.MethodPost, "/api/admin/quote/cache/clear", ""},
	}
	for _, rt := range routes {
		if rec := quoteAdminDo(t, s, rt.method, rt.path, "", rt.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s → %d, want 401", rt.method, rt.path, rec.Code)
		}
		// kazuha is a signed-in reader: allowed to look at a quote, not to decide where quotes come
		// from. This is the case a session-only gate would let through.
		if rec := quoteAdminDo(t, s, rt.method, rt.path, "kazuha", rt.body); rec.Code != http.StatusForbidden {
			t.Errorf("a non-admin %s %s → %d, want 403", rt.method, rt.path, rec.Code)
		}
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLOpen || strings.Join(cfg.Order, ",") != "tencent,sina" {
		t.Fatalf("a refused save still changed the configuration: %+v", cfg)
	}

	for _, rt := range routes {
		if rec := quoteAdminDo(t, s, rt.method, rt.path, "root", rt.body); rec.Code != http.StatusOK {
			t.Errorf("admin %s %s → %d (%s)", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}

	// The mux above is this test's own, so everything so far proves only that the gate works when it
	// is there. What it cannot see is server.go registering one of these behind requireUserJSON by
	// accident — which is the mistake that would actually ship — so the registration itself is read
	// out of the file, the way openapi_test.go reads its route table.
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	for _, rt := range routes {
		want := `mux.HandleFunc("` + rt.method + " " + rt.path + `", s.requireAdminJSON(`
		if !strings.Contains(string(src), want) {
			t.Errorf("server.go does not register %s %s behind requireAdminJSON", rt.method, rt.path)
		}
	}
}

// ---------- the defaults ----------

// Nothing changes until an admin changes something. The numbers below are written out as literals
// rather than derived from the constants they mirror: an assertion of quoteTTLOpen against
// int(quoteTTLOpen/time.Second) is true of any value the constant could ever hold, and the promise
// this test exists to keep is that an unconfigured portal behaves exactly as ADR 0028 shipped it.
func TestQuoteAdminDefaultsReproduceTodaysBehaviour(t *testing.T) {
	s := quoteAdminServer(t)

	cfg := s.quoteConfigLoad()
	if got := strings.Join(cfg.Order, ","); got != "tencent,sina" {
		t.Errorf("default order = %q, want tencent,sina", got)
	}
	if cfg.TTLOpen != 30*time.Second || cfg.TTLClosed != 5*time.Minute {
		t.Errorf("default TTLs = %v / %v, want 30s / 5m", cfg.TTLOpen, cfg.TTLClosed)
	}
	// And those are the constants the cache has always used, so "the default" and "what the code did
	// before it was configurable" are the same object rather than two numbers that agree today.
	if cfg.TTLOpen != quoteTTLOpen || cfg.TTLClosed != quoteTTLClosed {
		t.Errorf("the defaults have drifted from the cache's own constants: %v/%v vs %v/%v",
			cfg.TTLOpen, cfg.TTLClosed, quoteTTLOpen, quoteTTLClosed)
	}

	view := quoteAdminGet(t, s)
	if view.TTLOpen != 30 || view.TTLClosed != 300 {
		t.Errorf("panel reports open=%d closed=%d", view.TTLOpen, view.TTLClosed)
	}
	if view.TTLOpenFloor != 5 || view.TTLClosedFloor != 30 {
		t.Errorf("panel reports floors %d / %d, want 5 / 30", view.TTLOpenFloor, view.TTLClosedFloor)
	}

	// The sources here are the COMPILED-IN ones — no stub is wired in this test — so the markets
	// column is the real predicate's answer.
	if len(view.Sources) != 2 {
		t.Fatalf("panel lists %d sources, want both compiled-in ones: %+v", len(view.Sources), view.Sources)
	}
	tencent := quoteAdminRow(t, view, quoteSourceTencent)
	if !tencent.Enabled || tencent.Position != 1 {
		t.Errorf("tencent row = %+v, want enabled at position 1", tencent)
	}
	if len(tencent.Markets) != len(quoteMarkets) {
		t.Errorf("tencent serves %v; the fqkline endpoint answers all %d markets", tencent.Markets, len(quoteMarkets))
	}
	sina := quoteAdminRow(t, view, quoteSourceSina)
	if !sina.Enabled || sina.Position != 2 {
		t.Errorf("sina row = %+v, want enabled at position 2", sina)
	}
	// The A-share three and not the two whose line is a different shape entirely — the same column
	// quoteSinaTarget refuses on, so the panel cannot advertise a market the fetcher then rejects.
	if got := strings.Join(sina.Markets, ","); got != "bj,sh,sz" {
		t.Errorf("sina serves %q, want bj,sh,sz", got)
	}
	// A source that has never been called reports no history rather than the year 1.
	if tencent.LastSuccess != "" || tencent.LastError != "" || tencent.LastErrorAt != "" || tencent.Failures != 0 {
		t.Errorf("a never-called source has a record: %+v", tencent)
	}
}

// ---------- the floors ----------

// Out of range is CLAMPED, not refused and not obeyed — and clamped on the way in AND on the way
// out, so a value that reached meta from an older build, a restore or a hand edit cannot take the
// portal below the floor either.
func TestQuoteAdminClampsTTLsOnSaveAndOnRead(t *testing.T) {
	s := quoteAdminServer(t)

	rec := quoteAdminSave(t, s, `{"ttlOpenSecs":1,"ttlClosedSecs":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a below-floor save → %d (%s); it should be clamped, not refused", rec.Code, rec.Body.String())
	}
	var saved quoteAdminView
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.TTLOpen != 5 || saved.TTLClosed != 30 {
		t.Errorf("the save answered %d / %d; the form must see the values it actually got", saved.TTLOpen, saved.TTLClosed)
	}
	// The save answers with the WHOLE panel, so the page re-renders from it rather than from a second
	// GET — a reply of "ok" would leave the typed 1 on screen under a green "saved".
	if len(saved.Sources) == 0 {
		t.Errorf("the save answered without the source rows: %s", rec.Body.String())
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLOpenFloor || cfg.TTLClosed != quoteTTLClosedFloor {
		t.Errorf("effective TTLs = %v / %v, want the floors %v / %v", cfg.TTLOpen, cfg.TTLClosed, quoteTTLOpenFloor, quoteTTLClosedFloor)
	}
	// The clamp happened at the SAVE too, so what sits in meta is the value the portal honours.
	if got := s.st.GetSetting(setQuoteTTLOpenSecs, ""); got != "5" {
		t.Errorf("meta holds %q; a stored below-floor value would depend on the read clamp alone", got)
	}

	// Above the floor is kept exactly — otherwise "clamped" could mean "always the floor", and this
	// whole setting would be a constant with a form in front of it.
	if rec := quoteAdminSave(t, s, `{"ttlOpenSecs":45,"ttlClosedSecs":900}`); rec.Code != http.StatusOK {
		t.Fatalf("an in-range save → %d (%s)", rec.Code, rec.Body.String())
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != 45*time.Second || cfg.TTLClosed != 15*time.Minute {
		t.Errorf("in-range TTLs came back as %v / %v, want 45s / 15m", cfg.TTLOpen, cfg.TTLClosed)
	}

	// A value that arrived some other way is clamped on READ.
	s.st.SetSetting(setQuoteTTLOpenSecs, "2")
	s.st.SetSetting(setQuoteTTLClosedSecs, "-99")
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLOpenFloor || cfg.TTLClosed != quoteTTLClosedFloor {
		t.Errorf("hand-written TTLs read back as %v / %v, want the floors", cfg.TTLOpen, cfg.TTLClosed)
	}
	// An UNREADABLE value is the shipped default rather than the floor: a blank or a typo is not a
	// request for the shortest TTL the portal allows, it is no request at all.
	s.st.SetSetting(setQuoteTTLOpenSecs, "soon")
	s.st.SetSetting(setQuoteTTLClosedSecs, "")
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLOpen || cfg.TTLClosed != quoteTTLClosed {
		t.Errorf("an unparseable TTL read back as %v / %v, want the shipped defaults", cfg.TTLOpen, cfg.TTLClosed)
	}
}

// The other end of the same discipline, and it was open in both places: the floor was enforced on
// save and on read, the ceiling nowhere. An admin typing 2000000000 into 收盘 TTL asked for a valid
// Duration — 2e18ns is under the int64 wrap, so no overflow guard fires — and pinned every symbol in
// the LRU for about sixty-three years, leaving the reading page showing a price that would never
// change again with only the 缓存 chip as a hint.
//
// Both sites are asserted separately below (meta for the save, quoteConfigLoad for the read), because
// a ceiling on one of them is exactly the half-fix the floor's own history warns about.
func TestQuoteAdminClampsTTLsToTheCeilingOnSaveAndOnRead(t *testing.T) {
	s := quoteAdminServer(t)
	// The literal, not quoteTTLSecs(quoteTTLCeiling): shortening the ceiling is a policy change about
	// what a reader can be shown, and it should have to come past this line rather than through it.
	const day = 86400

	// ---- the save side, with the number from the failure scenario ----
	rec := quoteAdminSave(t, s, `{"ttlOpenSecs":2000000000,"ttlClosedSecs":2000000000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("an above-ceiling save → %d (%s); it should be capped, not refused", rec.Code, rec.Body.String())
	}
	var saved quoteAdminView
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.TTLOpen != day || saved.TTLClosed != day {
		t.Errorf("the save answered %d / %d, want the ceiling %d for both; the form must see what it got",
			saved.TTLOpen, saved.TTLClosed, day)
	}
	// Capped in meta, not only in the reply. meta is what a backup carries and what the next operator
	// greps, and a row holding sixty-three years that the portal does not honour is a lie to both.
	if got := s.st.GetSetting(setQuoteTTLClosedSecs, ""); got != "86400" {
		t.Errorf("meta holds %q; an above-ceiling value stored raw would depend on the read clamp alone", got)
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLCeiling || cfg.TTLClosed != quoteTTLCeiling {
		t.Errorf("effective TTLs = %v / %v, want the ceiling %v", cfg.TTLOpen, cfg.TTLClosed, quoteTTLCeiling)
	}

	// A second under the ceiling is kept exactly, or "capped" could mean "always a day" and the
	// setting would be a constant with a form in front of it.
	if rec := quoteAdminSave(t, s, `{"ttlOpenSecs":86399,"ttlClosedSecs":3600}`); rec.Code != http.StatusOK {
		t.Fatalf("an in-range save → %d (%s)", rec.Code, rec.Body.String())
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != 86399*time.Second || cfg.TTLClosed != time.Hour {
		t.Errorf("in-range TTLs came back as %v / %v, want 86399s / 1h", cfg.TTLOpen, cfg.TTLClosed)
	}

	// ---- the read side, for a value that reached meta some other way ----
	s.st.SetSetting(setQuoteTTLOpenSecs, "2000000000")
	s.st.SetSetting(setQuoteTTLClosedSecs, "999999999")
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLCeiling || cfg.TTLClosed != quoteTTLCeiling {
		t.Errorf("hand-written TTLs read back as %v / %v, want the ceiling %v", cfg.TTLOpen, cfg.TTLClosed, quoteTTLCeiling)
	}
	s.st.SetSetting(setQuoteTTLOpenSecs, "600")
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != 10*time.Minute {
		t.Errorf("an in-range stored TTL read back as %v, want 10m", cfg.TTLOpen)
	}

	// ---- and the overflow, which the ceiling does NOT catch and must not be confused with ----
	// 1e10 seconds is 1e19ns, past int64, and the wrap lands NEGATIVE — so this one is the floor's
	// catch, exactly as quoteClampTTL's comment claims. Asserted at both sites too: a clamp that
	// compared seconds before multiplying would let a negative Duration through here and store an
	// entry that expires before it is written, turning every page view into a vendor call.
	if rec := quoteAdminSave(t, s, `{"ttlClosedSecs":10000000000}`); rec.Code != http.StatusOK {
		t.Fatalf("an overflowing save → %d (%s)", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setQuoteTTLClosedSecs, ""); got != "30" {
		t.Errorf("meta holds %q after an overflowing save, want the floor 30", got)
	}
	s.st.SetSetting(setQuoteTTLClosedSecs, "10000000000")
	if cfg := s.quoteConfigLoad(); cfg.TTLClosed != quoteTTLClosedFloor {
		t.Errorf("an overflowing stored TTL read back as %v, want the floor %v", cfg.TTLClosed, quoteTTLClosedFloor)
	}
}

// The TTL an admin saves is the one the CACHE uses, not merely the one the panel prints back. Both
// halves are asserted: the max-age the browser is told, and the moment the entry actually expires.
func TestQuoteAdminTTLReachesTheCache(t *testing.T) {
	s := quoteAdminServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))
	now := time.Now()
	s.quotes.now = func() time.Time { return now }

	if rec := quoteAdminSave(t, s, `{"ttlClosedSecs":40}`); rec.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", rec.Code, rec.Body.String())
	}
	// The captured fixture's market is closed, so this is the TTL the entry gets.
	first := quoteGET(t, s, "/api/quote/601899")
	if first.Code != http.StatusOK {
		t.Fatalf("quote → %d (%s)", first.Code, first.Body.String())
	}
	if got := quoteMaxAge(t, first); got != 40 {
		t.Errorf("Cache-Control max-age=%d, want the configured 40", got)
	}

	now = now.Add(35 * time.Second)
	if !quoteBody(t, quoteGET(t, s, "/api/quote/601899")).Cached {
		t.Error("the entry expired before its configured 40 seconds were up")
	}
	now = now.Add(10 * time.Second)
	if quoteBody(t, quoteGET(t, s, "/api/quote/601899")).Cached {
		t.Error("the entry outlived its configured TTL — the setting reached the header and not the cache")
	}
	if tencent.n() != 2 {
		t.Errorf("%d upstream calls across the TTL boundary, want 2", tencent.n())
	}
}

// ---------- the source order ----------

// A vendor this build has no parser for is refused outright. There is no sensible interpretation of
// it, and dropping the name quietly would leave an admin looking at an order they did not type.
func TestQuoteAdminRefusesAnUnknownSource(t *testing.T) {
	s := quoteAdminServer(t)
	if rec := quoteAdminSave(t, s, `{"order":"sina"}`); rec.Code != http.StatusOK {
		t.Fatalf("a real change → %d (%s)", rec.Code, rec.Body.String())
	}

	rec := quoteAdminSave(t, s, `{"order":"tencent,bloomberg","ttlOpenSecs":45}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown source → %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bloomberg") {
		t.Errorf("the refusal does not name the source that caused it: %s", rec.Body.String())
	}
	// Nothing was persisted — including the TTL that travelled in the same body. Validation runs
	// before any write for exactly this: half a save is a configuration nobody chose.
	if got := s.st.GetSetting(setQuoteSourceOrder, ""); got != "sina" {
		t.Errorf("meta holds %q; the refused order was stored anyway", got)
	}
	cfg := s.quoteConfigLoad()
	if strings.Join(cfg.Order, ",") != "sina" || cfg.TTLOpen != quoteTTLOpen {
		t.Errorf("a refused save changed the configuration: %+v", cfg)
	}

	// Spelling and repetition are normalised rather than refused: they are the same order.
	if rec := quoteAdminSave(t, s, `{"order":" SINA , sina ,Tencent "}`); rec.Code != http.StatusOK {
		t.Fatalf("a messily-typed order → %d (%s)", rec.Code, rec.Body.String())
	}
	if got := strings.Join(s.quoteConfigLoad().Order, ","); got != "sina,tencent" {
		t.Errorf("normalised order = %q, want sina,tencent", got)
	}

	// Blank is a RESET to the shipped order, not "every source off" — undo without knowing the
	// default by heart. Turning quotes off entirely stays what ADR 0028 §9 says it is: a real
	// switch, which this is not.
	if rec := quoteAdminSave(t, s, `{"order":"  "}`); rec.Code != http.StatusOK {
		t.Fatalf("a blank order → %d (%s)", rec.Code, rec.Body.String())
	}
	if got := strings.Join(s.quoteConfigLoad().Order, ","); got != "tencent,sina" {
		t.Errorf("a blank order left %q, want the shipped tencent,sina", got)
	}
}

// A source left out of the order is OFF, and the proof is a call count rather than a read-back: a
// save that stored the order while load ignored it would pass any number of assertions about the
// setting.
func TestQuoteAdminOrderDisablesTheSourceItLeavesOut(t *testing.T) {
	s := quoteAdminServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	sina := sinaStub(t)
	wireQuoteSources(s, tencent, sina)

	if rec := quoteAdminSave(t, s, `{"order":"sina"}`); rec.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", rec.Code, rec.Body.String())
	}
	rec := quoteGET(t, s, "/api/quote/601899?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("quote with only sina enabled → %d (%s)", rec.Code, rec.Body.String())
	}
	if src := quoteBody(t, rec).Source; src != quoteSourceSina {
		t.Errorf("source = %q, want the one source left enabled", src)
	}
	if tencent.n() != 0 {
		t.Errorf("a disabled primary was called %d time(s)", tencent.n())
	}
	if sina.n() != 1 {
		t.Errorf("the enabled source was called %d time(s), want 1", sina.n())
	}
	// The panel says the same thing the fetch path just did.
	if row := quoteAdminRow(t, quoteAdminGet(t, s), quoteSourceTencent); row.Enabled || row.Position != 0 {
		t.Errorf("the panel still reports the disabled source as %+v", row)
	}

	// Putting it back brings it back. A different range so this is a cache MISS — the same symbol
	// inside its TTL would be answered from the entry sina filled, and would prove nothing.
	if rec := quoteAdminSave(t, s, `{"order":"tencent,sina"}`); rec.Code != http.StatusOK {
		t.Fatalf("re-enable → %d (%s)", rec.Code, rec.Body.String())
	}
	rec = quoteGET(t, s, "/api/quote/601899?range=1m")
	if rec.Code != http.StatusOK {
		t.Fatalf("quote after re-enabling → %d (%s)", rec.Code, rec.Body.String())
	}
	if src := quoteBody(t, rec).Source; src != quoteSourceTencent {
		t.Errorf("source = %q; the re-enabled primary should answer first", src)
	}
	if tencent.n() != 1 {
		t.Errorf("the re-enabled primary was called %d time(s), want 1", tencent.n())
	}
}

// ---------- the cache ----------

func TestQuoteAdminCacheClearReallyEmptiesIt(t *testing.T) {
	s := quoteAdminServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	wireQuoteSources(s, tencent, sinaStub(t))

	if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusOK {
		t.Fatalf("quote → %d (%s)", rec.Code, rec.Body.String())
	}
	before := quoteAdminGet(t, s)
	if before.CacheEntries != 1 || before.CacheBytes <= 0 {
		t.Fatalf("occupancy after one quote = %d entries / %d bytes, want one entry with a size",
			before.CacheEntries, before.CacheBytes)
	}

	rec := quoteAdminDo(t, s, http.MethodPost, "/api/admin/quote/cache/clear", "root", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear → %d (%s)", rec.Code, rec.Body.String())
	}
	// The clear answers with the fresh occupancy, so the number beside the button is read back rather
	// than assumed — and a second GET confirms it was not just this one response.
	var cleared quoteAdminView
	json.Unmarshal(rec.Body.Bytes(), &cleared)
	// The rows first. Zero entries and zero bytes are also what an ANSWER WITHOUT THEM decodes to, so
	// asserting the two numbers alone would pass against a bare {"ok":true} — which is the reply that
	// would leave the occupancy beside the button showing the old count.
	if len(cleared.Sources) == 0 {
		t.Fatalf("the clear answered without the panel state: %s", rec.Body.String())
	}
	if cleared.CacheEntries != 0 || cleared.CacheBytes != 0 {
		t.Errorf("the clear answered %d entries / %d bytes, want 0/0", cleared.CacheEntries, cleared.CacheBytes)
	}
	if after := quoteAdminGet(t, s); after.CacheEntries != 0 || after.CacheBytes != 0 {
		t.Errorf("occupancy after the clear = %d / %d, want 0/0", after.CacheEntries, after.CacheBytes)
	}

	// And the next reader pays for a fetch. Zeroing the counters while the entries stayed in the map
	// would satisfy every assertion above and leave the button doing nothing at all.
	rec = quoteGET(t, s, "/api/quote/601899?range=3m")
	if rec.Code != http.StatusOK {
		t.Fatalf("quote after the clear → %d (%s)", rec.Code, rec.Body.String())
	}
	if quoteBody(t, rec).Cached {
		t.Error("a cleared cache still served a hit")
	}
	if tencent.n() != 2 {
		t.Errorf("%d upstream calls; the second read must have gone to the vendor", tencent.n())
	}
	// The health counters are NOT cache data — an operator who clears the cache to test a source must
	// not erase the record they were about to read.
	if row := quoteAdminRow(t, quoteAdminGet(t, s), quoteSourceTencent); row.LastSuccess == "" {
		t.Error("the clear wiped the source's record along with the entries")
	}
}

// The clear button empties the CACHE and nothing else. The health counters are not cache data — they
// are the record of what the vendors have been doing — and an operator who presses 清空缓存 to watch a
// flapping source is pressing it to read exactly that record. clear() promises this in six lines of
// comment; this is the assertion behind them.
//
// The case above cannot make it: it fetches a quote AFTER the clear and only then looks at
// lastSuccess, so a clear() that wiped the whole health map passes — the fetch puts the row straight
// back. (Proven: adding `c.health = make(map[string]*QuoteSourceHealth)` to clear() left the whole
// package green.) So two things are different here. Nothing is fetched between the clear and the
// assertions, and the record that has to survive includes a FAILURE STREAK, which no amount of
// successful traffic could rebuild.
func TestQuoteAdminCacheClearKeepsTheHealthRecord(t *testing.T) {
	s := quoteAdminServer(t)

	// Both vendors down, twice, so sina ends up with a streak of two and a reason of its own.
	wireQuoteSources(s, failingStub("tencent 502"), failingStub("sina timed out"))
	for i := 0; i < 2; i++ {
		if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("both sources down → %d, want 503 (%s)", rec.Code, rec.Body.String())
		}
	}
	// Then tencent recovers, which is what puts something in the cache for the button to clear. Sina
	// is not called again — failover stops at the first source that answers — so its streak stands.
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSH)), failingStub("sina timed out"))
	if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusOK {
		t.Fatalf("recovered tencent → %d (%s)", rec.Code, rec.Body.String())
	}

	before := quoteAdminGet(t, s)
	if before.CacheEntries == 0 {
		t.Fatalf("nothing was cached before the clear, so 0/0 afterwards would prove nothing")
	}
	// The record has to be NON-TRIVIAL first. "Unchanged across the clear" compares zeros to zeros
	// on a server that never spoke to a vendor, and the wipe this test exists to catch would pass it.
	if row := quoteAdminRow(t, before, quoteSourceSina); row.Failures != 2 || row.LastError == "" || row.LastErrorAt == "" {
		t.Fatalf("sina's record before the clear = %+v, want a streak of 2 with a reason and a time", row)
	}
	if row := quoteAdminRow(t, before, quoteSourceTencent); row.LastSuccess == "" {
		t.Fatalf("tencent's record before the clear = %+v, want the success that filled the cache", row)
	}

	rec := quoteAdminDo(t, s, http.MethodPost, "/api/admin/quote/cache/clear", "root", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear → %d (%s)", rec.Code, rec.Body.String())
	}
	var cleared quoteAdminView
	if err := json.Unmarshal(rec.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode the clear reply: %v (body %s)", err, rec.Body.String())
	}
	// Both the clear's OWN reply and the next read are checked, because the panel re-renders from the
	// reply: counters wiped only there would blank the operator's screen just the same. And NOTHING
	// is fetched in between — a quote here would rebuild the very rows under test.
	for _, view := range []struct {
		what string
		got  quoteAdminView
	}{{"the clear's reply", cleared}, {"the next panel read", quoteAdminGet(t, s)}} {
		if view.got.CacheEntries != 0 || view.got.CacheBytes != 0 {
			t.Errorf("%s reports %d entries / %d bytes, want 0/0", view.what, view.got.CacheEntries, view.got.CacheBytes)
		}
		for _, want := range before.Sources {
			got := quoteAdminRow(t, view.got, want.Source)
			if got.Failures != want.Failures || got.LastError != want.LastError ||
				got.LastErrorAt != want.LastErrorAt || got.LastSuccess != want.LastSuccess {
				t.Errorf("%s: %s's record did not survive the clear\n got %+v\nwant %+v",
					view.what, want.Source, got, want)
			}
		}
	}
}

// ---------- health ----------

func TestQuoteAdminHealthReportsWhatTheSourcesDid(t *testing.T) {
	s := quoteAdminServer(t)
	sina := failingStub("sina timed out")
	wireQuoteSources(s, failingStub("tencent 502"), sina)

	if rec := quoteGET(t, s, "/api/quote/601899?range=3m"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("both sources down → %d, want 503 (%s)", rec.Code, rec.Body.String())
	}

	view := quoteAdminGet(t, s)
	for _, c := range []struct{ name, wantErr string }{
		{quoteSourceTencent, "tencent 502"},
		{quoteSourceSina, "sina timed out"},
	} {
		row := quoteAdminRow(t, view, c.name)
		if row.Failures != 1 {
			t.Errorf("%s consecutiveFailures = %d, want 1", c.name, row.Failures)
		}
		if !strings.Contains(row.LastError, c.wantErr) {
			t.Errorf("%s lastError = %q, want the vendor's own reason", c.name, row.LastError)
		}
		if _, err := time.Parse(time.RFC3339, row.LastErrorAt); err != nil {
			t.Errorf("%s lastErrorAt = %q: %v", c.name, row.LastErrorAt, err)
		}
		if row.LastSuccess != "" {
			t.Errorf("%s has never succeeded but reports lastSuccess=%q", c.name, row.LastSuccess)
		}
	}

	// A recovery resets the streak and stamps a success. A counter that only went up would report a
	// source that came back an hour ago as still broken.
	wireQuoteSources(s, tencentStub(readQuoteFixture(t, fixTencentSH)), sina)
	if rec := quoteGET(t, s, "/api/quote/601899?range=1m"); rec.Code != http.StatusOK {
		t.Fatalf("recovered tencent → %d (%s)", rec.Code, rec.Body.String())
	}
	row := quoteAdminRow(t, quoteAdminGet(t, s), quoteSourceTencent)
	if row.Failures != 0 {
		t.Errorf("a recovered source still reports %d consecutive failures", row.Failures)
	}
	if _, err := time.Parse(time.RFC3339, row.LastSuccess); err != nil {
		t.Errorf("lastSuccess = %q: %v", row.LastSuccess, err)
	}
}

// A source is never blamed for a market its parser does not cover. Sina answers a US symbol with
// "sina has no A-share-shaped line for the us market" — a fact about our own code — and counting
// that as a vendor failure would paint a permanent red streak on the fallback for every US symbol
// somebody looks up while Tencent is having a bad afternoon.
func TestQuoteAdminNeverBlamesASourceForAMarketItCannotServe(t *testing.T) {
	s := quoteAdminServer(t)
	sina := sinaStub(t)
	wireQuoteSources(s, failingStub("tencent 502"), sina)

	if rec := quoteGET(t, s, "/api/quote/AAPL"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("us quote with tencent down → %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if sina.n() != 0 {
		t.Errorf("the A-share fallback was called %d time(s) for a US ticker", sina.n())
	}

	view := quoteAdminGet(t, s)
	row := quoteAdminRow(t, view, quoteSourceSina)
	if row.Failures != 0 || row.LastError != "" || row.LastErrorAt != "" {
		t.Errorf("sina was blamed for a market it does not serve: %+v", row)
	}
	// The markets column is the same predicate the skip above consulted, so the panel and the fetch
	// path cannot disagree about who can answer what.
	for _, id := range row.Markets {
		if id == "us" || id == "hk" {
			t.Errorf("sina advertises %q, which its parser refuses", id)
		}
	}
	// And the source that actually failed still carries the failure.
	if tencent := quoteAdminRow(t, view, quoteSourceTencent); tencent.Failures != 1 {
		t.Errorf("tencent row = %+v, want the one real failure recorded", tencent)
	}
}

// ---------- the audit trail ----------

// Which source a price comes from, and how long the portal repeats it, is a policy about what
// readers are told — so a change to it goes in the log the same way the retention settings next door
// do. And a REFUSED save writes nothing: a log entry for a change that did not happen is worse than
// no entry, because it is read as evidence.
func TestQuoteAdminSaveIsRecordedInTheAuditLog(t *testing.T) {
	s := quoteAdminServer(t)
	if rec := quoteAdminSave(t, s, `{"order":"sina","ttlOpenSecs":45}`); rec.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", rec.Code, rec.Body.String())
	}
	rows, _ := s.st.ListAudit(AuditFilter{TargetType: "quote_config"})
	if len(rows) != 1 {
		t.Fatalf("%d audit rows for the save, want 1: %+v", len(rows), rows)
	}
	if rows[0].Action != AuditPolicyChange || rows[0].Actor != "root" {
		t.Errorf("audit row = %+v, want a %s by root", rows[0], AuditPolicyChange)
	}
	// The detail names the fields that were actually carried, so the log distinguishes "reordered the
	// sources" from "shortened the TTL" without keeping a copy of the values.
	for _, field := range []string{"Order", "TTLOpenSecs"} {
		if !strings.Contains(rows[0].Detail, field) {
			t.Errorf("audit detail %q does not name %s", rows[0].Detail, field)
		}
	}
	if strings.Contains(rows[0].Detail, "TTLClosedSecs") {
		t.Errorf("audit detail %q names a field the request did not carry", rows[0].Detail)
	}

	if rec := quoteAdminSave(t, s, `{"order":"bloomberg"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("refused save → %d, want 400", rec.Code)
	}
	if rows, _ := s.st.ListAudit(AuditFilter{TargetType: "quote_config"}); len(rows) != 1 {
		t.Errorf("a refused save added an audit row: %d rows", len(rows))
	}
}
