//go:build !windows && !linux

package ops

func processCPUSeconds() (float64, error) { return 0, nil }
func processRSSBytes() uint64             { return 0 }
func processHandleCount() int             { return 0 }
func processThreadCount() int             { return 0 }
