package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

type User struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	IsAdmin      bool   `yaml:"is_admin"`
}

type Config struct {
	Listen    string `yaml:"listen"`
	SecretKey string `yaml:"secret_key"`
	OldPortal struct {
		BaseURL  string `yaml:"base_url"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"old_portal"`
	Users    []User `yaml:"users"`
	DBDriver string `yaml:"db_driver"`            // "sqlite"(默认) | "postgres"
	DBPath   string `yaml:"db_path"`              // sqlite 文件路径
	DBDSN    string `yaml:"db_dsn"`               // postgres DSN: postgres://user:pass@host:5432/reports?sslmode=disable
	SyncMin  int    `yaml:"sync_interval_minutes"` // 旧元数据自动同步间隔(0=只启动时同步一次)
}

// dbSource 返回给 OpenStore 的连接源（sqlite=文件路径，postgres=DSN）。
func (c *Config) dbSource() string {
	if c.DBDriver == "postgres" {
		return c.DBDSN
	}
	return c.DBPath
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
	if c.Listen == "" {
		c.Listen = ":8790"
	}
	if c.DBPath == "" {
		c.DBPath = "data/portal.db"
	}
	return &c, nil
}

func (c *Config) user(name string) *User {
	for i := range c.Users {
		if c.Users[i].Username == name {
			return &c.Users[i]
		}
	}
	return nil
}
