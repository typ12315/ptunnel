package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ptunnel/internal/release"
	"ptunnel/internal/tunnel"
)

// 升级包体积上限：留足余量，避免误传大文件把磁盘写满。
const maxReleaseSize = 200 << 20

// 支持的平台。客户端通过 runtime.GOOS / GOARCH 上报，必须完全一致才会被选中。
var (
	releaseOS   = map[string]bool{"linux": true, "darwin": true}
	releaseArch = map[string]bool{"amd64": true, "arm64": true, "arm": true}
)

func (s *Server) handleListReleases(c *gin.Context) {
	list, err := s.st.ListReleases()
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询升级包失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "total": len(list)})
}

// handleUploadRelease 接收管理台上传的客户端二进制。
// 表单字段：file（文件）、version、os、arch、note（可选）。
func (s *Server) handleUploadRelease(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReleaseSize)
	fh, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_file", "请选择要上传的客户端二进制文件")
		return
	}
	version := strings.TrimSpace(c.PostForm("version"))
	if !validReleaseVersion(version) {
		fail(c, http.StatusBadRequest, "invalid_version", "版本号非法（仅允许字母、数字与 . - _，最长 32 字符）")
		return
	}
	osName := strings.ToLower(strings.TrimSpace(c.PostForm("os")))
	if !releaseOS[osName] {
		fail(c, http.StatusBadRequest, "invalid_os", "不支持的系统，可选 linux / darwin")
		return
	}
	arch := strings.ToLower(strings.TrimSpace(c.PostForm("arch")))
	if !releaseArch[arch] {
		fail(c, http.StatusBadRequest, "invalid_arch", "不支持的架构，可选 amd64 / arm64 / arm")
		return
	}
	if existing, err := s.st.FindRelease(version, osName, arch); err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询升级包失败")
		return
	} else if existing != nil {
		fail(c, http.StatusConflict, "release_exists", "该版本在该平台已存在，请先删除旧包")
		return
	}
	src, err := fh.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_file", "读取上传文件失败")
		return
	}
	defer src.Close()

	rel, err := s.rel.Save(src, fh.Filename, version, osName, arch, strings.TrimSpace(c.PostForm("note")))
	if err != nil {
		fail(c, http.StatusInternalServerError, "save_failed", "保存升级包失败: "+err.Error())
		return
	}
	s.audit(c, "release.upload", "release", rel.ID,
		"上传客户端升级包: "+version+" "+osName+"/"+arch)
	c.JSON(http.StatusOK, gin.H{"release": rel})
}

func (s *Server) handleDeleteRelease(c *gin.Context) {
	rel, err := s.st.GetRelease(c.Param("id"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询升级包失败")
		return
	}
	if rel == nil {
		fail(c, http.StatusNotFound, "not_found", "升级包不存在")
		return
	}
	if err := s.rel.Delete(rel); err != nil {
		fail(c, http.StatusInternalServerError, "delete_failed", "删除升级包失败: "+err.Error())
		return
	}
	s.audit(c, "release.delete", "release", rel.ID, "删除客户端升级包: "+rel.Version+" "+rel.OS+"/"+rel.Arch)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleUpgradeClient 向指定客户端下发升级指令。
func (s *Server) handleUpgradeClient(c *gin.Context) {
	cl, err := s.st.GetClient(c.Param("id"))
	if err != nil || cl == nil {
		fail(c, http.StatusNotFound, "not_found", "客户端不存在")
		return
	}
	if cl.OS == "" || cl.Arch == "" {
		fail(c, http.StatusBadRequest, "unknown_platform", "客户端尚未上报平台信息，请等其上线后再试")
		return
	}
	best, err := s.rel.Best(cl.OS, cl.Arch)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询升级包失败")
		return
	}
	if best == nil {
		fail(c, http.StatusBadRequest, "no_release", "服务端没有 "+cl.OS+"/"+cl.Arch+" 的升级包")
		return
	}
	if !release.IsUpgradeable(cl.Version, best.Version) {
		fail(c, http.StatusBadRequest, "already_latest", "客户端已是该平台最新版本 "+best.Version)
		return
	}
	up := tunnel.Upgrade{
		Version: best.Version,
		OS:      best.OS,
		Arch:    best.Arch,
		Size:    best.Size,
		SHA256:  best.SHA256,
	}
	if err := s.mgr.SendUpgrade(cl.ID, up); err != nil {
		fail(c, http.StatusConflict, "send_failed", "下发升级指令失败: "+err.Error())
		return
	}
	s.audit(c, "client.upgrade", "client", cl.ID,
		"下发升级指令: "+cl.Version+" -> "+best.Version)
	c.JSON(http.StatusOK, gin.H{"ok": true, "version": best.Version})
}

// handleDownloadArtifact 供客户端下载升级包，通过客户端证书 CN 识别身份。
// 只返回该客户端平台对应的最高版本，实际完整性由升级指令中的 sha256 保证。
func (s *Server) handleDownloadArtifact(c *gin.Context) {
	cn := ""
	if c.Request.TLS != nil && len(c.Request.TLS.PeerCertificates) > 0 {
		cn = c.Request.TLS.PeerCertificates[0].Subject.CommonName
	}
	if cn == "" {
		fail(c, http.StatusUnauthorized, "no_client_cert", "缺少客户端证书")
		return
	}
	cl, err := s.st.GetClient(cn)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询客户端失败")
		return
	}
	if cl == nil || !cl.Enabled || cl.RevokedAt != nil {
		fail(c, http.StatusForbidden, "client_forbidden", "客户端不存在、已禁用或证书已吊销")
		return
	}
	best, err := s.rel.Best(cl.OS, cl.Arch)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询升级包失败")
		return
	}
	if best == nil {
		fail(c, http.StatusNotFound, "no_release", "服务端暂无该平台的升级包")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+best.Filename)
	c.Header("X-Version", best.Version)
	c.Header("X-SHA256", best.SHA256)
	c.File(s.rel.Path(best))
}

func validReleaseVersion(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
