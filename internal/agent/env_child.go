package agent

import (
	"os"
	"runtime"
	"strings"
)

// childEnv builds the child process environment: a minimal safe baseline
// (PATH, HOME, temp dirs, Windows system vars) plus caller-provided entries.
// Caller entries win on key conflicts.
func childEnv(extra []string) []string {
	base := map[string]string{
		"PATH": os.Getenv("PATH"),
	}
	if home := os.Getenv("HOME"); home != "" {
		base["HOME"] = home
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		base["USERPROFILE"] = up
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		base["TMPDIR"] = tmp
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "SystemDrive", "COMSPEC", "TEMP", "TMP", "windir", "PATHEXT"} {
			if v := os.Getenv(k); v != "" {
				base[k] = v
			}
		}
	} else if tmp := os.Getenv("TMP"); tmp != "" && base["TMPDIR"] == "" {
		base["TMPDIR"] = tmp
	}

	for _, kv := range extra {
		if i := strings.IndexByte(kv, '='); i > 0 {
			base[kv[:i]] = kv[i+1:]
		}
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}
