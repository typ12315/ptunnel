// Package release 管理客户端升级包：落盘、校验、版本比较与按平台选包。
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"ptunnel/internal/store"
)

// Manager 负责升级包的磁盘存放与元数据。
type Manager struct {
	st  *store.Store
	dir string
}

// New 创建管理器，目录不存在时自动创建。
func New(st *store.Store, dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建升级包目录失败: %w", err)
	}
	return &Manager{st: st, dir: dir}, nil
}

// Path 返回升级包在磁盘上的路径（文件名使用记录 ID，避免任何外部输入参与路径拼接）。
func (m *Manager) Path(r *store.ClientRelease) string {
	return filepath.Join(m.dir, r.ID)
}

// Save 把上传内容落盘并写入元数据，返回带校验和的记录。
func (m *Manager) Save(src io.Reader, filename, version, osName, arch, note string) (*store.ClientRelease, error) {
	id := uuid.NewString()
	path := filepath.Join(m.dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("写入升级包失败: %w", err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), src)
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("保存升级包失败: %w", copyErr)
	}
	if n == 0 {
		_ = os.Remove(path)
		return nil, fmt.Errorf("上传内容为空")
	}
	r := &store.ClientRelease{
		ID:       id,
		Version:  version,
		OS:       osName,
		Arch:     arch,
		Filename: filename,
		Size:     n,
		SHA256:   hex.EncodeToString(h.Sum(nil)),
		Note:     note,
	}
	if err := m.st.CreateRelease(r); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return r, nil
}

// Delete 删除记录与磁盘文件。
func (m *Manager) Delete(r *store.ClientRelease) error {
	if err := m.st.DeleteRelease(r.ID); err != nil {
		return err
	}
	if err := os.Remove(m.Path(r)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Best 返回指定平台版本最高的升级包，没有则返回 nil。
func (m *Manager) Best(osName, arch string) (*store.ClientRelease, error) {
	list, err := m.st.ListReleases()
	if err != nil {
		return nil, err
	}
	var best *store.ClientRelease
	for _, r := range list {
		if r.OS != osName || r.Arch != arch {
			continue
		}
		if best == nil || Compare(r.Version, best.Version) > 0 {
			best = r
		}
	}
	return best, nil
}

// Index 返回 "os/arch" 到最高版本号的映射，供列表页批量标注可升级状态。
func (m *Manager) Index() (map[string]string, error) {
	list, err := m.st.ListReleases()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range list {
		key := r.OS + "/" + r.Arch
		if cur, ok := out[key]; !ok || Compare(r.Version, cur) > 0 {
			out[key] = r.Version
		}
	}
	return out, nil
}

// IsUpgradeable 判断 target 是否高于 current。
func IsUpgradeable(current, target string) bool {
	if target == "" {
		return false
	}
	return Compare(target, current) > 0
}

// Compare 比较两个版本号，返回 -1 / 0 / 1。
// 按 . - + 分段；两段都能解析为数字时按数值比较，否则按字符串比较。
// "v" 前缀会被忽略，"dev" 这类非版本串视为最低。
func Compare(a, b string) int {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := compareSegment(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func splitVersion(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return nil
	}
	return strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '+' || r == '_'
	})
}

func compareSegment(x, y string) int {
	if x == "" {
		x = "0"
	}
	if y == "" {
		y = "0"
	}
	xi, xerr := strconv.Atoi(x)
	yi, yerr := strconv.Atoi(y)
	switch {
	case xerr == nil && yerr == nil:
		switch {
		case xi < yi:
			return -1
		case xi > yi:
			return 1
		}
		return 0
	case xerr == nil:
		return 1 // 数字段高于非数字段（如 0.4.0 > dev）
	case yerr == nil:
		return -1
	default:
		return strings.Compare(x, y)
	}
}
