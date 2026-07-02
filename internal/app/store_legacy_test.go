package app

import "testing"

// ImportLegacyReport migrates an old report into the unified reports table with its
// body, marking it source="legacy" with a stable uid, and is idempotent on re-run.
func TestImportLegacyReport(t *testing.T) {
	st := newTestStore(t)

	if err := st.ImportLegacyReport(42, "Old T", "600000", "宏观", "2024-03-03", "2024-03-03 08:00:00", "# body", "<h1>body</h1>"); err != nil {
		t.Fatalf("ImportLegacyReport: %v", err)
	}

	if n := st.CountNew(); n != 1 {
		t.Fatalf("reports count = %d, want 1", n)
	}

	var uid, src, md, sym string
	st.queryRow("SELECT uid,source,body_md,symbol FROM reports WHERE uid=?", "legacy|42").
		Scan(&uid, &src, &md, &sym)
	if uid != "legacy|42" || src != "legacy" || md != "# body" || sym != "600000" {
		t.Errorf("stored = uid%q src%q md%q sym%q", uid, src, md, sym)
	}

	// Idempotent: a re-run updates in place, still exactly one report row.
	if err := st.ImportLegacyReport(42, "Old T2", "600000", "宏观", "2024-03-03", "2024-03-03 08:00:00", "# body2", ""); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if n := st.CountNew(); n != 1 {
		t.Errorf("after re-import reports count = %d, want 1", n)
	}
}
