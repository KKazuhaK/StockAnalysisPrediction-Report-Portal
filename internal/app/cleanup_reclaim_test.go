package app

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// fillAndDelete grows the database well past the vacuum floor and then empties it, which is the
// state a retention pass leaves behind: rows gone, file the same size.
func fillAndDelete(t *testing.T, st *Store) {
	t.Helper()
	big := strings.Repeat("正文", 4000) // ~24 KiB per report
	for i := 0; i < 800; i++ {
		if _, err := st.exec(`INSERT INTO reports(title,symbol,rtype,rdate,body_md,version)
			VALUES(?,?,?,?,?,?)`, fmt.Sprintf("r%d", i), "600519", "深度分析", "2020-01-01", big, ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := st.exec("DELETE FROM reports"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func fileSize(t *testing.T, st *Store) int64 {
	t.Helper()
	pages, _, size, err := st.sqlitePages()
	if err != nil {
		t.Fatalf("sqlitePages: %v", err)
	}
	return pages * size
}

// TestReclaimReturnsSpaceToTheFilesystem is the number an admin runs a retention pass to change.
// Deleting rows does not move it: SQLite parks the freed pages on the file's freelist and the file
// stays exactly as large as it was, so the console could report "deleted 12,431 rows" while `df`
// showed nothing at all.
func TestReclaimReturnsSpaceToTheFilesystem(t *testing.T) {
	st, err := OpenStore("sqlite", t.TempDir()+"/portal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fillAndDelete(t, st)
	before := fileSize(t, st)
	if before < vacuumMinPages*4096 {
		t.Fatalf("the fixture did not grow the file enough to be a test: %d bytes", before)
	}

	freed, err := st.reclaim()
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	after := fileSize(t, st)
	if freed <= 0 {
		t.Fatalf("reclaim reported %d bytes; the file went %d → %d", freed, before, after)
	}
	if after >= before {
		t.Errorf("the file did not shrink: %d → %d", before, after)
	}
	if got := before - after; got != freed {
		t.Errorf("reported %d bytes freed but the file lost %d", freed, got)
	}
}

// TestReclaimSkipsWhatIsNotWorthIt keeps a VACUUM — which rewrites the whole file and blocks every
// other query on SQLite's single connection — from running for a handful of pages.
func TestReclaimSkipsWhatIsNotWorthIt(t *testing.T) {
	path := t.TempDir() + "/portal.db"
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// The property is that the rewrite does not HAPPEN, not that it frees nothing — vacuuming a
	// database with nothing to reclaim also frees nothing, and would pass a byte-count assertion
	// while doing the very work the gate exists to avoid. VACUUM rewrites the file, so the file's
	// modification time is the observable.
	touched := func(before time.Time) bool {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return !fi.ModTime().Equal(before)
	}
	mtime := func() time.Time {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}

	// A fresh database: small, and nothing deleted from it.
	at := mtime()
	if freed, err := st.reclaim(); err != nil || freed != 0 || touched(at) {
		t.Errorf("a tiny database must not be rewritten; freed=%d err=%v rewritten=%v", freed, err, touched(at))
	}

	// Large enough, but almost nothing free: still not worth the rewrite.
	big := strings.Repeat("x", 40000)
	for i := 0; i < 400; i++ {
		st.exec(`INSERT INTO reports(title,symbol,rtype,rdate,body_md,version) VALUES(?,?,?,?,?,?)`,
			fmt.Sprintf("r%d", i), "600519", "深度分析", "2020-01-01", big, "")
	}
	at = mtime()
	if freed, err := st.reclaim(); err != nil || freed != 0 || touched(at) {
		t.Errorf("a full database must not be rewritten; freed=%d err=%v rewritten=%v", freed, err, touched(at))
	}
}

// TestReclaimIsASQLiteOnlyAnswer pins the deliberate asymmetry. Postgres runs autovacuum, which
// reclaims for reuse continuously; returning space to the OS needs VACUUM FULL, which takes an
// ACCESS EXCLUSIVE lock on every table — a full outage of unbounded length, and not something a
// background ticker should ever schedule on a database this portal does not own exclusively.
func TestReclaimIsASQLiteOnlyAnswer(t *testing.T) {
	pg := pgStore(t)
	freed, err := pg.reclaim()
	if err != nil {
		t.Fatalf("reclaim on postgres must be a no-op, not an error: %v", err)
	}
	if freed != 0 {
		t.Errorf("reclaim on postgres reported %d bytes; it must attempt nothing", freed)
	}
}

// TestCleanupReportsWhatItFreed carries the number all the way to the console: through the pass, the
// result the page reads, and the history row it writes.
func TestCleanupReportsWhatItFreed(t *testing.T) {
	st, err := OpenStore("sqlite", t.TempDir()+"/portal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{st: st}

	// 800 fat reports, all old enough for the reports target to take them.
	big := strings.Repeat("正文", 4000)
	for i := 0; i < 800; i++ {
		st.exec(`INSERT INTO reports(title,symbol,rtype,rdate,sent_at,body_md,version)
			VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("r%d", i), "600519", "深度分析",
			"2020-01-01", "2020-01-01T00:00:00Z", big, "")
	}
	// No config to set: the reports cutoff defaults to 730 days and these are dated 2020, so the
	// target takes them.
	res := s.runCleanup("manual", false, cleanupTargets{Reports: true})
	if !res.OK {
		t.Fatalf("cleanup failed: %s", res.Error)
	}
	if res.Reports == 0 {
		t.Fatalf("the fixture deleted nothing, so there was nothing to reclaim")
	}
	if res.Reclaimed <= 0 {
		t.Errorf("the pass deleted %d reports and reported %d bytes reclaimed", res.Reports, res.Reclaimed)
	}
	if res.Driver != "sqlite" {
		t.Errorf("driver = %q; the console needs it to explain a zero", res.Driver)
	}

	runs, err := st.ListCleanupRuns(5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("ListCleanupRuns = %v, %v", runs, err)
	}
	if runs[0].BytesReclaimed != res.Reclaimed {
		t.Errorf("history recorded %d bytes, the pass reported %d", runs[0].BytesReclaimed, res.Reclaimed)
	}
}

// TestPreviewReclaimsNothing keeps a dry run dry. VACUUM rewrites the file; a preview that did it
// would be the one "preview" in the product that changes something.
func TestPreviewReclaimsNothing(t *testing.T) {
	st, err := OpenStore("sqlite", t.TempDir()+"/portal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{st: st}
	// Free pages to reclaim AND reports still there for the preview to count — a preview that found
	// nothing to delete would never reach the reclaim step, so it would prove nothing.
	big := strings.Repeat("正文", 4000)
	for i := 0; i < 800; i++ {
		st.exec(`INSERT INTO reports(title,symbol,rtype,rdate,sent_at,body_md,version)
			VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("r%d", i), "600519", "深度分析",
			"2020-01-01", "2020-01-01T00:00:00Z", big, "")
	}
	if _, err := st.exec("DELETE FROM reports WHERE title LIKE 'r1%'"); err != nil {
		t.Fatal(err)
	}
	before := fileSize(t, st)

	res := s.runCleanup("preview", true, cleanupTargets{Reports: true})
	if res.Reports == 0 {
		t.Fatal("the preview counted nothing, so it never reached the step under test")
	}
	if res.Reclaimed != 0 {
		t.Errorf("a preview reported %d bytes reclaimed", res.Reclaimed)
	}
	if after := fileSize(t, st); after != before {
		t.Errorf("a preview rewrote the database: %d → %d", before, after)
	}
}
