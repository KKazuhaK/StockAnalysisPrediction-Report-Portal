package app

import (
	"testing"
	"time"
)

// agoLocal renders the local "2006-01-02 15:04:05" instant `days` in the past — the basis
// batch_jobs.finished_at / api_tokens.expires_at are stored in.
func agoLocal(days int) string {
	return time.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
}

func seedJob(t *testing.T, st *Store, status, finishedAt string) int64 {
	t.Helper()
	id, err := st.insertID("INSERT INTO batch_jobs(target_id,status,created_at,finished_at) VALUES(?,?,?,?)",
		1, status, "2026-01-01 00:00:00", finishedAt)
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := st.exec("INSERT INTO batch_items(job_id,row_index,inputs,status) VALUES(?,?,?,?)", id, 0, "{}", "succeeded"); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return id
}

func countRows(t *testing.T, st *Store, table string) int64 {
	t.Helper()
	var n int64
	if err := st.queryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// DeleteFinishedJobsBefore removes only terminal jobs (finished/cancelled/EXPIRED) older than the
// cutoff, cascades their items, keeps the boundary row (strict <), and never touches active or
// empty-finished_at jobs. The 'expired' inclusion is the bug DeleteFinishedJobs has.
func TestDeleteFinishedJobsBefore(t *testing.T) {
	st := newTestStore(t)
	cutoff := agoLocal(30)

	keepRecent := seedJob(t, st, "finished", agoLocal(10)) // newer than cutoff → keep
	delFinished := seedJob(t, st, "finished", agoLocal(40)) // older terminal → delete
	delExpired := seedJob(t, st, "expired", agoLocal(40))   // 'expired' must be included
	delCancelled := seedJob(t, st, "cancelled", agoLocal(40))
	keepRunning := seedJob(t, st, "running", "")     // active → keep
	keepEmpty := seedJob(t, st, "finished", "")      // terminal but no finished_at → keep
	keepBoundary := seedJob(t, st, "finished", cutoff) // exactly == cutoff → keep (strict <)

	if n, err := st.CountFinishedJobsBefore(cutoff); err != nil || n != 3 {
		t.Fatalf("CountFinishedJobsBefore = %d,%v; want 3", n, err)
	}
	n, err := st.DeleteFinishedJobsBefore(cutoff)
	if err != nil || n != 3 {
		t.Fatalf("DeleteFinishedJobsBefore = %d,%v; want 3", n, err)
	}
	// Deleted jobs' items are cascaded (3 jobs kept → 4 items remain).
	if got := countRows(t, st, "batch_items"); got != 4 {
		t.Errorf("batch_items after delete = %d; want 4", got)
	}
	got := map[int64]bool{}
	rows, _ := st.query("SELECT id FROM batch_jobs")
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		got[id] = true
	}
	rows.Close()
	for _, id := range []int64{keepRecent, keepRunning, keepEmpty, keepBoundary} {
		if !got[id] {
			t.Errorf("job %d should have survived", id)
		}
	}
	for _, id := range []int64{delFinished, delExpired, delCancelled} {
		if got[id] {
			t.Errorf("job %d should have been deleted", id)
		}
	}
}

// A job requeued (status→queued, finished_at cleared) after a count but before the delete is NOT
// removed: the set-based DELETE re-asserts the terminal predicate, so it no longer matches.
func TestDeleteFinishedJobsBefore_RaceSafe(t *testing.T) {
	st := newTestStore(t)
	cutoff := agoLocal(30)
	id := seedJob(t, st, "finished", agoLocal(40))
	if n, _ := st.CountFinishedJobsBefore(cutoff); n != 1 {
		t.Fatalf("precondition count = %d; want 1", n)
	}
	// Simulate RequeueItems flipping the job back to queued between count and delete.
	st.exec("UPDATE batch_jobs SET status='queued', finished_at='' WHERE id=?", id)
	if n, err := st.DeleteFinishedJobsBefore(cutoff); err != nil || n != 0 {
		t.Fatalf("DeleteFinishedJobsBefore = %d,%v; want 0 (requeued job must survive)", n, err)
	}
	if countRows(t, st, "batch_jobs") != 1 {
		t.Errorf("requeued job was deleted; it must survive")
	}
}

// parseReportInstant covers the sent_at formats seen in production — zoned RFC3339 (`...Z`) and the
// timezone-less microsecond form — plus defensive variants; date-only/empty carry no instant.
func TestParseReportInstant(t *testing.T) {
	ok := []struct{ in, wantUTC string }{
		{"2026-07-02T18:51:21Z", "2026-07-02T18:51:21Z"},              // zoned, no fraction (v1 stamp)
		{"2026-01-08T15:05:39.575420", "2026-01-08T15:05:39.57542Z"}, // no zone + micros → UTC (bulk of prod)
		{"2026-01-08T15:05:39", "2026-01-08T15:05:39Z"},              // no zone, no fraction → UTC
		{"2026-07-02T10:00:00+08:00", "2026-07-02T02:00:00Z"},        // offset → normalized to UTC
		{"2026-01-08 15:05:39", "2026-01-08T15:05:39Z"},              // space form → UTC (defensive)
	}
	for _, c := range ok {
		got, valid := parseReportInstant(c.in)
		if !valid {
			t.Errorf("parseReportInstant(%q) failed; want ok", c.in)
			continue
		}
		if got.UTC().Format(time.RFC3339Nano) != c.wantUTC {
			t.Errorf("parseReportInstant(%q) = %s; want %s", c.in, got.UTC().Format(time.RFC3339Nano), c.wantUTC)
		}
	}
	for _, bad := range []string{"", "2026-07-02", "not-a-time", "2026/07/02 10:00"} {
		if _, valid := parseReportInstant(bad); valid {
			t.Errorf("parseReportInstant(%q) parsed; want fail-closed", bad)
		}
	}
}

// Reports delete is fail-closed: only rows whose sent_at parses to a real instant older than the
// cutoff are eligible — covering BOTH production formats (zoned `...Z` and timezone-less microsecond).
// Date-only / empty / unparseable sent_at (and rdate) are never used.
func TestDeleteReportsIngestedBefore(t *testing.T) {
	st := newTestStore(t)
	mk := func(uid, date, sentAt string) {
		if _, err := st.UpsertReport(Rep{UID: uid, Symbol: "600000", Date: date, RType: "x", Time: sentAt}); err != nil {
			t.Fatalf("seed report %s: %v", uid, err)
		}
	}
	oldZ := time.Now().UTC().AddDate(0, 0, -800).Format(time.RFC3339)             // zoned Z
	oldFrac := time.Now().UTC().AddDate(0, 0, -800).Format("2006-01-02T15:04:05.000000") // no-zone micros
	recentZ := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	recentFrac := time.Now().UTC().AddDate(0, 0, -10).Format("2006-01-02T15:04:05.000000")
	mk("old-z", "2024-01-01", oldZ)                   // eligible (zoned)
	mk("old-frac", "2024-01-01", oldFrac)             // eligible (no-zone micros — the 84% case)
	mk("recent-z", "2026-07-01", recentZ)             // too new → keep
	mk("recent-frac", "2026-07-01", recentFrac)       // too new → keep
	mk("legacy-dateonly", "2020-01-01", "2020-01-01") // date-only → keep (fail-closed)
	mk("empty-sent", "2020-01-01", "")                // empty → keep
	mk("olddate-newinstant", "2000-01-01", recentZ)   // rdate old but sent_at recent → keep
	// tracking items for an eligible report must cascade
	st.exec("INSERT INTO tracking_items(report_uid,symbol,itype,content,status) VALUES(?,?,?,?,?)", "old-frac", "600000", "assumption", "x", "pending")

	cutoff := time.Now().UTC().AddDate(0, 0, -730)
	if n, err := st.CountReportsIngestedBefore(cutoff); err != nil || n != 2 {
		t.Fatalf("CountReportsIngestedBefore = %d,%v; want 2", n, err)
	}
	n, err := st.DeleteReportsIngestedBefore(cutoff)
	if err != nil || n != 2 {
		t.Fatalf("DeleteReportsIngestedBefore = %d,%v; want 2", n, err)
	}
	if countRows(t, st, "reports") != 5 {
		t.Errorf("reports after delete = %d; want 5", countRows(t, st, "reports"))
	}
	if countRows(t, st, "tracking_items") != 0 {
		t.Errorf("tracking_items should have cascaded to 0, got %d", countRows(t, st, "tracking_items"))
	}
	if st.GetByUID("old-z") != nil || st.GetByUID("old-frac") != nil {
		t.Errorf("both old reports should be gone")
	}
	if st.GetByUID("recent-z") == nil || st.GetByUID("recent-frac") == nil || st.GetByUID("legacy-dateonly") == nil || st.GetByUID("empty-sent") == nil {
		t.Errorf("a kept report was wrongly deleted")
	}
}

// The delete re-asserts sent_at: a report re-ingested (fresh sent_at) between the scan and the
// delete is skipped, not destroyed — preserving the fail-closed guarantee across the window.
func TestDeleteReportChunk_ReingestSkipped(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -800).Format(time.RFC3339)
	fresh := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	// The report currently carries a FRESH sent_at (it was just re-ingested)...
	if _, err := st.UpsertReport(Rep{UID: "r1", Symbol: "600000", Date: "2024-01-01", RType: "x", Time: fresh}); err != nil {
		t.Fatal(err)
	}
	st.exec("INSERT INTO tracking_items(report_uid,symbol,itype,content,status) VALUES(?,?,?,?,?)", "r1", "600000", "assumption", "x", "pending")

	// ...but the purge holds a STALE key from the earlier scan → must skip it.
	if n, err := st.deleteReportChunk([]reportKey{{uid: "r1", sent: old}}); err != nil || n != 0 {
		t.Fatalf("stale-key delete = %d,%v; want 0", n, err)
	}
	if st.GetByUID("r1") == nil {
		t.Fatal("re-ingested report was wrongly deleted")
	}
	if countRows(t, st, "tracking_items") != 1 {
		t.Errorf("tracking_items of a preserved report must NOT be cascaded")
	}
	// A key that matches the current sent_at deletes the report and cascades its items.
	if n, err := st.deleteReportChunk([]reportKey{{uid: "r1", sent: fresh}}); err != nil || n != 1 {
		t.Fatalf("matching-key delete = %d,%v; want 1", n, err)
	}
	if st.GetByUID("r1") != nil || countRows(t, st, "tracking_items") != 0 {
		t.Errorf("matching delete should remove report + cascade tracking_items")
	}
}

// Expired tokens older than the cutoff are removed; not-yet-expired, recently-expired (within
// grace), and never-expiring (empty expires_at) tokens are kept.
func TestDeleteExpiredTokensBefore(t *testing.T) {
	st := newTestStore(t)
	st.CreateToken("t-40", "n", "all", agoLocal(40))                                          // expired long ago → delete
	st.CreateToken("t-5", "n", "all", agoLocal(5))                                            // expired recently → keep (within grace)
	st.CreateToken("t-future", "n", "all", time.Now().AddDate(0, 0, 10).Format("2006-01-02 15:04:05")) // not expired → keep
	st.CreateToken("t-never", "n", "all", "")                                                 // never expires → keep

	cutoff := agoLocal(30) // grace 30 days
	if n, err := st.CountExpiredTokensBefore(cutoff); err != nil || n != 1 {
		t.Fatalf("CountExpiredTokensBefore = %d,%v; want 1", n, err)
	}
	n, err := st.DeleteExpiredTokensBefore(cutoff)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredTokensBefore = %d,%v; want 1", n, err)
	}
	if countRows(t, st, "api_tokens") != 3 {
		t.Errorf("api_tokens after delete = %d; want 3", countRows(t, st, "api_tokens"))
	}
}

// The audit ring buffer keeps only the most recent cleanupRunsKeep rows.
func TestCleanupRunsRingTrim(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < cleanupRunsKeep+25; i++ {
		if _, err := st.InsertCleanupRun(CleanupRun{RanAt: nowStr(), Trigger: "manual", OK: true, BatchDeleted: int64(i)}); err != nil {
			t.Fatalf("InsertCleanupRun: %v", err)
		}
	}
	if got := countRows(t, st, "cleanup_runs"); got != cleanupRunsKeep {
		t.Errorf("cleanup_runs rows = %d; want %d (ring trim)", got, cleanupRunsKeep)
	}
	runs, err := st.ListCleanupRuns(5)
	if err != nil || len(runs) != 5 {
		t.Fatalf("ListCleanupRuns = %d,%v; want 5", len(runs), err)
	}
	// Newest first: the last inserted had BatchDeleted = cleanupRunsKeep+24.
	if runs[0].BatchDeleted != int64(cleanupRunsKeep+24) {
		t.Errorf("newest run BatchDeleted = %d; want %d", runs[0].BatchDeleted, cleanupRunsKeep+24)
	}
}
