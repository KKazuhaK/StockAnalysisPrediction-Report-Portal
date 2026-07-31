package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The browser-facing review API. Session auth, and — unlike the v1 machine surface, which runs on
// an ingest token that already has everything — scoped to what the caller may read.

func trackingGET(t *testing.T, s *Server, user, query string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiTracking(rec, httptest.NewRequest(http.MethodGet, "/api/tracking?"+query, nil), user)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestTrackingAPIReturnsTheQueueWithItsVocabulary(t *testing.T) {
	s, _, _ := trackingFixture(t)
	out := trackingGET(t, s, "admin", "")

	items, _ := out["items"].([]any)
	if len(items) != 3 || out["total"].(float64) != 3 {
		t.Fatalf("items=%d total=%v", len(items), out["total"])
	}
	// The filters are built from the values actually present, because the ingest contract lets a
	// workflow send any string and a hardcoded list would hide whatever it started emitting.
	itypes, _ := out["itypes"].([]any)
	if len(itypes) != 2 {
		t.Errorf("itypes = %v, want the two present in the data", itypes)
	}
	counts, _ := out["counts"].(map[string]any)
	if counts["pending"] != float64(2) {
		t.Errorf("counts = %v", counts)
	}
	// A row carries enough to act on without a second request.
	first, _ := items[0].(map[string]any)
	for _, k := range []string{"id", "symbol", "content", "status", "report_id", "report_title", "due"} {
		if _, ok := first[k]; !ok {
			t.Errorf("row is missing %q: %v", k, first)
		}
	}
}

// The security property, and the reason this cannot just reuse the v1 handler.
func TestTrackingAPIRefusesToReviewWhatYouCannotRead(t *testing.T) {
	s, internal, external := trackingFixture(t)
	st := s.st
	st.UpsertUser(User{Username: "client@corp.example", PasswordHash: "h", Role: "user"})
	st.SetUserRestricted("client@corp.example", true)
	st.SetVersionGrants("对外版", []string{userPrincipal("client@corp.example")})
	st.AddReportViewer(external, "2026-07-28", "client@corp.example", 0)

	// They see only their own report's item.
	out := trackingGET(t, s, "client@corp.example", "")
	if out["total"].(float64) != 1 {
		t.Fatalf("the client sees %v items, want 1", out["total"])
	}

	// And cannot review an item belonging to a report they may not read, even knowing its id.
	// Target a specific item and compare it against ITSELF afterwards. The fixture seeds one
	// internal item as already-confirmed, so asserting on "the first row" would have passed
	// whatever the handler did.
	mine, _ := st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	if len(mine) != 1 || mine[0].Status != "pending" {
		t.Fatalf("fixture lookup: %+v", mine)
	}
	rec := httptest.NewRecorder()
	s.apiTrackingUpdate(rec, patchReq(mine[0].ID, `{"status":"confirmed"}`), "client@corp.example")
	if rec.Code != http.StatusNotFound {
		t.Errorf("reviewing another tenant's item → %d, want 404", rec.Code)
	}
	after, _ := st.ListTracking(TrackingFilter{Q: "白酒动销回暖"}, nil)
	if after[0].Status != "pending" {
		t.Errorf("the item was modified despite the refusal: status=%q", after[0].Status)
	}
	_ = internal

	// The owner of a readable item can review it.
	theirs, _ := st.ListTracking(TrackingFilter{Symbol: "000001"}, nil)
	rec = httptest.NewRecorder()
	s.apiTrackingUpdate(rec, patchReq(theirs[0].ID, `{"status":"confirmed","review_point":"看过了"}`),
		"client@corp.example")
	if rec.Code != http.StatusOK {
		t.Fatalf("reviewing their own item → %d", rec.Code)
	}
	done, _ := st.ListTracking(TrackingFilter{Symbol: "000001"}, nil)
	if done[0].Status != "confirmed" || done[0].ReviewPoint != "看过了" {
		t.Errorf("the review did not stick: %+v", done[0])
	}
}

// 404, not 403: telling someone an id exists but is not theirs is itself a disclosure.
func TestTrackingAPIDoesNotConfirmIdsExist(t *testing.T) {
	s, _, _ := trackingFixture(t)
	s.st.UpsertUser(User{Username: "client@corp.example", PasswordHash: "h", Role: "user"})
	s.st.SetUserRestricted("client@corp.example", true)

	real, _ := s.st.ListTracking(TrackingFilter{}, nil)
	rec1, rec2 := httptest.NewRecorder(), httptest.NewRecorder()
	s.apiTrackingUpdate(rec1, patchReq(real[0].ID, `{"status":"x"}`), "client@corp.example")
	s.apiTrackingUpdate(rec2, patchReq(999999, `{"status":"x"}`), "client@corp.example")
	if rec1.Code != rec2.Code {
		t.Errorf("an existing-but-forbidden id answers %d and a missing one %d — that is an oracle",
			rec1.Code, rec2.Code)
	}
}

func patchReq(id int64, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPatch, "/api/tracking/"+itoa(id), strings.NewReader(body))
	r.SetPathValue("id", itoa(id))
	return r
}
