// Package store 提供 SQLite 存储与各实体的数据访问。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationsFS embed.FS

// Options 数据库连接参数（driver 为空按 sqlite 处理）。
type Options struct {
	Driver          string // sqlite / mysql
	SQLitePath      string
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Store 封装数据库连接。
type Store struct {
	db *sql.DB
	d  dialect
}

// Open 按驱动打开数据库并执行对应方言的迁移。
func Open(opts Options) (*Store, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Driver)) {
	case "", "sqlite", "local":
		return openSQLite(opts)
	case "mysql":
		return openMySQL(opts)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s（可选 sqlite / mysql）", opts.Driver)
	}
}

func openSQLite(opts Options) (*Store, error) {
	path := opts.SQLitePath
	if path == "" {
		return nil, fmt.Errorf("sqlite 数据库路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	return finishOpen(db, sqliteDialect{})
}

func openMySQL(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.DSN) == "" {
		return nil, fmt.Errorf("storage.mysql.dsn 不能为空")
	}
	db, err := sql.Open("mysql", opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}
	maxOpen, maxIdle := opts.MaxOpenConns, opts.MaxIdleConns
	if maxOpen <= 0 {
		maxOpen = 20
	}
	if maxIdle <= 0 {
		maxIdle = 10
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	lifetime := opts.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = 30 * time.Minute
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)
	return finishOpen(db, mysqlDialect{})
}

func finishOpen(db *sql.DB, d dialect) (*Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("数据库连接失败(%s): %w", d.name(), err)
	}
	s := &Store{db: db, d: d}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接（供健康检查等使用）。
func (s *Store) DB() *sql.DB { return s.db }

// Driver 返回当前驱动名。
func (s *Store) Driver() string { return s.d.name() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(s.d.migrationsTableDDL()); err != nil {
		return fmt.Errorf("初始化迁移表失败: %w", err)
	}
	dir := "migrations/" + s.d.name()
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		if err := s.applyMigration(name, string(body)); err != nil {
			return fmt.Errorf("执行迁移 %s 失败: %w", name, err)
		}
	}
	return nil
}

func (s *Store) applyMigration(name, body string) error {
	stmts, err := splitStatements(body)
	if err != nil {
		return err
	}
	if !s.d.txDDL() {
		// MySQL 的 DDL 会隐式提交，不能用事务包裹
		for _, stmt := range stmts {
			if _, err := s.db.Exec(stmt); err != nil {
				return err
			}
		}
		_, err := s.db.Exec(`INSERT INTO schema_migrations(name, applied_at) VALUES (?, ?)`, name, nowMS())
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(name, applied_at) VALUES (?, ?)`, name, nowMS()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// splitStatements 按分号拆分 SQL，忽略 -- 行注释、/* */ 块注释与引号内的分号。
func splitStatements(body string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote byte
	inLine, inBlock := false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inLine {
			if c == '\n' {
				inLine = false
				cur.WriteByte(c)
			}
			continue
		}
		if inBlock {
			if c == '*' && i+1 < len(body) && body[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '-' && i+1 < len(body) && body[i+1] == '-':
			inLine = true
			i++
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			inBlock = true
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
			cur.WriteByte(c)
		case c == ';':
			if stmt := strings.TrimSpace(cur.String()); stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if quote != 0 || inBlock {
		return nil, fmt.Errorf("SQL 未闭合的引号或注释")
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out, nil
}

func nowMS() int64 { return time.Now().UnixMilli() }

// TimePtr 把毫秒时间戳转为 *time.Time（空值返回 nil）。
func TimePtr(v sql.NullInt64) *time.Time {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	t := time.UnixMilli(v.Int64)
	return &t
}

// Millis 把 *time.Time 转为可写入的毫秒值（nil 写 NULL）。
func Millis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

// ---- 用户 ----

// User 管理员账号。
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
}

// GetUserByName 按用户名查询。
func (s *Store) GetUserByName(username string) (*User, error) {
	var u User
	var created, last sql.NullInt64
	err := s.db.QueryRow(`SELECT id, username, password_hash, role, created_at, last_login_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created, &last)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(created.Int64)
	u.LastLoginAt = TimePtr(last)
	return &u, nil
}

// CreateUser 新建用户。
func (s *Store) CreateUser(username, passwordHash, role string) error {
	_, err := s.db.Exec(`INSERT INTO users(username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, role, nowMS())
	return err
}

// UpdateUserPassword 更新口令。
func (s *Store) UpdateUserPassword(username, passwordHash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	return err
}

// TouchUserLogin 记录最近登录时间。
func (s *Store) TouchUserLogin(id int64) {
	_, _ = s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, nowMS(), id)
}

// CountUsers 返回用户数量。
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// ---- 设置 ----

// GetSetting 读取设置项。
func (s *Store) GetSetting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SetSetting 写入设置项。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(s.d.upsertSettingSQL(), key, value)
	return err
}

// AllSettings 返回全部设置。
func (s *Store) AllSettings() map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT k, v FROM settings`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}
