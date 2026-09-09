package app

import (
	"strings"
	"testing"
	"time"
)

func refreshTestTarget(t *testing.T, market, code string) quoteTarget {
	t.Helper()
	target, err := quoteTargetFor(market, code)
	if err != nil {
		t.Fatalf("target %s:%s: %v", market, code, err)
	}
	return target
}

func refreshTestTime(t *testing.T, zone string, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("load zone %s: %v", zone, err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc)
}

func TestQuoteRefreshAdviceUsesTheRemainingOpenTTL(t *testing.T) {
	now := refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 10, 0)
	target := refreshTestTarget(t, "sh", "601899")
	resp := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionOpen}}
	got := quoteRefreshAdviceFor(now, true, quoteConfigDefault(), target, quoteIntervalDaily, 17*time.Second, resp)
	if got.RefreshAfterSecs != 17 || got.RefreshAt != "" {
		t.Fatalf("open advice = %+v, want the remaining 17 seconds", got)
	}

	off := quoteRefreshAdviceFor(now, false, quoteConfigDefault(), target, quoteIntervalDaily, 17*time.Second, resp)
	if off.RefreshAfterSecs != 0 || off.RefreshAt != "" {
		t.Fatalf("disabled automatic refresh still advised %+v", off)
	}
}

func TestQuoteRefreshAdviceSleepsAcrossCloseLunchAndWeekend(t *testing.T) {
	target := refreshTestTarget(t, "sh", "601899")
	closed := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{
			name: "lunch",
			now:  refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 11, 31),
			want: "2026-09-08T13:00:00+08:00",
		},
		{
			name: "after close",
			now:  refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 15, 1),
			want: "2026-09-09T09:30:00+08:00",
		},
		{
			name: "Friday close",
			now:  refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 11, 15, 1),
			want: "2026-09-14T09:30:00+08:00",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteRefreshAdviceFor(tc.now, true, quoteConfigDefault(), target, quoteIntervalDaily, time.Minute, closed)
			if got.RefreshAfterSecs != 0 || got.RefreshAt != tc.want {
				t.Fatalf("advice = %+v, want refreshAt %s", got, tc.want)
			}
		})
	}
}

func TestQuoteRefreshAdviceUsesNewYorkDaylightSavingAtTheNextOpen(t *testing.T) {
	target := refreshTestTarget(t, "us", "AAPL")
	now := refreshTestTime(t, "America/New_York", 2026, time.March, 6, 16, 1)
	closed := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}
	got := quoteRefreshAdviceFor(now, true, quoteConfigDefault(), target, quoteIntervalDaily, time.Minute, closed)
	if got.RefreshAt != "2026-03-09T09:30:00-04:00" {
		t.Fatalf("next New York open = %q, want the post-DST offset", got.RefreshAt)
	}
}

func TestQuoteRefreshAdviceGivesTheOpeningMarkerOneGraceProbe(t *testing.T) {
	now := refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 9, 31)
	target := refreshTestTarget(t, "sh", "601899")
	closed := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionClose}}
	got := quoteRefreshAdviceFor(now, true, quoteConfigDefault(), target, quoteIntervalIntraday, time.Minute, closed)
	if got.RefreshAfterSecs != 0 || got.RefreshAt != "2026-09-08T09:35:00+08:00" {
		t.Fatalf("opening grace advice = %+v", got)
	}
}

func TestQuoteRefreshAdviceUsesVendorTimeForUnknownSessions(t *testing.T) {
	now := refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 10, 0)
	target := refreshTestTarget(t, "sh", "601899")
	cases := []struct {
		name  string
		resp  *QuoteResp
		after int
		at    string
	}{
		{
			name:  "current vendor time",
			resp:  &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionUnknown, AsOf: "2026-09-08T09:59:00+08:00"}},
			after: 120,
		},
		{
			name: "holiday-like stale vendor time",
			resp: &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionUnknown, AsOf: "2026-09-07T15:00:00+08:00"}},
			at:   "2026-09-08T13:00:00+08:00",
		},
		{
			name:  "empty failed batch retries",
			resp:  nil,
			after: 120,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteRefreshAdviceFor(now, true, quoteConfigDefault(), target, quoteIntervalSnapshot, 5*time.Minute, tc.resp)
			if got.RefreshAfterSecs != tc.after || got.RefreshAt != tc.at {
				t.Fatalf("advice = %+v, want after=%d at=%q", got, tc.after, tc.at)
			}
		})
	}
}

func TestQuoteUnknownCacheCannotHideTheConservativeProbe(t *testing.T) {
	cfg := quoteConfigDefault()
	unknown := &QuoteResp{Snapshot: QuoteSnapshot{Session: quoteSessionUnknown}}
	if got := cfg.ttlFor(unknown, quoteIntervalSnapshot); got != quoteUnknownRefresh {
		t.Fatalf("unknown snapshot TTL = %v, want %v", got, quoteUnknownRefresh)
	}
	if got := cfg.ttlFor(unknown, quoteIntervalIntraday); got != quoteTTLIntraday {
		t.Fatalf("intraday TTL = %v, want its shorter configured value %v", got, quoteTTLIntraday)
	}
}

func TestQuoteAutoRefreshSettingIsWiredThroughTheAdminAPI(t *testing.T) {
	s := quoteAdminServer(t)
	if !s.quoteAutoRefresh() {
		t.Fatal("automatic refresh is off on a fresh portal")
	}
	if view := quoteAdminGet(t, s); !view.AutoRefresh {
		t.Fatal("the panel reports automatic refresh off on a fresh portal")
	}
	if rec := quoteAdminSave(t, s, `{"autoRefresh":false}`); rec.Code != 200 {
		t.Fatalf("save off status=%d body=%s", rec.Code, rec.Body.String())
	}
	if s.quoteAutoRefresh() || quoteAdminGet(t, s).AutoRefresh {
		t.Fatal("automatic refresh remained on after saving off")
	}
	if rec := quoteAdminSave(t, s, `{"autoRefresh":true}`); rec.Code != 200 {
		t.Fatalf("save on status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !s.quoteAutoRefresh() {
		t.Fatal("automatic refresh could not be turned back on")
	}
}

func TestQuoteEndpointsExposeRefreshAdviceAndRetryAdvice(t *testing.T) {
	now := refreshTestTime(t, "Asia/Shanghai", 2026, time.September, 8, 10, 0)

	openServer := quoteServer(t)
	openServer.quotes.now = func() time.Time { return now }
	body := strings.Replace(string(readQuoteFixture(t, fixTencentSH)), "SH_close_", "SH_open_", 1)
	wireQuoteSources(openServer, tencentStub([]byte(body)), sinaStub(t))
	rec := quoteGET(t, openServer, "/api/quote/601899?range=3m")
	if rec.Code != 200 {
		t.Fatalf("open quote status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := quoteBody(t, rec).RefreshAfterSecs; got != int(quoteTTLOpen/time.Second) {
		t.Fatalf("open refreshAfterSecs=%d, want %d", got, int(quoteTTLOpen/time.Second))
	}

	batchServer := quoteServer(t)
	batchServer.quotes.now = func() time.Time { return now }
	wireQuoteSources(batchServer, failingStub("batch unavailable"), sinaStub(t))
	batch := quoteBatchGET(t, batchServer, "/api/quotes?symbols=601899")
	view := quoteBatchBody(t, batch)
	if view.RefreshAfterSecs != int(quoteUnknownRefresh/time.Second) {
		t.Fatalf("empty batch refreshAfterSecs=%d, want %d (body %s)", view.RefreshAfterSecs, int(quoteUnknownRefresh/time.Second), batch.Body.String())
	}

	failing := quoteServer(t)
	failing.quotes.now = func() time.Time { return now }
	wireQuoteSources(failing, failingStub("tencent unavailable"), failingStub("sina unavailable"))
	unavailable := quoteGET(t, failing, "/api/quote/601899?range=3m")
	if unavailable.Code != 503 || unavailable.Header().Get("Retry-After") != "120" {
		t.Fatalf("unavailable status/retry = %d/%q (body %s)", unavailable.Code, unavailable.Header().Get("Retry-After"), unavailable.Body.String())
	}
	if !strings.Contains(unavailable.Body.String(), `"refreshAfterSecs":120`) {
		t.Fatalf("unavailable body has no refresh advice: %s", unavailable.Body.String())
	}
}
