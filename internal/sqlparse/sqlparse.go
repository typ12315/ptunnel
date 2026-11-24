// Package sqlparse 从数据库/中间件客户端的上行字节流中提取 SQL 语句或命令，用于审计。
// 仅能解析明文协议；客户端启用 TLS/SSL 加密后无法看到语句（解析器会静默停止）。
package sqlparse

import (
	"strings"
	"unicode/utf8"
)

// 应用类型取值（写入数据库的规范值）。
const (
	AppMySQL     = "mysql"
	AppPostgres  = "postgresql"
	AppSQLServer = "sqlserver"
	AppOracle    = "oracle"
	AppDM        = "dm"
	AppRedis     = "redis"
	AppMongoDB   = "mongodb"
)

// Apps 支持的应用类型，供校验与前端下拉使用。
var Apps = []string{AppMySQL, AppPostgres, AppSQLServer, AppOracle, AppDM, AppRedis, AppMongoDB}

// NormalizeApp 归一化应用类型，非法或空值返回空字符串。
func NormalizeApp(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "mysql", "mariadb":
		return AppMySQL
	case "postgres", "postgresql", "pgsql":
		return AppPostgres
	case "sqlserver", "mssql", "tds":
		return AppSQLServer
	case "oracle", "ora":
		return AppOracle
	case "dm", "dameng", "达梦":
		return AppDM
	case "redis":
		return AppRedis
	case "mongo", "mongodb":
		return AppMongoDB
	}
	return ""
}

// Event 一条提取到的语句。
type Event struct {
	App  string // 应用类型
	Kind string // query / prepare / command / batch / heuristic
	SQL  string
}

type parser interface {
	// feed 消费一段上行字节，遇到完整语句时回调 emit(kind, sql)。
	// parser 内部需自行缓冲跨包数据；解析失败时丢弃并停止，绝不影响转发。
	feed(p []byte, max int, emit func(kind, sql string))
}

// Sniffer 把上行字节流喂给对应协议的解析器，实现 io.Writer。
type Sniffer struct {
	app    string
	max    int
	emit   func(Event)
	parser parser
}

// New 创建提取器；app 为空或不支持时返回 nil（表示不审计语句）。
func New(app string, maxLen int, emit func(Event)) *Sniffer {
	app = NormalizeApp(app)
	if app == "" || emit == nil {
		return nil
	}
	if maxLen <= 0 {
		maxLen = 4096
	}
	s := &Sniffer{app: app, max: maxLen, emit: emit}
	switch app {
	case AppMySQL:
		s.parser = &mysqlParser{}
	case AppPostgres:
		s.parser = &pgParser{}
	case AppRedis:
		s.parser = &redisParser{}
	case AppMongoDB:
		s.parser = &mongoParser{}
	case AppSQLServer:
		s.parser = &tdsParser{}
	default: // Oracle / 达梦：TNS 私有编码，使用关键字启发式提取
		s.parser = &textParser{}
	}
	return s
}

// Write 实现 io.Writer；无论解析是否成功都返回 len(p)，保证转发不受影响。
func (s *Sniffer) Write(p []byte) (n int, err error) {
	n = len(p)
	defer func() {
		// 解析器绝不能把转发链路打挂
		_ = recover()
	}()
	s.parser.feed(p, s.max, s.deliver)
	return n, nil
}

func (s *Sniffer) deliver(kind, sql string) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return
	}
	s.emit(Event{App: s.app, Kind: kind, SQL: sql})
}

// cleanSQL 去掉 NUL 与首尾空白，并按字符数截断（避免切断 UTF-8）。
func cleanSQL(b []byte, max int) string {
	s := strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + " …(截断)"
}

func le16(b []byte) int { return int(b[0]) | int(b[1])<<8 }
func le32(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }
func be32(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }

// asciiLower 仅将 ASCII 字母转小写（DM 使用 GBK，不能整体按 UTF-8 处理）。
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}
