package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Proxy 端口代理规则。
type Proxy struct {
	ID          string    `json:"id"`
	ClientID    string    `json:"client_id"`
	Name        string    `json:"name"`
	Protocol    string    `json:"protocol"`
	BindAddr    string    `json:"bind_addr"`
	RemotePort  int       `json:"remote_port"`
	TargetAddr  string    `json:"target_addr"`
	AppType     string    `json:"app_type"`
	Enabled     bool      `json:"enabled"`
	AllowCIDRs  []string  `json:"allow_cidrs"`
	MaxConns    int       `json:"max_conns"`
	RateLimitMB int       `json:"rate_limit_mb"`
	Origin      string    `json:"origin"`
	Approval    string    `json:"approval"`
	HealthCheck bool      `json:"health_check"`
	State       string    `json:"state"`
	StateMsg    string    `json:"state_msg"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const proxyCols = `id, client_id, name, protocol, bind_addr, remote_port, target_addr, app_type, enabled,
	allow_cidrs, max_conns, rate_limit_mb, origin, approval, health_check, state, state_msg, created_at, updated_at`

func scanProxy(sc interface{ Scan(dest ...any) error }) (*Proxy, error) {
	var p Proxy
	var enabled, health int
	var cidrs string
	var created, updated int64
	err := sc.Scan(&p.ID, &p.ClientID, &p.Name, &p.Protocol, &p.BindAddr, &p.RemotePort, &p.TargetAddr, &p.AppType,
		&enabled, &cidrs, &p.MaxConns, &p.RateLimitMB, &p.Origin, &p.Approval, &health,
		&p.State, &p.StateMsg, &created, &updated)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled == 1
	p.HealthCheck = health == 1
	p.AllowCIDRs = []string{}
	_ = json.Unmarshal([]byte(cidrs), &p.AllowCIDRs)
	p.CreatedAt = time.UnixMilli(created)
	p.UpdatedAt = time.UnixMilli(updated)
	return &p, nil
}

func (s *Store) queryProxies(query string, args ...any) ([]*Proxy, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Proxy
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListProxies 返回代理列表；clientID 为空时返回全部。
func (s *Store) ListProxies(clientID string) ([]*Proxy, error) {
	if clientID == "" {
		return s.queryProxies(`SELECT ` + proxyCols + ` FROM proxies ORDER BY remote_port ASC`)
	}
	return s.queryProxies(`SELECT `+proxyCols+` FROM proxies WHERE client_id = ? ORDER BY remote_port ASC`, clientID)
}

// ListPushable 返回需要下发给客户端的代理（已审批且启用）。
func (s *Store) ListPushable(clientID string) ([]*Proxy, error) {
	return s.queryProxies(`SELECT `+proxyCols+` FROM proxies WHERE client_id = ? AND approval = 'approved' ORDER BY remote_port ASC`, clientID)
}

// ListRunnable 返回应处于监听状态的代理（已审批、启用、客户端未禁用）。
func (s *Store) ListRunnable() ([]*Proxy, error) {
	return s.queryProxies(`SELECT p.id, p.client_id, p.name, p.protocol, p.bind_addr, p.remote_port,
		p.target_addr, p.app_type, p.enabled, p.allow_cidrs, p.max_conns, p.rate_limit_mb, p.origin, p.approval,
		p.health_check, p.state, p.state_msg, p.created_at, p.updated_at
		FROM proxies p JOIN clients c ON c.id = p.client_id
		WHERE p.approval = 'approved' AND p.enabled = 1 AND c.enabled = 1 ORDER BY p.remote_port ASC`)
}

// GetProxy 按 ID 查询。
func (s *Store) GetProxy(id string) (*Proxy, error) {
	p, err := scanProxy(s.db.QueryRow(`SELECT `+proxyCols+` FROM proxies WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// ProxyConflict 判断端口是否已被其他代理占用。
func (s *Store) ProxyConflict(bindAddr, protocol string, port int, excludeID string) (string, error) {
	var id, name string
	err := s.db.QueryRow(`SELECT id, name FROM proxies WHERE bind_addr = ? AND protocol = ? AND remote_port = ? AND id <> ?`,
		bindAddr, protocol, port, excludeID).Scan(&id, &name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// CreateProxy 新建代理。
func (s *Store) CreateProxy(p *Proxy) error {
	cidrs, _ := json.Marshal(p.AllowCIDRs)
	now := nowMS()
	_, err := s.db.Exec(`INSERT INTO proxies(id, client_id, name, protocol, bind_addr, remote_port, target_addr, app_type,
		enabled, allow_cidrs, max_conns, rate_limit_mb, origin, approval, health_check, state, state_msg, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ClientID, p.Name, p.Protocol, p.BindAddr, p.RemotePort, p.TargetAddr, p.AppType,
		boolInt(p.Enabled), string(cidrs), p.MaxConns, p.RateLimitMB, p.Origin, p.Approval,
		boolInt(p.HealthCheck), p.State, p.StateMsg, now, now)
	if err == nil {
		p.CreatedAt = time.UnixMilli(now)
		p.UpdatedAt = p.CreatedAt
	}
	return err
}

// UpdateProxy 更新代理配置。
func (s *Store) UpdateProxy(p *Proxy) error {
	cidrs, _ := json.Marshal(p.AllowCIDRs)
	_, err := s.db.Exec(`UPDATE proxies SET name = ?, protocol = ?, bind_addr = ?, remote_port = ?,
		target_addr = ?, app_type = ?, enabled = ?, allow_cidrs = ?, max_conns = ?, rate_limit_mb = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Protocol, p.BindAddr, p.RemotePort, p.TargetAddr, p.AppType, boolInt(p.Enabled),
		string(cidrs), p.MaxConns, p.RateLimitMB, nowMS(), p.ID)
	return err
}

// DeleteProxy 删除代理。
func (s *Store) DeleteProxy(id string) error {
	_, err := s.db.Exec(`DELETE FROM proxies WHERE id = ?`, id)
	return err
}

// SetProxyEnabled 启停代理。
func (s *Store) SetProxyEnabled(id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE proxies SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(enabled), nowMS(), id)
	return err
}

// SetProxyApproval 审批本地登记的代理。
func (s *Store) SetProxyApproval(id, approval string) error {
	_, err := s.db.Exec(`UPDATE proxies SET approval = ?, updated_at = ? WHERE id = ?`, approval, nowMS(), id)
	return err
}

// SetProxyState 写入监听状态。
func (s *Store) SetProxyState(id, state, msg string) {
	_, _ = s.db.Exec(`UPDATE proxies SET state = ?, state_msg = ? WHERE id = ?`, state, msg, id)
}

// ResetProxyStates 启动时把残留状态重置为空闲。
func (s *Store) ResetProxyStates() {
	_, _ = s.db.Exec(`UPDATE proxies SET state = 'idle', state_msg = ''`)
}

// CountProxies 统计客户端的代理数量。
func (s *Store) CountProxies(clientID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM proxies WHERE client_id = ?`, clientID).Scan(&n)
	return n, err
}

// FindLocalProxy 查询客户端本地登记的同一端口代理（用于合并）。
func (s *Store) FindLocalProxy(clientID string, protocol string, port int) (*Proxy, error) {
	p, err := scanProxy(s.db.QueryRow(`SELECT `+proxyCols+` FROM proxies WHERE client_id = ? AND protocol = ? AND remote_port = ?`,
		clientID, protocol, port))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}
