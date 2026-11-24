//go:build windows

package ops

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modpsapi                  = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo  = modpsapi.NewProc("GetProcessMemoryInfo")
	modkernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessHandleCount = modkernel32.NewProc("GetProcessHandleCount")
)

// processMemoryCounters 对应 PROCESS_MEMORY_COUNTERS。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

func processCPUSeconds() (float64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	// kernel/user 是 100ns 为单位的“时长”，不能用 Filetime.Nanoseconds()（它按 1601 纪年换算）
	return filetimeDuration(kernel) + filetimeDuration(user), nil
}

func filetimeDuration(ft windows.Filetime) float64 {
	raw := int64(ft.HighDateTime)<<32 + int64(ft.LowDateTime)
	return float64(raw) / 1e7
}

func processRSSBytes() uint64 {
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&c)),
		uintptr(c.cb),
	)
	if r == 0 {
		return 0
	}
	return uint64(c.workingSetSize)
}

func processHandleCount() int {
	var n uint32
	r, _, _ := procGetProcessHandleCount.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 {
		return 0
	}
	return int(n)
}

func processThreadCount() int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	pid := windows.GetCurrentProcessId()
	n := 0
	for err := windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID == pid {
			n++
		}
	}
	return n
}
