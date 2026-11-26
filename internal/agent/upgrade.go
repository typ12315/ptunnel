package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ptunnel/internal/tunnel"
)

// upgradeHTTPTimeout 下载升级包的超时；比普通接口宽松得多。
const upgradeHTTPTimeout = 10 * time.Minute

// handleUpgrade 在独立协程中执行升级，避免阻塞控制流读取。
func (a *Agent) handleUpgrade(ctrl *tunnel.Control, u tunnel.Upgrade) {
	if !a.upgrading.CompareAndSwap(false, true) {
		a.log.Warn("已有升级任务在执行", "version", u.Version)
		return
	}
	defer a.upgrading.Store(false)

	exe, err := a.installUpgrade(u)
	res := tunnel.UpgradeResult{Version: u.Version, OK: err == nil}
	if err != nil {
		res.Message = err.Error()
		a.log.Error("在线升级失败", "version", u.Version, "err", err)
		_ = ctrl.Send(tunnel.MsgUpgradeResult, res)
		return
	}
	a.log.Info("升级包已替换，准备重启", "version", u.Version, "path", exe)
	// 先回报结果，再替换进程镜像；即使回报丢失，重连后服务端也会按新版本收敛状态
	_ = ctrl.Send(tunnel.MsgUpgradeResult, res)
	time.Sleep(300 * time.Millisecond)
	if err := restartSelf(exe); err != nil {
		a.log.Error("重启失败，新版本将在下次启动生效，请手工重启", "err", err)
	}
}

// installUpgrade 下载、校验并原子替换当前二进制，返回可执行文件路径。
func (a *Agent) installUpgrade(u tunnel.Upgrade) (string, error) {
	if u.Version == "" || u.SHA256 == "" {
		return "", fmt.Errorf("升级指令不完整")
	}
	if runtime.GOOS != u.OS || runtime.GOARCH != u.Arch {
		return "", fmt.Errorf("平台不匹配：服务端提供 %s/%s，本机为 %s/%s", u.OS, u.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if !supportsSelfUpgrade() {
		return "", fmt.Errorf("当前平台 %s 不支持在线升级，请手工替换二进制", runtime.GOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位可执行文件失败: %w", err)
	}
	// 若以软链接方式启动，需要替换真实文件而不是链接本身
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".ptunnel-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败（%s 需要写权限）: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // 替换成功后该文件已不存在，删除会被忽略

	h := sha256.New()
	n, err := a.downloadArtifact(io.MultiWriter(tmp, h))
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if u.Size > 0 && n != u.Size {
		return "", fmt.Errorf("升级包大小不符：期望 %d 字节，实际 %d 字节", u.Size, n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, u.SHA256) {
		return "", fmt.Errorf("升级包校验和不匹配，已丢弃")
	}
	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return "", fmt.Errorf("设置可执行权限失败: %w", err)
	}
	// 留一份上一版本，便于回滚（失败不影响升级）
	if err := copyFile(exe, exe+".bak"); err != nil {
		a.log.Warn("备份旧版本失败", "err", err)
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return "", fmt.Errorf("替换二进制失败: %w", err)
	}
	return exe, nil
}

// downloadArtifact 通过隧道端口的 mTLS 接口下载升级包。
func (a *Agent) downloadArtifact(w io.Writer) (int64, error) {
	client, err := a.httpClientTimeout("", upgradeHTTPTimeout)
	if err != nil {
		return 0, err
	}
	resp, err := client.Get("https://" + a.cfg.Server.Addr + "/api/client/upgrade/artifact")
	if err != nil {
		return 0, fmt.Errorf("下载升级包失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return 0, fmt.Errorf("下载升级包失败(%d): %s", resp.StatusCode, extractErr(data))
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("写入升级包失败: %w", err)
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
