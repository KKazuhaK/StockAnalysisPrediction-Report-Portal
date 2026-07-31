package app

import (
	"testing"
)

// The review queue.
//
// tracking_items has been written on every ingest since the v1 API shipped — the workflow records
// what a report ASSUMED and when that should be checked — and nothing has ever read it back except
// the machine API. A pile of assumptions nobody revisits is the difference between research and
// opinion, so the portal needs to ask "what is due for review", across symbols, not one at a time.
//
// The existing QueryTracking answers only "this symbol's items". Everything below is the
// cross-symbol view, and the property that matters most is the same one the read path has
// everywhere else: an item is visible only if the report it belongs to is.

func trackingFixture(t *testing.T) (*Server, int64, int64) {
	t.Helper()
	s := tenancyServer(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityOwner})

	internal, _, _ := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "内部版", MD: "x"})
	external, _, _ := st.UpsertReport(Rep{Symbol: "000001", Date: "2026-07-28", RType: "投资决策",
		Title: "对外版", Version: "对外版", MD: "y"})

	st.SetTracking(internal, "600519", []TrackingItem{
		{IType: "assumption", Content: "白酒动销回暖", Status: "pending", ReviewPoint: "2026-10-31 三季报"},
		{IType: "risk", Content: "政策风险", Status: "confirmed", ReviewPoint: ""},
	})
	st.SetTracking(external, "000001", []TrackingItem{
		{IType: "assumption", Content: "息差企稳", Status: "pending", ReviewPoint: "2026-09-30"},
	})
	return s, internal, external
}

func TestListTrackingSpansSymbols(t *testing.T) {
	s, _, _ := trackingFixture(t)

	all, total := s.st.ListTracking(TrackingFilter{}, nil)
	if total != 3 || len(all) != 3 {
		t.Fatalf("got %d items (total %d), want 3", len(all), total)
	}
	// The list has to be readable without a second query per row: an assumption with no company or
	// report title beside it is not reviewable.
	for _, it := range all {
		if it.Symbol == "" || it.ReportTitle == "" || it.ReportDate == "" {
			t.Errorf("row lacks the context to act on: %+v", it)
		}
	}

	if _, n := s.st.ListTracking(TrackingFilter{Status: "pending"}, nil); n != 2 {
		t.Errorf("pending = %d, want 2", n)
	}
	if _, n := s.st.ListTracking(TrackingFilter{Symbol: "600519"}, nil); n != 2 {
		t.Errorf("600519 = %d, want 2", n)
	}
	if _, n := s.st.ListTracking(TrackingFilter{IType: "risk"}, nil); n != 1 {
		t.Errorf("risk = %d, want 1", n)
	}
	if _, n := s.st.ListTracking(TrackingFilter{Q: "息差"}, nil); n != 1 {
		t.Errorf("content search = %d, want 1", n)
	}
}

// The security property: tracking rows carry no owner of their own, so visibility has to come from
// the report they belong to — the same rule as every other read path (ADR 0024).
func TestListTrackingIsScopedToWhatTheViewerMayRead(t *testing.T) {
	s, _, external := trackingFixture(t)
	st := s.st
	st.UpsertUser(User{Username: "client@corp.example", PasswordHash: "h", Role: "user"})
	st.SetUserRestricted("client@corp.example", true)
	st.SetVersionGrants("对外版", []string{userPrincipal("client@corp.example")})
	st.AddReportViewer(external, "2026-07-28", "client@corp.example", 0)

	sc := s.viewerScope("client@corp.example")
	if sc == nil {
		t.Fatal("a restricted account must have a scope")
	}
	rows, total := st.ListTracking(TrackingFilter{}, sc)
	if total != 1 || len(rows) != 1 {
		t.Fatalf("the client sees %d items, want only their own report's 1", total)
	}
	if rows[0].Symbol != "000001" {
		t.Errorf("the client sees %q — an internal report's assumptions leaked", rows[0].Symbol)
	}
	// And the counts that drive the UI's tabs must be scoped the same way, or the badge tells them
	// how many items exist that they cannot open.
	if n := st.TrackingStatusCounts(sc)["pending"]; n != 1 {
		t.Errorf("scoped pending count = %d, want 1", n)
	}
	if n := st.TrackingStatusCounts(nil)["pending"]; n != 2 {
		t.Errorf("unscoped pending count = %d, want 2", n)
	}
}

// Reviewing is the whole point: an item must be markable, and the change must stick.
func TestReviewingAnItemRecordsTheOutcome(t *testing.T) {
	s, _, _ := trackingFixture(t)
	rows, _ := s.st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	if len(rows) != 1 {
		t.Fatalf("fixture lookup returned %d rows", len(rows))
	}
	ok, err := s.st.UpdateTrackingStatus(rows[0].ID, "confirmed", "2026-10-31 三季报兑现")
	if err != nil || !ok {
		t.Fatalf("marking reviewed: ok=%v err=%v", ok, err)
	}
	again, _ := s.st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	if again[0].Status != "confirmed" || again[0].ReviewPoint != "2026-10-31 三季报兑现" {
		t.Errorf("the outcome did not stick: %+v", again[0])
	}
	// An id nobody holds reports not-found rather than pretending.
	if ok, _ := s.st.UpdateTrackingStatus(999999, "confirmed", ""); ok {
		t.Error("updating a missing item reported success")
	}
}

// A re-run must not erase a human's judgement. SetTracking clears and rewrites, which is right for
// the CONTENT — the latest body is the truth — but the status and the review note are the one part
// a person put there, and losing them on every regeneration would make the queue worthless: a
// nightly workflow would reset everything anyone had reviewed back to pending.
//
// So an item whose text is unchanged keeps its outcome, and only genuinely new text arrives pending.
func TestARerunKeepsWhatAHumanDecided(t *testing.T) {
	s, internal, _ := trackingFixture(t)
	rows, _ := s.st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	s.st.UpdateTrackingStatus(rows[0].ID, "confirmed", "三季报兑现")

	// The workflow re-runs: same assumption still holds, plus a new one, and the risk item is gone.
	s.st.SetTracking(internal, "600519", []TrackingItem{
		{IType: "assumption", Content: "白酒动销回暖", Status: "pending", ReviewPoint: "2026-10-31 三季报"},
		{IType: "assumption", Content: "新的假设"},
	})

	after, total := s.st.ListTracking(TrackingFilter{Symbol: "600519"}, nil)
	if total != 2 {
		t.Fatalf("after the re-run: %d items, want 2", total)
	}
	byContent := map[string]TrackingRow{}
	for _, r := range after {
		byContent[r.Content] = r
	}
	kept := byContent["白酒动销回暖"]
	if kept.Status != "confirmed" || kept.ReviewPoint != "三季报兑现" {
		t.Errorf("the re-run discarded a human review: status=%q review_point=%q",
			kept.Status, kept.ReviewPoint)
	}
	if fresh := byContent["新的假设"]; fresh.Status != "pending" {
		t.Errorf("a genuinely new assumption arrived as %q, want pending", fresh.Status)
	}
	// The dropped item is gone: the latest body is still the truth about WHICH assumptions exist.
	if _, still := byContent["政策风险"]; still {
		t.Error("an assumption the re-run no longer emits is still listed")
	}
}

// Carrying the outcome over is keyed on the text, so a REWORDED assumption is a new one and starts
// pending — the safe direction. Silently carrying "confirmed" onto a changed claim would be worse
// than losing the review.
func TestARerunDoesNotCarryAReviewOntoChangedText(t *testing.T) {
	s, internal, _ := trackingFixture(t)
	rows, _ := s.st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	s.st.UpdateTrackingStatus(rows[0].ID, "confirmed", "兑现")

	s.st.SetTracking(internal, "600519", []TrackingItem{
		{IType: "assumption", Content: "白酒动销回暖，但节奏慢于预期"},
	})
	after, _ := s.st.ListTracking(TrackingFilter{Symbol: "600519"}, nil)
	if after[0].Status != "pending" {
		t.Errorf("a reworded assumption inherited %q; it must be reviewed again", after[0].Status)
	}
}
