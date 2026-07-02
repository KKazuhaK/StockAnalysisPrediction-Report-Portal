package app

import (
	"fmt"
	"testing"
)

func insertResearch(t *testing.T, st *Store, uid, title, rtype, date, code string) {
	t.Helper()
	if _, err := st.UpsertReport(Rep{
		UID: uid, Title: title, Symbol: code, RType: rtype,
		Date: date, Source: "dify", Time: date + " 10:00",
	}); err != nil {
		t.Fatalf("UpsertReport: %v", err)
	}
}

// ResearchReports returns only the symbol-less (topic / Q&A) reports — the ones
// that don't belong to a fixed ticker — newest first, with optional title search
// and pagination. Post-cutover it reads the unified reports table (legacy reports
// were migrated there), so RIDs are n<rowid>.
func TestResearchReports(t *testing.T) {
	st := newTestStore(t)
	insertResearch(t, st, "r1", "航空产业链深度研究", "综合深度研究", "2026-05-01", "")
	insertResearch(t, st, "r2", "AI 泡沫程度评估", "综合深度研究", "2026-05-03", "")
	insertResearch(t, st, "r3", "贵州茅台点评", "估值分析", "2026-05-02", "600519") // has a ticker → excluded

	reps, total := st.ResearchReports("", 10, 0)
	if total != 2 {
		t.Fatalf("total = %d, want 2 (symbol-less only)", total)
	}
	if len(reps) != 2 {
		t.Fatalf("len(reps) = %d, want 2", len(reps))
	}
	// newest report_date first
	if reps[0].Title != "AI 泡沫程度评估" {
		t.Errorf("first = %q, want the newest (AI 泡沫程度评估)", reps[0].Title)
	}
	if reps[0].RID == "" || reps[0].RID[0] != 'n' {
		t.Errorf("RID = %q, want an n<rowid> report id", reps[0].RID)
	}

	// title search
	hits, htotal := st.ResearchReports("航空", 10, 0)
	if htotal != 1 || len(hits) != 1 || hits[0].Title != "航空产业链深度研究" {
		t.Errorf("search 航空 → total=%d hits=%v", htotal, hits)
	}
}

func TestResearchReportsPagination(t *testing.T) {
	st := newTestStore(t)
	for i := int64(1); i <= 5; i++ {
		insertResearch(t, st, fmt.Sprintf("r%d", i), "研究", "综合深度研究", "2026-05-0"+string(rune('0'+i)), "")
	}
	page1, total := st.ResearchReports("", 2, 0)
	page2, _ := st.ResearchReports("", 2, 2)
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(page1) != 2 || len(page2) != 2 {
		t.Fatalf("page sizes = %d,%d want 2,2", len(page1), len(page2))
	}
	if page1[0].RID == page2[0].RID {
		t.Errorf("pages overlap: %s", page1[0].RID)
	}
}
