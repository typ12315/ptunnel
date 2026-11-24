package store

import (
	"database/sql"
	"time"
)

// Client 客户端（Agent）记录。
type Client struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Remark       string     `json:"remark"`
	Enabled      bool       `json:"enabled"`
	Status       string     `json:"status"`
	TokenHash    string     `json:"-"`
	TokenExpire  *time.Time `json:"-"`
	CertSerial   string     `json:"cert_serial"`
	CertNotAfter *time.Time `json:"cert_not_after"`
	RevokedAt    *time.Time `json:"revoked_at"`
	Version      string     `json:"version"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	RemoteAddr   string     `json:"remote_addr"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const clientCols = `id, name, remark, enabled, status, token_hash, token_expire, cert_serial,
	cert_not_after, revoked_at, version, os, arch, remote_addr, last_seen_at, created_at, updated_at`

func scanClient(sc interface {
	Scan(dest ...any) error
}) (*Client, error) {
	var c Client
	var enabled int
	var tokenHash, certSerial sql.NullString
	var tokenExpire, certNotAfter, revokedAt, lastSeen sql.NullInt64
	var created, updated int64
	err := sc.Scan(&c.ID, &c.Name, &c.Remark, &enabled, &c.Status, &tokenHash,
		&tokenExpire, &certSerial, &certNotAfter, &revokedAt, &c.Version, &c.OS, &c.Arch,
		&c.RemoteAddr, &lastSeen, &created, &updated)
	if err != nil {
		return nil, err
	}
	c.TokenHash = tokenHash.String
	c.CertSerial = certSerial.String
	c.Enabled = enabled == 1
	c.TokenExpire = TimePtr(tokenExpire)
	c.CertNotAfter = TimePtr(certNotAfter)
	c.RevokedAt = TimePtr(revokedAt)
	c.LastSeenAt = TimePtr(lastSeen)
	c.CreatedAt = time.UnixMilli(created)
	c.UpdatedAt = time.UnixMilli(updated)
	return &c, nil
}

// ListClients 返回全部客户端。
func (s *Store) ListClients() ([]*Client, error) {
	rows, err := s.db.Query(`SELECT ` + clientCols + ` FROM clients ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetClient 按 ID 查询。
func (s *Store) GetClient(id string) (*Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// GetClientByName 按名称查询。
func (s *Store) GetClientByName(name string) (*Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE name = ?`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// GetClientByCertSerial 按证书序列号查询（用于吊销校验）。
func (s *Store) GetClientByCertSerial(serial string) (*Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE cert_serial = ?`, serial))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// FindClientByTokenHash 按注册令牌哈希查询。
func (s *Store) FindClientByTokenHash(hash string) (*Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE token_hash = ?`, hash))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// CreateClient 新建客户端。
func (s *Store) CreateClient(c *Client) error {
	now := nowMS()
	_, err := s.db.Exec(`INSERT INTO clients(id, name, remark, enabled, status, token_hash, token_expire,
		cert_serial, cert_not_after, version, os, arch, remote_addr, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', '', '', ?, ?)`,
		c.ID, c.Name, c.Remark, boolInt(c.Enabled), c.Status, nullableStr(c.TokenHash),
		Millis(c.TokenExpire), now, now)
	return err
}

// UpdateClient 更新名称/备注/启用状态。
func (s *Store) UpdateClient(id, name, remark string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE clients SET name = ?, remark = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		name, remark, boolInt(enabled), nowMS(), id)
	return err
}

// DeleteClient 删除客户端（级联删除代理）。
func (s *Store) DeleteClient(id string) error {
	_, err := s.db.Exec(`DELETE FROM clients WHERE id = ?`, id)
	return err
}

// SetClientStatus 更新在线状态。
func (s *Store) SetClientStatus(id, status string) {
	_, _ = s.db.Exec(`UPDATE clients SET status = ?, updated_at = ? WHERE id = ?`, status, nowMS(), id)
}

// SetClientToken 写入注册令牌哈希与过期时间。
func (s *Store) SetClientToken(id, hash string, expire time.Time) error {
	_, err := s.db.Exec(`UPDATE clients SET token_hash = ?, token_expire = ?, updated_at = ? WHERE id = ?`,
		hash, expire.UnixMilli(), nowMS(), id)
	return err
}

// ClearClientToken 令牌使用后清除。
func (s *Store) ClearClientToken(id string) error {
	_, err := s.db.Exec(`UPDATE clients SET token_hash = NULL, token_expire = NULL WHERE id = ?`, id)
	return err
}

// SetClientCert 记录当前证书序列号与到期时间，并清除吊销标记。
func (s *Store) SetClientCert(id, serial string, notAfter time.Time) error {
	_, err := s.db.Exec(`UPDATE clients SET cert_serial = ?, cert_not_after = ?, revoked_at = NULL, updated_at = ? WHERE id = ?`,
		serial, notAfter.UnixMilli(), nowMS(), id)
	return err
}

// RevokeClientCert 吊销客户端证书。
func (s *Store) RevokeClientCert(id string) error {
	_, err := s.db.Exec(`UPDATE clients SET revoked_at = ?, updated_at = ? WHERE id = ?`, nowMS(), nowMS(), id)
	return err
}

// SetClientSeen 更新上线信息。
func (s *Store) SetClientSeen(id, version, osName, arch, remoteAddr string) {
	_, _ = s.db.Exec(`UPDATE clients SET version = ?, os = ?, arch = ?, remote_addr = ?, last_seen_at = ?, updated_at = ? WHERE id = ?`,
		version, osName, arch, remoteAddr, nowMS(), nowMS(), id)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
