package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// User 账号（存数据库；这里的类型也用于 store 读写）。
type User struct {
	Username     string
	PasswordHash string
	Role         string // "admin" | "user"（可扩展更多角色）
}

// EffRole 解析出有效角色（默认 user）。
func (u User) EffRole() string {
	if u.Role != "" {
		return u.Role
	}
	return "user"
}

// IsAdmin 供模板/鉴权判断（{{if .IsAdmin}}）。
func (u User) IsAdmin() bool { return u.EffRole() == "admin" }

// Config 只放基础设施：监听端口、会话密钥、数据库连接。
// 其余（旧门户凭据、同步间隔、账号、入口按钮、报告类型…）都存数据库、网页里管。
type Config struct {
	Listen    string `yaml:"listen"`
	SecretKey string `yaml:"secret_key"`
	DBDriver  string `yaml:"db_driver"` // "sqlite"(默认) | "postgres"
	DBPath    string `yaml:"db_path"`   // sqlite 文件路径
	DBDSN     string `yaml:"db_dsn"`    // postgres DSN
}

// dbSource 返回给 OpenStore 的连接源（sqlite=文件路径，postgres=DSN）。
func (c *Config) dbSource() string {
	if c.DBDriver == "postgres" {
		return c.DBDSN
	}
	return c.DBPath
}

// EnsureConfig 读取配置；文件不存在则先生成一份默认配置（只含基础设施）再读。
func EnsureConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writeDefaultConfig(path); err != nil {
			return nil, fmt.Errorf("生成默认配置失败: %w", err)
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
	content := fmt.Sprintf(`# 研报门户配置 —— 只放基础设施（监听 / 会话密钥 / 数据库）。
# 旧门户凭据、同步间隔、账号、入口按钮、报告类型等都在网页里管、存数据库。
listen: ":8790"
secret_key: "%s"          # 会话签名密钥，已随机生成
db_driver: "sqlite"        # sqlite(默认) | postgres
db_path: "data/portal.db"
# 用 Postgres：把 db_driver 改成 postgres，并填 db_dsn
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
`, hex.EncodeToString(key))
	if d := dirOf(path); d != "" {
		os.MkdirAll(d, 0o755)
	}
	return os.WriteFile(path, []byte(content), 0o644)
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
	if c.DBDriver == "" {
		c.DBDriver = "sqlite"
	}
	if c.DBPath == "" {
		c.DBPath = "data/portal.db"
	}
	return &c, nil
}
