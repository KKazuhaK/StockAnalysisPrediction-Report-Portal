package app

import "testing"

// The restructuring (重组) report line rolls up under a single top-level kind
// named 重组决策 (renamed from 并购重组).
func TestRunKindRestructuring(t *testing.T) {
	for _, ty := range []string{"重组分析", "重组基本面分析", "重组交易分析"} {
		if got := runKind([]string{ty}); got != "重组决策" {
			t.Errorf("runKind(%q) = %q, want 重组决策", ty, got)
		}
	}
}

// Every known report category must resolve to one of the canonical top-level
// kinds — no subtype (e.g. 舆情分析/股权分析/管理能力分析) may leak through as its
// own "kind", or cards/timeline show inconsistent tags.
func TestRunKindMapsEveryCategory(t *testing.T) {
	cases := map[string]string{
		"汇总":      "投资决策",
		"投资决策建议":  "投资决策",
		"研报分析":    "投资决策",
		"行业分析":    "投资决策",
		"估值分析":    "投资决策",
		"财务分析":    "投资决策",
		"股权分析":    "投资决策",
		"投资机会":    "投资决策",
		"综合深度研究":  "深度研究",
		"管理能力分析":  "深度研究",
		"调研纪要":    "深度研究",
		"舆情分析":    "重组决策",
		"事件监测":    "重组决策",
		"信号监测":    "重组决策",
		"重组分析":    "重组决策",
		"重组基本面分析": "重组决策",
		"重组交易分析":  "重组决策",
		"资本运作分析":  "重组决策",
		"技术分析":    "技术分析",
		"缠论分析":    "技术分析",
		"盘前":      "每日金股",
		"盘中":      "每日金股",
		"盘后":      "每日金股",
		"未分类":     "未分类",
	}
	for rtype, want := range cases {
		if got := runKind([]string{rtype}); got != want {
			t.Errorf("runKind(%q) = %q, want %q", rtype, got, want)
		}
	}
}

// A card's kind tags should reflect EVERY distinct top-level kind present in the
// group (not one arbitrarily-collapsed guess), ordered by kindOrder — for legacy
// groups just like for new ones.
func TestBuildGroupsShowsAllKinds(t *testing.T) {
	name := func(string) string { return "利通电子" }
	reps := []Rep{
		{RID: "o1", Src: "old", Symbol: "603629", Date: "2026-07-01", RType: "估值分析"},    // → 投资决策
		{RID: "o2", Src: "old", Symbol: "603629", Date: "2026-07-01", RType: "重组分析"},    // → 重组决策
		{RID: "o3", Src: "old", Symbol: "603629", Date: "2026-07-01", RType: "重组基本面分析"}, // → 重组决策 (dup)
	}
	gs := buildGroups(reps, name)
	if len(gs) != 1 {
		t.Fatalf("groups = %d, want 1", len(gs))
	}
	want := []string{"重组决策", "投资决策"} // distinct, ordered by kindOrder
	got := gs[0].Kinds
	if len(got) != len(want) {
		t.Fatalf("Kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Kinds = %v, want %v", got, want)
		}
	}
}

// 重组决策 must be a registered top-level kind (drives the stock-page Segmented
// order and the type-management grouping).
func TestKindOrderHasRestructuringDecision(t *testing.T) {
	found := false
	for _, k := range kindOrder {
		if k == "重组决策" {
			found = true
		}
		if k == "并购重组" {
			t.Errorf("kindOrder still contains the old 并购重组")
		}
	}
	if !found {
		t.Errorf("kindOrder = %v, want it to contain 重组决策", kindOrder)
	}
}

// The browse/search feed shows one card per stock (its most recent run), not one
// per (stock, day) — collapseLatestBySymbol drops older-date duplicates. Thematic
// (symbol-less) groups are all kept; the full per-day history stays on the stock page.
func TestCollapseLatestBySymbol(t *testing.T) {
	name := func(string) string { return "麦加芯彩" }
	reps := []Rep{
		{RID: "n1", Src: "new", Symbol: "603062", Date: "2026-07-05", RType: "投资决策建议", Time: "2026-07-05 00:50"},
		{RID: "n2", Src: "new", Symbol: "603062", Date: "2026-07-01", RType: "估值分析", Time: "2026-07-01 09:44"},
		{RID: "n3", Src: "new", Symbol: "689009", Date: "2026-07-05", RType: "投资决策建议", Time: "2026-07-05 00:37"},
		{RID: "n4", Src: "new", Symbol: "", Date: "2026-07-04", RType: "专题研究", Title: "CPO行业深度研究", Time: "2026-07-04 10:00"},
		{RID: "n5", Src: "new", Symbol: "", Date: "2026-07-03", RType: "专题研究", Title: "AI算力研究", Time: "2026-07-03 10:00"},
	}
	col := collapseLatestBySymbol(buildGroups(reps, name))
	if len(col) != 4 { // 603062 (latest), 689009, + 2 thematic
		t.Fatalf("want 4 groups after collapse, got %d", len(col))
	}
	var g603 *Group
	topics := 0
	for i := range col {
		if col[i].Symbol == "603062" {
			g603 = &col[i]
		}
		if col[i].Symbol == "" {
			topics++
		}
	}
	if g603 == nil || g603.Date != "2026-07-05" {
		t.Fatalf("603062 collapsed group should be the 2026-07-05 run, got %+v", g603)
	}
	if topics != 2 {
		t.Fatalf("both thematic groups should be kept, got %d", topics)
	}
}
