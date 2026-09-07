package app

import (
	"bytes"
	"testing"
)

// A restore wipes everything itself, so these ask pgStore to clean nothing. The backup path is the
// most dialect-sensitive code in the store after the upsert: Postgres identity columns REFUSE an
// explicit id unless the insert says OVERRIDING SYSTEM VALUE, and the sequence behind them does not
// move when one is supplied. Neither behaviour exists on SQLite, so the SQLite tests prove nothing
// about either.

// TestRestoreIntoPostgresKeepsIdsAndAdvancesTheSequence is the production shape of a restore: the
// Docker stack runs Postgres. Ids must survive — half the database refers to reports and groups by
// them — and the identity sequence must end up past the restored rows, or the first report written
// after a restore collides with one that was just loaded.
func TestRestoreIntoPostgresKeepsIdsAndAdvancesTheSequence(t *testing.T) {
	dst := pgStore(t)

	// Wind the identity sequence back first. It is NOT reset by DELETE, so a previous run of this
	// test would otherwise leave it high enough to satisfy the assertion below no matter what the
	// restore does — the test would pass with the reset removed, which is no test at all.
	if _, err := dst.exec(`SELECT setval(pg_get_serial_sequence('reports','id'), 1, false)`); err != nil {
		t.Fatalf("rewind sequence: %v", err)
	}

	src := newTestStore(t) // sqlite: the dump is portable between drivers by design
	seedForBackup(t, src)
	dump := dumpOf(t, src)

	rep, err := dst.restoreFrom(bytes.NewReader(dump), true)
	if err != nil {
		t.Fatalf("restore into postgres: %v", err)
	}
	if rep.Rows["reports"] != 2 {
		t.Fatalf("reports restored = %d; want 2", rep.Rows["reports"])
	}

	const bigID = int64(1) << 60
	if got := scalar[string](t, dst, `SELECT title FROM reports WHERE id=?`, bigID); got != "深度分析 · 一季度" {
		t.Errorf("an explicit id must be inserted verbatim into an identity column; title = %q", got)
	}
	blob := scalar[[]byte](t, dst, `SELECT content FROM app_files WHERE app_id='demo' AND path='index.html'`)
	if !bytes.Equal(blob, []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i'}) {
		t.Errorf("BYTEA content came back as %v", blob)
	}

	// The sequence: a fresh insert must not collide with a restored row.
	id, err := dst.insertID(`INSERT INTO reports(title,symbol,rtype,rdate,version) VALUES(?,?,?,?,?)`,
		"恢复之后写的", "000002", "股权分析", "2026-05-01", "")
	if err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
	if id <= bigID {
		t.Errorf("the identity sequence was not advanced past the restored ids: new id %d <= restored %d", id, bigID)
	}
}

// TestPostgresDumpRestoresIntoSQLite is the other direction, and the reason the format is one format:
// "we outgrew SQLite" and "we want to go back to a single file" are both supported moves.
func TestPostgresDumpRestoresIntoSQLite(t *testing.T) {
	pg := pgStore(t)

	seed := newTestStore(t)
	seedForBackup(t, seed)
	if _, err := pg.restoreFrom(bytes.NewReader(dumpOf(t, seed)), true); err != nil {
		t.Fatalf("seed postgres: %v", err)
	}

	lite := newTestStore(t)
	if _, err := lite.restoreFrom(bytes.NewReader(dumpOf(t, pg)), true); err != nil {
		t.Fatalf("postgres dump -> sqlite: %v", err)
	}
	if got := scalar[string](t, lite, `SELECT body_md FROM reports WHERE id=?`, int64(1)<<60); got == "" {
		t.Error("the round trip through Postgres lost the report body")
	}
	if got := scalar[string](t, lite, `SELECT v FROM meta WHERE k='site_title'`); got != "报告门户" {
		t.Errorf("settings did not survive the round trip; site_title = %q", got)
	}
	blob := scalar[[]byte](t, lite, `SELECT content FROM app_files WHERE app_id='demo' AND path='index.html'`)
	if !bytes.Equal(blob, []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i'}) {
		t.Errorf("BYTEA -> BLOB lost the bytes: %v", blob)
	}
}
