package store

import (
	"database/sql"
	"strings"
	"time"
)

// AuditLog 配置操作审计。
type AuditLog struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Actor     string    `json:"actor"`
	IP        string    `json:"ip"`
	Action    string    `json:"action"`
	Entity    string    `json:"entity"`
	EntityID  string    `json:"entity_id"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// InsertAudit 写入操作审计。
func (s *Store) InsertAudit(a *AuditLog) error {
	_, err := s.db.Exec(`INSERT INTO audit_logs(user_id, actor, ip, action, entity, entity_id, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.Actor, a.IP, a.Action, a.Entity, a.EntityID, a.Detail, time.Now().UnixMilli())
	return err
}

// ListAudit 分页查询操作审计。
func (s *Store) ListAudit(action, actor string, page, size int) ([]*AuditLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 20
	}
	var where []string
	var args []any
	if action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, action+"%")
	}
	if actor != "" {
		where = append(where, "actor = ?")
		args = append(args, actor)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM audit_logs`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id, user_id, actor, ip, action, entity, entity_id, detail, created_at FROM audit_logs` +
		cond + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*AuditLog
	for rows.Next() {
		var a AuditLog
		var uid sql.NullInt64
		var created int64
		var actor, ip, entity, entityID, detail sql.NullString
		if err := rows.Scan(&a.ID, &uid, &actor, &ip, &a.Action, &entity, &entityID, &detail, &created); err != nil {
			return nil, 0, err
		}
		a.UserID = uid.Int64
		a.Actor, a.IP, a.Entity, a.EntityID, a.Detail = actor.String, ip.String, entity.String, entityID.String, detail.String
		a.CreatedAt = time.UnixMilli(created)
		out = append(out, &a)
	}
	return out, total, rows.Err()
}

// ConnLog 连接审计记录。
type ConnLog struct {
	ID          int64      `json:"id"`
	ConnID      string     `json:"conn_id"`
	ClientID    string     `json:"client_id"`
	ClientName  string     `json:"client_name"`
	ProxyID     string     `json:"proxy_id"`
	ProxyName   string     `json:"proxy_name"`
	Protocol    string     `json:"protocol"`
	ListenAddr  string     `json:"listen_addr"`
	RemotePort  int        `json:"remote_port"`
	TargetAddr  string     `json:"target_addr"`
	SrcIP       string     `json:"src_ip"`
	SrcPort     int        `json:"src_port"`
	SrcMAC      string     `json:"src_mac"`
	MACSource   string     `json:"mac_source"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at"`
	DurationMS  int64      `json:"duration_ms"`
	BytesIn     int64      `json:"bytes_in"`
	BytesOut    int64      `json:"bytes_out"`
	CloseReason string     `json:"close_reason"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ConnEnd 连接结束时的回填信息。
type ConnEnd struct {
	ConnID      string
	EndedAt     time.Time
	DurationMS  int64
	BytesIn     int64
	BytesOut    int64
	CloseReason string
}

// ConnFilter 连接审计查询条件。
type ConnFilter struct {
	ClientID   string
	ProxyID    string
	SrcIP      string
	RemotePort int
	From       *time.Time
	To         *time.Time
	ActiveOnly bool
	Page       int
	Size       int
}

// InsertConnLogs 批量写入连接审计（事务）。
func (s *Store) InsertConnLogs(list []*ConnLog) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO conn_logs(conn_id, client_id, client_name, proxy_id, proxy_name,
		protocol, listen_addr, remote_port, target_addr, src_ip, src_port, src_mac, mac_source,
		started_at, ended_at, duration_ms, bytes_in, bytes_out, close_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0, 0, '', ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, l := range list {
		if _, err := stmt.Exec(l.ConnID, l.ClientID, l.ClientName, l.ProxyID, l.ProxyName, l.Protocol,
			l.ListenAddr, l.RemotePort, l.TargetAddr, l.SrcIP, l.SrcPort, nullableStr(l.SrcMAC),
			orDefault(l.MACSource, "none"), l.StartedAt.UnixMilli(), l.StartedAt.UnixMilli()); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// FinishConnLogs 批量回填连接结束信息（事务）。
func (s *Store) FinishConnLogs(list []*ConnEnd) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE conn_logs SET ended_at = ?, duration_ms = ?, bytes_in = ?,
		bytes_out = ?, close_reason = ? WHERE conn_id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range list {
		if _, err := stmt.Exec(e.EndedAt.UnixMilli(), e.DurationMS, e.BytesIn, e.BytesOut, e.CloseReason, e.ConnID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// UpdateConnLogMAC 补写 MAC 信息。
func (s *Store) UpdateConnLogMAC(connID, mac, source string) error {
	_, err := s.db.Exec(`UPDATE conn_logs SET src_mac = ?, mac_source = ? WHERE conn_id = ?`,
		nullableStr(mac), source, connID)
	return err
}

const connCols = `id, conn_id, client_id, client_name, proxy_id, proxy_name, protocol, listen_addr,
	remote_port, target_addr, src_ip, src_port, src_mac, mac_source, started_at, ended_at,
	duration_ms, bytes_in, bytes_out, close_reason, created_at`

func scanConn(sc interface{ Scan(dest ...any) error }) (*ConnLog, error) {
	var l ConnLog
	var mac, reason sql.NullString
	var ended sql.NullInt64
	var duration sql.NullInt64
	var started, created int64
	err := sc.Scan(&l.ID, &l.ConnID, &l.ClientID, &l.ClientName, &l.ProxyID, &l.ProxyName, &l.Protocol,
		&l.ListenAddr, &l.RemotePort, &l.TargetAddr, &l.SrcIP, &l.SrcPort, &mac, &l.MACSource,
		&started, &ended, &duration, &l.BytesIn, &l.BytesOut, &reason, &created)
	if err != nil {
		return nil, err
	}
	l.SrcMAC = mac.String
	l.CloseReason = reason.String
	l.DurationMS = duration.Int64
	l.StartedAt = time.UnixMilli(started)
	l.EndedAt = TimePtr(ended)
	l.CreatedAt = time.UnixMilli(created)
	return &l, nil
}

func (s *Store) connWhere(f ConnFilter) (string, []any) {
	var where []string
	var args []any
	if f.ClientID != "" {
		where = append(where, "client_id = ?")
		args = append(args, f.ClientID)
	}
	if f.ProxyID != "" {
		where = append(where, "proxy_id = ?")
		args = append(args, f.ProxyID)
	}
	if f.SrcIP != "" {
		where = append(where, "src_ip = ?")
		args = append(args, f.SrcIP)
	}
	if f.RemotePort > 0 {
		where = append(where, "remote_port = ?")
		args = append(args, f.RemotePort)
	}
	if f.From != nil {
		where = append(where, "started_at >= ?")
		args = append(args, f.From.UnixMilli())
	}
	if f.To != nil {
		where = append(where, "started_at < ?")
		args = append(args, f.To.UnixMilli())
	}
	if f.ActiveOnly {
		where = append(where, "ended_at IS NULL")
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ListConnLogs 分页查询连接审计。
func (s *Store) ListConnLogs(f ConnFilter) ([]*ConnLog, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 || f.Size > 500 {
		f.Size = 50
	}
	cond, args := s.connWhere(f)
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM conn_logs`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + connCols + ` FROM conn_logs` + cond + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*ConnLog
	for rows.Next() {
		l, err := scanConn(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// ActiveConnLogs 返回当前活跃连接。
func (s *Store) ActiveConnLogs(limit int) ([]*ConnLog, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+connCols+` FROM conn_logs WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ConnLog
	for rows.Next() {
		l, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetConnLog 按主键查询。
func (s *Store) GetConnLog(id int64) (*ConnLog, error) {
	l, err := scanConn(s.db.QueryRow(`SELECT `+connCols+` FROM conn_logs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return l, err
}

// DeleteConnLogsBefore 分批删除过期记录，返回删除行数。
func (s *Store) DeleteConnLogsBefore(t time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 5000
	}
	res, err := s.db.Exec(s.d.deleteConnLogsBeforeSQL(), t.UnixMilli(), batch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkUnclean 启动时收尾悬挂记录。
func (s *Store) MarkUnclean() (int64, error) {
	res, err := s.db.Exec(`UPDATE conn_logs SET ended_at = started_at, duration_ms = 0,
		close_reason = 'unclean' WHERE ended_at IS NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TrafficPoint 流量曲线数据点。
type TrafficPoint struct {
	TS       time.Time `json:"ts"`
	Conns    int64     `json:"conns"`
	BytesIn  int64     `json:"bytes_in"`
	BytesOut int64     `json:"bytes_out"`
}

// TrafficSeries 按时间桶聚合连接流量（用于图表）。
func (s *Store) TrafficSeries(proxyID string, from, to time.Time, bucket time.Duration) ([]TrafficPoint, error) {
	sec := int64(bucket / time.Second)
	if sec <= 0 {
		sec = 300
	}
	q := `SELECT ` + s.d.trafficBucketExpr() + ` AS bucket, COUNT(1), COALESCE(SUM(bytes_in),0), COALESCE(SUM(bytes_out),0)
		FROM conn_logs WHERE started_at >= ? AND started_at < ?`
	args := []any{sec, sec, from.UnixMilli(), to.UnixMilli()}
	if proxyID != "" {
		q += ` AND proxy_id = ?`
		args = append(args, proxyID)
	}
	q += ` GROUP BY bucket ORDER BY bucket ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficPoint
	for rows.Next() {
		var bucket, conns, in, outB int64
		if err := rows.Scan(&bucket, &conns, &in, &outB); err != nil {
			return nil, err
		}
		out = append(out, TrafficPoint{TS: time.Unix(bucket*sec, 0), Conns: conns, BytesIn: in, BytesOut: outB})
	}
	return out, rows.Err()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// SumSince 统计某时间点之后的连接数与流量。
func (s *Store) SumSince(ts time.Time) (conns, bytesIn, bytesOut int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(bytes_in),0), COALESCE(SUM(bytes_out),0)
		FROM conn_logs WHERE started_at >= ?`, ts.UnixMilli()).Scan(&conns, &bytesIn, &bytesOut)
	return
}
