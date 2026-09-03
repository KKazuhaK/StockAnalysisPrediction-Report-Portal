package app

import (
	"strings"
	"testing"
)

// What the report-type strip calls each tab (orderAndDefault).
//
// Two tabs can share a label for two reasons that need different answers, and until v0.4.44 both got
// a number. A number is right for two DIFFERENT reports of one type; it says nothing at all about
// two written forms of ONE analysis (ADR 0024), which is what a hand-written correction produces
// (ADR 0026). Before this, correcting a report by hand made it show up next to the workflow's own as
// "深度分析 2", with no way for a reader to tell which was which.

func labelsOf(t *testing.T, s *Server, in []Rep) []string {
	t.Helper()
	out, _ := s.orderAndDefault(in)
	got := make([]string, 0, len(out))
	for _, r := range out {
		got = append(got, r.Label)
	}
	return got
}

func rep(rtype, title, version, at string) Rep {
	return Rep{RType: rtype, Title: title, Version: version, Time: at, Label: rtype}
}

func TestTabsNameTheWrittenFormAndNumberTheReport(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	for name, tc := range map[string]struct {
		in   []Rep
		want []string
	}{
		// The case this exists for: one analysis, two forms. The reader is told which is which, and
		// the workflow's own keeps the bare label.
		"two forms of one analysis": {
			in: []Rep{
				rep("深度分析", "工作流产出", "default", "2026-09-01T00:00:00Z"),
				rep("深度分析", "工作流产出", "manual", "2026-09-02T00:00:00Z"),
			},
			want: []string{"深度分析", "深度分析 · 人工"},
		},
		// Unchanged: title is part of a report's identity, so one code+date+subtype carries several
		// genuinely different reports and a number is exactly what tells them apart.
		"two different reports of one type": {
			in: []Rep{
				rep("重组交易分析", "甲方案", "default", "2026-09-01T00:00:00Z"),
				rep("重组交易分析", "乙方案", "default", "2026-09-02T00:00:00Z"),
			},
			want: []string{"重组交易分析", "重组交易分析 2"},
		},
		// Both at once. The number counts distinct REPORTS, so the hand-written form of the second
		// one carries the second one's number — it is not a third report.
		"a hand-written form of the second of two reports": {
			in: []Rep{
				rep("重组交易分析", "甲方案", "default", "2026-09-01T00:00:00Z"),
				rep("重组交易分析", "乙方案", "default", "2026-09-02T00:00:00Z"),
				rep("重组交易分析", "乙方案", "manual", "2026-09-03T00:00:00Z"),
			},
			want: []string{"重组交易分析", "重组交易分析 2", "重组交易分析 2 · 人工"},
		},
		// A portal that has never used versions reads exactly as it always did — every row carries
		// the empty version string, which resolves to the default.
		"reports written before versions existed": {
			in: []Rep{
				rep("深度分析", "a", "", "2026-09-01T00:00:00Z"),
				rep("深度分析", "b", "", "2026-09-02T00:00:00Z"),
			},
			want: []string{"深度分析", "深度分析 2"},
		},
		// One report, one form: nothing is appended to anything.
		"the ordinary case": {
			in:   []Rep{rep("深度分析", "t", "default", "2026-09-01T00:00:00Z")},
			want: []string{"深度分析"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := labelsOf(t, s, tc.in); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("labels = %q, want %q", got, tc.want)
			}
		})
	}
}

// The suffix is the version's own label from the registry, so renaming 人工 in 管理 → 报告版本 renames
// it on the strip too — rather than a second copy of the name living here.
func TestTheFormSuffixFollowsTheVersionRegistry(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	v, _ := st.Version(st.ManualVersion())
	v.Label = "编辑部"
	if err := st.SaveVersion(v); err != nil {
		t.Fatalf("rename version: %v", err)
	}
	got := labelsOf(t, s, []Rep{
		rep("深度分析", "t", "default", "2026-09-01T00:00:00Z"),
		rep("深度分析", "t", "manual", "2026-09-02T00:00:00Z"),
	})
	if strings.Join(got, "|") != "深度分析|深度分析 · 编辑部" {
		t.Fatalf("labels = %q", got)
	}
}

// A version nobody has labelled falls back to its own name rather than to an empty suffix, which
// would read as a trailing separator and say nothing.
func TestAnUnlabelledVersionFallsBackToItsName(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	if err := st.SaveVersion(ReportVersion{Name: "外部版", Ord: 9}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	got := labelsOf(t, s, []Rep{
		rep("深度分析", "t", "default", "2026-09-01T00:00:00Z"),
		rep("深度分析", "t", "外部版", "2026-09-02T00:00:00Z"),
	})
	if strings.Join(got, "|") != "深度分析|深度分析 · 外部版" {
		t.Fatalf("labels = %q", got)
	}
}
