//go:build windows

package ops

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestFiletimeDuration(t *testing.T) {
	// 312500 * 100ns = 31.25ms
	if got := filetimeDuration(windows.Filetime{LowDateTime: 312500}); got < 0.031249 || got > 0.031251 {
		t.Fatalf("低 32 位换算错误: %v", got)
	}
	// 1<<32 * 100ns ≈ 429.4967296s
	if got := filetimeDuration(windows.Filetime{HighDateTime: 1}); got < 429.496 || got > 429.498 {
		t.Fatalf("高 32 位换算错误: %v", got)
	}
}
