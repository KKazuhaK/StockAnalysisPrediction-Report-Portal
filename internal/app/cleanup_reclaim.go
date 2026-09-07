package app

import (
	"fmt"
	"log"
	"time"
)

// Reclaiming disk after a retention pass (ADR 0017).
//
// Deleting rows is not the same as getting space back, and until now the cleanup console reported
// "deleted 12,431 rows" while `df` did not move at all — which is the number an admin ran the pass
// to change. Both drivers keep the freed pages: SQLite parks them on the database file's freelist,
// Postgres marks them reusable within the table.
//
// The two drivers get different answers, and the difference is not an oversight:
//
//   - SQLite is a file the portal owns exclusively. VACUUM rewrites it and hands the space back to
//     the filesystem, so the pass can finish the job it was asked to do.
//   - Postgres already runs autovacuum, which reclaims for reuse continuously. Returning space to
//     the OS needs VACUUM FULL, which takes an ACCESS EXCLUSIVE lock on every table it touches —
//     a full outage of unbounded length, scheduled by a background ticker, on a database this
//     portal does not own exclusively. Nothing here does that. The console says so rather than
//     leaving an admin to wonder why the number is zero.
const (
	// vacuumFreeRatio is how much of the file must be free pages before a VACUUM is worth it.
	// VACUUM rewrites the whole database and needs roughly its own size in temporary disk, so doing
	// it for a few pages costs more than it returns. A tenth is the point where the rewrite pays.
	vacuumFreeRatio = 0.10
	// vacuumMinPages skips the rewrite on a database too small for any of this to matter. At the
	// default 4 KiB page size this is 8 MiB.
	vacuumMinPages = 2048
)

// reclaim compacts the database after a pass that deleted rows, returning the bytes handed back to
// the filesystem (0 when nothing was done, which includes every Postgres deployment).
func (s *Store) reclaim() (int64, error) {
	if s.driver != "sqlite" {
		return 0, nil // see the comment above: autovacuum has this, and VACUUM FULL is an outage
	}
	before, free, pageSize, err := s.sqlitePages()
	if err != nil {
		return 0, err
	}
	if before < vacuumMinPages || float64(free) < float64(before)*vacuumFreeRatio {
		return 0, nil
	}
	start := time.Now()
	// VACUUM cannot run inside a transaction, and on SQLite the pool is one connection wide, so this
	// necessarily blocks every other query for its duration. That is why it is gated above and why
	// it runs at the END of a cleanup pass, which is already the moment the admin chose.
	if _, err := s.exec("VACUUM"); err != nil {
		return 0, fmt.Errorf("vacuum: %w", err)
	}
	after, _, _, err := s.sqlitePages()
	if err != nil {
		return 0, err
	}
	freed := (before - after) * pageSize
	if freed < 0 {
		freed = 0 // a concurrent write grew the file; report nothing rather than a negative
	}
	log.Printf("cleanup: vacuumed sqlite in %s, %d bytes returned to the filesystem",
		time.Since(start).Round(time.Millisecond), freed)
	return freed, nil
}

// sqlitePages reads the file's size in pages, how many of those are free, and the page size.
func (s *Store) sqlitePages() (pages, free, pageSize int64, err error) {
	if err = s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	return pages, free, pageSize, nil
}
