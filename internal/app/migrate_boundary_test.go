package app

import (
	"database/sql"
	"strings"
	"testing"
)

func TestFreshStoreIsStampedAtCurrentBaseline(t *testing.T) {
	st := newTestStore(t)
	if got := st.schemaVersion(); got != schemaBaseline {
		t.Fatalf("fresh schema version = %d, want %d", got, schemaBaseline)
	}
}

func TestV03RejectsPreV02DatabaseWithoutChangingIt(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE users(username TEXT PRIMARY KEY, password_hash TEXT, role TEXT)`,
		`INSERT INTO users(username,password_hash,role) VALUES('alice','kept','admin')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}

	st := &Store{db: db, driver: "sqlite"}
	err = st.init()
	if err == nil || !strings.Contains(err.Error(), "first run v0.2.26") {
		t.Fatalf("init error = %v, want v0.2.26 upgrade guidance", err)
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE username='alice'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "kept" {
		t.Fatalf("legacy row changed to %q", hash)
	}
	var reportsTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='reports'`).Scan(&reportsTable); err != nil {
		t.Fatal(err)
	}
	if reportsTable != 0 {
		t.Fatal("boundary rejection created current-schema tables")
	}
}
