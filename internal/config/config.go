// Package config 负责服务端与客户端的配置加载、默认值与校验。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 "15s" / "5m" 这类 YAML 写法。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("解析时长 %q 失败: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D 返回标准库时长。
func (d Duration) D() time.Duration { return time.Duration(d) }

// ServerConfig 服务端配置。
type ServerConfig struct {
	Server struct {
		TunnelAddr string `yaml:"tunnel_addr"`
		AdminAddr  string `yaml:"admin_addr"`
		PublicBind string `yaml:"public_bind"`
		PortRange  [2]int `yaml:"port_range"`
		// TunnelCertHosts 自动生成隧道证书时写入的 SAN（域名或 IP）。
		TunnelCertHosts []string `yaml:"tunnel_cert_hosts"`
		// PProfAddr 可选的 pprof 调试监听地址（如 127.0.0.1:6060），留空不启动。
		PProfAddr string `yaml:"pprof_addr"`
	} `yaml:"server"`

	Storage struct {
		Driver     string `yaml:"driver"` // sqlite（本地，默认）/ mysql
		DataDir    string `yaml:"data_dir"`
		SQLitePath string `yaml:"sqlite_path"`
		MySQL      struct {
			DSN             string   `yaml:"dsn"`
			MaxOpenConns    int      `yaml:"max_open_conns"`
			MaxIdleConns    int      `yaml:"max_idle_conns"`
			ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
		} `yaml:"mysql"`
	} `yaml:"storage"`

	Auth struct {
		SessionTTL     Duration `yaml:"session_ttl"`
		LoginRateLimit int      `yaml:"login_rate_limit"` // 每 IP 每分钟失败次数上限
	} `yaml:"auth"`

	Tunnel struct {
		Heartbeat         Duration `yaml:"heartbeat"`
		IdleTimeout       Duration `yaml:"idle_timeout"`
		MaxStreams        int      `yaml:"max_streams_per_client"`
		AutoApproveLocal  bool     `yaml:"auto_approve_local"`
		HandshakeTimeout  Duration `yaml:"handshake_timeout"`
		ClientDialTimeout Duration `yaml:"client_dial_timeout"`
		MaxConnsPerProxy  int      `yaml:"max_conns_per_proxy"`
		MaxConnsPerClient int      `yaml:"max_conns_per_client"`
		// HalfCloseGrace 一端结束后等待另一方向收尾的时长；0 表示立即关闭。
		HalfCloseGrace Duration `yaml:"half_close_grace"`
	} `yaml:"tunnel"`

	Audit struct {
		ConnLogMode       string   `yaml:"conn_log_mode"` // full / sample / off
		ConnLogSampleRate int      `yaml:"conn_log_sample_rate"`
		ConnLogRetention  int      `yaml:"conn_log_retention_days"`
		ConnLogQueueSize  int      `yaml:"conn_log_queue_size"`
		MACLookup         bool     `yaml:"mac_lookup"`
		MACCacheTTL       Duration `yaml:"mac_cache_ttl"`
		// SQL 语句审计（仅对绑定了应用类型的代理生效），按天写本地文件并压缩归档
		SQLLogEnabled       bool   `yaml:"sql_log_enabled"`
		SQLLogDir           string `yaml:"sql_log_dir"`
		SQLLogRetentionDays int    `yaml:"sql_log_retention_days"`
		SQLStatementMaxLen  int    `yaml:"sql_statement_max_len"`
	} `yaml:"audit"`
}

// DefaultServer 返回带默认值的服务端配置。
func DefaultServer() *ServerConfig {
	c := &ServerConfig{}
	c.Server.TunnelAddr = "0.0.0.0:8443"
	c.Server.AdminAddr = "0.0.0.0:9443"
	c.Server.PublicBind = "0.0.0.0"
	c.Server.PortRange = [2]int{20000, 30000}
	c.Server.TunnelCertHosts = []string{"localhost", "127.0.0.1"}
	c.Storage.DataDir = "./data"
	c.Storage.Driver = "sqlite"
	c.Storage.SQLitePath = ""
	c.Storage.MySQL.MaxOpenConns = 20
	c.Storage.MySQL.MaxIdleConns = 10
	c.Storage.MySQL.ConnMaxLifetime = Duration(30 * time.Minute)
	c.Auth.SessionTTL = Duration(12 * time.Hour)
	c.Auth.LoginRateLimit = 10
	c.Tunnel.Heartbeat = Duration(15 * time.Second)
	c.Tunnel.IdleTimeout = Duration(5 * time.Minute)
	c.Tunnel.MaxStreams = 4096
	c.Tunnel.HandshakeTimeout = Duration(10 * time.Second)
	c.Tunnel.ClientDialTimeout = Duration(3 * time.Second)
	c.Tunnel.HalfCloseGrace = Duration(500 * time.Millisecond)
	c.Audit.ConnLogMode = "full"
	c.Audit.ConnLogSampleRate = 1
	c.Audit.ConnLogRetention = 30
	c.Audit.ConnLogQueueSize = 10000
	c.Audit.MACLookup = true
	c.Audit.MACCacheTTL = Duration(60 * time.Second)
	c.Audit.SQLLogEnabled = true
	c.Audit.SQLLogRetentionDays = 30
	c.Audit.SQLStatementMaxLen = 4096
	return c
}

// LoadServer 从文件加载服务端配置（文件不存在时使用默认值）。
func LoadServer(path string) (*ServerConfig, error) {
	c := DefaultServer()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("读取配置失败: %w", err)
			}
		} else if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("解析配置失败: %w", err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.Storage.Driver)) {
	case "", "sqlite", "local":
		c.Storage.Driver = "sqlite"
		if c.Storage.SQLitePath == "" {
			c.Storage.SQLitePath = filepath.Join(c.Storage.DataDir, "ptunnel.db")
		}
	case "mysql":
		c.Storage.Driver = "mysql"
		if strings.TrimSpace(c.Storage.MySQL.DSN) == "" {
			return nil, fmt.Errorf("storage.mysql.dsn 不能为空（driver=mysql）")
		}
	default:
		return nil, fmt.Errorf("storage.driver 仅支持 sqlite / mysql，当前: %q", c.Storage.Driver)
	}
	if c.Audit.SQLLogDir == "" {
		c.Audit.SQLLogDir = filepath.Join(c.Storage.DataDir, "sqllogs")
	}
	if c.Audit.SQLLogRetentionDays < 0 {
		c.Audit.SQLLogRetentionDays = 0
	}
	if c.Audit.SQLStatementMaxLen < 128 {
		c.Audit.SQLStatementMaxLen = 4096
	}
	if c.Auth.SessionTTL <= 0 {
		c.Auth.SessionTTL = Duration(12 * time.Hour)
	}
	if c.Audit.ConnLogSampleRate <= 0 {
		c.Audit.ConnLogSampleRate = 1
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate 校验配置合法性。
func (c *ServerConfig) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.TunnelAddr); err != nil {
		return fmt.Errorf("server.tunnel_addr 非法: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.Server.AdminAddr); err != nil {
		return fmt.Errorf("server.admin_addr 非法: %w", err)
	}
	if c.Server.PProfAddr != "" {
		if _, _, err := net.SplitHostPort(c.Server.PProfAddr); err != nil {
			return fmt.Errorf("server.pprof_addr 非法: %w", err)
		}
	}
	if c.Tunnel.HalfCloseGrace < 0 {
		return fmt.Errorf("tunnel.half_close_grace 不能为负数")
	}
	lo, hi := c.Server.PortRange[0], c.Server.PortRange[1]
	if lo <= 0 || hi <= lo || hi > 65535 {
		return fmt.Errorf("server.port_range 非法: [%d, %d]", lo, hi)
	}
	switch c.Audit.ConnLogMode {
	case "full", "sample", "off":
	default:
		return fmt.Errorf("audit.conn_log_mode 仅支持 full/sample/off")
	}
	return nil
}

// ClientConfig 客户端配置。
type ClientConfig struct {
	Server struct {
		Addr        string `yaml:"addr"`
		CA          string `yaml:"ca"`
		Fingerprint string `yaml:"fingerprint"`
		Cert        string `yaml:"cert"`
		Key         string `yaml:"key"`
		Name        string `yaml:"name"`
		EnrollToken string `yaml:"enroll_token"`
	} `yaml:"server"`

	Tunnel struct {
		ReconnectMin Duration `yaml:"reconnect_min"`
		ReconnectMax Duration `yaml:"reconnect_max"`
		Heartbeat    Duration `yaml:"heartbeat"`
		IdleTimeout  Duration `yaml:"idle_timeout"`
		DialTimeout  Duration `yaml:"dial_timeout"`
		// HalfCloseGrace 一端结束后等待另一方向收尾的时长；0 表示立即关闭。
		HalfCloseGrace Duration `yaml:"half_close_grace"`
	} `yaml:"tunnel"`

	Security struct {
		AllowTargets []string `yaml:"allow_targets"`
	} `yaml:"security"`

	Proxies []LocalProxy `yaml:"proxies"`
}

// LocalProxy 客户端本地登记用的代理声明。
type LocalProxy struct {
	Name       string `yaml:"name" json:"name"`
	Protocol   string `yaml:"protocol" json:"protocol"`
	RemotePort int    `yaml:"remote_port" json:"remote_port"`
	Target     string `yaml:"target" json:"target"`
}

// DefaultClient 返回带默认值的客户端配置。
func DefaultClient() *ClientConfig {
	c := &ClientConfig{}
	c.Tunnel.ReconnectMin = Duration(1 * time.Second)
	c.Tunnel.ReconnectMax = Duration(30 * time.Second)
	c.Tunnel.Heartbeat = Duration(15 * time.Second)
	c.Tunnel.IdleTimeout = Duration(5 * time.Minute)
	c.Tunnel.DialTimeout = Duration(3 * time.Second)
	c.Tunnel.HalfCloseGrace = Duration(500 * time.Millisecond)
	return c
}

// LoadClient 从文件加载客户端配置。
func LoadClient(path string) (*ClientConfig, error) {
	c := DefaultClient()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取客户端配置失败: %w", err)
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("解析客户端配置失败: %w", err)
	}
	if c.Server.Addr == "" {
		return nil, fmt.Errorf("server.addr 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Server.Addr); err != nil {
		return nil, fmt.Errorf("server.addr 需为 host:port 形式: %w", err)
	}
	if c.Tunnel.HalfCloseGrace < 0 {
		return nil, fmt.Errorf("tunnel.half_close_grace 不能为负数")
	}
	if c.Server.Cert == "" || c.Server.Key == "" {
		c.Server.Cert = "client.crt"
		c.Server.Key = "client.key"
	}
	if c.Server.CA == "" {
		c.Server.CA = "ca.pem"
	}
	for i := range c.Proxies {
		if c.Proxies[i].Protocol == "" {
			c.Proxies[i].Protocol = "tcp"
		}
		if c.Proxies[i].RemotePort <= 0 || c.Proxies[i].Target == "" {
			return nil, fmt.Errorf("本地代理 %q 配置不完整（需要 remote_port 与 target）", c.Proxies[i].Name)
		}
	}
	return c, nil
}

// ParseCIDRs 把字符串列表解析为网段列表，非法条目直接报错。
func ParseCIDRs(list []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("非法 IP/CIDR: %q", s)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			s = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("非法 CIDR %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, nil
}
