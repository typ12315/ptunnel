package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCIDRs(t *testing.T) {
	nets, err := ParseCIDRs([]string{"10.0.0.0/8", "192.168.1.1", "", "::1"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("应解析出 3 个网段，实际 %d", len(nets))
	}
	if _, err := ParseCIDRs([]string{"not-an-ip"}); err == nil {
		t.Fatal("非法输入应报错")
	}
}

func TestLoadServerDefaults(t *testing.T) {
	cfg, err := LoadServer(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("加载默认配置失败: %v", err)
	}
	if cfg.Server.TunnelAddr != "0.0.0.0:8443" || cfg.Audit.ConnLogMode != "full" {
		t.Fatalf("默认值异常: %+v", cfg.Server)
	}
	if cfg.Tunnel.HalfCloseGrace.D() != 500*time.Millisecond {
		t.Fatalf("默认收尾等待应为 500ms，实际 %v", cfg.Tunnel.HalfCloseGrace.D())
	}
	if cfg.Storage.SQLitePath == "" {
		t.Fatal("默认数据库路径未生成")
	}
}

func TestHalfCloseGraceConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	body := "server:\n  addr: 127.0.0.1:8443\ntunnel:\n  half_close_grace: 0s\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatalf("加载客户端配置失败: %v", err)
	}
	if cfg.Tunnel.HalfCloseGrace.D() != 0 {
		t.Fatalf("显式 0 应表示立即关闭，实际 %v", cfg.Tunnel.HalfCloseGrace.D())
	}

	bad := filepath.Join(dir, "bad-client.yaml")
	if err := os.WriteFile(bad, []byte("server:\n  addr: 127.0.0.1:8443\ntunnel:\n  half_close_grace: -1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(bad); err == nil {
		t.Fatal("负的 half_close_grace 应报错")
	}
}

func TestLoadServerFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	body := `
server:
  tunnel_addr: 127.0.0.1:18443
  admin_addr: 127.0.0.1:19443
  port_range: [20000, 21000]
storage:
  data_dir: ` + filepath.ToSlash(dir) + `
tunnel:
  heartbeat: 5s
  idle_timeout: 2m
audit:
  conn_log_mode: sample
  conn_log_sample_rate: 10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServer(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Server.TunnelAddr != "127.0.0.1:18443" {
		t.Fatalf("隧道地址未覆盖: %s", cfg.Server.TunnelAddr)
	}
	if cfg.Tunnel.Heartbeat.D().Seconds() != 5 {
		t.Fatalf("心跳时长解析错误: %v", cfg.Tunnel.Heartbeat.D())
	}
	if cfg.Audit.ConnLogMode != "sample" || cfg.Audit.ConnLogSampleRate != 10 {
		t.Fatalf("审计配置解析错误: %+v", cfg.Audit)
	}
	// 未提供 admin 配置时保留默认管理端口
	if cfg.Server.AdminAddr != "127.0.0.1:19443" {
		t.Fatalf("管理端口异常: %s", cfg.Server.AdminAddr)
	}
}

func TestInvalidPortRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	body := "server:\n  tunnel_addr: 127.0.0.1:8443\n  admin_addr: 127.0.0.1:9443\n  port_range: [30000, 20000]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(path); err == nil {
		t.Fatal("非法端口范围应报错")
	}
}
