// Package version carries the build-time version of the server and agent.
package version

import "strconv"

// Version is overridden via -ldflags "-X backupmanagementcenter/internal/version.Version=x.y.z".
// 构建脚本注入 "基础版本+构建标识"（如 0.1.1+d4ae775），使不同构建可辨识。
var Version = "0.0.0-dev"

// Parts returns (major, minor, patch, ok) of Version.
func Parts() (int, int, int, bool) { return PartsFromString(Version) }

// PartsFromString parses the leading major.minor.patch of ver, ignoring a build
// suffix on the patch component ("0.1.1+d4ae775", "0.1.1-12-gabc1234-dirty").
// 非 x.y.z 形式的版本返回 ok=false，不 panic。
func PartsFromString(ver string) (int, int, int, bool) {
	major, rest, ok := cut(ver)
	if !ok {
		return 0, 0, 0, false
	}
	minor, rest, ok := cut(rest)
	if !ok {
		return 0, 0, 0, false
	}
	m, err1 := strconv.Atoi(major)
	n, err2 := strconv.Atoi(minor)
	p, err3 := leadingInt(rest)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return m, n, p, true
}

// AtLeast reports whether ver >= want. 任一侧不可解析时返回 false：无法证明版本达标。
func AtLeast(ver, want string) bool {
	vm, vn, vp, ok := PartsFromString(ver)
	if !ok {
		return false
	}
	wm, wn, wp, ok := PartsFromString(want)
	if !ok {
		return false
	}
	if vm != wm {
		return vm > wm
	}
	if vn != wn {
		return vn > wn
	}
	return vp >= wp
}

// leadingInt parses the leading decimal digits of s ("1+d4ae775" → 1) and
// reports an error when there are none.
func leadingInt(s string) (int, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(s[:i])
}

func cut(s string) (head, tail string, ok bool) {
	for i := range s {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
