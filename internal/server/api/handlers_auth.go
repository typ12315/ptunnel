package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ptunnel/internal/auth"
)

const clientCertTTL = 90 * 24 * time.Hour

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (s *Server) handleLogin(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请输入用户名与口令")
		return
	}
	ip := c.ClientIP()
	s.loginMu.Lock()
	// 顺带清理已过期的失败计数，避免 map 随来源 IP 无限增长
	if len(s.loginFails) > 256 {
		now := time.Now()
		for k, v := range s.loginFails {
			if v.until.Before(now) && v.fails == 0 {
				delete(s.loginFails, k)
			}
		}
	}
	st := s.loginFails[ip]
	if st != nil && st.until.After(time.Now()) {
		s.loginMu.Unlock()
		fail(c, http.StatusTooManyRequests, "rate_limited", "登录失败次数过多，请稍后再试")
		return
	}
	s.loginMu.Unlock()

	user, err := s.st.GetUserByName(req.Username)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询用户失败")
		return
	}
	if user == nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		s.loginMu.Lock()
		if s.loginFails[ip] == nil {
			s.loginFails[ip] = &loginState{}
		}
		s.loginFails[ip].fails++
		limit := s.cfg.Auth.LoginRateLimit
		if limit <= 0 {
			limit = 10
		}
		if s.loginFails[ip].fails >= limit {
			s.loginFails[ip].fails = 0
			s.loginFails[ip].until = time.Now().Add(time.Minute)
		}
		s.loginMu.Unlock()
		s.audit(c, "auth.login_failed", "user", req.Username, "登录失败")
		fail(c, http.StatusUnauthorized, "invalid_credentials", "用户名或口令错误")
		return
	}

	s.loginMu.Lock()
	delete(s.loginFails, ip)
	s.loginMu.Unlock()

	token, exp := s.sess.Create(user.ID, user.Username, user.Role)
	csrf := randomHex(16)
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookie, token, int(time.Until(exp).Seconds()), "/", "", true, true)
	c.SetCookie(csrfCookie, csrf, int(time.Until(exp).Seconds()), "/", "", true, false)
	s.st.TouchUserLogin(user.ID)
	s.audit(c, "auth.login", "user", user.Username, "")

	c.JSON(http.StatusOK, gin.H{
		"username":   user.Username,
		"role":       user.Role,
		"expires_at": exp,
		"csrf_token": csrf,
	})
}

func (s *Server) handleLogout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookie); err == nil {
		s.sess.Revoke(token)
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookie, "", -1, "/", "", true, true)
	c.SetCookie(csrfCookie, "", -1, "/", "", true, false)
	s.audit(c, "auth.logout", "user", "", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleMe(c *gin.Context) {
	sess := currentSession(c)
	if sess == nil {
		fail(c, http.StatusUnauthorized, "unauthorized", "未登录")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"username":   sess.Username,
		"role":       sess.Role,
		"expires_at": time.Unix(sess.Expires, 0),
	})
}

type changePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func (s *Server) handleChangePassword(c *gin.Context) {
	var req changePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 8 {
		fail(c, http.StatusBadRequest, "invalid_body", "新口令至少 8 位")
		return
	}
	sess := currentSession(c)
	user, err := s.st.GetUserByName(sess.Username)
	if err != nil || user == nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询用户失败")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, req.OldPassword) {
		fail(c, http.StatusBadRequest, "invalid_old_password", "原口令错误")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash_failed", "生成口令哈希失败")
		return
	}
	if err := s.st.UpdateUserPassword(user.Username, hash); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "保存口令失败")
		return
	}
	s.sess.RevokeUser(user.ID)
	s.audit(c, "auth.password_change", "user", user.Username, "")
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "口令已修改，请重新登录"})
}

// ---- 客户端注册 / 证书轮换（走隧道端口，不使用管理会话） ----

type enrollReq struct {
	Token   string `json:"token" binding:"required"`
	Name    string `json:"name"`
	CSR     string `json:"csr" binding:"required"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func (s *Server) handleEnroll(c *gin.Context) {
	var req enrollReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求参数不完整")
		return
	}
	hash := auth.TokenHash(strings.TrimSpace(req.Token))
	client, err := s.st.FindClientByTokenHash(hash)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询客户端失败")
		return
	}
	if client == nil {
		fail(c, http.StatusUnauthorized, "invalid_token", "注册令牌无效")
		return
	}
	if client.TokenExpire == nil || client.TokenExpire.Before(time.Now()) {
		fail(c, http.StatusUnauthorized, "token_expired", "注册令牌已过期，请在服务端重新生成")
		return
	}
	if !client.Enabled {
		fail(c, http.StatusForbidden, "client_disabled", "客户端已被禁用")
		return
	}
	certPEM, serial, notAfter, err := s.ca.IssueClientCert([]byte(req.CSR), client.ID, clientCertTTL)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_csr", "签发证书失败: "+err.Error())
		return
	}
	if err := s.st.SetClientCert(client.ID, serial, notAfter); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "保存证书信息失败")
		return
	}
	if err := s.st.ClearClientToken(client.ID); err != nil {
		s.log.Warn("清除注册令牌失败", "err", err)
	}
	s.st.SetClientSeen(client.ID, req.Version, req.OS, req.Arch, c.ClientIP())
	s.audit(c, "client.enroll", "client", client.ID, "客户端完成证书注册: "+client.Name)

	c.JSON(http.StatusOK, gin.H{
		"client_id": client.ID,
		"name":      client.Name,
		"cert":      string(certPEM),
		"ca":        string(s.ca.CertPEM),
		"not_after": notAfter,
	})
}

type renewReq struct {
	CSR string `json:"csr" binding:"required"`
}

func (s *Server) handleRenewCert(c *gin.Context) {
	if c.Request.TLS == nil || len(c.Request.TLS.PeerCertificates) == 0 {
		fail(c, http.StatusUnauthorized, "no_client_cert", "缺少客户端证书")
		return
	}
	cn := c.Request.TLS.PeerCertificates[0].Subject.CommonName
	client, err := s.st.GetClient(cn)
	if err != nil || client == nil {
		fail(c, http.StatusUnauthorized, "unknown_client", "客户端不存在")
		return
	}
	if !client.Enabled || client.RevokedAt != nil {
		fail(c, http.StatusForbidden, "client_forbidden", "客户端已禁用或证书已吊销")
		return
	}
	var req renewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "请求参数不完整")
		return
	}
	certPEM, serial, notAfter, err := s.ca.IssueClientCert([]byte(req.CSR), cn, clientCertTTL)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_csr", "签发证书失败: "+err.Error())
		return
	}
	if err := s.st.SetClientCert(client.ID, serial, notAfter); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "保存证书信息失败")
		return
	}
	s.audit(c, "client.cert_renew", "client", client.ID, "")
	c.JSON(http.StatusOK, gin.H{
		"cert":      string(certPEM),
		"ca":        string(s.ca.CertPEM),
		"not_after": notAfter,
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
