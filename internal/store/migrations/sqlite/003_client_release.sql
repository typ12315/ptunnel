-- 客户端升级包：管理台上传，按 os/arch 供在线升级下发
CREATE TABLE IF NOT EXISTS client_releases (
    id          TEXT PRIMARY KEY,
    version     TEXT NOT NULL,
    os          TEXT NOT NULL,
    arch        TEXT NOT NULL,
    filename    TEXT NOT NULL,
    size        INTEGER NOT NULL,
    sha256      TEXT NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    uploaded_at INTEGER NOT NULL,
    UNIQUE (version, os, arch)
);