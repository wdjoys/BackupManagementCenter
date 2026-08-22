// Package config loads server configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Server struct {
	ListenAddr    string // BMC_LISTEN_ADDR, default :8080
	GRPCAddr      string // BMC_GRPC_ADDR, default :9090
	MetricsAddr   string // BMC_METRICS_ADDR, default 127.0.0.1:9100
	AcmeAddr      string // BMC_ACME_ADDR, optional plain HTTP ACME challenge listener
	AcmeWebroot   string // BMC_ACME_WEBROOT, default /var/lib/bmc/acme-webroot
	DataDir       string // BMC_DATA_DIR, default ./data
	PublicURL     string // BMC_PUBLIC_URL
	MasterKeyFile string // BMC_MASTER_KEY_FILE
	TLSCertFile   string // BMC_TLS_CERT_FILE
	TLSKeyFile    string // BMC_TLS_KEY_FILE
	// TLSMode: "auto" (default) serves TLS from TLSCertFile/KeyFile and
	// requires them in production; "none" serves plain HTTP + plain gRPC for
	// deployments where a reverse proxy (Caddy/Nginx) terminates all TLS.
	// The master key stays mandatory in production regardless of mode.
	TLSMode     string
	DevInsecure bool // BMC_DEV_INSECURE=1 allows missing TLS/master key (local dev only)
}

func LoadServer() (Server, error) {
	c := Server{
		ListenAddr:    env("BMC_LISTEN_ADDR", ":8080"),
		GRPCAddr:      env("BMC_GRPC_ADDR", ":9090"),
		MetricsAddr:   env("BMC_METRICS_ADDR", "127.0.0.1:9100"),
		AcmeAddr:      os.Getenv("BMC_ACME_ADDR"),
		AcmeWebroot:   env("BMC_ACME_WEBROOT", "./data/acme-webroot"),
		DataDir:       env("BMC_DATA_DIR", "./data"),
		PublicURL:     os.Getenv("BMC_PUBLIC_URL"),
		MasterKeyFile: os.Getenv("BMC_MASTER_KEY_FILE"),
		TLSCertFile:   os.Getenv("BMC_TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("BMC_TLS_KEY_FILE"),
		TLSMode:       env("BMC_TLS_MODE", "auto"),
		DevInsecure:   env("BMC_DEV_INSECURE", "") == "1",
	}
	switch c.TLSMode {
	case "auto", "none":
	default:
		return c, fmt.Errorf("config: BMC_TLS_MODE must be auto or none, got %q", c.TLSMode)
	}
	plaintext := c.TLSMode == "none"
	if c.DevInsecure {
		return c, nil
	}
	if !plaintext && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
		return c, fmt.Errorf("config: BMC_TLS_CERT_FILE and BMC_TLS_KEY_FILE are required (or BMC_TLS_MODE=none behind a TLS proxy, or BMC_DEV_INSECURE=1 for local development)")
	}
	if c.MasterKeyFile == "" {
		return c, fmt.Errorf("config: BMC_MASTER_KEY_FILE is required (or BMC_DEV_INSECURE=1 for local development)")
	}
	return c, nil
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
