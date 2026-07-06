package app

import (
	"strings"
	"testing"
)

// dayExportEntries must order a day's reports the way the stock page reads them
// (category order, summary tabs first, then by time) and give each a numbered,
// unique base name so the ZIP lists them in that order without filename collisions.
func TestDayExportEntriesOrdersAndNumbers(t *testing.T) {
	// Scrambled input; expected reading order is r3, r2, r1, r4.
	r1 := Rep{Kind: "投资决策", RType: "财务分析", Title: "财务分析", Time: "09:00"}
	r2 := Rep{Kind: "投资决策", RType: "投资决策建议", Title: "投资决策建议", Time: "08:00"}   // summary (建议)
	r3 := Rep{Kind: "重组决策", RType: "重组基本面分析", Title: "重组基本面分析", Time: "07:00"} // 重组决策 sorts first
	r4 := Rep{Kind: "深度研究", RType: "调研纪要", Title: "调研纪要", Time: "10:00"}       // 深度研究 sorts last

	got := dayExportEntries([]Rep{r1, r2, r3, r4})
	want := []string{
		"01_重组基本面分析",
		"02_投资决策建议",
		"03_财务分析",
		"04_调研纪要",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for i, e := range got {
		if e.base != want[i] {
			t.Errorf("entry %d base = %q, want %q", i, e.base, want[i])
		}
		if seen[e.base] {
			t.Errorf("duplicate base name %q", e.base)
		}
		seen[e.base] = true
	}
}

// A running index makes even identical labels produce distinct entries.
func TestDayExportEntriesUniqueOnDuplicateLabels(t *testing.T) {
	dup := Rep{Kind: "重组决策", RType: "交易分析", Title: "交易分析", Time: "01:00"}
	got := dayExportEntries([]Rep{dup, dup, dup})
	seen := map[string]bool{}
	for _, e := range got {
		if seen[e.base] {
			t.Fatalf("duplicate base name %q despite the NN_ prefix", e.base)
		}
		seen[e.base] = true
	}
	if got[0].base != "01_交易分析" || got[2].base != "03_交易分析" {
		t.Errorf("bases = %q/%q, want 01_/03_ prefixes", got[0].base, got[2].base)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		`a/b:c*d`:      "a_b_c_d",
		`研报/分析`:        "研报_分析",
		`   ..名字..   `: "名字",
		``:             "_",
		`///`:          "_",
		`x?<>|"y`:      "x_y",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	// Result must never contain a path separator (would break the ZIP layout).
	if strings.ContainsAny(sanitizeFilename(`deep/nested\name`), `/\`) {
		t.Error("sanitizeFilename left a path separator in the result")
	}
}
