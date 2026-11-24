-- 为代理绑定数据库/中间件应用类型，用于解析并审计 SQL 语句（空表示普通 TCP 转发）
ALTER TABLE proxies ADD COLUMN app_type TEXT NOT NULL DEFAULT '';