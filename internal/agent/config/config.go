// Package config loads agent configuration from the environment.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

type Agent struct {
	ServerGRPCURL string // BMC_SERVER_GRPC_URL, e.g. 127.0.0.1:9090 or host:443
	ServerTLS     bool   // BMC_SERVER_TLS, default true; set 0 only for local dev
	EnrollToken   string // BMC_ENROLLMENT_TOKEN, one-time, first boot only
	StateDir      string // BMC_AGENT_STATE_DIR, default ./agent-state
	DataDir       string // BMC_AGENT_DATA_DIR, scratch for temp dirs, defaults to StateDir
	DevInsecure   bool   // BMC_DEV_INSECURE=1 skips TLS server-name verification (local dev)
	ProbeInterval int    // seconds, default 600
}

func LoadAgent() (Agent, error) {
	a := Agent{
		ServerGRPCURL: os.Getenv("BMC_SERVER_GRPC_URL"),
		ServerTLS:     os.Getenv("BMC_SERVER_TLS") != "0",
		EnrollToken:   os.Getenv("BMC_ENROLLMENT_TOKEN"),
		StateDir:      envOr("BMC_AGENT_STATE_DIR", "./agent-state"),
		DataDir:       os.Getenv("BMC_AGENT_DATA_DIR"),
		DevInsecure:   os.Getenv("BMC_DEV_INSECURE") == "1",
		ProbeInterval: envInt("BMC_AGENT_PROBE_INTERVAL", 600),
	}
	if a.ServerGRPCURL == "" {
		return a, errors.New("config: BMC_SERVER_GRPC_URL is required")
	}
	if a.DataDir == "" {
		a.DataDir = filepath.Join(a.StateDir, "scratch")
	}
	return a, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
