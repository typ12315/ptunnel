package proxy

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"ptunnel/internal/server/portmgr"
)

// TestActiveConns 验证在途连接快照按开始时间倒序并读取实时字节数。
func TestActiveConns(t *testing.T) {
	h := &Handler{}
	var in1, out1, in2, out2 atomic.Int64
	in1.Store(10)
	out1.Store(20)
	in2.Store(30)
	out2.Store(40)
	now := time.Now()
	h.active.Store("a", &activeConn{
		connID: "a", clientName: "c1", proxyName: "p1",
		startedAt: now.Add(-time.Second), in: &in1, out: &out1,
	})
	h.active.Store("b", &activeConn{
		connID: "b", clientName: "c2", proxyName: "p2",
		startedAt: now, in: &in2, out: &out2,
	})

	list := h.ActiveConns()
	if len(list) != 2 {
		t.Fatalf("应返回 2 条在途连接，实际 %d", len(list))
	}
	if list[0].ConnID != "b" {
		t.Fatalf("应按开始时间倒序，首条为 %s", list[0].ConnID)
	}
	if list[1].BytesIn != 10 || list[1].BytesOut != 20 {
		t.Fatalf("实时字节数错误: %+v", list[1])
	}
}

// TestPipeUpstreamEOFHonorsGrace 验证上行先结束（短连接关闭）时会按配置等待下行收尾。
func TestPipeUpstreamEOFHonorsGrace(t *testing.T) {
	h := &Handler{halfCloseGrace: 100 * time.Millisecond}
	userLocal, userRemote := net.Pipe()
	streamLocal, streamRemote := net.Pipe()
	defer streamRemote.Close()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		h.pipe(userLocal, streamLocal, portmgr.Proxy{}, new(atomic.Int64), new(atomic.Int64), nil)
		close(done)
	}()

	// 用户侧关闭，模拟短连接结束；隧道流保持打开，closeWrite 对 yamux 流无效。
	_ = userRemote.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipe 未在收尾窗口内返回")
	}
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond {
		t.Fatalf("收尾等待未生效，耗时 %v", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("收尾等待过长，耗时 %v", elapsed)
	}
}

// TestPipeZeroGraceClosesImmediately 验证 half_close_grace=0 时立即释放连接。
func TestPipeZeroGraceClosesImmediately(t *testing.T) {
	h := &Handler{halfCloseGrace: 0}
	userLocal, userRemote := net.Pipe()
	streamLocal, streamRemote := net.Pipe()
	defer streamRemote.Close()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		h.pipe(userLocal, streamLocal, portmgr.Proxy{}, new(atomic.Int64), new(atomic.Int64), nil)
		close(done)
	}()
	_ = userRemote.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("grace=0 时 pipe 应立即返回")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("grace=0 时收尾过慢: %v", elapsed)
	}
}
