package app

import "testing"

// What a report is CALLED in the report-type strip, and what it is called in a day-export filename.
//
// These were one function until the type strip started showing titles long enough to be clipped. A
// tab names a report TYPE — short, stable, the same on every stock — while an exported file is kept
// and read on its own, so it wants the report's own subject. Sharing one clamp between the two gave
// the strip a 14-rune ceiling, and a title whose last characters are a version suffix came out as a
// different, entirely plausible version: a 15-rune title ending "V3.15" was shown as "V3.1". A tab
// cannot be read as truncated when the truncation leaves a well-formed value behind.

func TestTabLabelNamesTheTypeNotTheTitle(t *testing.T) {
	for name, tc := range map[string]struct {
		in   Rep
		want string
	}{
		// The case this exists for: the type is short and exact, and no clamp can reach it.
		"a decision report whose title carries a version suffix": {
			in:   Rep{RType: "投资决策建议", Symbol: "000021", Title: "000021 投资研究与决策报告 V3.15"},
			want: "投资决策建议",
		},
		// Unchanged for every ordinary report: these already showed their type, because their title
		// was the symbol plus that same type.
		"a report whose title is the symbol plus its type": {
			in:   Rep{RType: "研报分析", Symbol: "000021", Title: "000021 研报分析"},
			want: "研报分析",
		},
		// A thematic report has a subject of its own and no symbol. The type still names the tab —
		// the subject is what the tooltip and the reader's own header are for.
		"a thematic report": {
			in:   Rep{RType: "综合深度研究", Title: "五家A股创新药企深度对比分析"},
			want: "综合深度研究",
		},
		// Fallback: a row with no type at all still has to be called something, and its title is
		// the only thing left that describes it. It is NOT clipped — that is what produced the
		// misleading version in the first place.
		"a typeless row falls back to its title, uncut": {
			in:   Rep{Symbol: "000021", Title: "000021 投资研究与决策报告 V3.15"},
			want: "投资研究与决策报告 V3.15",
		},
		"a row with neither type nor title": {
			in:   Rep{Symbol: "000021"},
			want: "报告",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tabLabel(tc.in); got != tc.want {
				t.Errorf("tabLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExportNameKeepsTheTitleItIsNamedFor(t *testing.T) {
	// An exported file is named for the report, not for its type: two reports of one type in the
	// same ZIP are told apart by what they say, and only the running NN_ prefix guarantees
	// uniqueness. The old 14-rune clamp cut this title one rune short of its version suffix, turning
	// "V3.15" into "V3.1" — the same misleading version, written into a file the reader keeps.
	got := exportName(Rep{RType: "投资决策建议", Symbol: "000021", Title: "000021 投资研究与决策报告 V3.15"})
	if want := "投资研究与决策报告 V3.15"; got != want {
		t.Errorf("exportName = %q, want %q", got, want)
	}
}

func TestExportNameStaysWithinAFilesystemSafeLength(t *testing.T) {
	// The clamp that remains is a filesystem limit, not a display one: a path component must fit in
	// 255 BYTES on every platform we ship. The cap counts runes, so the binding case is the widest
	// rune a title can hold, not the common one — a CJK Extension B character (U+20000 and up, used
	// in names and in rare hanzi) is 4 bytes in UTF-8 where a BMP hanzi is 3. Measuring with a
	// 3-byte rune lets an unsafe cap pass: at 80 runes it reports 247 bytes and at 4 bytes per rune
	// the same name is 327. Titles do run this long — the longest in production is 232 runes.
	for name, filler := range map[string]rune{
		"BMP hanzi (3 bytes)":   '深',
		"Extension B (4 bytes)": '\U00020000',
	} {
		t.Run(name, func(t *testing.T) {
			long := make([]rune, 400)
			for i := range long {
				long[i] = filler
			}
			got := exportName(Rep{RType: "综合深度研究", Title: string(long)})
			if n := len([]rune(got)); n != exportNameMaxRunes {
				t.Errorf("exportName kept %d runes, want the %d-rune cap", n, exportNameMaxRunes)
			}
			// "NN_" + name + ".pdf" must still be a legal path component. sanitizeFilename can only
			// shrink it (it substitutes single bytes), so measuring the bare name is the bound.
			if n := len("99_") + len(got) + len(".pdf"); n > 255 {
				t.Errorf("worst-case entry name is %d bytes, want <= 255 (cap is %d runes)", n, exportNameMaxRunes)
			}
		})
	}
}

func TestTabsAndExportsAreNamedByDifferentThings(t *testing.T) {
	// The whole point of the split. One report, two names: the strip says what KIND of report this
	// is, the ZIP entry says WHICH report it is. Pointing either call site at the other function
	// leaves every other test in this package green — day_export's own tests use fixtures whose
	// title IS their type, so they cannot tell the two apart.
	r := Rep{RType: "投资决策建议", Symbol: "000021", Title: "000021 投资研究与决策报告 V3.15"}
	if got, want := tabLabel(r), "投资决策建议"; got != want {
		t.Errorf("tabLabel = %q, want the type %q", got, want)
	}
	if got, want := exportName(r), "投资研究与决策报告 V3.15"; got != want {
		t.Errorf("exportName = %q, want the report %q", got, want)
	}
	if tabLabel(r) == exportName(r) {
		t.Error("tabLabel and exportName agree on a report whose title differs from its type; the split is not wired")
	}
}
