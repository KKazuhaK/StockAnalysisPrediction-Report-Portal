package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Reports carry a real ingest instant in sent_at: the server stamps a UTC RFC3339
// timestamp (never the old date-only fallback), a valid client-supplied instant is
// honored (e.g. Dify passing /api/v1/now's utc), and an invalid one is replaced.
func TestV1IngestStampsRealTime(t *testing.T) {
	s := newV1Server(t) // tok-all

	ingest := func(body string) {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	timeOf := func(symbol, date, rtype string) string {
		rep := repByIdent(t, s.st, symbol, date, rtype)
		if rep == nil {
			t.Fatalf("no report %s/%s/%s", symbol, date, rtype)
		}
		return rep.Time
	}

	// no client time → server-stamped UTC RFC3339 instant, NOT date-only
	ingest(`{"symbol":"600160","date":"2026-07-02","subtype":"综合决策","body_md":"x"}`)
	got := timeOf("600160", "2026-07-02", "综合决策")
	ts, err := time.Parse(time.RFC3339, got)
	if err != nil || !strings.HasSuffix(got, "Z") || len(got) == 10 {
		t.Fatalf("sent_at=%q not a UTC RFC3339 instant (%v)", got, err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("sent_at=%q not ~now", got)
	}

	// valid RFC3339 client time → honored verbatim
	ingest(`{"symbol":"300750","date":"2026-07-02","subtype":"汇总","time":"2026-07-02T01:02:03Z"}`)
	if got := timeOf("300750", "2026-07-02", "汇总"); got != "2026-07-02T01:02:03Z" {
		t.Errorf("valid client time not honored: %q", got)
	}

	// invalid client time (date-only) → ignored, server stamps a real instant
	ingest(`{"symbol":"000001","date":"2026-07-02","subtype":"汇总","time":"2026-07-02"}`)
	if got := timeOf("000001", "2026-07-02", "汇总"); len(got) == 10 || !strings.HasSuffix(got, "Z") {
		t.Errorf("invalid client time should be replaced by a server instant, got %q", got)
	}
}

// Same-day reports order by the real instant (UTC ISO8601 sorts chronologically).
func TestSameDayOrderingByInstant(t *testing.T) {
	st := newTestStore(t)
	// Distinct subtypes: same code+date+subtype is now ONE report, so the two rows
	// under test have to differ somewhere in the identity tuple.
	for _, r := range []struct{ rtype, time string }{
		{"a", "2026-07-02T03:00:00Z"},
		{"b", "2026-07-02T09:00:00Z"},
	} {
		if _, _, err := st.UpsertReport(Rep{Symbol: "600160", Date: "2026-07-02", RType: r.rtype, Time: r.time}); err != nil {
			t.Fatal(err)
		}
	}
	reps, _, err := st.QueryReports(ReportQuery{Symbol: "600160"})
	if err != nil || len(reps) != 2 {
		t.Fatalf("query: len=%d err=%v", len(reps), err)
	}
	// QueryReports sorts rdate DESC, sent_at DESC → later instant first
	if reps[0].RType != "b" {
		t.Errorf("same-day order wrong: got %q first, want the 09:00 report", reps[0].RType)
	}
}

// The instant is exposed as `time` on v1 report responses (list path shown).
func TestV1ReportResponsesIncludeTime(t *testing.T) {
	s := newV1Server(t)
	ireq := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(
		`{"symbol":"600160","date":"2026-07-02","subtype":"综合决策","body_md":"x"}`))
	ireq.Header.Set("Authorization", "Bearer tok-all")
	s.v1Ingest(httptest.NewRecorder(), ireq)

	lreq := httptest.NewRequest("GET", "/api/v1/reports?symbol=600160", nil)
	lreq.Header.Set("Authorization", "Bearer tok-all")
	lrec := httptest.NewRecorder()
	s.v1QueryReports(lrec, lreq)
	var lm map[string]any
	json.Unmarshal(lrec.Body.Bytes(), &lm)
	items, _ := lm["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no items: %s", lrec.Body.String())
	}
	tv, ok := items[0].(map[string]any)["time"].(string)
	if !ok || !strings.HasSuffix(tv, "Z") {
		t.Errorf("list item missing UTC `time`: %v", items[0])
	}
}
