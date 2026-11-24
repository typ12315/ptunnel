-- PortTunnel 初始结构（MySQL 8.0 / 5.7，InnoDB + utf8mb4）
-- 时间统一使用 Unix 毫秒（BIGINT）；布尔使用 TINYINT(1)；文件与 SQLite schema 保持字段对齐。
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT       NOT NULL AUTO_INCREMENT,
    username      VARCHAR(64)  NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role          VARCHAR(32)  NOT NULL DEFAULT 'admin',
    created_at    BIGINT       NOT NULL,
    last_login_at BIGINT       NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS clients (
    id             VARCHAR(36)  NOT NULL,
    name           VARCHAR(128) NOT NULL,
    remark         VARCHAR(512) NOT NULL DEFAULT '',
    enabled        TINYINT(1)   NOT NULL DEFAULT 1,
    status         VARCHAR(16)  NOT NULL DEFAULT 'offline',
    token_hash     VARCHAR(64)  NULL,
    token_expire   BIGINT       NULL,
    cert_serial    VARCHAR(64)  NULL,
    cert_not_after BIGINT       NULL,
    revoked_at     BIGINT       NULL,
    version        VARCHAR(32)  NOT NULL DEFAULT '',
    os             VARCHAR(32)  NOT NULL DEFAULT '',
    arch           VARCHAR(32)  NOT NULL DEFAULT '',
    remote_addr    VARCHAR(128) NOT NULL DEFAULT '',
    last_seen_at   BIGINT       NULL,
    created_at     BIGINT       NOT NULL,
    updated_at     BIGINT       NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_clients_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS proxies (
    id            VARCHAR(36)  NOT NULL,
    client_id     VARCHAR(36)  NOT NULL,
    name          VARCHAR(128) NOT NULL,
    protocol      VARCHAR(16)  NOT NULL DEFAULT 'tcp',
    bind_addr     VARCHAR(64)  NOT NULL DEFAULT '0.0.0.0',
    remote_port   INT          NOT NULL,
    target_addr   VARCHAR(255) NOT NULL,
    app_type      VARCHAR(16)  NOT NULL DEFAULT '',
    enabled       TINYINT(1)   NOT NULL DEFAULT 1,
    allow_cidrs   TEXT         NOT NULL,
    max_conns     INT          NOT NULL DEFAULT 0,
    rate_limit_mb INT          NOT NULL DEFAULT 0,
    origin        VARCHAR(16)  NOT NULL DEFAULT 'server',
    approval      VARCHAR(16)  NOT NULL DEFAULT 'approved',
    health_check  TINYINT(1)   NOT NULL DEFAULT 0,
    state         VARCHAR(16)  NOT NULL DEFAULT 'idle',
    state_msg     VARCHAR(512) NOT NULL DEFAULT '',
    created_at    BIGINT       NOT NULL,
    updated_at    BIGINT       NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_proxies_bind (bind_addr, protocol, remote_port),
    KEY idx_proxies_client (client_id),
    CONSTRAINT fk_proxies_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS audit_logs (
    id         BIGINT       NOT NULL AUTO_INCREMENT,
    user_id    BIGINT       NULL,
    actor      VARCHAR(64)  NULL,
    ip         VARCHAR(64)  NULL,
    action     VARCHAR(64)  NOT NULL,
    entity     VARCHAR(32)  NULL,
    entity_id  VARCHAR(64)  NULL,
    detail     TEXT         NULL,
    created_at BIGINT       NOT NULL,
    PRIMARY KEY (id),
    KEY idx_audit_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS conn_logs (
    id           BIGINT       NOT NULL AUTO_INCREMENT,
    conn_id      VARCHAR(36)  NOT NULL,
    client_id    VARCHAR(36)  NOT NULL,
    client_name  VARCHAR(128) NOT NULL,
    proxy_id     VARCHAR(36)  NOT NULL,
    proxy_name   VARCHAR(128) NOT NULL,
    protocol     VARCHAR(16)  NOT NULL,
    listen_addr  VARCHAR(64)  NOT NULL,
    remote_port  INT          NOT NULL,
    target_addr  VARCHAR(255) NOT NULL,
    src_ip       VARCHAR(64)  NOT NULL,
    src_port     INT          NOT NULL,
    src_mac      VARCHAR(32)  NULL,
    mac_source   VARCHAR(16)  NOT NULL DEFAULT 'none',
    started_at   BIGINT       NOT NULL,
    ended_at     BIGINT       NULL,
    duration_ms  BIGINT       NULL,
    bytes_in     BIGINT       NOT NULL DEFAULT 0,
    bytes_out    BIGINT       NOT NULL DEFAULT 0,
    close_reason VARCHAR(32)  NULL,
    created_at   BIGINT       NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_conn_logs_conn (conn_id),
    KEY idx_conn_started (started_at),
    KEY idx_conn_client (client_id, started_at),
    KEY idx_conn_port (remote_port, started_at),
    KEY idx_conn_src (src_ip, started_at),
    KEY idx_conn_active (ended_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS settings (
    k VARCHAR(64) NOT NULL,
    v TEXT        NOT NULL,
    PRIMARY KEY (k)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;