package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"ptunnel/internal/auth"
	"ptunnel/internal/release"
	"ptunnel/internal/server/clientmgr"
	"ptunnel/internal/store"
)

type clientDTO struct {
	*store.Client
	Online        bool                    `json:"online"`
	ProxyCount    int                     `json:"proxy_count"`
	PendingCount  int                     `json:"pending_count"`
	LatestVersion string                  `json:"latest_version"`
	Upgradable    bool                    `json:"upgradable"`
	Upgrade       *clientmgr.UpgradeState `json:"upgrade,omitempty"`
}

func (s *Server) toClientDTO(c *store.Client) clientDTO {
	idx, _ := s.rel.Index()
	return s.toClientDTOWith(c, idx)
}

// toClientDTOWith 使用预取的版本索引构造 DTO，避免列表接口逐行查询升级包。
func (s *Server) toClientDTOWith(c *store.Client, idx map[string]string) clientDTO {
	proxies, _ := s.st.ListProxies(c.ID)
	pending := 0
	for _, p := range proxies {
		if p.Approval == "pending" {
			pending++
		}
	}
	latest := idx[c.OS+"/"+c.Arch]
	return clientDTO{
		Client:        c,
		Online:        s.mgr.IsOnline(c.ID),
		ProxyCount:    len(proxies),
		PendingCount:  pending,
		LatestVersion: latest,
		Upgradable:    release.IsUpgradeable(c.Version, latest),
		Upgrade:       s.mgr.UpgradeStateFor(c.ID),
	}
}

func (s *Server) handleListClients(c *gin.Context) {
	list, err := s.st.ListClients()
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询客户端失败")
		return
	}
	out := make([]clientDTO, 0, len(list))
	idx, _ := s.rel.Index()
	for _, cl := range list {
		out = append(out, s.toClientDTOWith(cl, idx))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

type createClientReq struct {
	Name   string `json:"name" binding:"required"`
	Remark string `json:"remark"`
}

func (s *Server) handleCreateClient(c *gin.Context) {
	var req createClientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请填写客户端名称")
		return
	}
	if existing, _ := s.st.GetClientByName(req.Name); existing != nil {
		fail(c, http.StatusConflict, "name_exists", "客户端名称已存在")
		return
	}
	token := auth.RandomToken()
	cl := &store.Client{
		ID:        uuid.NewString(),
		Name:      req.Name,
		Remark:    req.Remark,
		Enabled:   true,
		Status:    "offline",
		TokenHash: auth.TokenHash(token),
	}
	expire := time.Now().Add(15 * time.Minute)
	cl.TokenExpire = &expire
	if err := s.st.CreateClient(cl); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "创建客户端失败")
		return
	}
	s.audit(c, "client.create", "client", cl.ID, "新建客户端: "+cl.Name)
	c.JSON(http.StatusOK, gin.H{
		"client":       s.toClientDTO(cl),
		"enroll_token": token,
		"token_expire": expire,
	})
}

func (s *Server) handleGetClient(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询客户端失败")
		return
	}
	if cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	proxies, _ := s.st.ListProxies(cl.ID)
	c.JSON(http.StatusOK, gin.H{"client": s.toClientDTO(cl), "proxies": proxies})
}

type updateClientReq struct {
	Name    *string `json:"name"`
	Remark  *string `json:"remark"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) handleUpdateClient(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil || cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	var req updateClientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求体格式错误")
		return
	}
	name, remark, enabled := cl.Name, cl.Remark, cl.Enabled
	if req.Name != nil && *req.Name != "" {
		if other, _ := s.st.GetClientByName(*req.Name); other != nil && other.ID != cl.ID {
			fail(c, http.StatusConflict, "name_exists", "客户端名称已存在")
			return
		}
		name = *req.Name
	}
	if req.Remark != nil {
		remark = *req.Remark
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.st.UpdateClient(cl.ID, name, remark, enabled); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "更新客户端失败")
		return
	}
	if !enabled && cl.Enabled {
		s.mgr.Kick(cl.ID, "客户端已被禁用")
	}
	s.SyncPorts()
	_ = s.mgr.Push(cl.ID)
	s.audit(c, "client.update", "client", cl.ID, "更新客户端")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleDeleteClient(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil || cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	s.mgr.Kick(cl.ID, "客户端已被删除")
	if err := s.st.DeleteClient(cl.ID); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "删除客户端失败")
		return
	}
	s.SyncPorts()
	s.audit(c, "client.delete", "client", cl.ID, "删除客户端: "+cl.Name)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleNewToken(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil || cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	token := auth.RandomToken()
	expire := time.Now().Add(15 * time.Minute)
	if err := s.st.SetClientToken(cl.ID, auth.TokenHash(token), expire); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "生成令牌失败")
		return
	}
	s.audit(c, "client.enroll_token", "client", cl.ID, "重新生成注册令牌")
	c.JSON(http.StatusOK, gin.H{"enroll_token": token, "token_expire": expire, "client_name": cl.Name})
}

func (s *Server) handleRevokeCert(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil || cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	if err := s.st.RevokeClientCert(cl.ID); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "吊销证书失败")
		return
	}
	s.mgr.Kick(cl.ID, "证书已被吊销")
	s.audit(c, "client.revoke_cert", "client", cl.ID, "吊销客户端证书: "+cl.Name)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handlePendingProxies(c *gin.Context) {
	list, err := s.st.ListProxies(c.Param("id"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询失败")
		return
	}
	out := make([]*store.Proxy, 0)
	for _, p := range list {
		if p.Approval == "pending" {
			out = append(out, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}
