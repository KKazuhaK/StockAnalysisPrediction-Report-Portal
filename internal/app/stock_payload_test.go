package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Reading is where people spend their time, and every move within it — another date on the
// timeline, another report type, another version — is a fresh request for this endpoint. Warming
// the next one is only worth doing if what it costs is mostly the report the reader wanted, rather
// than the same navigation shipped again.
//
// This measures the split on a stock shaped like a real one, because the answer decides the design:
// a heavy skeleton would mean the endpoint has to be cut in two before anything can be prefetched.
func TestReadingPayloadIsMostlyTheReportItself(t *testing.T) {
	s := tenancyServer(t)
	s.names = LoadNames(t.TempDir(), s.st) // the handler resolves the company name through it
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	const dates, typesPerDay = 60, 4
	body := strings.Repeat("这是一段研报正文，覆盖公司经营、行业与风险。\n\n", 400) // ~ 40 KB
	var wantID int64
	for d := 0; d < dates; d++ {
		date := fmt.Sprintf("2026-06-%02d", (d%28)+1)
		for k := 0; k < typesPerDay; k++ {
			id, _, _ := s.st.UpsertReport(Rep{
				Symbol: "600519", Date: date, RType: fmt.Sprintf("类型%d", k),
				Title: fmt.Sprintf("600519 类型%d", k), MD: body,
			})
			if wantID == 0 {
				wantID = id
			}
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/stock/600519", nil)
	req.SetPathValue("symbol", "600519") // only the mux fills this in; a hand-built request must
	s.apiStock(rec, req, "alice")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Timeline []map[string]any `json:"timeline"`
		Subtabs  []map[string]any `json:"subtabs"`
		Kinds    []string         `json:"kinds"`
		Rep      map[string]any   `json:"rep"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	total := rec.Body.Len()
	repJSON, _ := json.Marshal(out.Rep)
	skeleton := total - len(repJSON)
	t.Logf("stock response: %d KB total, report %d KB, navigation %d KB (%.0f%%) — %d dates, %d tabs",
		total/1024, len(repJSON)/1024, skeleton/1024, float64(skeleton)*100/float64(total), len(out.Timeline), len(out.Subtabs))

	// The conclusion this test exists to pin: the navigation that would be re-sent by warming a
	// neighbouring date is a small fraction of the payload, so warming can reuse this endpoint as
	// it stands. If that stops being true, prefetching is paying for the same timeline over and
	// over and the endpoint needs splitting — which is a design decision, not a tuning one.
	if skeleton*4 > total {
		t.Errorf("navigation is %d of %d bytes (>25%%): warming this endpoint would re-send it each time", skeleton, total)
	}
}
