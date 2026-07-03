package app

import (
	"regexp"
	"sort"
	"strings"
)

// Group collects one run (same symbol + date) into a single card.
type Group struct {
	Key, Symbol, Date string
	Name              string   // as-of company name (snapshot at ingest; falls back to current)
	CurName           string   // current company name; differs after rename / backdoor listing
	Title             string   // fallback display title when there is no stock code/name (thematic/industry reports)
	Kind              string   // category of this run (重组决策/投资决策/…)
	Kinds             []string // new reports: the multiple top-level categories for this symbol on this date
	Source, Src       string
	Members           []Rep
	N                 int
	Types             []string
	Time              string // latest member's ingest instant (when it was pushed to the portal; UTC RFC3339)
}

// runKind maps report type(s) to their canonical top-level kind — one of the four
// pipelines we run (重组决策 / 投资决策 / 深度研究 / 技术分析), or 未分类 as the
// catch-all. Keyword order encodes priority when a run mixes several types. 舆情 /
// 事件监测 / 信号监测 belong to the 重组 pipeline (its sentiment/signal sub-models).
func runKind(types []string) string {
	j := strings.Join(types, "")
	switch {
	case strings.Contains(j, "重组") || strings.Contains(j, "资本运作") ||
		strings.Contains(j, "舆情") || strings.Contains(j, "事件监测") || strings.Contains(j, "信号监测"):
		return "重组决策"
	case strings.Contains(j, "深度研究") || strings.Contains(j, "DeepResearch") ||
		strings.Contains(j, "管理能力") || strings.Contains(j, "调研"):
		return "深度研究"
	case strings.Contains(j, "技术分析") || strings.Contains(j, "缠论"):
		return "技术分析"
	case strings.Contains(j, "投资") || strings.Contains(j, "估值") ||
		strings.Contains(j, "财务") || strings.Contains(j, "行业") ||
		strings.Contains(j, "研报") || strings.Contains(j, "股权") ||
		strings.Contains(j, "汇总"):
		return "投资决策"
	}
	return "未分类"
}

// foldKind collapses any stored/legacy kind into the current buckets: the four
// pipelines (重组决策 / 投资决策 / 深度研究 / 技术分析) plus 未分类. Legacy kinds
// 事件监测→重组决策 (they were 舆情分析更新) and 其他→未分类; anything unknown → 未分类.
func foldKind(k string) string {
	switch k {
	case "重组决策", "投资决策", "深度研究", "技术分析", "未分类":
		return k
	case "事件监测":
		return "重组决策"
	default: // 其他 and anything unrecognized
		return "未分类"
	}
}

var (
	reDate   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}\s*$`)
	reLeadNo = regexp.MustCompile(`^\d+\s*`)
	summary  = []string{"汇总", "综合", "决策", "建议"}
)

// gkey grouping key = symbol|date; if there is no symbol, each stands alone (using RID).
func gkey(r Rep) string {
	if r.Symbol != "" {
		return r.Symbol + "|" + r.Date
	}
	return r.RID
}

func isSummary(r Rep) bool {
	s := r.RType + r.Title
	for _, h := range summary {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

// label short tab label: strips the symbol prefix and date suffix.
func label(r Rep) string {
	t := strings.TrimSpace(r.Title)
	if r.Symbol != "" {
		t = strings.Replace(t, r.Symbol, "", 1)
	}
	t = strings.TrimSpace(reDate.ReplaceAllString(t, ""))
	t = strings.TrimSpace(reLeadNo.ReplaceAllString(t, ""))
	if t == "" {
		t = r.RType
	}
	if t == "" {
		t = "报告"
	}
	if r := []rune(t); len(r) > 14 {
		t = string(r[:14])
	}
	return t
}

// buildGroups groups reports by run, sorted by date in descending order. nameOf: code → company name.
func buildGroups(reps []Rep, nameOf func(string) string) []Group {
	m := map[string]*Group{}
	var order []string
	for _, r := range reps {
		k := gkey(r)
		g, ok := m[k]
		if !ok {
			g = &Group{Key: k, Symbol: r.Symbol, Date: r.Date, Source: r.Source, Src: r.Src}
			m[k] = g
			order = append(order, k)
		}
		g.Members = append(g.Members, r)
		if r.Src == "new" {
			g.Src = "new"
		}
	}
	out := make([]Group, 0, len(order))
	for _, k := range order {
		g := m[k]
		sort.SliceStable(g.Members, func(i, j int) bool { return g.Members[i].Time < g.Members[j].Time })
		g.N = len(g.Members)
		g.Time = g.Members[len(g.Members)-1].Time // latest ingest instant in the run
		for _, mm := range g.Members {
			if mm.RType != "" {
				g.Types = append(g.Types, mm.RType)
			}
		}
		g.Kind = runKind(g.Types)
		// Every distinct top-level kind present in the run, ordered by kindOrder —
		// for legacy groups just like new ones (a card shows all kinds, or none).
		ks := map[string]bool{}
		for _, m := range g.Members {
			ks[repKind(m)] = true
		}
		for _, k := range kindOrder {
			if ks[k] {
				g.Kinds = append(g.Kinds, k)
				delete(ks, k)
			}
		}
		for k := range ks {
			g.Kinds = append(g.Kinds, k)
		}
		if nameOf != nil {
			g.CurName = nameOf(g.Symbol)
			g.Name = g.CurName // as-of: prefer a member's ingest-time snapshot, else current
			for _, m := range g.Members {
				if m.Name != "" {
					g.Name = m.Name
					break
				}
			}
		}
		// No stock code/name (thematic/industry reports): surface the original
		// document title so the card is identifiable instead of a bare "报告".
		if g.Symbol == "" && g.Name == "" {
			g.Title = strings.TrimSpace(g.Members[0].Title)
		}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		li, lj := out[i].Members[len(out[i].Members)-1].Time, out[j].Members[len(out[j].Members)-1].Time
		return li > lj
	})
	return out
}
