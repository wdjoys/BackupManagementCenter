// Package config loads server configuration from the environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Server struct {
	ListenAddr        string // BMC_LISTEN_ADDR, default :8080
	GRPCAddr          string // BMC_GRPC_ADDR, default :9090
	MetricsAddr       string // BMC_METRICS_ADDR, default 127.0.0.1:9100
	DataDir           string // BMC_DATA_DIR, default ./data
	PublicURL         string // BMC_PUBLIC_URL
	MasterKeyFile     string // BMC_MASTER_KEY_FILE or DataDir/master.key
	MasterKeyExplicit bool
	// Telegram failure notifications. Both variables must be set together
	// to enable; both empty disables; exactly one is a config error.
	TelegramBotToken string // BMC_TELEGRAM_BOT_TOKEN
	TelegramChatID   string // BMC_TELEGRAM_CHAT_ID
	TLSCertFile      string // BMC_TLS_CERT_FILE
	TLSKeyFile       string // BMC_TLS_KEY_FILE
	// TLSMode: "auto" (default) serves TLS from TLSCertFile/KeyFile and
	// requires them in production; "none" serves plain HTTP + plain gRPC for
	// deployments where a reverse proxy (Caddy/Nginx) terminates all TLS.
	TLSMode     string
	DevInsecure bool // BMC_DEV_INSECURE=1 allows missing TLS (local dev only)

	// DatabaseRestoreKinds lists the database kinds whose restore path has been
	// verified end-to-end on this deployment (BMC_DATABASE_RESTORE_KINDS, e.g.
	// "postgresql,sqlite"). Every kind is disabled by default: a database
	// restore overwrites data and must not run until the operator has verified
	// pre-restore backup and rollback against the real target.
	DatabaseRestoreKinds []string
	// HistoryRetentionDays 控制运行历史（runs/日志/审计事件）的保留天数，
	// 0（默认）表示永久保留 —— 升级不删除既有数据；正数时由调度器每日清理一次。
	HistoryRetentionDays int
}

// knownDatabaseRestoreKinds 是允许通过 BMC_DATABASE_RESTORE_KINDS 启用的 kind。
var knownDatabaseRestoreKinds = map[string]bool{
	"postgresql": true,
	"mysql":      true,
	"mongodb":    true,
	"sqlite":     true,
}

func LoadServer() (Server, error) {
	dataDir := env("BMC_DATA_DIR", "./data")
	masterKeyFile := os.Getenv("BMC_MASTER_KEY_FILE")
	c := Server{
		ListenAddr: env("BMC_LISTEN_ADDR", ":8080"), GRPCAddr: env("BMC_GRPC_ADDR", ":9090"),
		MetricsAddr: env("BMC_METRICS_ADDR", "127.0.0.1:9100"), DataDir: dataDir,
		PublicURL: os.Getenv("BMC_PUBLIC_URL"), MasterKeyFile: masterKeyFile,
		MasterKeyExplicit: masterKeyFile != "", TLSCertFile: os.Getenv("BMC_TLS_CERT_FILE"),
		TLSKeyFile: os.Getenv("BMC_TLS_KEY_FILE"), TLSMode: env("BMC_TLS_MODE", "auto"),
		DevInsecure: env("BMC_DEV_INSECURE", "") == "1",
	}
	if !c.MasterKeyExplicit {
		c.MasterKeyFile = filepath.Join(dataDir, "master.key")
	}
	switch c.TLSMode {
	case "auto", "none":
	default:
		return c, fmt.Errorf("config: BMC_TLS_MODE must be auto or none, got %q", c.TLSMode)
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("config: BMC_PUBLIC_URL must be an absolute http(s) URL")
		}
	}
	if !c.DevInsecure && c.TLSMode != "none" && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
		return c, fmt.Errorf("config: BMC_TLS_CERT_FILE and BMC_TLS_KEY_FILE are required (or BMC_TLS_MODE=none behind a TLS proxy, or BMC_DEV_INSECURE=1 for local development)")
	}
	for _, kind := range splitList(os.Getenv("BMC_DATABASE_RESTORE_KINDS")) {
		if !knownDatabaseRestoreKinds[kind] {
			return c, fmt.Errorf("config: BMC_DATABASE_RESTORE_KINDS contains unknown kind %q", kind)
		}
		c.DatabaseRestoreKinds = append(c.DatabaseRestoreKinds, kind)
	}
	c.HistoryRetentionDays = envInt("BMC_HISTORY_RETENTION_DAYS", 0)
	if c.HistoryRetentionDays < 0 {
		return c, fmt.Errorf("config: BMC_HISTORY_RETENTION_DAYS must be >= 0 (0 keeps history forever)")
	}
	return c, nil
}

// splitList 解析逗号分隔的环境变量列表，去除空白与空项。
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func EnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envInt 读取整型环境变量，缺省或非法时返回默认值。
func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
