package version

import "testing"

func TestPartsFromString(t *testing.T) {
	cases := []struct {
		in      string
		m, n, p int
		ok      bool
	}{
		{"0.1.0", 0, 1, 0, true},
		{"0.1.1+d4ae775", 0, 1, 1, true},
		{"0.1.1-12-gabc1234-dirty", 0, 1, 1, true},
		{"1.2.3.4", 1, 2, 3, true},
		{"0.0.0-dev", 0, 0, 0, true},
		// 非语义化版本安全降级：ok=false，且不得 panic。
		{"d4ae775", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"0.1", 0, 0, 0, false},
		{"0.1.x", 0, 0, 0, false},
		{"v0.1.0", 0, 0, 0, false},
	}
	for _, c := range cases {
		m, n, p, ok := PartsFromString(c.in)
		if ok != c.ok || (ok && (m != c.m || n != c.n || p != c.p)) {
			t.Errorf("PartsFromString(%q) = (%d,%d,%d,%t), want (%d,%d,%d,%t)", c.in, m, n, p, ok, c.m, c.n, c.p, c.ok)
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		ver, want string
		wantOK    bool
	}{
		{"0.1.1", "0.1.1", true},
		{"0.1.1+d4ae775", "0.1.1", true},
		{"0.1.0", "0.1.1", false},
		{"0.2.0", "0.1.1", true},
		{"1.0.0", "0.9.9", true},
		// 无法解析 → 不能证明达标。
		{"d4ae775", "0.1.1", false},
		{"", "0.1.1", false},
	}
	for _, c := range cases {
		if got := AtLeast(c.ver, c.want); got != c.wantOK {
			t.Errorf("AtLeast(%q, %q) = %t, want %t", c.ver, c.want, got, c.wantOK)
		}
	}
}
