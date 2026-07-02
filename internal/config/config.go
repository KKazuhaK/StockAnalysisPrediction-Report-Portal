// Package config holds infrastructure-only settings: listen address, session
// secret, and database connection. Everything else (legacy-portal credentials,
// sync interval, accounts, entry buttons, report types...) lives in the DB and
// is managed from the web UI.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the infrastructure config: listen port, session key, database.
type Config struct {
	Listen    string `yaml:"listen"`
	SecretKey string `yaml:"secret_key"`
	DBDriver  string `yaml:"db_driver"` // "sqlite" (default) | "postgres"
	DBPath    string `yaml:"db_path"`   // sqlite file path
	DBDSN     string `yaml:"db_dsn"`    // postgres DSN
}

// DBSource returns the connection source for OpenStore (sqlite=file path, postgres=DSN).
func (c *Config) DBSource() string {
	if c.DBDriver == "postgres" {
		return c.DBDSN
	}
	return c.DBPath
}

// EnsureConfig loads the config, writing a default (infra-only) file first if missing.
func EnsureConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writeDefaultConfig(path); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
		log.Printf("no config file, generated default %s (edit secret_key / db as needed)", path)
	}
	return LoadConfig(path)
}

func writeDefaultConfig(path string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	content := fmt.Sprintf(`# report-portal config — infrastructure only (listen / session key / database).
# Legacy-portal credentials, sync interval, accounts, entry buttons, report types
# etc. are all managed in the web UI and stored in the database.
listen: ":8790"
secret_key: "%s"          # session signing key, randomly generated
db_driver: "sqlite"        # sqlite (default) | postgres
db_path: "data/portal.db"
# To use Postgres: set db_driver to postgres and fill in db_dsn
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
`, hex.EncodeToString(key))
	if d := DirOf(path); d != "" {
		os.MkdirAll(d, 0o755)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// LoadConfig reads and parses the YAML config, applying defaults for empty fields.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	// RP_* env vars override the file (used by the Docker image, which defaults to
	// Postgres). Empty/unset vars leave the file value untouched.
	if v := os.Getenv("RP_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("RP_SECRET_KEY"); v != "" {
		c.SecretKey = v
	}
	if v := os.Getenv("RP_DB_DRIVER"); v != "" {
		c.DBDriver = v
	}
	if v := os.Getenv("RP_DB_DSN"); v != "" {
		c.DBDSN = v
	}
	if v := os.Getenv("RP_DB_PATH"); v != "" {
		c.DBPath = v
	}
	// Defaults for anything still empty (standalone binary with no env → SQLite).
	if c.Listen == "" {
		c.Listen = ":8790"
	}
	if c.DBDriver == "" {
		c.DBDriver = "sqlite"
	}
	if c.DBPath == "" {
		c.DBPath = "data/portal.db"
	}
	return &c, nil
}

// DirOf returns the directory portion of a path, or "." if there is none.
func DirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}
