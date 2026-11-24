package sqlparse_test

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"ptunnel/internal/sqlparse"
)

func collect(t *testing.T, app string, parts ...[]byte) []sqlparse.Event {
	t.Helper()
	var out []sqlparse.Event
	s := sqlparse.New(app, 4096, func(e sqlparse.Event) { out = append(out, e) })
	if s == nil {
		t.Fatalf("New(%q) 返回 nil", app)
	}
	for _, p := range parts {
		if _, err := s.Write(p); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return out
}

func TestMySQL(t *testing.T) {
	// 握手响应包（32 字节 payload，capability 含 CLIENT_LONG_PASSWORD）
	hs := make([]byte, 4+32)
	hs[0] = 32
	binary.LittleEndian.PutUint32(hs[4:], 0x0000a68d)

	query := []byte("select * from t where id=1")
	pkt := make([]byte, 4+1+len(query))
	pkt[0] = byte(1 + len(query))
	pkt[3] = 1
	pkt[4] = 0x03
	copy(pkt[5:], query)

	// 拆包喂入，验证跨包缓冲
	all := append(hs, pkt...)
	ev := collect(t, "mysql", all[:10], all[10:20], all[20:])
	if len(ev) != 1 || ev[0].Kind != "query" || ev[0].SQL != string(query) {
		t.Fatalf("unexpected events: %+v", ev)
	}
}

func TestMySQLStopsOnSSL(t *testing.T) {
	hs := make([]byte, 4+32)
	hs[0] = 32
	binary.LittleEndian.PutUint32(hs[4:], 0x0000a68d|0x0800) // CLIENT_SSL
	ev := collect(t, "mysql", hs, []byte{0x10, 0, 0, 0, 0x03, 's', 'e', 'l', 'e', 'c', 't'})
	if len(ev) != 0 {
		t.Fatalf("SSL 后不应再解析: %+v", ev)
	}
}

func TestPostgres(t *testing.T) {
	startup := make([]byte, 8)
	binary.BigEndian.PutUint32(startup[0:4], 8)
	binary.BigEndian.PutUint32(startup[4:8], 196608) // 3.0

	sql := "select 1\x00"
	msg := make([]byte, 1+4+len(sql))
	msg[0] = 'Q'
	binary.BigEndian.PutUint32(msg[1:5], uint32(4+len(sql)))
	copy(msg[5:], sql)

	ev := collect(t, "postgresql", append(startup, msg...))
	if len(ev) != 1 || ev[0].SQL != "select 1" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestPostgresSSLRequestStops(t *testing.T) {
	req := make([]byte, 8)
	binary.BigEndian.PutUint32(req[0:4], 8)
	binary.BigEndian.PutUint32(req[4:8], 80877103)
	ev := collect(t, "postgresql", req, []byte("garbage"))
	if len(ev) != 0 {
		t.Fatalf("SSLRequest 后应停止: %+v", ev)
	}
}

func TestRedis(t *testing.T) {
	req := []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n")
	ev := collect(t, "redis", req)
	if len(ev) != 1 || ev[0].SQL != "SET a 1" || ev[0].Kind != "command" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestMongo(t *testing.T) {
	doc := []byte{0, 0, 0, 0, 0x02}
	doc = append(doc, []byte("find\x00")...)
	sl := make([]byte, 4)
	binary.LittleEndian.PutUint32(sl, 6)
	doc = append(doc, sl...)
	doc = append(doc, []byte("users\x00")...)
	doc = append(doc, 0)
	binary.LittleEndian.PutUint32(doc[0:4], uint32(len(doc)))

	body := make([]byte, 5+len(doc))
	body[4] = 0 // kind 0
	copy(body[5:], doc)

	msg := make([]byte, 16+len(body))
	binary.LittleEndian.PutUint32(msg[0:4], uint32(len(msg)))
	binary.LittleEndian.PutUint32(msg[12:16], 2013)
	copy(msg[16:], body)

	ev := collect(t, "mongodb", msg)
	if len(ev) != 1 || ev[0].SQL != "find users" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestSQLServer(t *testing.T) {
	raw := "SELECT * FROM users"
	u := utf16.Encode([]rune(raw))
	payload := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(payload[i*2:], v)
	}
	pkt := make([]byte, 8+len(payload))
	pkt[0] = 0x01
	pkt[1] = 0x01 // EOM
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	copy(pkt[8:], payload)

	ev := collect(t, "sqlserver", pkt)
	if len(ev) != 1 || ev[0].SQL != raw || ev[0].Kind != "batch" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestTextHeuristic(t *testing.T) {
	data := []byte{0x06, 0x00, 0xff, 0x01}
	data = append(data, []byte("select * from dual")...)
	data = append(data, 0x00)
	ev := collect(t, "oracle", data)
	if len(ev) != 1 || ev[0].Kind != "heuristic" || !strings.HasPrefix(ev[0].SQL, "select * from dual") {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestNormalizeApp(t *testing.T) {
	cases := map[string]string{
		"MySQL": sqlparse.AppMySQL, "mariadb": sqlparse.AppMySQL,
		"postgres": sqlparse.AppPostgres, "PostgreSQL": sqlparse.AppPostgres,
		"mssql": sqlparse.AppSQLServer, "DM": sqlparse.AppDM,
		"达梦": sqlparse.AppDM, "mongo": sqlparse.AppMongoDB, "foo": "",
	}
	for in, want := range cases {
		if got := sqlparse.NormalizeApp(in); got != want {
			t.Errorf("NormalizeApp(%q)=%q want %q", in, got, want)
		}
	}
}

// BenchmarkMySQLParse 测量解析单条 COM_QUERY 的成本（不含日志落盘与转发本身）。
func BenchmarkMySQLParse(b *testing.B) {
	hs := make([]byte, 4+32)
	hs[0] = 32
	binary.LittleEndian.PutUint32(hs[4:], 0x0000a68d)

	query := []byte("select id, name, status from orders where customer_id = 12345 and status = 'paid' order by id desc limit 50")
	pkt := make([]byte, 4+1+len(query))
	l := 1 + len(query)
	pkt[0] = byte(l)
	pkt[1] = byte(l >> 8)
	pkt[2] = byte(l >> 16)
	pkt[3] = 2
	pkt[4] = 0x03
	copy(pkt[5:], query)

	var n int
	s := sqlparse.New("mysql", 4096, func(sqlparse.Event) { n++ })
	if _, err := s.Write(hs); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(pkt)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Write(pkt); err != nil {
			b.Fatal(err)
		}
	}
	_ = n
}

// BenchmarkSQLRuleScan 测量按关键字做危险语句判断的额外开销（大小写归一 + 多次包含判断）。
func BenchmarkSQLRuleScan(b *testing.B) {
	sql := "update orders set status = 'paid' where customer_id = 12345 and status = 'new'"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := strings.ToUpper(sql)
		_ = strings.Contains(u, "DROP TABLE") || strings.Contains(u, "TRUNCATE") ||
			strings.Contains(u, "ALTER TABLE") || strings.Contains(u, "DELETE FROM")
	}
}
