package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(Options{SQLitePath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenDriverDispatch(t *testing.T) {
	if _, err := Open(Options{Driver: "postgres"}); err == nil {
		t.Fatal("不支持的驱动应报错")
	}
	if _, err := Open(Options{Driver: "mysql"}); err == nil {
		t.Fatal("MySQL 缺少 DSN 应报错")
	}
	st := newTestStore(t)
	if st.Driver() != "sqlite" {
		t.Fatalf("默认驱动应为 sqlite: %s", st.Driver())
	}
}

// TestMigrationsSplit 校验两套方言的迁移脚本都能被正确拆分（MySQL DDL 逐条执行依赖此逻辑）。
func TestMigrationsSplit(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		entries, err := migrationsFS.ReadDir("migrations/" + driver)
		if err != nil {
			t.Fatalf("%s 迁移目录读取失败: %v", driver, err)
		}
		for _, e := range entries {
			body, err := migrationsFS.ReadFile("migrations/" + driver + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			stmts, err := splitStatements(string(body))
			if err != nil {
				t.Fatalf("%s/%s 拆分失败: %v", driver, e.Name(), err)
			}
			if len(stmts) == 0 {
				t.Fatalf("%s/%s 未拆出任何语句", driver, e.Name())
			}
		}
	}
}

func TestClientLifecycle(t *testing.T) {
	st := newTestStore(t)
	expire := time.Now().Add(time.Minute)
	c := &Client{
		ID: "c1", Name: "web01", Enabled: true, Status: "offline",
		TokenHash: "hash1", TokenExpire: &expire,
	}
	if err := st.CreateClient(c); err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	got, err := st.GetClient("c1")
	if err != nil || got == nil {
		t.Fatalf("查询客户端失败: %v", err)
	}
	if !got.Enabled || got.TokenHash != "hash1" {
		t.Fatalf("客户端字段异常: %+v", got)
	}

	byToken, err := st.FindClientByTokenHash("hash1")
	if err != nil || byToken == nil || byToken.ID != "c1" {
		t.Fatalf("按令牌查询失败: %v", err)
	}

	if err := st.SetClientCert("c1", "ABC123", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	bySerial, err := st.GetClientByCertSerial("ABC123")
	if err != nil || bySerial == nil || bySerial.ID != "c1" {
		t.Fatalf("按序列号查询失败: %v", err)
	}

	// 清除令牌后 token_hash 为 NULL，不应导致扫描错误
	if err := st.ClearClientToken("c1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetClient("c1")
	if err != nil || got == nil || got.TokenHash != "" {
		t.Fatalf("清除令牌后读取失败: %v got=%+v", err, got)
	}
	if _, err := st.ListClients(); err != nil {
		t.Fatalf("列出客户端失败（NULL 字段扫描问题）: %v", err)
	}

	if err := st.RevokeClientCert("c1"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetClient("c1")
	if got.RevokedAt == nil {
		t.Fatal("吊销标记未写入")
	}
}

func TestProxyAndRunnable(t *testing.T) {
	st := newTestStore(t)
	c := &Client{ID: "c1", Name: "web01", Enabled: true, Status: "online"}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{
		ID: "p1", ClientID: "c1", Name: "mysql", Protocol: "tcp",
		BindAddr: "0.0.0.0", RemotePort: 23306, TargetAddr: "10.1.2.3:3306", AppType: "mysql",
		Enabled: true, AllowCIDRs: []string{"10.0.0.0/8"}, Approval: "approved", State: "idle",
	}
	if err := st.CreateProxy(p); err != nil {
		t.Fatalf("创建代理失败: %v", err)
	}
	conflict, err := st.ProxyConflict("0.0.0.0", "tcp", 23306, "")
	if err != nil || conflict != "mysql" {
		t.Fatalf("冲突检测异常: %q %v", conflict, err)
	}
	if conflict, _ := st.ProxyConflict("0.0.0.0", "tcp", 23306, "p1"); conflict != "" {
		t.Fatal("排除自身时不应冲突")
	}

	list, err := st.ListRunnable()
	if err != nil || len(list) != 1 {
		t.Fatalf("应返回 1 条可运行代理: %v %d", err, len(list))
	}
	if len(list[0].AllowCIDRs) != 1 || list[0].AllowCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("白名单反序列化失败: %+v", list[0].AllowCIDRs)
	}
	if list[0].AppType != "mysql" {
		t.Fatalf("应用类型未持久化: %q", list[0].AppType)
	}
	got, err := st.GetProxy("p1")
	if err != nil || got == nil || got.AppType != "mysql" {
		t.Fatalf("查询代理应用类型失败: %v %+v", err, got)
	}

	// 禁用客户端的代理不再监听
	if err := st.UpdateClient("c1", "web01", "", false); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListRunnable()
	if len(list) != 0 {
		t.Fatal("客户端禁用后不应返回可运行代理")
	}
}

func TestConnLogFlow(t *testing.T) {
	st := newTestStore(t)
	start := time.Now().Add(-time.Minute)
	logs := []*ConnLog{{
		ConnID: "conn-1", ClientID: "c1", ClientName: "web01", ProxyID: "p1", ProxyName: "mysql",
		Protocol: "tcp", ListenAddr: "0.0.0.0:23306", RemotePort: 23306, TargetAddr: "10.1.2.3:3306",
		SrcIP: "203.0.113.5", SrcPort: 5555, StartedAt: start,
	}}
	if err := st.InsertConnLogs(logs); err != nil {
		t.Fatalf("写入连接审计失败: %v", err)
	}
	active, err := st.ActiveConnLogs(10)
	if err != nil || len(active) != 1 {
		t.Fatalf("活跃连接查询失败: %v %d", err, len(active))
	}

	end := start.Add(2 * time.Second)
	if err := st.FinishConnLogs([]*ConnEnd{{
		ConnID: "conn-1", EndedAt: end, DurationMS: 2000, BytesIn: 100, BytesOut: 2000, CloseReason: "eof",
	}}); err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	if err := st.UpdateConnLogMAC("conn-1", "AA:BB:CC:DD:EE:FF", "arp"); err != nil {
		t.Fatal(err)
	}

	items, total, err := st.ListConnLogs(ConnFilter{Page: 1, Size: 10})
	if err != nil || total != 1 {
		t.Fatalf("列表查询失败: %v total=%d", err, total)
	}
	it := items[0]
	if it.DurationMS != 2000 || it.BytesIn != 100 || it.BytesOut != 2000 || it.CloseReason != "eof" {
		t.Fatalf("回填字段不符: %+v", it)
	}
	if it.SrcMAC != "AA:BB:CC:DD:EE:FF" || it.MACSource != "arp" {
		t.Fatalf("MAC 字段不符: %+v", it)
	}
	if it.EndedAt == nil {
		t.Fatal("结束时间未写入")
	}

	// 条件筛选
	if _, n, _ := st.ListConnLogs(ConnFilter{SrcIP: "203.0.113.5", Page: 1, Size: 10}); n != 1 {
		t.Fatalf("按来源 IP 筛选失败: %d", n)
	}
	if _, n, _ := st.ListConnLogs(ConnFilter{SrcIP: "198.51.100.1", Page: 1, Size: 10}); n != 0 {
		t.Fatalf("不存在的 IP 不应命中: %d", n)
	}
	if _, n, _ := st.ListConnLogs(ConnFilter{From: &end, Page: 1, Size: 10}); n != 0 {
		t.Fatalf("时间筛选异常: %d", n)
	}

	conns, in, out, err := st.SumSince(start.Add(-time.Second))
	if err != nil || conns != 1 || in != 100 || out != 2000 {
		t.Fatalf("汇总异常: %v %d %d %d", err, conns, in, out)
	}

	pts, err := st.TrafficSeries("", start.Add(-time.Hour), time.Now(), 5*time.Minute)
	if err != nil || len(pts) == 0 {
		t.Fatalf("流量曲线查询失败: %v %d", err, len(pts))
	}

	if _, err := st.DeleteConnLogsBefore(time.Now().Add(time.Hour), 100); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, n, _ := st.ListConnLogs(ConnFilter{Page: 1, Size: 10}); n != 0 {
		t.Fatalf("清理后应为空: %d", n)
	}
}

func TestMarkUncleanAndSettings(t *testing.T) {
	st := newTestStore(t)
	if err := st.InsertConnLogs([]*ConnLog{{ConnID: "x", ClientID: "c", ClientName: "c", ProxyID: "p",
		ProxyName: "p", Protocol: "tcp", ListenAddr: "0.0.0.0:20000", RemotePort: 20000,
		TargetAddr: "127.0.0.1:1", SrcIP: "127.0.0.1", SrcPort: 1, StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	n, err := st.MarkUnclean()
	if err != nil || n != 1 {
		t.Fatalf("收尾悬挂记录失败: %v %d", err, n)
	}
	items, _, _ := st.ListConnLogs(ConnFilter{Page: 1, Size: 5})
	if items[0].CloseReason != "unclean" {
		t.Fatalf("收尾原因不符: %s", items[0].CloseReason)
	}

	if err := st.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if v := st.GetSetting("k", "def"); v != "v1" {
		t.Fatalf("设置读取失败: %s", v)
	}
	if err := st.SetSetting("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v := st.GetSetting("k", ""); v != "v2" {
		t.Fatalf("设置覆盖失败: %s", v)
	}
}
