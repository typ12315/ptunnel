// Package api 提供服务端管理接口与 Web 界面。
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ptunnel/internal/audit"
	"ptunnel/internal/auth"
	"ptunnel/internal/config"
	"ptunnel/internal/ops"
	"ptunnel/internal/pki"
	"ptunnel/internal/release"
	"ptunnel/internal/server/clientmgr"
	"ptunnel/internal/server/portmgr"
	"ptunnel/internal/server/proxy"
	"ptunnel/internal/server/web"
	"ptunnel/internal/sqllog"
	"ptunnel/internal/store"
)

const (
	sessionCookie = "pt_session"
	csrfCookie    = "pt_csrf"
)

// Server 管理面服务。
type Server struct {
	cfg   *config.ServerConfig
	st    *store.Store
	mgr   *clientmgr.Manager
	ports *portmgr.Registry
	ph    *proxy.Handler
	aw    *audit.Writer
	sqlw  *sqllog.Writer
	sess  *auth.SessionManager
	ca    *pki.CA
	rel   *release.Manager
	log   *slog.Logger
	proc  *ops.Collector

	router    *gin.Engine
	startedAt time.Time

	loginMu    sync.Mutex
	loginFails map[string]*loginState
	version    string
}

type loginState struct {
	fails int
	until time.Time
}

// New 创建管理面服务。
func New(cfg *config.ServerConfig, st *store.Store, mgr *clientmgr.Manager, ports *portmgr.Registry,
	ph *proxy.Handler, aw *audit.Writer, sqlw *sqllog.Writer, sess *auth.SessionManager, ca *pki.CA,
	rel *release.Manager, logger *slog.Logger, version string) *Server {
	gin.SetMode(gin.ReleaseMode)
	procs := ops.NewCollector()
	procs.Sample() // 预热采样，首次接口调用即可得到 CPU 百分比
	s := &Server{
		cfg:        cfg,
		st:         st,
		mgr:        mgr,
		ports:      ports,
		ph:         ph,
		aw:         aw,
		sqlw:       sqlw,
		sess:       sess,
		ca:         ca,
		rel:        rel,
		log:        logger,
		proc:       procs,
		startedAt:  time.Now(),
		loginFails: map[string]*loginState{},
		version:    version,
	}
	s.router = s.buildRouter()
	return s
}

// Handler 返回 HTTP 处理器。
func (s *Server) Handler() http.Handler { return s.router }

// ClientHandler 仅暴露客户端注册与证书续期接口。
// 隧道端口需要对公网开放，因此不能在这里挂载管理面接口。
func (s *Server) ClientHandler() http.Handler {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", s.handleHealth)
	api := r.Group("/api")
	api.POST("/enroll", s.handleEnroll)
	api.POST("/client/cert/renew", s.handleRenewCert)
	api.GET("/client/upgrade/artifact", s.handleDownloadArtifact)
	return r
}

// SyncPorts 使端口监听与数据库配置保持一致。
func (s *Server) SyncPorts() {
	list, err := s.st.ListRunnable()
	if err != nil {
		s.log.Error("读取代理配置失败", "err", err)
		return
	}
	specs := make([]portmgr.Proxy, 0, len(list))
	for _, p := range list {
		nets, _ := config.ParseCIDRs(p.AllowCIDRs) // 创建/更新时已校验，这里忽略错误
		specs = append(specs, portmgr.Proxy{
			ID:          p.ID,
			ClientID:    p.ClientID,
			Name:        p.Name,
			Protocol:    p.Protocol,
			BindAddr:    p.BindAddr,
			Port:        p.RemotePort,
			TargetAddr:  p.TargetAddr,
			AppType:     p.AppType,
			AllowCIDRs:  p.AllowCIDRs,
			AllowNets:   nets,
			MaxConns:    p.MaxConns,
			RateLimitMB: p.RateLimitMB,
			IdleTimeout: s.cfg.Tunnel.IdleTimeout.D(),
		})
	}
	for _, st := range s.ports.Sync(specs) {
		s.st.SetProxyState(st.ProxyID, st.State, st.Msg)
		if st.State == "error" {
			s.log.Warn("代理端口监听失败", "proxy", st.ProxyID, "msg", st.Msg)
		}
	}
	// 清理已删除代理的运行时统计，防止 metrics 无界增长
	if all, err := s.st.ListProxies(""); err == nil {
		keep := make(map[string]struct{}, len(all))
		for _, p := range all {
			keep[p.ID] = struct{}{}
		}
		s.ph.Prune(keep)
	}
}

func (s *Server) buildRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		c.Next()
	})

	api := r.Group("/api")
	api.POST("/auth/login", s.handleLogin)
	api.POST("/enroll", s.handleEnroll)
	api.POST("/client/cert/renew", s.handleRenewCert)

	authed := api.Group("")
	authed.Use(s.requireAuth())
	{
		authed.POST("/auth/logout", s.handleLogout)
		authed.GET("/auth/me", s.handleMe)
		authed.POST("/auth/password", s.handleChangePassword)

		authed.GET("/clients", s.handleListClients)
		authed.POST("/clients", s.handleCreateClient)
		authed.GET("/clients/:id", s.handleGetClient)
		authed.PATCH("/clients/:id", s.handleUpdateClient)
		authed.DELETE("/clients/:id", s.handleDeleteClient)
		authed.POST("/clients/:id/enroll-token", s.handleNewToken)
		authed.POST("/clients/:id/revoke-cert", s.handleRevokeCert)
		authed.GET("/clients/:id/pending", s.handlePendingProxies)
		authed.POST("/clients/:id/upgrade", s.handleUpgradeClient)

		authed.GET("/releases", s.handleListReleases)
		authed.POST("/releases", s.handleUploadRelease)
		authed.DELETE("/releases/:id", s.handleDeleteRelease)

		authed.GET("/proxies", s.handleListProxies)
		authed.POST("/proxies", s.handleCreateProxy)
		authed.PATCH("/proxies/:id", s.handleUpdateProxy)
		authed.DELETE("/proxies/:id", s.handleDeleteProxy)
		authed.POST("/proxies/:id/enable", s.handleEnableProxy(true))
		authed.POST("/proxies/:id/disable", s.handleEnableProxy(false))
		authed.POST("/proxies/:id/approve", s.handleApproveProxy("approved"))
		authed.POST("/proxies/:id/reject", s.handleApproveProxy("rejected"))

		authed.GET("/connections", s.handleListConnections)
		authed.GET("/active-connections", s.handleActiveConnections)
		authed.GET("/export/connections.csv", s.handleExportConnections)
		authed.GET("/connections/:id", s.handleGetConnLog)
		authed.DELETE("/connections", s.handleDeleteConnections)

		authed.GET("/audit", s.handleListAudit)
		authed.GET("/sql-logs", s.handleListSQLLogs)
		authed.GET("/sql-logs/dates", s.handleSQLLogDates)
		authed.GET("/stats/overview", s.handleOverview)
		authed.GET("/stats/series", s.handleSeries)
		authed.GET("/system/info", s.handleSystemInfo)
		authed.GET("/system/runtime", s.handleRuntime)
		authed.GET("/settings", s.handleGetSettings)
		authed.PUT("/settings", s.handlePutSettings)
	}

	r.GET("/healthz", s.handleHealth)
	r.GET("/metrics", s.handleMetrics)
	s.mountStatic(r)
	return r
}

// mountStatic 挂载嵌入的前端资源，未匹配路径回退 index.html（支持前端 history 路由）。
func (s *Server) mountStatic(r *gin.Engine) {
	dist := web.Dist()
	fileServer := http.FileServer(http.FS(dist))
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			fail(c, http.StatusNotFound, "not_found", "接口不存在")
			return
		}
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := dist.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

// requireAuth 校验会话 Cookie；写操作额外校验 CSRF。
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookie)
		if err != nil || token == "" {
			fail(c, http.StatusUnauthorized, "unauthorized", "未登录")
			c.Abort()
			return
		}
		session, ok := s.sess.Verify(token)
		if !ok {
			fail(c, http.StatusUnauthorized, "unauthorized", "会话已失效")
			c.Abort()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			csrf, _ := c.Cookie(csrfCookie)
			if csrf == "" || c.GetHeader("X-CSRF-Token") != csrf {
				fail(c, http.StatusForbidden, "csrf_failed", "CSRF 校验失败")
				c.Abort()
				return
			}
		}
		c.Set("session", session)
		c.Next()
	}
}

func currentSession(c *gin.Context) *auth.Session {
	v, _ := c.Get("session")
	if s, ok := v.(*auth.Session); ok {
		return s
	}
	return nil
}

// audit 记录配置操作。
func (s *Server) audit(c *gin.Context, action, entity, entityID string, detail string) {
	sess := currentSession(c)
	a := &store.AuditLog{Action: action, Entity: entity, EntityID: entityID, Detail: detail, IP: c.ClientIP()}
	if sess != nil {
		a.UserID = sess.UserID
		a.Actor = sess.Username
	}
	if err := s.st.InsertAudit(a); err != nil {
		s.log.Warn("写入操作审计失败", "err", err)
	}
}

func fail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"code": code, "message": msg})
}

func (s *Server) handleHealth(c *gin.Context) {
	dbOK := s.st.DB().Ping() == nil
	status := "ok"
	if !dbOK {
		status = "degraded"
	}
	c.JSON(http.StatusOK, gin.H{
		"status":      status,
		"db":          dbOK,
		"clients":     s.mgr.Count(),
		"listeners":   s.ports.Count(),
		"uptime_sec":  int(time.Since(s.startedAt).Seconds()),
		"version":     s.version,
		"audit_queue": s.aw.QueueLen(),
	})
}

// handleMetrics 输出 Prometheus 文本格式指标。
func (s *Server) handleMetrics(c *gin.Context) {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP ptunnel_client_online 在线客户端数\n# TYPE ptunnel_client_online gauge\nptunnel_client_online %d\n", s.mgr.Count())
	fmt.Fprintf(&b, "# HELP ptunnel_proxy_listeners 监听中的代理端口数\n# TYPE ptunnel_proxy_listeners gauge\nptunnel_proxy_listeners %d\n", s.ports.Count())
	fmt.Fprintf(&b, "# HELP ptunnel_connlog_dropped_total 审计队列丢弃条数\n# TYPE ptunnel_connlog_dropped_total counter\nptunnel_connlog_dropped_total %d\n", s.aw.Dropped())
	fmt.Fprintf(&b, "# HELP ptunnel_connlog_queue 审计队列积压\n# TYPE ptunnel_connlog_queue gauge\nptunnel_connlog_queue %d\n", s.aw.QueueLen())
	stats := s.ph.Snapshot()
	b.WriteString("# HELP ptunnel_active_conns 当前活跃连接数\n# TYPE ptunnel_active_conns gauge\n")
	for id, st := range stats {
		fmt.Fprintf(&b, "ptunnel_active_conns{proxy=%q} %d\n", id, st.ActiveConns)
	}
	b.WriteString("# HELP ptunnel_bytes_total 累计转发字节\n# TYPE ptunnel_bytes_total counter\n")
	for id, st := range stats {
		fmt.Fprintf(&b, "ptunnel_bytes_total{proxy=%q,direction=\"in\"} %d\n", id, st.BytesIn)
		fmt.Fprintf(&b, "ptunnel_bytes_total{proxy=%q,direction=\"out\"} %d\n", id, st.BytesOut)
	}
	b.WriteString("# HELP ptunnel_rejected_total 被拒绝连接数\n# TYPE ptunnel_rejected_total counter\n")
	for id, st := range stats {
		fmt.Fprintf(&b, "ptunnel_rejected_total{proxy=%q} %d\n", id, st.Rejected)
	}
	c.Data(http.StatusOK, "text/plain; version=0.0.4", []byte(b.String()))
}

func (s *Server) handleSystemInfo(c *gin.Context) {
	host, _ := os.Hostname()
	sqlEnabled := s.sqlw != nil
	var sqlQueue, sqlDropped int64
	if sqlEnabled {
		sqlQueue = int64(s.sqlw.QueueLen())
		sqlDropped = s.sqlw.Dropped()
	}
	c.JSON(http.StatusOK, gin.H{
		"version":        s.version,
		"hostname":       host,
		"started_at":     s.startedAt,
		"tunnel_addr":    s.cfg.Server.TunnelAddr,
		"admin_addr":     s.cfg.Server.AdminAddr,
		"port_range":     s.cfg.Server.PortRange,
		"auto_approve":   s.cfg.Tunnel.AutoApproveLocal,
		"audit_mode":     s.cfg.Audit.ConnLogMode,
		"retention_days": s.cfg.Audit.ConnLogRetention,
		"idle_timeout":   s.cfg.Tunnel.IdleTimeout.D().String(),
		"sql_log": gin.H{
			"enabled":        sqlEnabled,
			"dir":            s.cfg.Audit.SQLLogDir,
			"retention_days": s.cfg.Audit.SQLLogRetentionDays,
			"queue":          sqlQueue,
			"dropped":        sqlDropped,
		},
	})
}

// handleRuntime 输出进程资源、Go 运行时、隧道会话、监听器与在途连接，供运维检测与仪表盘展示。
func (s *Server) handleRuntime(c *gin.Context) {
	proc := s.proc.Sample()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	stats := s.ph.Snapshot()
	var active, total, rejected, bytesIn, bytesOut int64
	for _, st := range stats {
		active += st.ActiveConns
		total += st.TotalConns
		rejected += st.Rejected
		bytesIn += st.BytesIn
		bytesOut += st.BytesOut
	}

	c.JSON(http.StatusOK, gin.H{
		"time":           time.Now(),
		"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
		"process": gin.H{
			"cpu_percent": float64(int(proc.CPUPercent*10)) / 10,
			"cpu_seconds": float64(int(proc.CPUSeconds*100)) / 100,
			"rss_bytes":   proc.RSSBytes,
			"handles":     proc.Handles,
			"threads":     proc.Threads,
		},
		"go": gin.H{
			"version":          runtime.Version(),
			"goroutines":       runtime.NumGoroutine(),
			"heap_alloc_bytes": ms.HeapAlloc,
			"heap_inuse_bytes": ms.HeapInuse,
			"heap_sys_bytes":   ms.HeapSys,
			"sys_bytes":        ms.Sys,
			"num_gc":           ms.NumGC,
			"gc_pause_ms":      float64(int(float64(ms.PauseTotalNs)/1e6*10)) / 10,
		},
		"tunnel": gin.H{
			"clients_online": s.mgr.Count(),
			"listeners":      s.ports.Count(),
			"active_conns":   active,
			"total_conns":    total,
			"rejected_conns": rejected,
			"bytes_in":       bytesIn,
			"bytes_out":      bytesOut,
			"audit_queue":    s.aw.QueueLen(),
			"audit_dropped":  s.aw.Dropped(),
		},
		"sessions":    s.mgr.Details(),
		"listeners":   s.ports.Details(),
		"connections": s.ph.ActiveConns(),
	})
}

func (s *Server) handleGetSettings(c *gin.Context) {
	out := s.st.AllSettings()
	delete(out, "session_key")
	c.JSON(http.StatusOK, gin.H{"settings": out, "runtime": gin.H{
		"conn_log_mode":      s.cfg.Audit.ConnLogMode,
		"conn_log_retention": s.cfg.Audit.ConnLogRetention,
		"auto_approve_local": s.cfg.Tunnel.AutoApproveLocal,
		"port_range":         s.cfg.Server.PortRange,
		"default_max_conns":  s.cfg.Tunnel.MaxConnsPerProxy,
		"mac_lookup":         s.cfg.Audit.MACLookup,
	}})
}

func (s *Server) handlePutSettings(c *gin.Context) {
	var body map[string]string
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求体格式错误")
		return
	}
	allowed := map[string]bool{"conn_log_retention_days": true, "conn_log_mode": true}
	for k, v := range body {
		if !allowed[k] {
			continue
		}
		switch k {
		case "conn_log_mode":
			if v != "full" && v != "sample" && v != "off" {
				fail(c, http.StatusBadRequest, "invalid_value", "conn_log_mode 仅支持 full/sample/off")
				return
			}
		case "conn_log_retention_days":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fail(c, http.StatusBadRequest, "invalid_value", "保留天数必须是非负整数")
				return
			}
		}
		if err := s.st.SetSetting(k, v); err != nil {
			fail(c, http.StatusInternalServerError, "save_failed", "保存设置失败")
			return
		}
	}
	s.audit(c, "settings.update", "settings", "", "更新系统设置（部分重启后生效）")
	c.JSON(http.StatusOK, gin.H{"ok": true, "note": "部分设置重启后生效"})
}
