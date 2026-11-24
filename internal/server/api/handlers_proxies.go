package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"ptunnel/internal/config"
	"ptunnel/internal/server/proxy"
	"ptunnel/internal/sqlparse"
	"ptunnel/internal/store"
)

type proxyDTO struct {
	*store.Proxy
	ClientName string     `json:"client_name"`
	Online     bool       `json:"online"`
	Stat       proxy.Stat `json:"stat"`
}

func (s *Server) toProxyDTO(p *store.Proxy, stats map[string]proxy.Stat) proxyDTO {
	dto := proxyDTO{Proxy: p, Stat: stats[p.ID]}
	if cl, err := s.st.GetClient(p.ClientID); err == nil && cl != nil {
		dto.ClientName = cl.Name
		dto.Online = s.mgr.IsOnline(p.ClientID)
	}
	return dto
}

func (s *Server) handleListProxies(c *gin.Context) {
	list, err := s.st.ListProxies(c.Query("client_id"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询代理失败")
		return
	}
	stats := s.ph.Snapshot()
	out := make([]proxyDTO, 0, len(list))
	for _, p := range list {
		out = append(out, s.toProxyDTO(p, stats))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

type proxyReq struct {
	ClientID    string   `json:"client_id"`
	Name        string   `json:"name"`
	Protocol    string   `json:"protocol"`
	BindAddr    string   `json:"bind_addr"`
	RemotePort  int      `json:"remote_port"`
	TargetAddr  string   `json:"target_addr"`
	Enabled     *bool    `json:"enabled"`
	AppType     *string  `json:"app_type"`
	AllowCIDRs  []string `json:"allow_cidrs"`
	MaxConns    *int     `json:"max_conns"`
	RateLimitMB *int     `json:"rate_limit_mb"`
}

func validateProxyInput(req *proxyReq, cfg *config.ServerConfig) string {
	if req.Protocol == "" {
		req.Protocol = "tcp"
	}
	if req.Protocol != "tcp" {
		return "目前仅支持 tcp 协议"
	}
	if req.BindAddr == "" {
		req.BindAddr = cfg.Server.PublicBind
	}
	if ip := net.ParseIP(req.BindAddr); ip == nil {
		return "监听地址必须是合法的 IP"
	}
	lo, hi := cfg.Server.PortRange[0], cfg.Server.PortRange[1]
	if req.RemotePort < lo || req.RemotePort > hi {
		return fmt.Sprintf("代理端口必须在 %d-%d 之间", lo, hi)
	}
	if err := validateTarget(req.TargetAddr); err != nil {
		return err.Error()
	}
	if _, err := config.ParseCIDRs(req.AllowCIDRs); err != nil {
		return err.Error()
	}
	if req.MaxConns != nil && *req.MaxConns < 0 {
		return "最大连接数不能为负数"
	}
	if req.RateLimitMB != nil && *req.RateLimitMB < 0 {
		return "限速值不能为负数"
	}
	if req.AppType != nil && strings.TrimSpace(*req.AppType) != "" {
		if sqlparse.NormalizeApp(*req.AppType) == "" {
			return "不支持的应用类型，可选: mysql/postgresql/sqlserver/oracle/dm/redis/mongodb"
		}
	}
	if req.Name == "" {
		req.Name = "proxy-" + strconv.Itoa(req.RemotePort)
	}
	return ""
}

func validateTarget(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("转发地址格式应为 host:port")
	}
	if host == "" {
		return fmt.Errorf("转发地址缺少主机")
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return fmt.Errorf("转发端口非法")
	}
	return nil
}

func (s *Server) handleCreateProxy(c *gin.Context) {
	var req proxyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求体格式错误")
		return
	}
	if msg := validateProxyInput(&req, s.cfg); msg != "" {
		fail(c, http.StatusBadRequest, "invalid_proxy", msg)
		return
	}
	cl, err := s.st.GetClient(req.ClientID)
	if err != nil || cl == nil {
		fail(c, http.StatusBadRequest, "invalid_client", "客户端不存在")
		return
	}
	if name, err := s.st.ProxyConflict(req.BindAddr, req.Protocol, req.RemotePort, ""); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "检查端口冲突失败")
		return
	} else if name != "" {
		fail(c, http.StatusConflict, "proxy_port_conflict", fmt.Sprintf("端口 %d 已被代理 %q 占用", req.RemotePort, name))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	maxConns, rateLimit := 0, 0
	if req.MaxConns != nil {
		maxConns = *req.MaxConns
	}
	if req.RateLimitMB != nil {
		rateLimit = *req.RateLimitMB
	}
	appType := ""
	if req.AppType != nil {
		appType = sqlparse.NormalizeApp(*req.AppType)
	}
	p := &store.Proxy{
		ID:          uuid.NewString(),
		ClientID:    cl.ID,
		Name:        req.Name,
		Protocol:    req.Protocol,
		BindAddr:    req.BindAddr,
		RemotePort:  req.RemotePort,
		TargetAddr:  req.TargetAddr,
		AppType:     appType,
		Enabled:     enabled,
		AllowCIDRs:  req.AllowCIDRs,
		MaxConns:    maxConns,
		RateLimitMB: rateLimit,
		Origin:      "server",
		Approval:    "approved",
		State:       "idle",
	}
	if err := s.st.CreateProxy(p); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "创建代理失败: "+err.Error())
		return
	}
	s.SyncPorts()
	_ = s.mgr.Push(cl.ID)
	s.audit(c, "proxy.create", "proxy", p.ID, fmt.Sprintf("新建代理 %s: %d -> %s", p.Name, p.RemotePort, p.TargetAddr))
	c.JSON(http.StatusOK, gin.H{"proxy": s.toProxyDTO(p, s.ph.Snapshot())})
}

func (s *Server) handleUpdateProxy(c *gin.Context) {
	p, err := s.st.GetProxy(c.Param("id"))
	if err != nil || p == nil {
		fail(c, http.StatusNotFound, "not_found", "代理不存在")
		return
	}
	var req proxyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求体格式错误")
		return
	}
	// 未传的字段保持原值
	if req.Name == "" {
		req.Name = p.Name
	}
	if req.Protocol == "" {
		req.Protocol = p.Protocol
	}
	if req.BindAddr == "" {
		req.BindAddr = p.BindAddr
	}
	if req.RemotePort == 0 {
		req.RemotePort = p.RemotePort
	}
	if req.TargetAddr == "" {
		req.TargetAddr = p.TargetAddr
	}
	if req.AllowCIDRs == nil {
		req.AllowCIDRs = p.AllowCIDRs
	}
	if msg := validateProxyInput(&req, s.cfg); msg != "" {
		fail(c, http.StatusBadRequest, "invalid_proxy", msg)
		return
	}
	if name, err := s.st.ProxyConflict(req.BindAddr, req.Protocol, req.RemotePort, p.ID); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "检查端口冲突失败")
		return
	} else if name != "" {
		fail(c, http.StatusConflict, "proxy_port_conflict", fmt.Sprintf("端口 %d 已被代理 %q 占用", req.RemotePort, name))
		return
	}
	p.Name = req.Name
	p.Protocol = req.Protocol
	p.BindAddr = req.BindAddr
	p.RemotePort = req.RemotePort
	p.TargetAddr = req.TargetAddr
	p.AllowCIDRs = req.AllowCIDRs
	// 未传上限/限速时保持原值，避免部分更新（如前端只改名称）把限制清零
	if req.MaxConns != nil {
		p.MaxConns = *req.MaxConns
	}
	if req.RateLimitMB != nil {
		p.RateLimitMB = *req.RateLimitMB
	}
	if req.AppType != nil {
		p.AppType = sqlparse.NormalizeApp(*req.AppType)
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := s.st.UpdateProxy(p); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "更新代理失败")
		return
	}
	s.SyncPorts()
	_ = s.mgr.Push(p.ClientID)
	s.audit(c, "proxy.update", "proxy", p.ID, fmt.Sprintf("更新代理 %s: %d -> %s", p.Name, p.RemotePort, p.TargetAddr))
	c.JSON(http.StatusOK, gin.H{"proxy": s.toProxyDTO(p, s.ph.Snapshot())})
}

func (s *Server) handleDeleteProxy(c *gin.Context) {
	p, err := s.st.GetProxy(c.Param("id"))
	if err != nil || p == nil {
		fail(c, http.StatusNotFound, "not_found", "代理不存在")
		return
	}
	s.ports.Remove(p.ID)
	if err := s.st.DeleteProxy(p.ID); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "删除代理失败")
		return
	}
	_ = s.mgr.Push(p.ClientID)
	s.audit(c, "proxy.delete", "proxy", p.ID, "删除代理: "+p.Name)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleEnableProxy(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := s.st.GetProxy(c.Param("id"))
		if err != nil || p == nil {
			fail(c, http.StatusNotFound, "not_found", "代理不存在")
			return
		}
		if p.Approval != "approved" {
			fail(c, http.StatusBadRequest, "not_approved", "代理尚未审批通过")
			return
		}
		if err := s.st.SetProxyEnabled(p.ID, enabled); err != nil {
			fail(c, http.StatusInternalServerError, "db_error", "更新状态失败")
			return
		}
		s.SyncPorts()
		_ = s.mgr.Push(p.ClientID)
		action := "proxy.disable"
		if enabled {
			action = "proxy.enable"
		}
		s.audit(c, action, "proxy", p.ID, p.Name)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func (s *Server) handleApproveProxy(approval string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := s.st.GetProxy(c.Param("id"))
		if err != nil || p == nil {
			fail(c, http.StatusNotFound, "not_found", "代理不存在")
			return
		}
		if err := s.st.SetProxyApproval(p.ID, approval); err != nil {
			fail(c, http.StatusInternalServerError, "db_error", "更新审批状态失败")
			return
		}
		s.SyncPorts()
		_ = s.mgr.Push(p.ClientID)
		s.audit(c, "proxy.approve", "proxy", p.ID, "审批结果: "+approval+" / "+p.Name)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
