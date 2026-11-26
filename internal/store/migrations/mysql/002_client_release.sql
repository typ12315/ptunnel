CREATE TABLE IF NOT EXISTS client_releases (
    id          VARCHAR(36)  NOT NULL,
    version     VARCHAR(32)  NOT NULL,
    os          VARCHAR(16)  NOT NULL,
    arch        VARCHAR(16)  NOT NULL,
    filename    VARCHAR(255) NOT NULL,
    size        BIGINT       NOT NULL,
    sha256      CHAR(64)     NOT NULL,
    note        VARCHAR(512) NOT NULL DEFAULT '',
    uploaded_at BIGINT       NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_release_version (version, os, arch)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;