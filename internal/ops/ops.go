// Package ops 采集服务端进程运行状态（CPU、内存、线程/句柄），用于运维检测接口。
package ops

import (
	"sync"
	"time"
)

// Snapshot 进程级运行状态。
type Snapshot struct {
	CPUSeconds float64 `json:"cpu_seconds"`
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   uint64  `json:"rss_bytes"`
	Handles    int     `json:"handles"`
	Threads    int     `json:"threads"`
}

// Collector 保存上次采样，用差值计算 CPU 使用率。
type Collector struct {
	mu      sync.Mutex
	lastCPU float64
	lastAt  time.Time
	valid   bool
}

// NewCollector 创建采集器。
func NewCollector() *Collector { return &Collector{} }

// Sample 返回当前进程状态；首次调用 CPU 百分比为 0。
func (c *Collector) Sample() Snapshot {
	cpu, err := processCPUSeconds()
	s := Snapshot{
		CPUSeconds: cpu,
		RSSBytes:   processRSSBytes(),
		Handles:    processHandleCount(),
		Threads:    processThreadCount(),
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && c.valid && cpu >= c.lastCPU {
		if dt := now.Sub(c.lastAt).Seconds(); dt > 0 {
			s.CPUPercent = (cpu - c.lastCPU) / dt * 100
		}
	}
	if err == nil {
		c.lastCPU = cpu
		c.lastAt = now
		c.valid = true
	}
	return s
}
