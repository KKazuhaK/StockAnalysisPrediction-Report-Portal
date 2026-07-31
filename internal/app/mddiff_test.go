package app

import "testing"

// Comparing two editions of the same analysis.
//
// The pipeline regenerates the same report for the same symbol again and again, so the DELTA is the
// news: a target price that moved, a risk that appeared, a section that was dropped. Reading two
// markdown documents side by side to find that by eye is what nobody does.
//
// This is deliberately format-agnostic. It matches on the documents' OWN headings rather than on
// any schema, so it works across the nine report types without knowing anything about them — the
// objection that "our reports have all kinds of formats" is exactly why it is done this way.

func sectionsByHeading(d []SectionDiff) map[string]SectionDiff {
	out := map[string]SectionDiff{}
	for _, s := range d {
		out[s.Heading] = s
	}
	return out
}

func TestDiffClassifiesSectionsByHeading(t *testing.T) {
	before := "# 结论\n买入\n\n## 风险\n政策风险\n\n## 估值\nPE 20x\n"
	after := "# 结论\n买入\n\n## 风险\n政策风险\n汇率风险\n\n## 催化剂\n三季报\n"

	got := sectionsByHeading(diffMarkdown(before, after))
	if s := got["结论"]; s.Status != "same" {
		t.Errorf("unchanged section reported as %q", s.Status)
	}
	if s := got["风险"]; s.Status != "changed" {
		t.Errorf("edited section reported as %q", s.Status)
	}
	if s := got["估值"]; s.Status != "removed" {
		t.Errorf("dropped section reported as %q", s.Status)
	}
	if s := got["催化剂"]; s.Status != "added" {
		t.Errorf("new section reported as %q", s.Status)
	}
	// An added or removed section carries its body too: announcing only that a heading appeared
	// tells a reader a section exists without telling them what it says.
	if s := got["催化剂"]; len(s.Lines) != 1 || s.Lines[0].Op != "+" || s.Lines[0].Text != "三季报" {
		t.Errorf("new section did not carry its content: %+v", s.Lines)
	}
	if s := got["估值"]; len(s.Lines) != 1 || s.Lines[0].Op != "-" || s.Lines[0].Text != "PE 20x" {
		t.Errorf("dropped section did not carry what was lost: %+v", s.Lines)
	}
}

func TestDiffShowsTheChangedLines(t *testing.T) {
	got := sectionsByHeading(diffMarkdown("## 估值\n目标价 48 元\n维持\n", "## 估值\n目标价 55 元\n维持\n"))
	s := got["估值"]
	if s.Status != "changed" {
		t.Fatalf("status = %q", s.Status)
	}
	var added, removed, same []string
	for _, l := range s.Lines {
		switch l.Op {
		case "+":
			added = append(added, l.Text)
		case "-":
			removed = append(removed, l.Text)
		default:
			same = append(same, l.Text)
		}
	}
	if len(removed) != 1 || removed[0] != "目标价 48 元" {
		t.Errorf("removed = %v", removed)
	}
	if len(added) != 1 || added[0] != "目标价 55 元" {
		t.Errorf("added = %v", added)
	}
	// Context is kept, or a reader cannot see where in the section the change is.
	if len(same) != 1 || same[0] != "维持" {
		t.Errorf("unchanged context = %v", same)
	}
}

// Order is the reader's, not the algorithm's: sections come in the order of the NEWER document,
// with anything only in the older one appended, so the diff reads like the current report.
func TestDiffFollowsTheNewerDocumentsOrder(t *testing.T) {
	d := diffMarkdown("## B\nx\n\n## A\ny\n", "## A\ny\n\n## C\nz\n")
	var order []string
	for _, s := range d {
		order = append(order, s.Heading)
	}
	if len(order) != 3 || order[0] != "A" || order[1] != "C" || order[2] != "B" {
		t.Errorf("order = %v, want [A C B]", order)
	}
}

func TestDiffHandlesTheAwkwardShapes(t *testing.T) {
	// Text before any heading is a section of its own, not dropped.
	if d := diffMarkdown("开头一段\n", "开头两段\n"); len(d) != 1 || d[0].Heading != "" || d[0].Status != "changed" {
		t.Errorf("preamble handling: %+v", d)
	}
	// Two sections can share a heading; they must not collapse into one.
	d := diffMarkdown("## 风险\na\n\n## 风险\nb\n", "## 风险\na\n\n## 风险\nc\n")
	if len(d) != 2 {
		t.Fatalf("duplicate headings collapsed: %d sections", len(d))
	}
	if d[0].Status != "same" || d[1].Status != "changed" {
		t.Errorf("duplicates matched out of order: %q then %q", d[0].Status, d[1].Status)
	}
	// Identical documents produce no changes at all, which is what lets the UI say "nothing moved".
	for _, s := range diffMarkdown("# A\nx\n", "# A\nx\n") {
		if s.Status != "same" {
			t.Errorf("identical documents reported %q", s.Status)
		}
	}
	// Empty input is not a crash.
	diffMarkdown("", "")
	diffMarkdown("", "# A\nx\n")
	diffMarkdown("# A\nx\n", "")
}

// A fenced code block can contain lines that look like headings. Splitting on them would cut a
// table or a mermaid diagram in half and report nonsense.
func TestDiffIgnoresHeadingsInsideCodeFences(t *testing.T) {
	doc := "## 图\n```\n# not a heading\n```\n"
	d := diffMarkdown(doc, doc)
	if len(d) != 1 || d[0].Heading != "图" {
		t.Errorf("a fenced '#' line was treated as a heading: %+v", d)
	}
}
