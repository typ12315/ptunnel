package sqllog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	w, err := NewWriter(t.TempDir(), 30, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func drain(t *testing.T, w *Writer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w.QueueLen() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Flush()
}

func TestWriteAndQuery(t *testing.T) {
	w := newTestWriter(t)
	w.Start()
	defer w.Close()

	now := time.Now()
	for i := 0; i < 5; i++ {
		w.Write(Record{
			Time:    now.Add(time.Duration(i) * time.Second),
			ConnID:  fmt.Sprintf("c%d", i),
			AppType: "mysql",
			SrcIP:   fmt.Sprintf("10.0.0.%d", i),
			SrcMAC:  fmt.Sprintf("AA:BB:CC:00:00:0%d", i),
			SQL:     fmt.Sprintf("select %d from t", i),
		})
	}
	drain(t, w)

	items, total, err := w.List(Filter{Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(items) != 5 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	if items[0].SQL != "select 4 from t" { // 新→旧
		t.Fatalf("排序错误: %+v", items[0])
	}

	items, total, err = w.List(Filter{IP: "10.0.0.3", Size: 10})
	if err != nil || total != 1 || items[0].ConnID != "c3" {
		t.Fatalf("IP 过滤失败: total=%d err=%v items=%+v", total, err, items)
	}

	items, total, _ = w.List(Filter{MAC: "aabbcc000002", Size: 10})
	if total != 1 || items[0].ConnID != "c2" {
		t.Fatalf("MAC 过滤失败: total=%d items=%+v", total, items)
	}

	_, total, _ = w.List(Filter{Keyword: "SELECT 2", Size: 10})
	if total != 1 {
		t.Fatalf("关键字过滤失败: total=%d", total)
	}

	_, total, _ = w.List(Filter{App: "postgresql", Size: 10})
	if total != 0 {
		t.Fatalf("应用过滤失败: total=%d", total)
	}

	items, total, _ = w.List(Filter{Page: 1, Size: 2})
	if total != 5 || len(items) != 2 || items[0].ConnID != "c4" || items[1].ConnID != "c3" {
		t.Fatalf("分页失败: total=%d items=%+v", total, items)
	}
}

func TestGzipAndDates(t *testing.T) {
	w := newTestWriter(t)
	day := "2020-01-02"
	path := filepath.Join(w.Dir(), "sql-"+day+".log")
	body := `{"time":"2020-01-02T10:00:00+08:00","src_ip":"192.168.1.5","src_mac":"aa:bb:cc:dd:ee:ff","app_type":"redis","sql":"GET k"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	w.compressDay(day)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("压缩后原文件应被删除")
	}
	if _, err := os.Stat(path + ".gz"); err != nil {
		t.Fatalf("未生成 gz: %v", err)
	}
	items, total, err := w.List(Filter{Date: day, Size: 10})
	if err != nil || total != 1 || items[0].SQL != "GET k" {
		t.Fatalf("gz 查询失败: total=%d err=%v items=%+v", total, err, items)
	}
	dates := w.Dates()
	if len(dates) != 1 || dates[0] != day {
		t.Fatalf("日期列表错误: %+v", dates)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, "sql-"+old+".log.gz"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	w.prune()
	if _, err := os.Stat(filepath.Join(dir, "sql-"+old+".log.gz")); !os.IsNotExist(err) {
		t.Fatalf("过期日志未清理")
	}
}
