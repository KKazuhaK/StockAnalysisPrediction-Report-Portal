package app

import (
	"os"
	"testing"
)

// Dedup identity regression tests.
//
// A report's identity is "the stock code, or the title when there is no code" + rdate
// + rtype, enforced by the idx_reports_ident unique index rather than by a derived
// column. UpsertReport's ON CONFLICT target must match that index expression exactly
// or conflict inference silently stops resolving and every re-ingest forks a new row —
// so these tests assert the observable contract (row count + returned id + created
// flag), not the SQL text.
//
// kind is deliberately NOT part of the identity: re-categorising a subtype in the
// registry must never fork one report into two rows.
//
// Each case runs on sqlite and, when TEST_POSTGRES_DSN is set, on Postgres too — the
// index expression and the upsert inference are the most dialect-sensitive SQL in the
// store, so sqlite passing alone proves little about production.
func eachDriver(t *testing.T, fn func(t *testing.T, st *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres dialect check")
		}
		st, err := OpenStore("postgres", dsn)
		if err != nil {
			t.Fatalf("OpenStore postgres: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, err := st.exec("DELETE FROM reports"); err != nil {
			t.Fatalf("clean reports: %v", err)
		}
		fn(t, st)
	})
}

func countReports(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.queryRow("SELECT COUNT(*) FROM reports").Scan(&n); err != nil {
		t.Fatalf("count reports: %v", err)
	}
	return n
}

func mustUpsert(t *testing.T, st *Store, r Rep) (int64, bool) {
	t.Helper()
	id, created, err := st.UpsertReport(r)
	if err != nil {
		t.Fatalf("UpsertReport(%+v): %v", r, err)
	}
	if id == 0 {
		t.Fatalf("UpsertReport returned id=0 for %+v", r)
	}
	return id, created
}

// Re-ingesting the same code+date+subtype+title overwrites the one row and reports the
// same id back, so callers (tracking, webhooks, the ingest response) all agree.
func TestUpsertReportDedupsOnIdentity(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		base := Rep{Symbol: "600519", Title: "T1", RType: "交易分析", Date: "2026-07-16", Kind: "研究", MD: "v1"}
		id1, created1 := mustUpsert(t, st, base)
		if !created1 {
			t.Error("first ingest: created = false, want true")
		}

		again := base
		again.MD = "v2" // a re-run of the same report: same identity, fresh body
		id2, created2 := mustUpsert(t, st, again)
		if created2 {
			t.Error("re-ingest: created = true, want false (must overwrite, not insert)")
		}
		if id2 != id1 {
			t.Errorf("re-ingest id = %d, want %d (identity must be stable)", id2, id1)
		}
		if n := countReports(t, st); n != 1 {
			t.Fatalf("report count = %d, want 1 (re-ingest duplicated the row)", n)
		}

		got, err := st.GetNew(id1)
		if err != nil || got == nil {
			t.Fatalf("GetNew(%d) = %v, %v", id1, got, err)
		}
		if got.MD != "v2" {
			t.Errorf("after re-ingest MD=%q, want v2 (body was not overwritten)", got.MD)
		}
		if got.ID != id1 {
			t.Errorf("GetNew().ID = %d, want %d", got.ID, id1)
		}
	})
}

// title IS part of the identity. rtype is a coarse registry label ("股权分析", "估值分析"):
// production stores several genuinely different reports under one code+date+subtype,
// told apart only by their title. Keying without the title merges them into one row and
// destroys every report but the last — this is the v0.3.0 regression that took the portal
// down, so it is pinned here.
func TestUpsertReportTitleIsPartOfIdentity(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		base := Rep{Symbol: "002891", RType: "估值分析", Date: "2026-04-24", Kind: "投资决策"}

		a := base
		a.Title, a.MD = "002891 估值分析 上篇", "a"
		idA, createdA := mustUpsert(t, st, a)
		if !createdA {
			t.Error("first ingest: created = false, want true")
		}

		b := base
		b.Title, b.MD = "002891 估值分析 下篇", "b"
		idB, createdB := mustUpsert(t, st, b)
		if !createdB || idB == idA {
			t.Errorf("different title: id=%d created=%v, want a NEW row (id != %d)", idB, createdB, idA)
		}

		if n := countReports(t, st); n != 2 {
			t.Fatalf("report count = %d, want 2 (different titles were merged into one report)", n)
		}
		gotA, _ := st.GetNew(idA)
		if gotA == nil || gotA.MD != "a" {
			t.Errorf("first report = %v, want body a intact (it was overwritten by the second)", gotA)
		}
	})
}

// A different date or a different subtype is a different report, not an overwrite.
func TestUpsertReportDistinctIdentities(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		base := Rep{Symbol: "600519", Title: "T", RType: "交易分析", Date: "2026-07-16"}
		id1, _ := mustUpsert(t, st, base)

		otherDate := base
		otherDate.Date = "2026-07-17"
		id2, created2 := mustUpsert(t, st, otherDate)
		if !created2 || id2 == id1 {
			t.Errorf("different date: id=%d created=%v, want a new row (id != %d)", id2, created2, id1)
		}

		otherType := base
		otherType.RType = "舆情分析"
		id3, created3 := mustUpsert(t, st, otherType)
		if !created3 || id3 == id1 {
			t.Errorf("different rtype: id=%d created=%v, want a new row (id != %d)", id3, created3, id1)
		}

		if n := countReports(t, st); n != 3 {
			t.Fatalf("report count = %d, want 3", n)
		}
	})
}

// kind is not part of the identity: the same code+date+subtype re-ingested under a
// re-categorised kind must overwrite the existing row, never fork a second one.
func TestUpsertReportKindNotPartOfIdentity(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		base := Rep{Symbol: "600519", Title: "T", RType: "交易分析", Date: "2026-07-16", Kind: "研究"}
		id1, _ := mustUpsert(t, st, base)

		recat := base
		recat.Kind = "每日推荐"
		id2, created := mustUpsert(t, st, recat)
		if created || id2 != id1 {
			t.Errorf("re-categorised kind: id=%d created=%v, want overwrite of id=%d", id2, created, id1)
		}
		if n := countReports(t, st); n != 1 {
			t.Fatalf("report count = %d, want 1 (kind forked the identity)", n)
		}
	})
}

// A thematic report has no stock code, so its identity falls back to the title.
// Same title = same report; different titles on the same day+subtype must not collide.
func TestUpsertReportThematicDedupsOnTitle(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		x := Rep{Symbol: "", Title: "新能源主题", RType: "交易分析", Date: "2026-07-16", MD: "v1"}
		id1, created1 := mustUpsert(t, st, x)
		if !created1 {
			t.Error("first thematic ingest: created = false, want true")
		}

		xAgain := x
		xAgain.MD = "v2"
		id2, created2 := mustUpsert(t, st, xAgain)
		if created2 || id2 != id1 {
			t.Errorf("same thematic title: id=%d created=%v, want overwrite of id=%d", id2, created2, id1)
		}

		y := x
		y.Title, y.MD = "半导体主题", "v3"
		id3, created3 := mustUpsert(t, st, y)
		if !created3 || id3 == id1 {
			t.Errorf("different thematic title: id=%d created=%v, want a new row (id != %d)", id3, created3, id1)
		}

		if n := countReports(t, st); n != 2 {
			t.Fatalf("report count = %d, want 2 (thematic titles collided or duplicated)", n)
		}
	})
}

// Upgrade path. init() runs createBaseSchema (CREATE ... IF NOT EXISTS) BEFORE ensureColumns,
// so a base-schema index over a column that only ensureColumns back-fills onto an existing
// database can never resolve there: CREATE TABLE IF NOT EXISTS is a no-op once the table
// exists, so the column is still missing when the index statement runs. Declaring idx_track_id
// in the base schema did exactly that and left every pre-existing database unable to start —
// a fresh DB hid it, because CREATE TABLE brought the column with it. Simulate the old shape
// and assert the upgrade completes.
func TestInitUpgradesDatabaseMissingReportID(t *testing.T) {
	st := newTestStore(t)

	// Roll tracking_items back to its pre-report_id shape.
	if _, err := st.exec(`DROP INDEX IF EXISTS idx_track_id`); err != nil {
		t.Fatalf("drop idx_track_id: %v", err)
	}
	if _, err := st.exec(`ALTER TABLE tracking_items DROP COLUMN report_id`); err != nil {
		t.Fatalf("drop report_id: %v", err)
	}
	if st.columnExists("tracking_items", "report_id") {
		t.Fatal("setup failed: report_id still present")
	}

	if err := st.init(); err != nil {
		t.Fatalf("init on a database predating report_id: %v", err)
	}
	if !st.columnExists("tracking_items", "report_id") {
		t.Error("report_id was not restored by ensureColumns")
	}
}

// The id UpsertReport returns is the row it actually wrote, on both the insert and
// the overwrite path — tracking items, the webhook payload and the API response all
// key off it, so a wrong id silently attaches data to the wrong report.
func TestUpsertReportReturnsWrittenID(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		a := Rep{Symbol: "600519", Title: "A", RType: "交易分析", Date: "2026-07-16", MD: "a"}
		b := Rep{Symbol: "000001", Title: "B", RType: "交易分析", Date: "2026-07-16", MD: "b"}
		idA, _ := mustUpsert(t, st, a)
		idB, _ := mustUpsert(t, st, b)
		if idA == idB {
			t.Fatalf("distinct reports share id %d", idA)
		}

		// Overwrite A only; B must be untouched and A's id must come back unchanged.
		a2 := a
		a2.MD = "a2"
		idA2, created := mustUpsert(t, st, a2)
		if created || idA2 != idA {
			t.Errorf("overwrite returned id=%d created=%v, want id=%d created=false", idA2, created, idA)
		}
		gotA, _ := st.GetNew(idA)
		gotB, _ := st.GetNew(idB)
		if gotA == nil || gotA.MD != "a2" {
			t.Errorf("A body = %v, want a2", gotA)
		}
		if gotB == nil || gotB.MD != "b" {
			t.Errorf("B body = %v, want b (overwriting A must not touch B)", gotB)
		}
	})
}
