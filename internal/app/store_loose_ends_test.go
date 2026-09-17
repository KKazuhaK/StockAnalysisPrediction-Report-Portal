package app

import (
	"errors"
	"net/http"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/batch"
)

// ListRuns folds the subtypes of one symbol+date into a single run; the Postgres integration test
// covers the STRING_AGG path, this pins the SQLite default the whole product runs on.
func TestListRunsGroupsSubtypesSQLite(t *testing.T) {
	st := newTestStore(t)
	for _, rtype := range []string{"交易分析", "舆情分析"} {
		if _, _, err := st.UpsertReport(Rep{Title: rtype, Symbol: "600519", Name: "Moutai", RType: rtype, Kind: "重组决策", Date: "2026-07-01"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.UpsertReport(Rep{Title: "later", Symbol: "600519", Name: "Moutai", RType: "交易分析", Kind: "重组决策", Date: "2026-07-02"}); err != nil {
		t.Fatal(err)
	}

	runs := st.ListRuns("600519", "", nil)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want one per date", len(runs))
	}
	byDate := map[string]RunInfo{}
	for _, r := range runs {
		byDate[r.Date] = r
	}
	if r := byDate["2026-07-01"]; len(r.Subtypes) != 2 {
		t.Fatalf("2026-07-01 subtypes = %v, want both folded into one run", r.Subtypes)
	}
	if r := byDate["2026-07-02"]; len(r.Subtypes) != 1 || r.Subtypes[0] != "交易分析" {
		t.Fatalf("2026-07-02 run = %+v", r)
	}
	if runs := st.ListRuns("600519", "2026-07-03", nil); len(runs) != 0 {
		t.Fatalf("filtered runs = %d, want 0", len(runs))
	}
	if runs := st.ListRuns("000001", "", nil); len(runs) != 0 {
		t.Fatalf("other symbol runs = %d, want 0", len(runs))
	}
}

// A hand-written report is recognized by its manual version; a workflow report never is, and the
// identity collision names the occupant.
func TestManualReportIdentity(t *testing.T) {
	st := newTestStore(t)
	workflowID, _, err := st.UpsertReport(Rep{Title: "ingested", Symbol: "600519", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	if st.IsManualReport(workflowID) {
		t.Fatal("workflow report reported as manual")
	}

	manualID, err := st.CreateManualReport(Rep{Title: "hand written", Symbol: "600519", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsManualReport(manualID) {
		t.Fatal("hand-written report not recognized as manual")
	}
	if st.IsManualReport(999999) {
		t.Fatal("missing id reported as a manual report")
	}

	// The same identity again is a collision that carries the occupant's id.
	_, err = st.CreateManualReport(Rep{Title: "hand written", Symbol: "600519", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01"})
	var exists ErrReportExists
	if !errors.As(err, &exists) {
		t.Fatalf("duplicate manual create error = %v, want ErrReportExists", err)
	}
	if exists.ID != manualID || exists.Error() == "" {
		t.Fatalf("collision = %+v (message %q)", exists, exists.Error())
	}
}

// apiCleanupUsage is the storage-analysis screen: row counts, approximate bytes and the
// oldest/newest spans that retention policies are set from.
func TestCleanupUsageEndpoint(t *testing.T) {
	s := userAdminServer(t)
	if _, _, err := s.st.UpsertReport(Rep{Title: "r1", Symbol: "600519", Name: "Moutai", RType: "投资决策", Kind: "投资决策", Date: "2026-07-01", MD: "some body text", Time: nowStr()}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.CreateToken("cleanup-usage-token", "t", "query", ""); err != nil {
		t.Fatal(err)
	}
	jobID := seedQueuedJob(t, s, "admin", 1)
	finishJob(t, s, jobID, batch.Ok) // any outcome; only the finished_at stamp matters here
	s.st.WriteAudit(AuditEntry{Actor: "admin", Action: "cleanup.usage.test", TargetType: "x", TargetID: "1", Detail: "{}"})

	code, out := call(t, s.apiCleanupUsage, ``, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiCleanupUsage → %d", code)
	}
	if dbBytes, ok := out["db_bytes"].(float64); !ok || dbBytes <= 0 {
		t.Fatalf("db_bytes = %v, want a positive size", out["db_bytes"])
	}
	cats, ok := out["categories"].([]any)
	if !ok || len(cats) != 6 {
		t.Fatalf("categories = %v, want 6", out["categories"])
	}
	byKey := map[string]map[string]any{}
	for _, raw := range cats {
		c := raw.(map[string]any)
		byKey[c["key"].(string)] = c
	}
	rep := byKey["reports"]
	if rep["rows"] != float64(1) || rep["bytes"].(float64) <= 0 || rep["oldest"] == "" || rep["newest"] == "" {
		t.Fatalf("reports category = %v", rep)
	}
	if tok := byKey["tokens"]; tok["rows"] != float64(1) || tok["oldest"] == "" {
		t.Fatalf("tokens category = %v", tok)
	}
	if batch := byKey["batch"]; batch["rows"] != float64(1) || batch["newest"] == "" {
		t.Fatalf("batch category = %v", batch)
	}
	if audit := byKey["audit"]; audit["rows"] == float64(0) {
		t.Fatalf("audit category = %v", audit)
	}
	if _, ok := byKey["chat"]; !ok {
		t.Fatal("chat category missing")
	}
}
