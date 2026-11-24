package sqllog

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ptunnel/internal/sqlparse"
)

// Filter SQL 审计查询条件。
type Filter struct {
	Date    string // YYYY-MM-DD，空表示今天
	IP      string // 来源 IP（支持部分匹配）
	MAC     string // 来源 MAC（忽略分隔符，大小写不敏感）
	App     string // 应用类型（精确，空表示全部）
	Keyword string // SQL 关键字（不区分大小写）
	Page    int
	Size    int
}

// Today 返回本地日期字符串。
func (w *Writer) Today() string { return time.Now().Format("2006-01-02") }

// Dates 返回有日志的日期列表（新→旧）。
func (w *Writer) Dates() []string {
	days := w.days()
	out := make([]string, 0, len(days))
	for i := len(days) - 1; i >= 0; i-- {
		out = append(out, days[i])
	}
	return out
}

// List 查询某天的 SQL 审计，返回当页记录（新→旧）与匹配总数。
func (w *Writer) List(f Filter) ([]Record, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 || f.Size > 200 {
		f.Size = 50
	}
	day := strings.TrimSpace(f.Date)
	if day == "" {
		day = w.Today()
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return nil, 0, err
	}

	// 第一遍：统计匹配总数
	total := 0
	err := w.scanDay(day, func(r Record) bool {
		if !match(r, f) {
			return true
		}
		total++
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []Record{}, 0, nil
	}

	// 文件按时间升序追加；分页从最新（文件末尾）往前取
	offset := (f.Page - 1) * f.Size
	start := total - offset - f.Size
	if start < 0 {
		start = 0
	}
	end := total - offset
	if end <= 0 {
		return []Record{}, total, nil
	}

	var page []Record
	idx := 0
	err = w.scanDay(day, func(r Record) bool {
		if !match(r, f) {
			return true
		}
		if idx >= start && idx < end {
			page = append(page, r)
		}
		idx++
		return idx < end
	})
	if err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
		page[i], page[j] = page[j], page[i]
	}
	return page, total, nil
}

// scanDay 逐行扫描某天日志；fn 返回 false 时提前结束。
func (w *Writer) scanDay(day string, fn func(Record) bool) error {
	rc, err := openDay(w.dir, day)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if !fn(r) {
			return nil
		}
	}
	return sc.Err()
}

func openDay(dir, day string) (io.ReadCloser, error) {
	plain := filepath.Join(dir, "sql-"+day+".log")
	if f, err := os.Open(plain); err == nil {
		return f, nil
	}
	f, err := os.Open(plain + ".gz")
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &gzFile{gz: gz, f: f}, nil
}

type gzFile struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzFile) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzFile) Close() error {
	_ = g.gz.Close()
	return g.f.Close()
}

func match(r Record, f Filter) bool {
	if ip := strings.TrimSpace(f.IP); ip != "" && !strings.Contains(r.SrcIP, ip) {
		return false
	}
	if mac := normMAC(f.MAC); mac != "" && !strings.Contains(normMAC(r.SrcMAC), mac) {
		return false
	}
	if app := strings.TrimSpace(f.App); app != "" {
		if want := sqlparse.NormalizeApp(app); want != "" {
			if r.AppType != want {
				return false
			}
		} else if !strings.EqualFold(r.AppType, app) {
			return false
		}
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" && !strings.Contains(strings.ToLower(r.SQL), strings.ToLower(kw)) {
		return false
	}
	return true
}

func normMAC(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	r := strings.NewReplacer(":", "", "-", "", ".", "", " ", "")
	return r.Replace(v)
}
