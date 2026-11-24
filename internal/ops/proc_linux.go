//go:build linux

package ops

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func processCPUSeconds() (float64, error) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	idx := strings.LastIndexByte(s, ')')
	if idx < 0 || idx+2 >= len(s) {
		return 0, errors.New("无法解析 /proc/self/stat")
	}
	fields := strings.Fields(s[idx+1:])
	// fields[0] 是第 3 列 state，utime/stime 分别为第 14/15 列
	if len(fields) < 13 {
		return 0, errors.New("/proc/self/stat 字段不足")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	// Linux 用户态时钟通常为 100Hz
	return float64(utime+stime) / 100.0, nil
}

func processRSSBytes() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func processHandleCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}

func processThreadCount() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "Threads:") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
			if err == nil {
				return n
			}
		}
	}
	return 0
}
