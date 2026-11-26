package store

import (
	"database/sql"
	"time"
)

// ClientRelease 一个可供在线升级下发的客户端二进制包。
type ClientRelease struct {
	ID         string    `json:"id"`
	Version    string    `json:"version"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	Filename   string    `json:"filename"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	Note       string    `json:"note"`
	UploadedAt time.Time `json:"uploaded_at"`
}

const releaseCols = `id, version, os, arch, filename, size, sha256, note, uploaded_at`

func scanRelease(sc interface {
	Scan(dest ...any) error
}) (*ClientRelease, error) {
	var r ClientRelease
	var uploaded int64
	if err := sc.Scan(&r.ID, &r.Version, &r.OS, &r.Arch, &r.Filename, &r.Size, &r.SHA256, &r.Note, &uploaded); err != nil {
		return nil, err
	}
	r.UploadedAt = time.UnixMilli(uploaded)
	return &r, nil
}

// ListReleases 返回全部升级包（按上传时间倒序）。
func (s *Store) ListReleases() ([]*ClientRelease, error) {
	rows, err := s.db.Query(`SELECT ` + releaseCols + ` FROM client_releases ORDER BY uploaded_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ClientRelease
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRelease 按 ID 查询升级包。
func (s *Store) GetRelease(id string) (*ClientRelease, error) {
	r, err := scanRelease(s.db.QueryRow(`SELECT `+releaseCols+` FROM client_releases WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// FindRelease 按 版本+系统+架构 查询升级包（用于上传时的重名校验）。
func (s *Store) FindRelease(version, osName, arch string) (*ClientRelease, error) {
	r, err := scanRelease(s.db.QueryRow(
		`SELECT `+releaseCols+` FROM client_releases WHERE version = ? AND os = ? AND arch = ?`,
		version, osName, arch))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// CreateRelease 写入升级包记录。
func (s *Store) CreateRelease(r *ClientRelease) error {
	now := nowMS()
	_, err := s.db.Exec(`INSERT INTO client_releases(id, version, os, arch, filename, size, sha256, note, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Version, r.OS, r.Arch, r.Filename, r.Size, r.SHA256, r.Note, now)
	if err == nil {
		r.UploadedAt = time.UnixMilli(now)
	}
	return err
}

// DeleteRelease 删除升级包记录。
func (s *Store) DeleteRelease(id string) error {
	_, err := s.db.Exec(`DELETE FROM client_releases WHERE id = ?`, id)
	return err
}
