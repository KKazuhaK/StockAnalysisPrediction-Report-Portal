package app

import "testing"

func TestSearchNewLatestCollapsesStockHistoryInSQL(t *testing.T) {
	st := newTestStore(t)
	for _, r := range []Rep{
		{Symbol: "600001", Date: "2026-07-01", RType: "summary", Title: "old"},
		{Symbol: "600001", Date: "2026-07-02", RType: "summary", Title: "latest summary"},
		{Symbol: "600001", Date: "2026-07-02", RType: "technical", Title: "latest technical"},
		{Symbol: "600002", Date: "2026-07-01", RType: "summary", Title: "other stock"},
		{Date: "2026-06-01", RType: "theme", Title: "theme one"},
		{Date: "2026-06-02", RType: "theme", Title: "theme two"},
	} {
		if _, _, err := st.UpsertReport(r); err != nil {
			t.Fatal(err)
		}
	}

	reps, total, err := st.SearchNewLatest(Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 {
		t.Fatalf("uncollapsed total = %d, want 6", total)
	}
	if len(reps) != 5 {
		t.Fatalf("latest report count = %d, want 5", len(reps))
	}
	for _, r := range reps {
		if r.Symbol == "600001" && r.Date != "2026-07-02" {
			t.Fatalf("older stock history leaked into home feed: %+v", r)
		}
	}
}
