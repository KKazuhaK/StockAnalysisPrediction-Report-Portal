package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The Docker image configures the database via RP_* env vars (12-factor), which
// must override whatever the on-disk config.yaml says while leaving unset fields
// (e.g. the persisted secret_key) intact.
func TestEnvOverridesFileConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("listen: \":8790\"\nsecret_key: \"filekey\"\ndb_driver: \"sqlite\"\ndb_path: \"data/portal.db\"\n"), 0o644)

	t.Setenv("RP_DB_DRIVER", "postgres")
	t.Setenv("RP_DB_DSN", "postgres://u:p@db:5432/reports?sslmode=disable")
	t.Setenv("RP_LISTEN", ":10560")

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DBDriver != "postgres" {
		t.Errorf("DBDriver = %q, want postgres (env override)", c.DBDriver)
	}
	if c.DBSource() != "postgres://u:p@db:5432/reports?sslmode=disable" {
		t.Errorf("DBSource = %q, want the env DSN", c.DBSource())
	}
	if c.Listen != ":10560" {
		t.Errorf("Listen = %q, want :10560 (env override)", c.Listen)
	}
	if c.SecretKey != "filekey" {
		t.Errorf("SecretKey = %q, want filekey (no env → keep file value)", c.SecretKey)
	}
}

// With no RP_* env vars set, the standalone binary keeps the SQLite defaults.
func TestDefaultsWhenNoEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("secret_key: \"k\"\n"), 0o644)

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DBDriver != "sqlite" || c.Listen != ":8790" || c.DBPath != "data/portal.db" {
		t.Errorf("defaults not applied: %+v", c)
	}
}
