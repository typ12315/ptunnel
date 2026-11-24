// Package portmgr 负责代理端口的动态监听与热更新。
package portmgr

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy 监听所需的代理信息。
type Proxy struct {
	ID          string
	ClientID    string
	Name        string
	Protocol    string
	BindAddr    string
	Port        int
	TargetAddr  string
	AppType     string // 绑定的应用类型（mysql/postgresql/...），空表示普通 TCP 转发
	AllowCIDRs  []string
	AllowNets   []*net.IPNet // AllowCIDRs 的预解析结果，避免每次连接重复解析
	MaxConns    int
	RateLimitMB int
	IdleTimeout time.Duration
}

// State 监听状态变更。
type State struct {
	ProxyID string
	State   string // listening / error / idle
	Msg     string
}

// Handler 处理一条到来的外部连接（实现方负责关闭连接）。
type Handler func(p Proxy, c net.Conn)

type entry struct {
	proxy atomic.Pointer[Proxy] // 配置可被 Sync 热更新，acceptLoop 需无锁读取
	ln    net.Listener
	conns int64
}

func (e *entry) current() Proxy {
	if p := e.proxy.Load(); p != nil {
		return *p
	}
	return Proxy{}
}

// Registry 管理所有代理端口监听器。
type Registry struct {
	mu      sync.Mutex
	entries map[string]*entry
	handler Handler
	onState func(State)
}

// New 创建端口注册表。
func New(handler Handler, onState func(State)) *Registry {
	return &Registry{
		entries: map[string]*entry{},
		handler: handler,
		onState: onState,
	}
}

// Sync 将监听器对齐到期望配置，返回各代理的最终状态。
func (r *Registry) Sync(list []Proxy) []State {
	r.mu.Lock()
	defer r.mu.Unlock()

	desired := make(map[string]Proxy, len(list))
	for _, p := range list {
		desired[p.ID] = p
	}

	var states []State

	// 关闭不再需要或配置已变化的监听器
	for id, e := range r.entries {
		want, ok := desired[id]
		cur := e.current()
		if !ok || want.BindAddr != cur.BindAddr || want.Port != cur.Port || want.Protocol != cur.Protocol {
			_ = e.ln.Close()
			delete(r.entries, id)
			states = append(states, State{ProxyID: id, State: "idle"})
		}
	}

	// 启动新增监听器
	for id, p := range desired {
		if e, ok := r.entries[id]; ok {
			// 仅更新元数据
			pc := p
			e.proxy.Store(&pc)
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(p.BindAddr, fmt.Sprintf("%d", p.Port)))
		if err != nil {
			states = append(states, State{ProxyID: id, State: "error", Msg: fmt.Sprintf("端口监听失败: %v", err)})
			continue
		}
		e := &entry{ln: ln}
		pc := p
		e.proxy.Store(&pc)
		r.entries[id] = e
		states = append(states, State{ProxyID: id, State: "listening", Msg: ""})
		go r.acceptLoop(e)
	}
	return states
}

// Remove 关闭并移除单个监听器。
func (r *Registry) Remove(proxyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[proxyID]; ok {
		_ = e.ln.Close()
		delete(r.entries, proxyID)
	}
}

// CloseAll 关闭全部监听器。
func (r *Registry) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.entries {
		_ = e.ln.Close()
		delete(r.entries, id)
	}
}

// Count 返回当前监听器数量。
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// ActiveConns 返回某代理当前活跃连接数。
func (r *Registry) ActiveConns(proxyID string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[proxyID]; ok {
		return atomic.LoadInt64(&e.conns)
	}
	return 0
}

// ActiveSnapshot 返回全部代理的活跃连接数。
func (r *Registry) ActiveSnapshot() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int64, len(r.entries))
	for id, e := range r.entries {
		out[id] = atomic.LoadInt64(&e.conns)
	}
	return out
}

// ListenerInfo 监听器运行状态（供运维检测）。
type ListenerInfo struct {
	ProxyID     string `json:"proxy_id"`
	ClientID    string `json:"client_id"`
	ProxyName   string `json:"proxy_name"`
	Protocol    string `json:"protocol"`
	BindAddr    string `json:"bind_addr"`
	Port        int    `json:"port"`
	TargetAddr  string `json:"target_addr"`
	ActiveConns int64  `json:"active_conns"`
}

// Details 返回当前所有监听器的详细信息，按端口升序。
func (r *Registry) Details() []ListenerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ListenerInfo, 0, len(r.entries))
	for id, e := range r.entries {
		p := e.current()
		out = append(out, ListenerInfo{
			ProxyID:     id,
			ClientID:    p.ClientID,
			ProxyName:   p.Name,
			Protocol:    p.Protocol,
			BindAddr:    p.BindAddr,
			Port:        p.Port,
			TargetAddr:  p.TargetAddr,
			ActiveConns: atomic.LoadInt64(&e.conns),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func (r *Registry) acceptLoop(e *entry) {
	var backoff time.Duration
	for {
		c, err := e.ln.Accept()
		if err != nil {
			// 临时性错误（如文件描述符耗尽）退避重试，避免监听器被静默放弃
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if backoff == 0 {
					backoff = 5 * time.Millisecond
				} else {
					backoff *= 2
				}
				if backoff > time.Second {
					backoff = time.Second
				}
				time.Sleep(backoff)
				continue
			}
			return
		}
		backoff = 0
		p := e.current()
		atomic.AddInt64(&e.conns, 1)
		go func() {
			defer atomic.AddInt64(&e.conns, -1)
			defer c.Close()
			r.handler(p, c)
		}()
	}
}
