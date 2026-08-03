package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// TestRunsTodayCountsRowsSincePanelMidnight locks the daily-run counter (ADR 0022 R2): it sums ROWS
// (batch_jobs.total), not jobs, so a multi-row submit cannot dodge the cap; it counts only the
// caller's own jobs; and it counts only jobs created since the panel-tz civil midnight.
func TestRunsTodayCountsRowsSincePanelMidnight(t *testing.T) {
	st := newTestStore(t)
	tgt, err := st.CreateTarget(difyPluginSlug, "t", "{}")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(user string, rows int, createdAt string) {
		rs := make([]map[string]string, rows)
		for i := range rs {
			rs[i] = map[string]string{"symbol": "600000"}
		}
		id, err := st.CreateBatchJob(tgt, 1, 0, user, rs, "normal")
		if err != nil {
			t.Fatal(err)
		}
		if createdAt != "" { // re-stamp to test the day boundary
			if _, err := st.exec("UPDATE batch_jobs SET created_at=? WHERE id=?", createdAt, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	midnight := time.Now().Truncate(24 * time.Hour) // local civil midnight is what the store format uses
	yesterday := midnight.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")

	mk("ext", 1, "")        // today, 1 row
	mk("ext", 3, "")        // today, 3 rows → rows, not jobs
	mk("ext", 5, yesterday) // before today's boundary → not counted
	mk("other", 4, "")      // another user → not counted

	if n := st.RunsToday("ext", midnight); n != 4 {
		t.Errorf("RunsToday = %d, want 4 (1+3 rows today, excluding yesterday and other users)", n)
	}
	if n := st.RunsToday("nobody", midnight); n != 0 {
		t.Errorf("RunsToday for a user with no jobs = %d, want 0", n)
	}
}

// TestRunQuotaGate locks the enforcement decision: restricted users are capped by their effective
// OU quota (rows counted for the whole submit), internal users and admins are never capped, and a
// quota of 0 means unlimited.
func TestRunQuotaGate(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	root := st.EnsureDefaultGroup()
	ou, _ := st.CreateUserGroup("ext-org", "", 0)
	st.SetGroupParent(ou, root)
	st.SetGroupRestricted(ou, true)
	st.SetGroupDailyQuota(ou, quotaPtr(2), QuotaDay)

	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "operator"})
	st.SetPrimaryGroup("ext", ou)
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "operator"}) // internal
	st.UpsertUser(User{Username: "boss", PasswordHash: "h", Role: "admin"})
	st.SetPrimaryGroup("boss", ou) // admin inside a restricted OU stays exempt

	// Nothing run yet: a 2-row submit fits exactly, a 3-row submit does not.
	if lim, used, ok := s.runQuotaCheck("ext", 2); !ok {
		t.Errorf("2 rows within a quota of 2 must pass (limit %d used %d)", lim, used)
	}
	if _, _, ok := s.runQuotaCheck("ext", 3); ok {
		t.Error("3 rows against a quota of 2 must be refused")
	}
	// Internal and admin users are never capped, whatever the size.
	if _, _, ok := s.runQuotaCheck("staff", 99); !ok {
		t.Error("an internal user must never be rate-limited")
	}
	if _, _, ok := s.runQuotaCheck("boss", 99); !ok {
		t.Error("an admin must never be rate-limited")
	}

	// Consume the quota, then even a 1-row submit is refused.
	tgt, _ := st.CreateTarget(difyPluginSlug, "t", "{}")
	st.CreateBatchJob(tgt, 1, 0, "ext", []map[string]string{{"s": "1"}, {"s": "2"}}, "normal")
	lim, used, ok := s.runQuotaCheck("ext", 1)
	if ok {
		t.Error("a submit past the daily quota must be refused")
	}
	if lim != 2 || used != 2 {
		t.Errorf("quota report = limit %d used %d, want 2/2", lim, used)
	}

	// quota 0 = unlimited.
	st.SetGroupDailyQuota(ou, quotaPtr(0), QuotaDay)
	if _, _, ok := s.runQuotaCheck("ext", 50); !ok {
		t.Error("a quota of 0 means unlimited")
	}
}

// TestApiBatchJobCreateEnforcesQuota proves the gate actually fires at the submit endpoint: a
// restricted user's third run of a 2/day quota is refused with 429 + a machine-readable payload,
// while an internal user submitting the same body is unaffected.
func TestApiBatchJobCreateEnforcesQuota(t *testing.T) {
	s := batchServer(t)
	st := s.st
	root := st.EnsureDefaultGroup()
	ou, _ := st.CreateUserGroup("ext-org", "", 0)
	st.SetGroupParent(ou, root)
	st.SetGroupRestricted(ou, true)
	st.SetGroupDailyQuota(ou, quotaPtr(2), QuotaDay)
	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "operator"})
	st.SetPrimaryGroup("ext", ou)
	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "operator"})

	if err := st.UpsertPlugin("p", "P", "1.0.0", batchTestSpec, "test"); err != nil {
		t.Fatal(err)
	}
	tgt, err := st.CreateTarget("p", "t", "{}")
	if err != nil {
		t.Fatal(err)
	}
	// The OU must be allowed to run this target at all (ADR 0022 R3 is default-deny), otherwise the
	// allow-list gate would 403 before the quota gate under test is ever reached.
	st.SetGroupTargets(ou, []GroupTarget{{TargetID: tgt, Surfaces: ""}})
	body := fmt.Sprintf(`{"target_id":%d,"rows":[{"code":"600000"}]}`, tgt)
	submit := func(user string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/api/admin/batch/jobs", strings.NewReader(body)), user)
		return rec
	}

	for i := 1; i <= 2; i++ { // the first two runs fit the quota
		if rec := submit("ext"); rec.Code != http.StatusOK {
			t.Fatalf("restricted run %d → %d (%s), want 200", i, rec.Code, rec.Body.String())
		}
	}
	rec := submit("ext")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third restricted run → %d (%s), want 429", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	json.Unmarshal(rec.Body.Bytes(), &payload)
	// The machine token belongs in "code"; "error" is what the toast prints when the client has no
	// translation for the code, so a raw "rate_limited" there is a user-visible bug.
	if payload["code"] != "quota_exceeded" {
		t.Errorf("429 payload code = %v, want quota_exceeded", payload["code"])
	}
	if msg, _ := payload["error"].(string); msg == "" || msg == "quota_exceeded" || msg == "rate_limited" {
		t.Errorf("429 payload error = %q, want a human sentence", msg)
	}
	if payload["limit"] != float64(2) || payload["used"] != float64(2) {
		t.Errorf("429 payload limit/used = %v/%v, want 2/2", payload["limit"], payload["used"])
	}
	if payload["resets_at"] == "" || payload["resets_at"] == nil {
		t.Error("429 payload must carry resets_at")
	}

	// An internal user is never rate-limited, even after the restricted user exhausted theirs.
	for i := 1; i <= 3; i++ {
		if rec := submit("staff"); rec.Code != http.StatusOK {
			t.Fatalf("internal run %d → %d (%s), want 200", i, rec.Code, rec.Body.String())
		}
	}
}
