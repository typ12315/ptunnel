// Package clientmgr 管理客户端隧道会话、配置下发与心跳。
package clientmgr

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/yamux"

	"ptunnel/internal/release"
	"ptunnel/internal/store"
	"ptunnel/internal/tunnel"
)

// Options 会话管理参数。
type Options struct {
	Heartbeat         time.Duration
	IdleTimeout       time.Duration
	HandshakeTimeout  time.Duration
	MaxStreams        int
	MaxConnsPerClient int
	AutoApprove       bool
	PublicBind        string
	DialTimeout       time.Duration
	DefaultMaxConns   int
}

// Session 一个在线的客户端会话。
type Session struct {
	ClientID   string
	ClientName string
	RemoteAddr string
	Since      time.Time
	mux        *yamux.Session
	ctrl       *tunnel.Control
	revision   int64
	streams    atomic.Int64
	lastPong   atomic.Int64
	closed     chan struct{}
	closeOnce  sync.Once
	kickReason string
}

// Manager 在线会话管理器。
type Manager struct {
	st   *store.Store
	log  *slog.Logger
	opts Options

	mu       sync.RWMutex
	sessions map[string]*Session
	upgrades map[string]*UpgradeState

	// OnProxiesRegistered 在客户端本地代理登记为「已通过」后触发，用于同步端口监听。
	OnProxiesRegistered func()
}

// New 创建会话管理器。
func New(st *store.Store, logger *slog.Logger, opts Options) *Manager {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 15 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.MaxStreams <= 0 {
		opts.MaxStreams = 4096
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}
	if opts.PublicBind == "" {
		opts.PublicBind = "0.0.0.0"
	}
	return &Manager{st: st, log: logger, opts: opts, sessions: map[string]*Session{}, upgrades: map[string]*UpgradeState{}}
}

// PeerClientID 从 TLS 连接状态中取出客户端 ID（证书 CN）。
func PeerClientID(conn net.Conn) string {
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return ""
	}
	state := tc.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	return state.PeerCertificates[0].Subject.CommonName
}

// Attach 接管一条已认证的隧道连接，阻塞直到连接结束。
func (m *Manager) Attach(conn net.Conn) error {
	peerID := PeerClientID(conn)
	if peerID == "" {
		return fmt.Errorf("隧道连接缺少客户端证书")
	}

	muxCfg := yamux.DefaultConfig()
	muxCfg.EnableKeepAlive = false
	muxCfg.LogOutput = discard{}
	muxCfg.AcceptBacklog = 1024
	muxCfg.MaxStreamWindowSize = 512 * 1024
	muxSession, err := yamux.Server(conn, muxCfg)
	if err != nil {
		return fmt.Errorf("创建多路复用会话失败: %w", err)
	}
	defer muxSession.Close()

	ctrlStream, err := m.awaitStream(muxSession)
	if err != nil {
		return fmt.Errorf("等待控制流失败: %w", err)
	}
	ctrl := tunnel.NewControl(ctrlStream)

	msgType, payload, err := ctrl.Receive()
	if err != nil {
		return fmt.Errorf("读取握手消息失败: %w", err)
	}
	if msgType != tunnel.MsgHello {
		return fmt.Errorf("首条消息必须是 hello，收到 %q", msgType)
	}
	var hello tunnel.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return fmt.Errorf("解析 hello 失败: %w", err)
	}
	if hello.ClientID != "" && hello.ClientID != peerID {
		return fmt.Errorf("hello 中的 client_id 与证书不一致")
	}

	client, err := m.st.GetClient(peerID)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("客户端记录不存在: %s", peerID)
	}
	if !client.Enabled {
		return fmt.Errorf("客户端已被禁用")
	}
	if client.RevokedAt != nil {
		return fmt.Errorf("客户端证书已被吊销")
	}

	m.st.SetClientSeen(client.ID, hello.Version, hello.OS, hello.Arch, conn.RemoteAddr().String())
	m.markUpgraded(client.ID, hello.Version)
	m.registerLocalProxies(client, hello.Proxies)

	sess := &Session{
		ClientID:   client.ID,
		ClientName: client.Name,
		RemoteAddr: conn.RemoteAddr().String(),
		Since:      time.Now(),
		mux:        muxSession,
		ctrl:       ctrl,
		closed:     make(chan struct{}),
	}
	sess.lastPong.Store(time.Now().UnixMilli())
	m.install(sess)
	defer m.remove(sess)

	if err := ctrl.Send(tunnel.MsgHelloAck, tunnel.HelloAck{
		ServerTime:     time.Now().UnixMilli(),
		HeartbeatSec:   int(m.opts.Heartbeat / time.Second),
		IdleTimeoutSec: int(m.opts.IdleTimeout / time.Second),
	}); err != nil {
		return err
	}
	if err := m.Push(client.ID); err != nil {
		m.log.Warn("首次下发配置失败", "client", client.Name, "err", err)
	}

	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	go m.heartbeat(sess, stopHeartbeat)

	_ = ctrlStream.SetReadDeadline(time.Now().Add(m.opts.Heartbeat * 4))
	for {
		_ = ctrlStream.SetReadDeadline(time.Now().Add(m.opts.Heartbeat * 4))
		msgType, payload, err := ctrl.Receive()
		if err != nil {
			if sess.kickReason != "" {
				m.log.Info("客户端被踢下线", "client", client.Name, "reason", sess.kickReason)
			}
			// 在线状态统一由 remove 处理，避免旧会话回写覆盖新会话的 online 状态
			return nil
		}
		switch msgType {
		case tunnel.MsgPong:
			sess.lastPong.Store(time.Now().UnixMilli())
		case tunnel.MsgApplyResult:
			var res tunnel.ApplyResult
			if json.Unmarshal(payload, &res) == nil {
				m.applyResult(client, res)
			}
		case tunnel.MsgStats:
			// 统计以服务端本地计数为准，此处仅作为保活信号
			sess.lastPong.Store(time.Now().UnixMilli())
		case tunnel.MsgUpgradeResult:
			var res tunnel.UpgradeResult
			if json.Unmarshal(payload, &res) == nil {
				status, msg := "ok", res.Message
				if !res.OK {
					status = "failed"
				}
				m.setUpgrade(client.ID, &UpgradeState{Target: res.Version, Status: status, Message: msg, At: time.Now()})
				m.log.Info("客户端升级结果", "client", client.Name, "status", status, "target", res.Version, "msg", msg)
			}
		case tunnel.MsgPing:
			var p tunnel.Ping
			_ = json.Unmarshal(payload, &p)
			_ = ctrl.Send(tunnel.MsgPong, tunnel.Pong{Seq: p.Seq, TS: time.Now().UnixMilli()})
		default:
			m.log.Debug("忽略未知控制消息", "type", msgType, "client", client.Name)
		}
	}
}

func (m *Manager) applyResult(client *store.Client, res tunnel.ApplyResult) {
	for _, e := range res.Errors {
		m.st.SetProxyState(e.ProxyID, "error", e.Message)
		m.log.Warn("客户端应用代理失败", "client", client.Name, "proxy", e.ProxyID, "msg", e.Message)
	}
}

func (m *Manager) install(s *Session) {
	m.mu.Lock()
	old := m.sessions[s.ClientID]
	m.sessions[s.ClientID] = s
	m.mu.Unlock()
	if old != nil {
		old.closeWithReason("被新的连接替换")
	}
	m.st.SetClientStatus(s.ClientID, "online")
	m.log.Info("客户端上线", "client", s.ClientName, "since", s.Since.Format(time.RFC3339))
}

func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	current := m.sessions[s.ClientID]
	if current == s {
		delete(m.sessions, s.ClientID)
	}
	m.mu.Unlock()
	s.closeWithReason("连接结束")
	// 只有当前会话失效时才置为离线；被新会话替换的旧会话不能覆盖在线状态
	if current == s {
		m.st.SetClientStatus(s.ClientID, "offline")
		m.log.Info("客户端下线", "client", s.ClientName)
	}
}

// awaitStream 带超时地等待客户端打开控制流。
func (m *Manager) awaitStream(sess *yamux.Session) (net.Conn, error) {
	type result struct {
		s   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := sess.AcceptStream()
		ch <- result{s, err}
	}()
	timeout := m.opts.HandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("等待控制流超时")
	}
}

func (s *Session) closeWithReason(reason string) {
	s.closeOnce.Do(func() {
		s.kickReason = reason
		close(s.closed)
		_ = s.ctrl.Close()
		_ = s.mux.Close()
	})
}

func (m *Manager) heartbeat(s *Session, stop <-chan struct{}) {
	ticker := time.NewTicker(m.opts.Heartbeat)
	defer ticker.Stop()
	var seq int64
	for {
		select {
		case <-stop:
			return
		case <-s.closed:
			return
		case <-ticker.C:
			last := time.UnixMilli(s.lastPong.Load())
			if time.Since(last) > m.opts.Heartbeat*3 {
				m.log.Warn("心跳超时，断开客户端", "client", s.ClientName)
				s.closeWithReason("心跳超时")
				return
			}
			seq++
			if err := s.ctrl.Send(tunnel.MsgPing, tunnel.Ping{Seq: seq, TS: time.Now().UnixMilli()}); err != nil {
				return
			}
		}
	}
}

// registerLocalProxies 将客户端本地声明的代理登记到服务端。
func (m *Manager) registerLocalProxies(client *store.Client, list []tunnel.LocalProxy) {
	approved := 0
	for _, lp := range list {
		if lp.RemotePort <= 0 || lp.Target == "" {
			continue
		}
		proto := lp.Protocol
		if proto == "" {
			proto = "tcp"
		}
		existing, err := m.st.FindLocalProxy(client.ID, proto, lp.RemotePort)
		if err != nil {
			m.log.Warn("查询本地代理失败", "err", err)
			continue
		}
		if existing != nil {
			continue
		}
		name := lp.Name
		if name == "" {
			name = fmt.Sprintf("local-%d", lp.RemotePort)
		}
		approval := "pending"
		if m.opts.AutoApprove {
			approval = "approved"
		}
		p := &store.Proxy{
			ID:         uuid.NewString(),
			ClientID:   client.ID,
			Name:       name,
			Protocol:   proto,
			BindAddr:   m.opts.PublicBind,
			RemotePort: lp.RemotePort,
			TargetAddr: lp.Target,
			Enabled:    true,
			AllowCIDRs: []string{},
			Origin:     "local",
			Approval:   approval,
			State:      "idle",
		}
		if err := m.st.CreateProxy(p); err != nil {
			m.log.Warn("登记本地代理失败", "client", client.Name, "port", lp.RemotePort, "err", err)
			continue
		}
		m.log.Info("登记本地代理", "client", client.Name, "port", lp.RemotePort, "target", lp.Target, "approval", approval)
		if approval == "approved" {
			approved++
		}
	}
	if approved > 0 && m.OnProxiesRegistered != nil {
		go m.OnProxiesRegistered()
	}
}

// Push 向指定客户端下发全量期望配置。
func (m *Manager) Push(clientID string) error {
	s := m.Get(clientID)
	if s == nil {
		return fmt.Errorf("客户端不在线")
	}
	proxies, err := m.st.ListPushable(clientID)
	if err != nil {
		return err
	}
	specs := make([]tunnel.ProxySpec, 0, len(proxies))
	for _, p := range proxies {
		specs = append(specs, tunnel.ProxySpec{
			ID:             p.ID,
			Name:           p.Name,
			Protocol:       p.Protocol,
			BindAddr:       p.BindAddr,
			RemotePort:     p.RemotePort,
			TargetAddr:     p.TargetAddr,
			Enabled:        p.Enabled,
			MaxConns:       p.MaxConns,
			AllowCIDRs:     p.AllowCIDRs,
			IdleTimeoutSec: int(m.opts.IdleTimeout / time.Second),
		})
	}
	rev := atomic.AddInt64(&s.revision, 1)
	if err := s.ctrl.Send(tunnel.MsgConfigPush, tunnel.ConfigPush{Revision: rev, Proxies: specs}); err != nil {
		return err
	}
	m.log.Info("已下发配置", "client", s.ClientName, "revision", rev, "proxies", len(specs))
	return nil
}

// PushAll 向所有在线客户端下发配置。
func (m *Manager) PushAll() {
	for _, s := range m.Snapshot() {
		if err := m.Push(s.ClientID); err != nil {
			m.log.Debug("下发配置失败", "client", s.ClientName, "err", err)
		}
	}
}

// Get 返回在线会话。
func (m *Manager) Get(clientID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[clientID]
}

// IsOnline 判断客户端是否在线。
func (m *Manager) IsOnline(clientID string) bool { return m.Get(clientID) != nil }

// SessionInfo 会话快照。
type SessionInfo struct {
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	Since      time.Time `json:"since"`
	Revision   int64     `json:"revision"`
}

// Snapshot 返回全部在线会话。
func (m *Manager) Snapshot() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, SessionInfo{
			ClientID:   s.ClientID,
			ClientName: s.ClientName,
			Since:      s.Since,
			Revision:   atomic.LoadInt64(&s.revision),
		})
	}
	return out
}

// Count 返回在线客户端数量。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// SessionDetail 在线会话的详细快照（供运维检测）。
type SessionDetail struct {
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	RemoteAddr string    `json:"remote_addr"`
	Since      time.Time `json:"since"`
	LastPong   time.Time `json:"last_pong"`
	Revision   int64     `json:"revision"`
	Streams    int64     `json:"streams"`
}

// Details 返回全部在线会话的详细信息。
func (m *Manager) Details() []SessionDetail {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SessionDetail, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, SessionDetail{
			ClientID:   s.ClientID,
			ClientName: s.ClientName,
			RemoteAddr: s.RemoteAddr,
			Since:      s.Since,
			LastPong:   time.UnixMilli(s.lastPong.Load()),
			Revision:   atomic.LoadInt64(&s.revision),
			Streams:    s.streams.Load(),
		})
	}
	return out
}

// Kick 踢下线。
func (m *Manager) Kick(clientID, reason string) {
	s := m.Get(clientID)
	if s == nil {
		return
	}
	_ = s.ctrl.Send(tunnel.MsgKick, tunnel.Kick{Reason: reason})
	time.AfterFunc(time.Second, func() { s.closeWithReason(reason) })
}

// KickAll 通知全部客户端服务端即将关闭。
func (m *Manager) KickAll(retryAfter int) {
	for _, s := range m.Snapshot() {
		sess := m.Get(s.ClientID)
		if sess == nil {
			continue
		}
		_ = sess.ctrl.Send(tunnel.MsgServerShutdown, tunnel.ServerShutdown{RetryAfterSec: retryAfter})
	}
}

// UpgradeState 客户端最近一次在线升级的执行状态（内存态，服务端重启后清空）。
type UpgradeState struct {
	Target  string    `json:"target"`
	Status  string    `json:"status"` // pending / ok / failed
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// SendUpgrade 向在线客户端下发升级指令。
func (m *Manager) SendUpgrade(clientID string, up tunnel.Upgrade) error {
	s := m.Get(clientID)
	if s == nil {
		return fmt.Errorf("客户端不在线")
	}
	if err := s.ctrl.Send(tunnel.MsgUpgrade, up); err != nil {
		return err
	}
	m.setUpgrade(clientID, &UpgradeState{Target: up.Version, Status: "pending", At: time.Now()})
	return nil
}

// UpgradeStateFor 返回客户端最近一次升级状态，没有则返回 nil。
func (m *Manager) UpgradeStateFor(clientID string) *UpgradeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.upgrades[clientID]
	if st == nil {
		return nil
	}
	cp := *st
	return &cp
}

func (m *Manager) setUpgrade(clientID string, st *UpgradeState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 客户端回报的结果可能不带版本号，此时沿用原目标版本
	if st.Target == "" {
		if old := m.upgrades[clientID]; old != nil {
			st.Target = old.Target
		}
	}
	m.upgrades[clientID] = st
}

// markUpgraded 在客户端重连上报的版本达到升级目标时，把状态收敛为成功。
func (m *Manager) markUpgraded(clientID, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.upgrades[clientID]
	if st == nil || st.Target == "" || st.Status == "ok" {
		return
	}
	if release.Compare(version, st.Target) >= 0 {
		st.Status = "ok"
		st.Message = ""
		st.At = time.Now()
	}
}

// OpenStream 为一次外部访问打开数据流并写入流头。
func (m *Manager) OpenStream(clientID, proxyID, srcAddr string, timeout time.Duration) (net.Conn, error) {
	s := m.Get(clientID)
	if s == nil {
		return nil, fmt.Errorf("客户端不在线")
	}
	// 并发流上限：取 max_streams_per_client 与 max_conns_per_client 的较小值
	limit := m.opts.MaxStreams
	if m.opts.MaxConnsPerClient > 0 && (limit <= 0 || m.opts.MaxConnsPerClient < limit) {
		limit = m.opts.MaxConnsPerClient
	}
	if limit > 0 && s.streams.Add(1) > int64(limit) {
		s.streams.Add(-1)
		return nil, fmt.Errorf("客户端并发连接数已达上限 %d", limit)
	}
	stream, err := s.mux.OpenStream()
	if err != nil {
		s.streams.Add(-1)
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = stream.SetWriteDeadline(time.Now().Add(timeout))
	if err := tunnel.WriteStreamHeader(stream, proxyID, srcAddr); err != nil {
		_ = stream.Close()
		s.streams.Add(-1)
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	return &countedStream{Conn: stream, done: func() { s.streams.Add(-1) }}, nil
}

// countedStream 在流关闭时归还并发额度。
type countedStream struct {
	net.Conn
	once sync.Once
	done func()
}

func (c *countedStream) Close() error {
	c.once.Do(c.done)
	return c.Conn.Close()
}

// DetectOrphan 占位（保留扩展点）。

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
