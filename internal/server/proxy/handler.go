// Package proxy 实现服务端到客户端的数据面转发与访问审计。
package proxy

import (
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"ptunnel/internal/audit"
	"ptunnel/internal/audit/mac"
	"ptunnel/internal/server/clientmgr"
	"ptunnel/internal/server/portmgr"
	"ptunnel/internal/sqllog"
	"ptunnel/internal/sqlparse"
	"ptunnel/internal/store"
	"ptunnel/internal/tunnel"
)

// Stat 单代理运行统计。
type Stat struct {
	ProxyID     string `json:"proxy_id"`
	ActiveConns int64  `json:"active_conns"`
	TotalConns  int64  `json:"total_conns"`
	Rejected    int64  `json:"rejected"`
	BytesIn     int64  `json:"bytes_in"`
	BytesOut    int64  `json:"bytes_out"`
}

type counters struct {
	active   atomic.Int64
	total    atomic.Int64
	rejected atomic.Int64
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
}

// activeConn 一条在途的外部访问连接（用于运维检测接口）。
type activeConn struct {
	connID     string
	clientID   string
	clientName string
	proxyID    string
	proxyName  string
	protocol   string
	listenAddr string
	srcIP      string
	srcPort    int
	targetAddr string
	startedAt  time.Time
	in         *atomic.Int64
	out        *atomic.Int64
}

// ActiveConnInfo 在途连接的接口输出。
type ActiveConnInfo struct {
	ConnID     string    `json:"conn_id"`
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	ProxyID    string    `json:"proxy_id"`
	ProxyName  string    `json:"proxy_name"`
	Protocol   string    `json:"protocol"`
	ListenAddr string    `json:"listen_addr"`
	SrcIP      string    `json:"src_ip"`
	SrcPort    int       `json:"src_port"`
	TargetAddr string    `json:"target_addr"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
}

// Handler 处理外部访问连接。
type Handler struct {
	mgr             *clientmgr.Manager
	st              *store.Store
	aw              *audit.Writer
	sqlw            *sqllog.Writer
	log             *slog.Logger
	defaultMaxConns int
	sqlMaxLen       int
	macLookup       bool
	halfCloseGrace  time.Duration

	mu      sync.Mutex
	metrics map[string]*counters
	active  sync.Map // connID -> *activeConn
}

// New 创建转发处理器。
func New(mgr *clientmgr.Manager, st *store.Store, aw *audit.Writer, sqlw *sqllog.Writer,
	logger *slog.Logger, defaultMaxConns, sqlMaxLen int, macLookup bool, halfCloseGrace time.Duration) *Handler {
	if sqlMaxLen <= 0 {
		sqlMaxLen = 4096
	}
	if halfCloseGrace < 0 {
		halfCloseGrace = 0
	}
	return &Handler{
		mgr:             mgr,
		st:              st,
		aw:              aw,
		sqlw:            sqlw,
		log:             logger,
		defaultMaxConns: defaultMaxConns,
		sqlMaxLen:       sqlMaxLen,
		macLookup:       macLookup,
		halfCloseGrace:  halfCloseGrace,
		metrics:         map[string]*counters{},
	}
}

func (h *Handler) counter(proxyID string) *counters {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.metrics[proxyID]
	if !ok {
		c = &counters{}
		h.metrics[proxyID] = c
	}
	return c
}

// Snapshot 返回全部代理统计。
func (h *Handler) Snapshot() map[string]Stat {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]Stat, len(h.metrics))
	for id, c := range h.metrics {
		out[id] = Stat{
			ProxyID:     id,
			ActiveConns: c.active.Load(),
			TotalConns:  c.total.Load(),
			Rejected:    c.rejected.Load(),
			BytesIn:     c.bytesIn.Load(),
			BytesOut:    c.bytesOut.Load(),
		}
	}
	return out
}

// Prune 清理已不存在的代理的统计数据，避免 map 无界增长。
func (h *Handler) Prune(existing map[string]struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, c := range h.metrics {
		if _, ok := existing[id]; ok {
			continue
		}
		if c.active.Load() > 0 {
			continue // 仍有在途连接，等其结束后下次再清
		}
		delete(h.metrics, id)
	}
}

// ActiveConns 返回当前所有在途外部连接，按开始时间倒序。
func (h *Handler) ActiveConns() []ActiveConnInfo {
	out := make([]ActiveConnInfo, 0, 64)
	now := time.Now()
	h.active.Range(func(_, v any) bool {
		ac, ok := v.(*activeConn)
		if !ok || ac == nil {
			return true
		}
		out = append(out, ActiveConnInfo{
			ConnID:     ac.connID,
			ClientID:   ac.clientID,
			ClientName: ac.clientName,
			ProxyID:    ac.proxyID,
			ProxyName:  ac.proxyName,
			Protocol:   ac.protocol,
			ListenAddr: ac.listenAddr,
			SrcIP:      ac.srcIP,
			SrcPort:    ac.srcPort,
			TargetAddr: ac.targetAddr,
			StartedAt:  ac.startedAt,
			DurationMS: now.Sub(ac.startedAt).Milliseconds(),
			BytesIn:    ac.in.Load(),
			BytesOut:   ac.out.Load(),
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// PortHandler 返回给 PortRegistry 使用的处理器。
func (h *Handler) PortHandler(p portmgr.Proxy, c net.Conn) {
	start := time.Now()
	connID := uuid.NewString()
	srcIP, srcPort := splitAddr(c.RemoteAddr())

	clientName := p.ClientID
	if s := h.mgr.Get(p.ClientID); s != nil {
		clientName = s.ClientName
	} else if cl, err := h.st.GetClient(p.ClientID); err == nil && cl != nil {
		clientName = cl.Name
	}

	entry := &store.ConnLog{
		ConnID:     connID,
		ClientID:   p.ClientID,
		ClientName: clientName,
		ProxyID:    p.ID,
		ProxyName:  p.Name,
		Protocol:   p.Protocol,
		ListenAddr: net.JoinHostPort(p.BindAddr, strconv.Itoa(p.Port)),
		RemotePort: p.Port,
		TargetAddr: p.TargetAddr,
		SrcIP:      srcIP,
		SrcPort:    srcPort,
		StartedAt:  start,
	}

	mt := h.counter(p.ID)
	recorded := false
	finish := func(reason string, in, out int64) {
		if !recorded {
			return
		}
		end := time.Now()
		h.aw.EndConn(&store.ConnEnd{
			ConnID:      connID,
			EndedAt:     end,
			DurationMS:  end.Sub(start).Milliseconds(),
			BytesIn:     in,
			BytesOut:    out,
			CloseReason: reason,
		})
	}

	// 来源 IP 白名单
	if len(p.AllowNets) > 0 && !ipAllowed(p.AllowNets, srcIP) {
		mt.rejected.Add(1)
		recorded = h.aw.StartConn(entry)
		finish("rejected", 0, 0)
		h.log.Warn("来源 IP 不在白名单，拒绝访问", "proxy", p.Name, "src", srcIP)
		return
	}

	// 连接数限制：CAS 抢占额度，避免并发检查-自增之间的超额
	maxConns := p.MaxConns
	if maxConns <= 0 {
		maxConns = h.defaultMaxConns
	}
	if maxConns > 0 {
		admitted := false
		for {
			cur := mt.active.Load()
			if cur >= int64(maxConns) {
				break
			}
			if mt.active.CompareAndSwap(cur, cur+1) {
				admitted = true
				break
			}
		}
		if !admitted {
			mt.rejected.Add(1)
			recorded = h.aw.StartConn(entry)
			finish("rejected", 0, 0)
			return
		}
	} else {
		mt.active.Add(1)
	}
	mt.total.Add(1)
	defer mt.active.Add(-1)
	recorded = h.aw.StartConn(entry)

	// 单连接字节计数与在途登记：先登记，结束后汇总到代理累计值
	var connIn, connOut atomic.Int64
	h.active.Store(connID, &activeConn{
		connID:     connID,
		clientID:   p.ClientID,
		clientName: clientName,
		proxyID:    p.ID,
		proxyName:  p.Name,
		protocol:   p.Protocol,
		listenAddr: entry.ListenAddr,
		srcIP:      srcIP,
		srcPort:    srcPort,
		targetAddr: p.TargetAddr,
		startedAt:  start,
		in:         &connIn,
		out:        &connOut,
	})
	defer h.active.Delete(connID)

	stream, err := h.mgr.OpenStream(p.ClientID, p.ID, c.RemoteAddr().String(), 5*time.Second)
	if err != nil {
		h.log.Warn("打开隧道流失败", "proxy", p.Name, "client", clientName, "err", err)
		finish("client_offline", 0, 0)
		return
	}
	defer stream.Close()

	// 读取客户端拨号结果（成功后才开始复制，便于准确记录失败原因）
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	ok, msg, err := tunnel.ReadStreamStatus(stream)
	_ = stream.SetReadDeadline(time.Time{})
	if err != nil {
		finish("client_offline", 0, 0)
		return
	}
	if !ok {
		h.log.Warn("客户端拨号目标失败", "proxy", p.Name, "target", p.TargetAddr, "msg", msg)
		finish("target_unreachable", 0, 0)
		return
	}

	// 单连接字节计数：先本地累加，结束时汇总到代理累计值，避免把代理历史流量算进本条记录
	reason := h.pipe(c, stream, p, &connIn, &connOut, h.newSniffer(p, entry))
	mt.bytesIn.Add(connIn.Load())
	mt.bytesOut.Add(connOut.Load())
	finish(reason, connIn.Load(), connOut.Load())
}

// newSniffer 为绑定了应用类型的代理创建 SQL 提取器；未绑定或未启用 SQL 审计时返回 nil。
func (h *Handler) newSniffer(p portmgr.Proxy, entry *store.ConnLog) io.Writer {
	if h.sqlw == nil || p.AppType == "" {
		return nil
	}
	base := sqllog.Record{
		ConnID:     entry.ConnID,
		ClientID:   p.ClientID,
		ClientName: entry.ClientName,
		ProxyID:    p.ID,
		ProxyName:  p.Name,
		AppType:    p.AppType,
		SrcIP:      entry.SrcIP,
		SrcPort:    entry.SrcPort,
		TargetAddr: p.TargetAddr,
	}
	// 同步取一次 MAC（内部有 60s 缓存），保证 SQL 记录也能按 MAC 检索
	if h.macLookup && entry.SrcIP != "" {
		if hw, _ := mac.Lookup(entry.SrcIP); hw != "" {
			base.SrcMAC = hw
		}
	}
	return sqlparse.New(p.AppType, h.sqlMaxLen, func(ev sqlparse.Event) {
		r := base
		r.Time = time.Now()
		r.Kind = ev.Kind
		r.SQL = ev.SQL
		h.sqlw.Write(r)
	})
}

// pipe 双向复制数据，返回结束原因。
func (h *Handler) pipe(user net.Conn, stream net.Conn, p portmgr.Proxy, bytesIn, bytesOut *atomic.Int64, sniffer io.Writer) string {
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = user.Close()
			_ = stream.Close()
		})
	}

	lastActive := &atomic.Int64{}
	lastActive.Store(time.Now().UnixNano())
	var idleHit atomic.Bool
	idleTimeout := p.IdleTimeout
	watchdogDone := make(chan struct{})
	go func() {
		interval := 15 * time.Second
		if idleTimeout > 0 && idleTimeout/2 < interval {
			interval = idleTimeout / 2
		}
		if interval < time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				if idleTimeout > 0 && time.Since(time.Unix(0, lastActive.Load())) > idleTimeout {
					idleHit.Store(true)
					closeAll()
					return
				}
			}
		}
	}()

	// 上下行各自独立限速，避免双向共享同一份配额
	var upLim, downLim *limiter
	if p.RateLimitMB > 0 {
		upLim = newLimiter(p.RateLimitMB * 1024 * 1024)
		downLim = newLimiter(p.RateLimitMB * 1024 * 1024)
	}
	ch := make(chan string, 2)
	go func() {
		var src io.Reader = &trackReader{r: user, last: lastActive}
		if sniffer != nil {
			// 上行流量旁路给 SQL 解析器；解析失败不影响转发
			src = io.TeeReader(src, sniffer)
		}
		_, _ = io.Copy(&trackWriter{w: stream, n: bytesIn, last: lastActive, lim: upLim}, src)
		closeWrite(stream)
		ch <- "up"
	}()
	go func() {
		_, _ = io.Copy(&trackWriter{w: user, n: bytesOut, last: lastActive, lim: downLim},
			&trackReader{r: stream, last: lastActive})
		closeWrite(user)
		ch <- "down"
	}()

	first := <-ch
	if first == "down" {
		// 下行先结束说明响应已收完，直接释放，避免无谓占用隧道流配额
		closeAll()
		<-ch
	} else {
		// 上行先结束：给下行留出可配置的收尾窗口（yamux 流不支持半关闭时尤为重要）
		select {
		case <-ch:
		case <-time.After(h.halfCloseGrace):
			closeAll()
			<-ch
		}
	}
	close(watchdogDone)
	closeAll()
	if idleHit.Load() {
		return "timeout"
	}
	return "eof"
}

// closeWrite 尽力半关闭写方向；不支持时保持连接，交由空闲超时兜底。
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

type trackReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (t *trackReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type trackWriter struct {
	w    io.Writer
	n    *atomic.Int64
	last *atomic.Int64
	lim  *limiter
}

func (t *trackWriter) Write(p []byte) (int, error) {
	if t.lim != nil {
		t.lim.wait(len(p))
	}
	n, err := t.w.Write(p)
	if n > 0 {
		t.n.Add(int64(n))
		t.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// limiter 是一个简单的令牌桶，用于代理级带宽限制（字节/秒）。
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newLimiter(bytesPerSec int) *limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	burst := float64(bytesPerSec) / 4
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	return &limiter{rate: float64(bytesPerSec), burst: burst, tokens: burst, last: time.Now()}
}

func (l *limiter) wait(n int) {
	if n > int(l.burst) {
		n = int(l.burst)
	}
	l.mu.Lock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	l.tokens -= float64(n)
	deficit := -l.tokens
	l.mu.Unlock()
	if deficit > 0 {
		time.Sleep(time.Duration(deficit / l.rate * float64(time.Second)))
	}
}

func ipAllowed(nets []*net.IPNet, ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func splitAddr(addr net.Addr) (string, int) {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String(), 0
	}
	p, _ := strconv.Atoi(port)
	return host, p
}
