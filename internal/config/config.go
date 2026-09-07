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
	// SecretKeyPrevious is the key secret_key was rotated AWAY from, supplied for one boot so the
	// sealed-secret keyring can be re-wrapped under the new one (ADR 0023, secretbox.go).
	//
	// It exists because the keyring is envelope-encrypted: stored secrets are encrypted with a data
	// key, and only that data key is wrapped under secret_key — so a rotation should re-wrap one row
	// rather than re-encrypt everything. That was the stated design and nothing implemented it, so
	// rotating secret_key made the data key permanently unopenable: SSO stopped working and no page
	// in the product could repair it, because saving a secret needs the same key that will not open.
	//
	// Set it beside the new secret_key, restart once, and remove it. Leaving it set is not dangerous
	// but it is a second live key on disk, so the portal says so on every boot.
	SecretKeyPrevious string   `yaml:"secret_key_previous"`
	TrustedProxies    []string `yaml:"trusted_proxies"` // CIDRs/IPs allowed to supply X-Forwarded-For
	DBDriver          string   `yaml:"db_driver"`       // "sqlite" (default) | "postgres"
	DBPath            string   `yaml:"db_path"`         // sqlite file path
	DBDSN             string   `yaml:"db_dsn"`          // postgres DSN
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
# Rotating secret_key: put the OLD key in secret_key_previous, restart once, then delete that line.
# Without it the SSO secret keyring cannot be reopened and the portal refuses to guess.
db_driver: "sqlite"        # sqlite (default) | postgres
db_path: "data/portal.db"
# To use Postgres: set db_driver to postgres and fill in db_dsn
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
`, hex.EncodeToString(key))
	if d := DirOf(path); d != "" {
		os.MkdirAll(d, 0o755)
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// LoadConfig reads and parses the YAML config, applying defaults for empty fields.
// WarnIfWorldReadable reports the config file's permissions when anyone but its owner can read it.
//
// The file holds secret_key — which signs every session cookie and, through HKDF, wraps the SSO
// keyring — and since the rotation work it can hold secret_key_previous beside it, so a readable
// config is briefly TWO live keys on disk. The generated file is 0600, but a config copied from the
// example, restored from an archive, or checked out of a repository arrives with whatever mode it
// had, and nothing said anything.
//
// A warning rather than a refusal: the portal starting is almost always more valuable than the
// portal being right about this, and the operator may not be able to chmod it (a read-only mount, a
// bind mount owned by the host). It follows the two warnings the boot log already carries.
//
// Returns the message rather than logging it, so the caller decides where it goes and a test can
// read it.
func WarnIfWorldReadable(path string) string {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return ""
	}
	perm := fi.Mode().Perm()
	if perm&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("WARNING: %s is mode %04o — readable by more than its owner. It holds "+
		"secret_key (and secret_key_previous during a rotation), which signs every session and "+
		"unwraps the stored SSO secrets. Run: chmod 600 %s", path, perm, path)
}

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
	if v := os.Getenv("RP_SECRET_KEY_PREVIOUS"); v != "" {
		c.SecretKeyPrevious = v
	}
	if v := os.Getenv("RP_TRUSTED_PROXIES"); v != "" {
		c.TrustedProxies = splitNonEmpty(v)
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

func splitNonEmpty(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// DirOf returns the directory portion of a path, or "." if there is none.
func DirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}
