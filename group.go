package main

import (
	"regexp"
	"sort"
	"strings"
)

// Group 一次 run（同 标的+日期）收成一张卡。
type Group struct {
	Key, Symbol, Date string
	Name              string   // 公司名（代码→名映射）
	Kind              string   // 这次 run 的类别（并购重组/综合分析/…）
	Kinds             []string // 新报告：该票该日包含的多个大类
	Source, Src       string
	Members           []Rep
	N                 int
	Types             []string
}

// runKind 从报告类型推断"这次跑的是什么"。
func runKind(types []string) string {
	j := strings.Join(types, "")
	switch {
	case strings.Contains(j, "重组"):
		return "并购重组"
	case strings.Contains(j, "深度研究") || strings.Contains(j, "DeepResearch"):
		return "深度研究"
	case strings.Contains(j, "技术分析"):
		return "技术分析"
	case strings.Contains(j, "事件监测"):
		return "事件监测"
	case strings.Contains(j, "投资决策") || strings.Contains(j, "估值") ||
		strings.Contains(j, "财务") || strings.Contains(j, "行业") || strings.Contains(j, "研报"):
		return "投资决策"
	}
	if len(types) > 0 {
		return types[0]
	}
	return "研报"
}

var (
	reDate   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}\s*$`)
	reLeadNo = regexp.MustCompile(`^\d+\s*`)
	summary  = []string{"汇总", "综合", "决策", "建议"}
)

// gkey 分组键 = 标的|日期；无标的则各自独立(用 RID)。
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

// label tab 短标签：去掉标的前缀和日期后缀。
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

// buildGroups 把报告按 run 分组，按日期倒序。nameOf: 代码→公司名。
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
		for _, mm := range g.Members {
			if mm.RType != "" {
				g.Types = append(g.Types, mm.RType)
			}
		}
		g.Kind = runKind(g.Types)
		if g.Src == "new" { // 新报告：该票该日的多个大类
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
		} else {
			g.Kinds = []string{g.Kind}
		}
		if nameOf != nil {
			g.Name = nameOf(g.Symbol)
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
