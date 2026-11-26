package release

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.5.0", "0.4.0", 1},
		{"0.4.0", "0.5.0", -1},
		{"0.4.0", "0.4.0", 0},
		{"v0.5.0", "0.4.9", 1},
		{"1.0", "1.0.0", 0},
		{"0.4.10", "0.4.9", 1},
		{"0.4.0", "dev", 1},
		{"dev", "0.4.0", -1},
		{"", "0.1.0", -1},
		{"1.0.0-rc1", "1.0.0", -1}, // 预发布段低于正式版
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsUpgradeable(t *testing.T) {
	if !IsUpgradeable("0.4.0", "0.5.0") {
		t.Fatal("更高版本应可升级")
	}
	if IsUpgradeable("0.5.0", "0.5.0") {
		t.Fatal("同版本不应升级")
	}
	if IsUpgradeable("0.5.0", "0.4.0") {
		t.Fatal("更低版本不应升级")
	}
	if IsUpgradeable("0.4.0", "") {
		t.Fatal("没有可用版本时不应升级")
	}
}