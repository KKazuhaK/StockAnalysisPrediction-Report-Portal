package app

import "testing"

// GET /api/reports needs real pagination (offset + total), name/code-aware
// keyword search, and source/run_id filters.
func TestQueryReportsPaginationAndFilters(t *testing.T) {
	st := newTestStore(t)
	st.SyncStocks(map[string]string{"300750": "宁德时代"})
	for _, d := range []string{"2026-01-01", "2026-02-01", "2026-03-01"} {
		if _, err := st.UpsertReport(Rep{
			UID: "300750|" + d + "|投资决策|汇总", Symbol: "300750", Date: d, Kind: "投资决策",
			RType: "汇总", Title: "投资决策汇总", Source: "dify", RunID: "run-" + d, Time: d, MD: "body",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// pagination + total
	p1, total, err := st.QueryReports(ReportQuery{Symbol: "300750", Limit: 2, Offset: 0})
	if err != nil || total != 3 || len(p1) != 2 {
		t.Fatalf("page1: total=%d len=%d err=%v (want total=3 len=2)", total, len(p1), err)
	}
	p2, _, _ := st.QueryReports(ReportQuery{Symbol: "300750", Limit: 2, Offset: 2})
	if len(p2) != 1 || p2[0].UID == p1[0].UID {
		t.Fatalf("page2 len=%d overlap=%v", len(p2), p2[0].UID == p1[0].UID)
	}

	// keyword now matches company name and code (not just title/body)
	if _, n, _ := st.QueryReports(ReportQuery{Q: "宁德时代"}); n != 3 {
		t.Errorf("q=名字 total=%d, want 3", n)
	}
	if _, n, _ := st.QueryReports(ReportQuery{Q: "300750"}); n != 3 {
		t.Errorf("q=code total=%d, want 3", n)
	}

	// source + run_id filters
	if _, n, _ := st.QueryReports(ReportQuery{Symbol: "300750", Source: "dify"}); n != 3 {
		t.Errorf("source filter total=%d, want 3", n)
	}
	if _, n, _ := st.QueryReports(ReportQuery{RunID: "run-2026-02-01"}); n != 1 {
		t.Errorf("run_id filter total=%d, want 1", n)
	}
}

// Ingest must signal whether the row was created or overwritten.
func TestUpsertReportCreatedFlag(t *testing.T) {
	st := newTestStore(t)
	created, err := st.UpsertReport(Rep{UID: "u1", Symbol: "300750", Date: "2026-01-01", RType: "汇总"})
	if err != nil || !created {
		t.Fatalf("first ingest created=%v err=%v, want created=true", created, err)
	}
	created2, err := st.UpsertReport(Rep{UID: "u1", Symbol: "300750", Date: "2026-01-01", RType: "汇总", Title: "updated"})
	if err != nil || created2 {
		t.Fatalf("second ingest created=%v err=%v, want created=false", created2, err)
	}
}

// Single tracking item can be updated by id (the hypothesis re-check loop).
func TestUpdateTrackingStatus(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetTracking("u1", "300750", []TrackingItem{{IType: "assumption", Content: "c1", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	items := st.QueryTracking("300750", "", 100)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	ok, err := st.UpdateTrackingStatus(items[0].ID, "confirmed", "复核完成")
	if err != nil || !ok {
		t.Fatalf("update ok=%v err=%v, want ok=true", ok, err)
	}
	items = st.QueryTracking("300750", "", 100)
	if items[0].Status != "confirmed" || items[0].ReviewPoint != "复核完成" {
		t.Errorf("after update = %+v", items[0])
	}
	if ok, _ := st.UpdateTrackingStatus(999999, "confirmed", ""); ok {
		t.Errorf("updating a missing id should return ok=false")
	}
}

// Reports can be deleted/retracted, cascading their tracking items; idempotent.
func TestDeleteReport(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.UpsertReport(Rep{UID: "u1", Symbol: "300750", Date: "2026-01-01", RType: "汇总"}); err != nil {
		t.Fatal(err)
	}
	st.SetTracking("u1", "300750", []TrackingItem{{IType: "assumption", Content: "c1"}})

	n, err := st.DeleteReport("u1")
	if err != nil || n != 1 {
		t.Fatalf("delete n=%d err=%v, want 1", n, err)
	}
	if _, total, _ := st.QueryReports(ReportQuery{Symbol: "300750"}); total != 0 {
		t.Errorf("after delete total=%d, want 0", total)
	}
	if len(st.QueryTracking("300750", "", 100)) != 0 {
		t.Errorf("tracking items should be cascade-deleted")
	}
	// idempotent retry
	if n2, err := st.DeleteReport("u1"); err != nil || n2 != 0 {
		t.Fatalf("re-delete n=%d err=%v, want 0", n2, err)
	}
}
