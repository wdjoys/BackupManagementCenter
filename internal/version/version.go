// Package version carries the build-time version of the server and agent.
package version

import "strconv"

// Version is overridden via -ldflags "-X backupmanagementcenter/internal/version.Version=x.y.z".
var Version = "0.0.0-dev"

// Parts returns (major, minor, patch, ok) of a "major.minor.patch[...]" string.
func Parts() (int, int, int, bool) {
	major, rest, ok := cut(Version)
	if !ok {
		return 0, 0, 0, false
	}
	minor, rest, ok := cut(rest)
	if !ok {
		return 0, 0, 0, false
	}
	patch, _, _ := cut(rest)
	m, err1 := strconv.Atoi(major)
	n, err2 := strconv.Atoi(minor)
	p, err3 := strconv.Atoi(patch)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return m, n, p, true
}

func cut(s string) (head, tail string, ok bool) {
	for i := range s {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
