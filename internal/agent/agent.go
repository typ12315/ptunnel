// Package agent 实现客户端：注册、维持隧道、执行端口转发。
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"ptunnel/internal/config"
	"ptunnel/internal/pki"
	"ptunnel/internal/tunnel"
)

type proxyCounter struct {
	active atomic.Int64
	in     atomic.Int64
	out    atomic.Int64
}

// Agent 客户端运行时。
type Agent struct {
	cfg     *config.ClientConfig
	log     *slog.Logger
	version string

	allowNets []*net.IPNet

	mu      sync.RWMutex
	specs   map[string]tunnel.ProxySpec
	counter map[string]*proxyCounter

	upgrading atomic.Bool
}

// New 创建客户端。
func New(cfg *config.ClientConfig, logger *slog.Logger, version string) (*Agent, error) {
	nets, err := config.ParseCIDRs(cfg.Security.AllowTargets)
	if err != nil {
		return nil, err
	}
	return &Agent{
		cfg:       cfg,
		log:       logger,
		version:   version,
		allowNets: nets,
		specs:     map[string]tunnel.ProxySpec{},
		counter:   map[string]*proxyCounter{},
	}, nil
}

// Run 启动客户端并持续重连，直到 ctx 取消。
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureCert(); err != nil {
		return err
	}
	backoff := a.cfg.Tunnel.ReconnectMin.D()
	if backoff <= 0 {
		backoff = time.Second
	}
	minBackoff := backoff
	maxBackoff := a.cfg.Tunnel.ReconnectMax.D()
	if maxBackoff < backoff {
		maxBackoff = 30 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		started := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// 稳定运行一段时间后再断线，退避从头开始，避免长期停留在最大间隔
		if time.Since(started) > time.Minute {
			backoff = minBackoff
		}
		if err != nil {
			a.log.Warn("隧道连接中断", "err", err, "retry_in", backoff.String())
		}
		jitter := time.Duration(rand.Int63n(int64(backoff)/5 + 1))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff + jitter):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (a *Agent) ensureCert() error {
	certPath, keyPath := a.cfg.Server.Cert, a.cfg.Server.Key
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			if !a.certNeedsRenew(certPath) {
				return nil
			}
			if err := a.renewCert(); err != nil {
				a.log.Warn("证书续期失败，继续使用旧证书", "err", err)
			}
			return nil
		}
	}
	return a.enroll()
}

func (a *Agent) certNeedsRenew(certPath string) bool {
	pair, err := tls.LoadX509KeyPair(certPath, a.cfg.Server.Key)
	if err != nil || len(pair.Certificate) == 0 {
		return true
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return true
	}
	return time.Until(leaf.NotAfter) < 30*24*time.Hour
}

// enroll 使用一次性令牌完成注册并落地证书。
func (a *Agent) enroll() error {
	token := strings.TrimSpace(a.cfg.Server.EnrollToken)
	if token == "" {
		return fmt.Errorf("缺少客户端证书且未提供 enroll_token，请先在服务端创建客户端并获取令牌")
	}
	name := a.cfg.Server.Name
	if name == "" {
		name = "client"
	}
	keyTmp := a.cfg.Server.Key + ".new"
	csr, err := pki.GenerateCSR(keyTmp, name)
	if err != nil {
		return fmt.Errorf("生成密钥失败: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"token": token, "name": name, "csr": string(csr), "version": a.version,
	})
	client, err := a.httpClient("")
	if err != nil {
		return err
	}
	resp, err := client.Post("https://"+a.cfg.Server.Addr+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("注册请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("注册失败(%d): %s", resp.StatusCode, extractErr(data))
	}
	var out struct {
		ClientID string `json:"client_id"`
		Cert     string `json:"cert"`
		CA       string `json:"ca"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("解析注册响应失败: %w", err)
	}
	if err := pki.WritePEM(a.cfg.Server.Cert, []byte(out.Cert), 0o644); err != nil {
		return err
	}
	if err := renameFile(keyTmp, a.cfg.Server.Key); err != nil {
		return err
	}
	if a.cfg.Server.CA != "" {
		_ = pki.WritePEM(a.cfg.Server.CA, []byte(out.CA), 0o644)
	}
	a.log.Info("注册成功", "client_id", out.ClientID, "server", a.cfg.Server.Addr)
	return nil
}

func (a *Agent) renewCert() error {
	keyTmp := a.cfg.Server.Key + ".new"
	cn := a.cfg.Server.Name
	csr, err := pki.GenerateCSR(keyTmp, cn)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"csr": string(csr)})
	client, err := a.httpClient("")
	if err != nil {
		return err
	}
	resp, err := client.Post("https://"+a.cfg.Server.Addr+"/api/client/cert/renew", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("续期失败(%d): %s", resp.StatusCode, extractErr(data))
	}
	var out struct {
		Cert string `json:"cert"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	if err := pki.WritePEM(a.cfg.Server.Cert, []byte(out.Cert), 0o644); err != nil {
		return err
	}
	if err := renameFile(keyTmp, a.cfg.Server.Key); err != nil {
		return err
	}
	a.log.Info("证书续期成功")
	return nil
}

func renameFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func extractErr(data []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(data))
}

func (a *Agent) httpClient(serverName string) (*http.Client, error) {
	return a.httpClientTimeout(serverName, 20*time.Second)
}

func (a *Agent) httpClientTimeout(serverName string, timeout time.Duration) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if a.cfg.Server.Fingerprint != "" {
		want := strings.ToLower(strings.TrimPrefix(a.cfg.Server.Fingerprint, "sha256:"))
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("服务端未提供证书")
			}
			sum := sha256.Sum256(rawCerts[0])
			if hex.EncodeToString(sum[:]) != want {
				return fmt.Errorf("服务端证书指纹不匹配")
			}
			return nil
		}
	} else if pool, err := loadPool(a.cfg.Server.CA); err == nil {
		tlsCfg.RootCAs = pool
	} else {
		tlsCfg.InsecureSkipVerify = true
		a.log.Warn("未找到 CA 证书，注册阶段跳过服务端校验（建议配置 server.ca 或 server.fingerprint）")
	}
	if serverName != "" {
		tlsCfg.ServerName = serverName
	}
	// 续期请求需要携带现有客户端证书（首次注册时证书尚不存在）
	if cert, err := tls.LoadX509KeyPair(a.cfg.Server.Cert, a.cfg.Server.Key); err == nil {
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func loadPool(caPath string) (*x509.CertPool, error) {
	if caPath == "" {
		return nil, fmt.Errorf("未配置 CA 路径")
	}
	b, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("CA 证书解析失败")
	}
	return pool, nil
}

func (a *Agent) tunnelTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.cfg.Server.Cert, a.cfg.Server.Key)
	if err != nil {
		return nil, fmt.Errorf("加载客户端证书失败: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// 强制 TLS 1.3：1.2 下服务端证书为明文，其主体会随抓包外泄。
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{tunnel.ALPN, tunnel.ALPNLegacy},
	}
	host, _, _ := net.SplitHostPort(a.cfg.Server.Addr)
	cfg.ServerName = host
	if pool, err := loadPool(a.cfg.Server.CA); err == nil {
		cfg.RootCAs = pool
	} else {
		cfg.InsecureSkipVerify = true
		a.log.Warn("未找到 CA 证书，隧道连接跳过服务端校验")
	}
	return cfg, nil
}

func (a *Agent) session(ctx context.Context) error {
	tlsCfg, err := a.tunnelTLSConfig()
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", a.cfg.Server.Addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("连接服务端失败: %w", err)
	}
	defer conn.Close()
	if got := conn.ConnectionState().NegotiatedProtocol; !tunnel.IsTunnelALPN(got) {
		return fmt.Errorf("服务端 ALPN 不匹配: %q", got)
	}

	muxCfg := yamux.DefaultConfig()
	muxCfg.EnableKeepAlive = false
	muxCfg.LogOutput = discard{}
	muxCfg.MaxStreamWindowSize = 512 * 1024
	sess, err := yamux.Client(conn, muxCfg)
	if err != nil {
		return fmt.Errorf("创建多路复用会话失败: %w", err)
	}
	defer sess.Close()

	ctrlStream, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("打开控制流失败: %w", err)
	}
	ctrl := tunnel.NewControl(ctrlStream)

	// 客户端身份由证书 CN 决定，hello 中的 client_id 留空由服务端补齐
	hello := tunnel.Hello{
		Version: a.version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Proxies: make([]tunnel.LocalProxy, 0, len(a.cfg.Proxies)),
	}
	for _, p := range a.cfg.Proxies {
		hello.Proxies = append(hello.Proxies, tunnel.LocalProxy{
			Name: p.Name, Protocol: p.Protocol, RemotePort: p.RemotePort, Target: p.Target,
		})
	}
	if err := ctrl.Send(tunnel.MsgHello, hello); err != nil {
		return err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.acceptLoop(sessionCtx, sess)
	go a.statsLoop(sessionCtx, ctrl)

	_ = ctrlStream.SetReadDeadline(time.Now().Add(a.readDeadline()))
	for {
		if sessionCtx.Err() != nil {
			return nil
		}
		_ = ctrlStream.SetReadDeadline(time.Now().Add(a.readDeadline()))
		msgType, payload, err := ctrl.Receive()
		if err != nil {
			return err
		}
		switch msgType {
		case tunnel.MsgHelloAck:
			a.log.Info("隧道已建立", "server", a.cfg.Server.Addr)
		case tunnel.MsgConfigPush:
			var push tunnel.ConfigPush
			if err := json.Unmarshal(payload, &push); err != nil {
				continue
			}
			result := a.applyConfig(push)
			_ = ctrl.Send(tunnel.MsgApplyResult, result)
		case tunnel.MsgPing:
			var p tunnel.Ping
			_ = json.Unmarshal(payload, &p)
			_ = ctrl.Send(tunnel.MsgPong, tunnel.Pong{Seq: p.Seq, TS: time.Now().UnixMilli()})
		case tunnel.MsgKick:
			var k tunnel.Kick
			_ = json.Unmarshal(payload, &k)
			return fmt.Errorf("被服务端断开: %s", k.Reason)
		case tunnel.MsgUpgrade:
			var u tunnel.Upgrade
			if err := json.Unmarshal(payload, &u); err != nil {
				continue
			}
			a.log.Info("收到升级指令", "version", u.Version, "arch", u.Arch, "size", u.Size)
			go a.handleUpgrade(ctrl, u)
		case tunnel.MsgServerShutdown:
			return fmt.Errorf("服务端正在关闭")
		}
	}
}

// readDeadline 计算控制流读超时；心跳缺省为 15s，避免配置为 0 时立刻超时。
func (a *Agent) readDeadline() time.Duration {
	hb := a.cfg.Tunnel.Heartbeat.D()
	if hb <= 0 {
		hb = 15 * time.Second
	}
	return hb * 4
}

func (a *Agent) applyConfig(push tunnel.ConfigPush) tunnel.ApplyResult {
	res := tunnel.ApplyResult{Revision: push.Revision}
	next := make(map[string]tunnel.ProxySpec, len(push.Proxies))
	for _, spec := range push.Proxies {
		if !spec.Enabled {
			continue
		}
		if err := a.checkTarget(spec.TargetAddr); err != nil {
			res.Errors = append(res.Errors, tunnel.ApplyError{ProxyID: spec.ID, Message: err.Error()})
			continue
		}
		next[spec.ID] = spec
	}
	a.mu.Lock()
	a.specs = next
	a.counter = map[string]*proxyCounter{}
	for id := range next {
		a.counter[id] = &proxyCounter{}
	}
	a.mu.Unlock()
	a.log.Info("已应用配置", "revision", push.Revision, "proxies", len(next), "errors", len(res.Errors))
	return res
}

// checkTarget 校验目标地址是否在本地 allow_targets 白名单内。
func (a *Agent) checkTarget(addr string) error {
	if len(a.allowNets) == 0 {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("目标地址非法: %s", addr)
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		resolved, err := net.LookupHost(host)
		if err != nil {
			return fmt.Errorf("目标域名解析失败: %s", host)
		}
		for _, s := range resolved {
			if ip := net.ParseIP(s); ip != nil {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("目标地址无法解析: %s", host)
	}
	for _, ip := range ips {
		ok := false
		for _, n := range a.allowNets {
			if n.Contains(ip) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("目标地址 %s 不在本地 allow_targets 白名单内", ip.String())
		}
	}
	return nil
}

func (a *Agent) acceptLoop(ctx context.Context, sess *yamux.Session) {
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			_ = stream.Close()
			return
		}
		go a.handleStream(stream)
	}
}

func (a *Agent) handleStream(stream net.Conn) {
	defer stream.Close()

	proxyID, srcAddr, err := tunnel.ReadStreamHeader(stream)
	if err != nil {
		return
	}
	a.mu.RLock()
	spec, ok := a.specs[proxyID]
	ctr := a.counter[proxyID]
	a.mu.RUnlock()
	if !ok || ctr == nil || !spec.Enabled {
		_ = tunnel.WriteStreamStatus(stream, false, "代理未启用")
		a.log.Warn("收到未知代理的数据流", "proxy", proxyID)
		return
	}

	ctr.active.Add(1)
	defer ctr.active.Add(-1)

	dialTimeout := a.cfg.Tunnel.DialTimeout.D()
	if dialTimeout <= 0 {
		dialTimeout = 3 * time.Second
	}
	target, err := net.DialTimeout("tcp", spec.TargetAddr, dialTimeout)
	if err != nil {
		_ = tunnel.WriteStreamStatus(stream, false, err.Error())
		a.log.Warn("连接目标失败", "proxy", spec.Name, "target", spec.TargetAddr, "err", err)
		return
	}
	defer target.Close()

	_ = stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := tunnel.WriteStreamStatus(stream, true, ""); err != nil {
		return
	}
	_ = stream.SetWriteDeadline(time.Time{})

	a.log.Debug("开始转发", "proxy", spec.Name, "target", spec.TargetAddr, "src", srcAddr)

	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = target.Close()
			_ = stream.Close()
		})
	}
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	idle := time.Duration(spec.IdleTimeoutSec) * time.Second
	done := make(chan struct{})
	go func() {
		interval := 15 * time.Second
		if idle > 0 && idle/2 < interval {
			interval = idle / 2
		}
		if interval < time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if idle > 0 && time.Since(time.Unix(0, lastActive.Load())) > idle {
					closeAll()
					return
				}
			}
		}
	}()

	grace := a.cfg.Tunnel.HalfCloseGrace.D()
	if grace < 0 {
		grace = 0
	}
	ch := make(chan string, 2)
	go func() {
		_, _ = io.Copy(&countWriter{w: target, n: &ctr.in, last: &lastActive}, &touchReader{r: stream, last: &lastActive})
		if cw, ok := target.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		ch <- "up"
	}()
	go func() {
		_, _ = io.Copy(&countWriter{w: stream, n: &ctr.out, last: &lastActive}, &touchReader{r: target, last: &lastActive})
		if cw, ok := stream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		ch <- "down"
	}()
	first := <-ch
	if first == "down" {
		// 目标已关闭并回传完毕，立即释放，不再占用流配额
		closeAll()
		<-ch
	} else {
		select {
		case <-ch:
		case <-time.After(grace):
			closeAll()
			<-ch
		}
	}
	close(done)
	closeAll()
}

func (a *Agent) statsLoop(ctx context.Context, ctrl *tunnel.Control) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.RLock()
			stats := make([]tunnel.ProxyStats, 0, len(a.counter))
			for id, c := range a.counter {
				stats = append(stats, tunnel.ProxyStats{
					ProxyID:     id,
					ActiveConns: int(c.active.Load()),
					BytesIn:     c.in.Load(),
					BytesOut:    c.out.Load(),
				})
			}
			a.mu.RUnlock()
			if err := ctrl.Send(tunnel.MsgStats, tunnel.Stats{Proxies: stats}); err != nil {
				return
			}
		}
	}
}

type touchReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (t *touchReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type countWriter struct {
	w    io.Writer
	n    *atomic.Int64
	last *atomic.Int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n.Add(int64(n))
		c.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
