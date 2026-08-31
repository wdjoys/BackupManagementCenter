// Package config loads agent configuration from the environment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"backupmanagementcenter/internal/model"
)

type Agent struct {
	ServerGRPCURL       string
	ServerTLS           bool
	EnrollToken         string
	StateDir            string
	DataDir             string
	ResticCacheDir      string
	DevInsecure         bool
	ProbeInterval       int
	SourceRoots         []string
	RestoreRoots        []string
	SourcePathMappings  []model.PathMapping
	RestorePathMappings []model.PathMapping
	ScratchMinFreeBytes int64
	MaxConcurrency      int
}

func LoadAgent() (Agent, error) {
	stateDir := envOr("BMC_AGENT_STATE_DIR", "./agent-state")
	sourceMappings, err := parsePathMappings("BMC_SOURCE_PATH_MAPPINGS", os.Getenv("BMC_SOURCE_PATH_MAPPINGS"), true)
	if err != nil {
		return Agent{}, err
	}
	restoreMappings, restoreRoots, err := loadRestoreConfiguration()
	if err != nil {
		return Agent{}, err
	}
	a := Agent{
		ServerGRPCURL: os.Getenv("BMC_SERVER_GRPC_URL"), ServerTLS: os.Getenv("BMC_SERVER_TLS") != "0",
		EnrollToken: os.Getenv("BMC_ENROLLMENT_TOKEN"), StateDir: stateDir, DataDir: os.Getenv("BMC_AGENT_DATA_DIR"),
		ResticCacheDir: filepath.Clean(envOr("BMC_RESTIC_CACHE_DIR", filepath.Join(stateDir, ".cache", "restic"))),
		DevInsecure:    os.Getenv("BMC_DEV_INSECURE") == "1", ProbeInterval: envInt("BMC_AGENT_PROBE_INTERVAL", 600),
		SourceRoots: splitPaths(os.Getenv("BMC_SOURCE_ROOTS")), RestoreRoots: restoreRoots, SourcePathMappings: sourceMappings, RestorePathMappings: restoreMappings,
		ScratchMinFreeBytes: envInt64("BMC_SCRATCH_MIN_FREE_BYTES", 0), MaxConcurrency: envInt("BMC_AGENT_MAX_CONCURRENCY", 2),
	}
	if a.ServerGRPCURL == "" {
		return a, errors.New("config: BMC_SERVER_GRPC_URL is required")
	}
	if a.DataDir == "" {
		a.DataDir = filepath.Join(a.StateDir, "scratch")
	}
	return a, nil
}

func loadRestoreConfiguration() ([]model.PathMapping, []string, error) {
	pathMappingsRaw := os.Getenv("BMC_RESTORE_PATH_MAPPINGS")
	restoreRootsRaw := os.Getenv("BMC_RESTORE_ROOTS")
	restoreRootRaw := strings.TrimSpace(os.Getenv("BMC_RESTORE_ROOT"))

	if strings.TrimSpace(pathMappingsRaw) != "" {
		mappings, err := parsePathMappings("BMC_RESTORE_PATH_MAPPINGS", pathMappingsRaw, false)
		if err != nil {
			return nil, nil, err
		}
		return mappings, splitPaths(restoreRootsRaw), nil
	}
	if restoreRootRaw == "" {
		return nil, splitPaths(restoreRootsRaw), nil
	}

	hostPath := filepath.Clean(restoreRootRaw)
	if !isAbsolutePath(hostPath) || hostPath == "." || isRootPath(hostPath) {
		return nil, nil, fmt.Errorf("config: invalid BMC_RESTORE_ROOT %q: path must be absolute and non-root", restoreRootRaw)
	}
	return []model.PathMapping{{HostPath: hostPath, RuntimePath: "/backup-restore", ReadOnly: false}}, restoreRootsOrDefault(restoreRootsRaw), nil
}

func restoreRootsOrDefault(raw string) []string {
	if roots := splitPaths(raw); len(roots) > 0 {
		return roots
	}
	return []string{"/backup-restore"}
}

func isRootPath(p string) bool {
	if p == string(filepath.Separator) {
		return true
	}
	volume := filepath.VolumeName(p)
	return volume != "" && p == volume+string(filepath.Separator)
}

func parsePathMappings(key, raw string, readOnly bool) ([]model.PathMapping, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values map[string]string
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&values); err != nil || values == nil {
		if err == nil {
			err = errors.New("mapping must be a JSON object")
		}
		return nil, fmt.Errorf("config: invalid %s: %w", key, err)
	}
	var extra any
	if dec.Decode(&extra) == nil {
		return nil, fmt.Errorf("config: invalid %s: trailing data", key)
	}
	result := make([]model.PathMapping, 0, len(values))
	for host, runtime := range values {
		host = filepath.Clean(strings.TrimSpace(host))
		runtime = filepath.Clean(strings.TrimSpace(runtime))
		if !isAbsolutePath(host) || !isAbsolutePath(runtime) || host == string(filepath.Separator) || runtime == string(filepath.Separator) || host == "." || runtime == "." {
			return nil, fmt.Errorf("config: invalid %s path mapping %q: paths must be absolute and non-root", key, host)
		}
		result = append(result, model.PathMapping{HostPath: host, RuntimePath: runtime, ReadOnly: readOnly})
	}
	return result, nil
}

func isAbsolutePath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':')
}

func splitPaths(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = filepath.Clean(strings.TrimSpace(p)); p != "." && p != "" {
			out = append(out, p)
		}
	}
	return out
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
func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return def
}
