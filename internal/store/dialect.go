package store

// dialect 隔离 SQLite 与 MySQL 在 DDL / Upsert / 批量删除上的差异。
type dialect interface {
	name() string
	migrationsTableDDL() string
	// txDDL SQLite 支持 DDL 事务；MySQL 会隐式提交，不能包事务。
	txDDL() bool
	upsertSettingSQL() string
	deleteConnLogsBeforeSQL() string
	// trafficBucketExpr 把毫秒时间戳按秒桶聚合（两个占位参数：桶秒数、桶秒数）。
	trafficBucketExpr() string
}

type sqliteDialect struct{}

func (sqliteDialect) name() string { return "sqlite" }

func (sqliteDialect) migrationsTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`
}

func (sqliteDialect) txDDL() bool { return true }

func (sqliteDialect) upsertSettingSQL() string {
	return `INSERT INTO settings(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`
}

func (sqliteDialect) deleteConnLogsBeforeSQL() string {
	return `DELETE FROM conn_logs WHERE id IN
		(SELECT id FROM (SELECT id FROM conn_logs WHERE started_at < ? ORDER BY started_at LIMIT ?) t)`
}

func (sqliteDialect) trafficBucketExpr() string {
	return `(started_at / 1000 / ?) * ?`
}

type mysqlDialect struct{}

func (mysqlDialect) name() string { return "mysql" }

func (mysqlDialect) migrationsTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS schema_migrations (
		name VARCHAR(191) PRIMARY KEY,
		applied_at BIGINT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
}

func (mysqlDialect) txDDL() bool { return false }

func (mysqlDialect) upsertSettingSQL() string {
	return `INSERT INTO settings(k, v) VALUES (?, ?) ON DUPLICATE KEY UPDATE v = VALUES(v)`
}

func (mysqlDialect) deleteConnLogsBeforeSQL() string {
	return `DELETE FROM conn_logs WHERE id IN
		(SELECT id FROM (SELECT id FROM conn_logs WHERE started_at < ? ORDER BY started_at LIMIT ?) t)`
}

// MySQL 的 / 返回 DECIMAL，用 DIV 保证整数桶。
func (mysqlDialect) trafficBucketExpr() string {
	return `(started_at DIV 1000 DIV ?) * ?`
}
