package app

import "testing"

// review_point is free text — the workflow writes things like "2026-10-31 三季报", "2026Q3", or a
// whole sentence. A queue still needs to answer "what should I look at today", so a date is pulled
// out of it opportunistically: when one is there it is a real due date, and when it is not the item
// falls back to its age, which the portal always knows because it stamped created_at on ingest.
//
// Parsing is deliberately narrow. Guessing a due date wrong is worse than admitting there isn't one:
// it would sort a note to the top of someone's day for no reason.

func TestDueDateFromAReviewPoint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-10-31 三季报", "2026-10-31"},
		{"2026-10-31", "2026-10-31"},
		{"复盘点：2026/09/30 之前", "2026-09-30"},
		{"2026年10月31日", "2026-10-31"},
		{"2026年9月1日检查", "2026-09-01"},
		{"see 2026-1-5", "2026-01-05"},
		// No date, or nothing a date can be trusted from.
		{"", ""},
		{"三季报发布后", ""},
		{"Q3", ""},
		{"2026", ""},
		{"2026-13-01", ""}, // month 13 is not a date
		{"2026-02-30", ""}, // February has no 30th
	} {
		if got := trackingDueDate(tc.in); got != tc.want {
			t.Errorf("trackingDueDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Sorting the queue: items with a due date come first, oldest due first, and everything else
// follows by age. An item nobody gave a date to must never outrank one that is actually due.
func TestQueueOrdersByDueThenAge(t *testing.T) {
	s, internal, _ := trackingFixture(t)
	s.st.SetTracking(internal, "600519", []TrackingItem{
		{IType: "assumption", Content: "无日期的老假设"},
		{IType: "assumption", Content: "十月到期", ReviewPoint: "2026-10-31 三季报"},
		{IType: "assumption", Content: "九月到期", ReviewPoint: "2026-09-30"},
	})

	rows, _ := s.st.ListTracking(TrackingFilter{Symbol: "600519", Sort: "due"}, nil)
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Content != "九月到期" || rows[1].Content != "十月到期" {
		t.Errorf("dated items are out of order: %q then %q", rows[0].Content, rows[1].Content)
	}
	if rows[2].Content != "无日期的老假设" {
		t.Errorf("an undated item outranked a due one: %q", rows[2].Content)
	}
	// The parsed date is handed to the UI so it can show and colour it.
	if rows[0].Due != "2026-09-30" {
		t.Errorf("Due = %q, want the parsed date", rows[0].Due)
	}
	if rows[2].Due != "" {
		t.Errorf("Due = %q for an item with no date in its review point", rows[2].Due)
	}
}
