package app

import (
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/legacy"
)

// legacyImporter needs old_base configured; CountLegacy tracks imported rows.
func TestLegacyImporterHelperAndCount(t *testing.T) {
	st := newTestStore(t)
	if im := st.legacyImporter(nil); im != nil {
		t.Error("legacyImporter without old_base should be nil")
	}
	st.SetSetting("old_base", "http://example")
	if im := st.legacyImporter(nil); im == nil {
		t.Error("legacyImporter with old_base should be non-nil")
	}
	if st.CountLegacy() != 0 {
		t.Errorf("CountLegacy = %d, want 0", st.CountLegacy())
	}
	st.ImportLegacyReport(1, "T", "600000", "宏观", "2026-01-01", "t", "b", "")
	if st.CountLegacy() != 1 {
		t.Errorf("CountLegacy after import = %d, want 1", st.CountLegacy())
	}
}

// The background job runs one import at a time and reports its result.
func TestLegacyImportJob(t *testing.T) {
	var j legacyImportJob
	if !j.tryStart() {
		t.Fatal("first tryStart should succeed")
	}
	if j.tryStart() {
		t.Error("second tryStart while running should fail")
	}
	if j.snapshot()["running"] != true {
		t.Error("snapshot.running should be true while running")
	}
	j.done(legacy.Result{Imported: 5, Skipped: 2}, nil)
	snap := j.snapshot()
	if snap["running"] != false || snap["imported"] != 5 || snap["skipped"] != 2 {
		t.Errorf("after done snapshot = %+v", snap)
	}
	if !j.tryStart() {
		t.Error("tryStart after done should succeed")
	}
}

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
