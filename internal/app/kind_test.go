package app

import "testing"

// foldKind must collapse any stored/legacy kind into the current buckets (the five
// pipelines plus 未分类), with the two legacy remaps: 事件监测→重组决策 and 其他→未分类.
// (runKind's mapping is covered by TestRunKindMapsEveryCategory.)
func TestFoldKindCollapsesToBuckets(t *testing.T) {
	cases := map[string]string{
		"重组决策":    "重组决策",
		"投资决策":    "投资决策",
		"深度研究":    "深度研究",
		"技术分析":    "技术分析",
		"每日金股":    "每日金股",
		"未分类":     "未分类",
		"事件监测":    "重组决策",
		"每日推荐":    "每日金股",
		"其他":      "未分类",
		"某乱七八糟的值": "未分类",
	}
	for in, want := range cases {
		if got := foldKind(in); got != want {
			t.Errorf("foldKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// RecomputeKinds re-derives stored kinds: legacy rows from their subtype, Dify rows
// by folding their explicit kind; unchanged rows are left alone.
func TestRecomputeKinds(t *testing.T) {
	st := newTestStore(t)
	ins := "INSERT INTO reports(uid,symbol,rtype,rdate,kind,source,sent_at) VALUES(?,?,?,?,?,?,?)"
	st.exec(ins, "legacy|1", "600000", "舆情分析", "2026-01-01", "事件监测", "legacy", "2026-01-01") // → 重组决策
	st.exec(ins, "legacy|2", "600001", "宏观", "2026-01-02", "宏观", "legacy", "2026-01-02")     // → 未分类
	st.exec(ins, "legacy|3", "600002", "估值分析", "2026-01-03", "投资决策", "legacy", "2026-01-03") // stays 投资决策
	st.exec(ins, "n|1", "600003", "舆情分析", "2026-01-04", "事件监测", "dify", "2026-01-04")        // fold → 重组决策
	st.exec(ins, "legacy|4", "600004", "专题研究", "2026-01-05", "未分类", "legacy", "2026-01-05")
	st.UpsertTypeConfig("专题研究", "深度研究", "", 0, false) // 类型管理 mapping → 深度研究

	n, err := st.RecomputeKinds()
	if err != nil {
		t.Fatalf("RecomputeKinds: %v", err)
	}
	if n != 4 {
		t.Errorf("updated = %d, want 4 (legacy|3 unchanged)", n)
	}
	check := func(uid, want string) {
		var k string
		st.queryRow("SELECT kind FROM reports WHERE uid=?", uid).Scan(&k)
		if k != want {
			t.Errorf("%s kind = %q, want %q", uid, k, want)
		}
	}
	check("legacy|1", "重组决策")
	check("legacy|2", "未分类")
	check("legacy|3", "投资决策")
	check("n|1", "重组决策")
	check("legacy|4", "深度研究") // 类型管理 mapping wins over runKind
}
