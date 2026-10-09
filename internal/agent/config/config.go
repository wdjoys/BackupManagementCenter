// Package config loads agent configuration from the environment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	TargetAgentID       string
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
	// ResticCheckReadDataSubset 是 restic check 的 --read-data-subset 取值
	// （n/t、x%、或带 k/m/g/t 后缀的大小；空或 0 表示只做结构校验）。默认 1/10：
	// 只做结构校验时仓库内的**静默位腐**不会被发现（实测：位腐仓库 restic check
	// 报健康，而 --read-data/恢复均失败），因此默认每次校验读取 10% 数据。
	ResticCheckReadDataSubset string
}

// validateReadDataSubset 校验 restic --read-data-subset 的取值：空串或 "0" 表示
// 关闭数据校验；否则接受 n/t（1≤n≤t）、x%（0<x≤100，可带小数）、或带 k/m/g/t
// 后缀的正数字节大小。
func validateReadDataSubset(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return nil
	}
	if parts := strings.SplitN(v, "/", 2); len(parts) == 2 && !strings.Contains(parts[1], "/") {
		n, errN := strconv.Atoi(parts[0])
		t2, errT := strconv.Atoi(parts[1])
		if errN == nil && errT == nil && n >= 1 && t2 >= 1 && n <= t2 {
			return nil
		}
		return fmt.Errorf("\"n/t\" must satisfy 1 <= n <= t (got %q)", v)
	}
	if strings.HasSuffix(v, "%") {
		x, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
		if err == nil && x > 0 && x <= 100 {
			return nil
		}
		return fmt.Errorf("percentage must satisfy 0 < x <= 100 (got %q)", v)
	}
	if n, err := strconv.ParseFloat(v[:len(v)-1], 64); err == nil && len(v) > 1 && n > 0 &&
		strings.ContainsRune("kKmMgGtT", rune(v[len(v)-1])) {
		return nil
	}
	return fmt.Errorf("must be \"n/t\", \"x%%\", a positive byte size with k/m/g/t suffix, or 0/empty to disable (got %q)", v)
}

func LoadAgent() (Agent, error) {
	stateDir := envOr("BMC_AGENT_STATE_DIR", "./agent-state")
	readDataSubset := envOr("BMC_RESTIC_CHECK_READ_DATA_SUBSET", "1/10")
	if err := validateReadDataSubset(readDataSubset); err != nil {
		return Agent{}, fmt.Errorf("config: invalid BMC_RESTIC_CHECK_READ_DATA_SUBSET: %w", err)
	}
	sourceMappings, err := parsePathMappings("BMC_SOURCE_PATH_MAPPINGS", os.Getenv("BMC_SOURCE_PATH_MAPPINGS"), true)
	if err != nil {
		return Agent{}, err
	}
	restoreMappings, restoreRoots, err := loadRestoreConfiguration()
	if err != nil {
		return Agent{}, err
	}
	target, tls, err := parseServerEndpoint(os.Getenv("BMC_SERVER_GRPC_URL"), os.Getenv("BMC_SERVER_TLS"))
	if err != nil {
		return Agent{}, err
	}
	a := Agent{ServerGRPCURL: target, ServerTLS: tls, EnrollToken: os.Getenv("BMC_ENROLLMENT_TOKEN"), TargetAgentID: os.Getenv("BMC_TARGET_AGENT_ID"), StateDir: stateDir, DataDir: os.Getenv("BMC_AGENT_DATA_DIR"), ResticCacheDir: filepath.Clean(envOr("BMC_RESTIC_CACHE_DIR", filepath.Join(stateDir, ".cache", "restic"))), DevInsecure: os.Getenv("BMC_DEV_INSECURE") == "1", ProbeInterval: envInt("BMC_AGENT_PROBE_INTERVAL", 600), SourceRoots: splitPaths(os.Getenv("BMC_SOURCE_ROOTS")), RestoreRoots: restoreRoots, SourcePathMappings: sourceMappings, RestorePathMappings: restoreMappings, ScratchMinFreeBytes: envInt64("BMC_SCRATCH_MIN_FREE_BYTES", 0), MaxConcurrency: envInt("BMC_AGENT_MAX_CONCURRENCY", 2), ResticCheckReadDataSubset: readDataSubset}
	if a.ServerGRPCURL == "" {
		return a, errors.New("config: BMC_SERVER_GRPC_URL is required")
	}
	if a.DataDir == "" {
		a.DataDir = filepath.Join(a.StateDir, "scratch")
	}
	return a, nil
}

func parseServerEndpoint(rawURL, legacyTLS string) (string, bool, error) {
	if strings.TrimSpace(rawURL) == "" {
		return "", false, errors.New("config: BMC_SERVER_GRPC_URL is required")
	}
	if rawURL == "" {
		return "", legacyTLS != "0", nil
	}
	if !strings.Contains(rawURL, "://") {
		if legacyTLS != "" && legacyTLS != "0" && legacyTLS != "1" {
			return "", false, fmt.Errorf("config: BMC_SERVER_TLS must be 0 or 1")
		}
		return rawURL, legacyTLS != "0", nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false, fmt.Errorf("config: BMC_SERVER_GRPC_URL must be http(s) host:port without path, query or credentials")
	}
	tls := u.Scheme == "https"
	if legacyTLS != "" {
		if legacyTLS != "0" && legacyTLS != "1" {
			return "", false, fmt.Errorf("config: BMC_SERVER_TLS must be 0 or 1")
		}
		if (legacyTLS == "1") != tls {
			return "", false, errors.New("config: BMC_SERVER_GRPC_URL scheme conflicts with BMC_SERVER_TLS")
		}
	}
	return u.Host, tls, nil
}

// DefaultRestoreRoot is the restore target root used when BMC_RESTORE_ROOT is
// unset; it matches the container path fixed in the agent image.
const DefaultRestoreRoot = "/backup-restore"

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

	hostPath := DefaultRestoreRoot
	if restoreRootRaw != "" {
		hostPath = filepath.Clean(restoreRootRaw)
		if !isAbsolutePath(restoreRootRaw) || hostPath == "." || isRootPath(hostPath) {
			return nil, nil, fmt.Errorf("config: invalid BMC_RESTORE_ROOT %q: path must be absolute and non-root", restoreRootRaw)
		}
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
