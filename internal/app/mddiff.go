package app

import "strings"

// Comparing two editions of the same analysis.
//
// The pipeline regenerates the same report for the same symbol over and over, so the DELTA is the
// news — a target price that moved, a risk that appeared, a section that was dropped. Finding that
// by reading two markdown documents side by side is what nobody actually does.
//
// Format-agnostic by construction. It matches on the documents' OWN headings rather than on any
// schema, so it works across every report type without knowing anything about them. That is the
// answer to "our reports come in all kinds of formats": nothing here needs to understand them, only
// that markdown has headings.

// DiffLine is one line of a changed section. Op is "+", "-" or " ".
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// SectionDiff is what happened to one section between the two documents.
type SectionDiff struct {
	Heading string     `json:"heading"` // "" for the text before the first heading
	Level   int        `json:"level"`
	Status  string     `json:"status"` // same | changed | added | removed
	Lines   []DiffLine `json:"lines"`  // populated for "changed" only
}

type mdSection struct {
	heading string
	level   int
	body    []string
}

// splitSections cuts a document at its headings. Lines inside a fenced code block are never
// headings, or a table or mermaid diagram containing a '#' line would be cut in half.
func splitSections(doc string) []mdSection {
	out := []mdSection{{}}
	fenced := false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(trimmed, "#") {
			if level := len(trimmed) - len(strings.TrimLeft(trimmed, "#")); level >= 1 && level <= 6 {
				if rest := strings.TrimSpace(trimmed[level:]); rest != "" {
					out = append(out, mdSection{heading: rest, level: level})
					continue
				}
			}
		}
		out[len(out)-1].body = append(out[len(out)-1].body, line)
	}
	// Drop a leading preamble that holds nothing, so a document starting with a heading does not
	// report an empty section before it.
	if len(out) > 1 && strings.TrimSpace(strings.Join(out[0].body, "\n")) == "" {
		out = out[1:]
	}
	return out
}

// diffMarkdown compares two documents section by section. Sections are returned in the NEWER
// document's order, with anything only in the older one appended — so the result reads like the
// current report, with the losses at the end.
func diffMarkdown(before, after string) []SectionDiff {
	old, new_ := splitSections(before), splitSections(after)

	// Match by heading, and by ORDER among sections that share one: two "风险" sections must pair
	// first-with-first rather than collapse into a single entry.
	used := make([]bool, len(old))
	findOld := func(heading string) int {
		for i, s := range old {
			if !used[i] && s.heading == heading {
				used[i] = true
				return i
			}
		}
		return -1
	}

	out := make([]SectionDiff, 0, len(new_)+len(old))
	for _, ns := range new_ {
		i := findOld(ns.heading)
		if i < 0 {
			// A new section carries its whole body as additions. Announcing only that a heading
			// appeared tells a reader a section exists without telling them what it says, which is
			// the one thing they opened this to find out.
			out = append(out, SectionDiff{Heading: ns.heading, Level: ns.level, Status: "added",
				Lines: allLines("+", ns.body)})
			continue
		}
		if bodyEqual(old[i].body, ns.body) {
			out = append(out, SectionDiff{Heading: ns.heading, Level: ns.level, Status: "same"})
			continue
		}
		out = append(out, SectionDiff{Heading: ns.heading, Level: ns.level, Status: "changed",
			Lines: diffLines(old[i].body, ns.body)})
	}
	for i, os := range old {
		if !used[i] {
			out = append(out, SectionDiff{Heading: os.heading, Level: os.level, Status: "removed",
				Lines: allLines("-", os.body)})
		}
	}
	return out
}

// allLines marks a whole section body as one kind of change, for a section that is entirely new or
// entirely gone.
func allLines(op string, body []string) []DiffLine {
	body = trimBlank(body)
	out := make([]DiffLine, 0, len(body))
	for _, l := range body {
		out = append(out, DiffLine{op, l})
	}
	return out
}

func bodyEqual(a, b []string) bool {
	return strings.TrimSpace(strings.Join(a, "\n")) == strings.TrimSpace(strings.Join(b, "\n"))
}

// diffLines is a plain longest-common-subsequence diff over lines, keeping the unchanged ones as
// context — without them a reader cannot see WHERE in the section the change is.
//
// Quadratic in the section length, which is the right trade here: sections are paragraphs, not
// files, and the alternative (Myers) is a lot of machinery for documents this size.
func diffLines(a, b []string) []DiffLine {
	a, b = trimBlank(a), trimBlank(b)
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{" ", a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"-", a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"+", b[j]})
	}
	return out
}

// trimBlank drops leading and trailing blank lines, which are an artifact of where the headings
// fell rather than a difference anyone wants reported.
func trimBlank(v []string) []string {
	for len(v) > 0 && strings.TrimSpace(v[0]) == "" {
		v = v[1:]
	}
	for len(v) > 0 && strings.TrimSpace(v[len(v)-1]) == "" {
		v = v[:len(v)-1]
	}
	return v
}
