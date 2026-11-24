-- PortTunnel 初始结构
-- 时间统一使用 Unix 毫秒（INTEGER），便于范围查询与跨库迁移。
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

CREATE TABLE IF NOT EXISTS clients (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    remark         TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    status         TEXT NOT NULL DEFAULT 'offline',
    token_hash     TEXT,
    token_expire   INTEGER,
    cert_serial    TEXT,
    cert_not_after INTEGER,
    revoked_at     INTEGER,
    version        TEXT NOT NULL DEFAULT '',
    os             TEXT NOT NULL DEFAULT '',
    arch           TEXT NOT NULL DEFAULT '',
    remote_addr    TEXT NOT NULL DEFAULT '',
    last_seen_at   INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS proxies (
    id            TEXT PRIMARY KEY,
    client_id     TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    protocol      TEXT NOT NULL DEFAULT 'tcp',
    bind_addr     TEXT NOT NULL DEFAULT '0.0.0.0',
    remote_port   INTEGER NOT NULL,
    target_addr   TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    allow_cidrs   TEXT NOT NULL DEFAULT '[]',
    max_conns     INTEGER NOT NULL DEFAULT 0,
    rate_limit_mb INTEGER NOT NULL DEFAULT 0,
    origin        TEXT NOT NULL DEFAULT 'server',
    approval      TEXT NOT NULL DEFAULT 'approved',
    health_check  INTEGER NOT NULL DEFAULT 0,
    state         TEXT NOT NULL DEFAULT 'idle',
    state_msg     TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE(bind_addr, protocol, remote_port)
);
CREATE INDEX IF NOT EXISTS idx_proxies_client ON proxies(client_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER,
    actor      TEXT,
    ip         TEXT,
    action     TEXT NOT NULL,
    entity     TEXT,
    entity_id  TEXT,
    detail     TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);

-- 连接审计（访问日志）
CREATE TABLE IF NOT EXISTS conn_logs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    conn_id      TEXT NOT NULL UNIQUE,
    client_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL,
    proxy_id     TEXT NOT NULL,
    proxy_name   TEXT NOT NULL,
    protocol     TEXT NOT NULL,
    listen_addr  TEXT NOT NULL,
    remote_port  INTEGER NOT NULL,
    target_addr  TEXT NOT NULL,
    src_ip       TEXT NOT NULL,
    src_port     INTEGER NOT NULL,
    src_mac      TEXT,
    mac_source   TEXT NOT NULL DEFAULT 'none',
    started_at   INTEGER NOT NULL,
    ended_at     INTEGER,
    duration_ms  INTEGER,
    bytes_in     INTEGER NOT NULL DEFAULT 0,
    bytes_out    INTEGER NOT NULL DEFAULT 0,
    close_reason TEXT,
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conn_started ON conn_logs(started_at);
CREATE INDEX IF NOT EXISTS idx_conn_client  ON conn_logs(client_id, started_at);
CREATE INDEX IF NOT EXISTS idx_conn_port    ON conn_logs(remote_port, started_at);
CREATE INDEX IF NOT EXISTS idx_conn_src     ON conn_logs(src_ip, started_at);
CREATE INDEX IF NOT EXISTS idx_conn_active  ON conn_logs(ended_at) WHERE ended_at IS NULL;

CREATE TABLE IF NOT EXISTS settings (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);