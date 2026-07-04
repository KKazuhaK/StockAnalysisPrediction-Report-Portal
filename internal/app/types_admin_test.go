package app

import (
	"net/http"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The "Restore defaults" button wipes the type configuration and re-seeds the
// shipped first-run defaults, so the page returns to exactly what the program
// generates on first run. Admin-added custom types are removed; already-stored
// report data is left untouched (a type still backed by reports reappears as an
// unconfigured / discovered entry).
func TestRestoreDefaultTypesWipesToFirstRunDefaults(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
	seedDefaultTypes(s.st) // establish the shipped baseline, as first-run bootstrap does

	// Admin diverges the config: move + rename + reorder + flip-summary a default,
	// delete another default outright, and add two custom (non-default) types.
	s.st.UpsertTypeConfig("交易分析", "投资决策", "成交分析", 99, true) // moved out of 重组决策, renamed, reordered
	s.st.DeleteTypeConfig("信号监测")                            // a default removed entirely
	s.st.UpsertTypeConfig("重组分析", "重组决策", "自定义", 5, true)  // custom type with NO report data
	s.st.UpsertTypeConfig("重组交易分析", "重组决策", "", 6, false)  // custom type that DOES have report data

	// A stored report keeps the (now-custom) type alive in the data. Restore must not touch report data.
	if _, err := s.st.UpsertReport(Rep{UID: "r1", RType: "重组交易分析", Kind: "重组决策", Date: "2026-07-04"}); err != nil {
		t.Fatalf("UpsertReport: %v", err)
	}

	code, out := call(t, s.apiTypesRestoreDefaults, `{}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("restore → %d", code)
	}
	if got := int(out["restored"].(float64)); got != len(defaultSeedTypes) {
		t.Fatalf("restored = %d, want %d", got, len(defaultSeedTypes))
	}

	cfg := s.st.TypeConfigs()
	// The config is now EXACTLY the seed set — no leftover custom rows.
	if len(cfg) != len(defaultSeedTypes) {
		t.Fatalf("type_config has %d rows, want exactly the %d seed defaults", len(cfg), len(defaultSeedTypes))
	}
	// The moved/renamed default snaps back to its shipped values, label cleared.
	if c := cfg["交易分析"]; c.Kind != "重组决策" || c.Ord != 20 || c.IsSummary || c.Label != "" {
		t.Fatalf("交易分析 not restored to defaults: %+v", c)
	}
	// The deleted default is re-created.
	if _, ok := cfg["信号监测"]; !ok {
		t.Fatal("信号监测 default was not re-seeded")
	}
	// The custom type with no data is gone entirely.
	if _, ok := cfg["重组分析"]; ok {
		t.Fatal("custom 重组分析 (no report data) should have been wiped")
	}
	// The custom type with data loses its config row too...
	if _, ok := cfg["重组交易分析"]; ok {
		t.Fatal("custom 重组交易分析 config should have been wiped")
	}
	// ...but still surfaces as a discovered type, because it has real reports.
	discovered := false
	for _, n := range s.st.DiscoveredTypes() {
		if n == "重组交易分析" {
			discovered = true
		}
	}
	if !discovered {
		t.Fatal("重组交易分析 has report data and must remain discoverable")
	}
	// Report data itself is untouched — restore only resets this page's settings.
	var kind string
	s.st.queryRow("SELECT kind FROM reports WHERE uid=?", "r1").Scan(&kind)
	if kind != "重组决策" {
		t.Fatalf("report kind = %q, restore must not touch report data", kind)
	}
}
