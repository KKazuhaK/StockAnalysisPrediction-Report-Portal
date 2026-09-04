package app

import "testing"

// A facts-snapshot cache entry must never reach a reader-facing list: it is written by
// 获取公司基本信息 for its own callers, and before this filter it surfaced as a 未分类 tab on
// every stock analysed that day.
func TestDropInternalRemovesCacheEntries(t *testing.T) {
	reps := []Rep{
		{ID: 1, RType: "投资决策汇总", Symbol: "300620"},
		{ID: 2, RType: "公司基础快照", Symbol: "300620"},
		{ID: 3, RType: "重组舆情分析", Symbol: "300620"},
	}
	got := dropInternal(reps)
	if len(got) != 2 {
		t.Fatalf("want 2 reports left, got %d", len(got))
	}
	for _, r := range got {
		if r.RType == "公司基础快照" {
			t.Fatalf("cache entry %d survived the filter", r.ID)
		}
	}
	if got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("input order not preserved: %v", []int64{got[0].ID, got[1].ID})
	}
}

// The common path is "nothing to drop"; it must not allocate a copy.
func TestDropInternalKeepsSliceWhenNothingToDrop(t *testing.T) {
	reps := []Rep{{ID: 1, RType: "投资决策汇总"}}
	got := dropInternal(reps)
	if len(got) != 1 || &got[0] != &reps[0] {
		t.Fatalf("want the input slice back untouched")
	}
}

func TestDropInternalEmptyInput(t *testing.T) {
	if got := dropInternal(nil); len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

// A writer must not be able to pick a cache type: the identity key is
// symbol|date|subtype|title, so a hand-written 公司基础快照 would be served back to the
// workflow that believes it is reading its own snapshot.
func TestPickableTypesDropsInternal(t *testing.T) {
	got := pickableTypes([]string{"投资决策汇总", "公司基础快照", "重组舆情分析"})
	if len(got) != 2 {
		t.Fatalf("want 2 pickable types, got %d: %v", len(got), got)
	}
	for _, v := range got {
		if v == "公司基础快照" {
			t.Fatalf("cache type offered to a writer")
		}
	}
	if got[0] != "投资决策汇总" || got[1] != "重组舆情分析" {
		t.Fatalf("order not preserved: %v", got)
	}
}

func TestPickableTypesKeepsEverythingElse(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := pickableTypes(in)
	if len(got) != 3 {
		t.Fatalf("want all 3 kept, got %v", got)
	}
}
