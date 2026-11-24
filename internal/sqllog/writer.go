// Package sqllog 把 SQL/命令审计写入本地文件：按天分割、过期自动 gzip 压缩、支持条件查询。
package sqllog

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Record 一条 SQL/命令审计记录（JSON Lines 存盘）。
type Record struct {
	Time       time.Time `json:"time"`
	ConnID     string    `json:"conn_id"`
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	ProxyID    string    `json:"proxy_id"`
	ProxyName  string    `json:"proxy_name"`
	AppType    string    `json:"app_type"`
	SrcIP      string    `json:"src_ip"`
	SrcPort    int       `json:"src_port"`
	SrcMAC     string    `json:"src_mac"`
	TargetAddr string    `json:"target_addr"`
	Kind       string    `json:"kind"`
	SQL        string    `json:"sql"`
}

// Writer 异步写入 SQL 审计日志；队列满时丢弃并计数，不阻塞转发。
type Writer struct {
	dir       string
	retention int
	log       *slog.Logger
	ch        chan Record
	stop      chan struct{}
	once      sync.Once
	dropped   atomic.Int64

	mu   sync.Mutex
	file *os.File
	bw   *bufio.Writer
	day  string
}

// NewWriter 创建写入器并确保目录存在。
func NewWriter(dir string, retentionDays int, logger *slog.Logger) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建 SQL 日志目录失败: %w", err)
	}
	return &Writer{
		dir:       dir,
		retention: retentionDays,
		log:       logger,
		ch:        make(chan Record, 5000),
		stop:      make(chan struct{}),
	}, nil
}

// Dir 返回日志目录。
func (w *Writer) Dir() string { return w.dir }

// Dropped 返回因队列满丢弃的记录数。
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// QueueLen 返回当前积压。
func (w *Writer) QueueLen() int { return len(w.ch) }

// Write 异步记录一条语句。
func (w *Writer) Write(r Record) {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

// Start 启动后台写协程，并在后台压缩历史日志。
func (w *Writer) Start() {
	go w.loop()
	go w.compressOld()
	go w.runRetention()
}

// Close 停止写入协程并落盘。
func (w *Writer) Close() {
	w.once.Do(func() { close(w.stop) })
}

// Flush 立即把缓冲刷到磁盘（查询前调用，保证能看到最新语句）。
func (w *Writer) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bw != nil {
		_ = w.bw.Flush()
	}
}

func (w *Writer) loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			for {
				select {
				case r := <-w.ch:
					w.append(r)
				default:
					w.mu.Lock()
					if w.bw != nil {
						_ = w.bw.Flush()
					}
					if w.file != nil {
						_ = w.file.Close()
						w.file = nil
						w.bw = nil
					}
					w.mu.Unlock()
					return
				}
			}
		case r := <-w.ch:
			w.append(r)
		case <-ticker.C:
			w.Flush()
		}
	}
}

func (w *Writer) append(r Record) {
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	day := r.Time.Format("2006-01-02")
	if w.file == nil || w.day != day {
		w.rotateLocked(day)
	}
	if w.bw == nil {
		return
	}
	if _, err := w.bw.Write(append(line, '\n')); err != nil {
		w.log.Warn("写入 SQL 审计失败", "err", err)
	}
}

// rotateLocked 关闭旧文件并打开新一天的文件（调用方需持有 mu）。
func (w *Writer) rotateLocked(day string) {
	if w.bw != nil {
		_ = w.bw.Flush()
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
		w.bw = nil
	}
	oldDay := w.day
	path := filepath.Join(w.dir, "sql-"+day+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		w.log.Warn("打开 SQL 审计文件失败", "err", err, "path", path)
		w.day = day
		return
	}
	w.file = f
	w.bw = bufio.NewWriterSize(f, 64*1024)
	w.day = day
	if oldDay != "" && oldDay != day {
		go w.compressDay(oldDay)
	}
}

// compressDay 把某天的 .log 压缩为 .log.gz。
func (w *Writer) compressDay(day string) {
	src := filepath.Join(w.dir, "sql-"+day+".log")
	dst := src + ".gz"
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, dst)
	_ = in.Close()
	_ = os.Remove(src)
}

// compressOld 启动时压缩所有非当天的 .log 文件。
func (w *Writer) compressOld() {
	today := time.Now().Format("2006-01-02")
	for _, d := range w.days() {
		if d != today {
			w.compressDay(d)
		}
	}
}

func (w *Writer) runRetention() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	w.prune()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.prune()
		}
	}
}

func (w *Writer) prune() {
	if w.retention <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.retention).Format("2006-01-02")
	for _, d := range w.days() {
		if d < cutoff {
			for _, suffix := range []string{".log", ".log.gz"} {
				_ = os.Remove(filepath.Join(w.dir, "sql-"+d+suffix))
			}
		}
	}
}

// datesUnlocked 列出存在的日志日期（升序）。
func (w *Writer) days() []string {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "sql-") {
			continue
		}
		name = strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".log")
		name = strings.TrimPrefix(name, "sql-")
		if len(name) != 10 || seen[name] {
			continue
		}
		if _, err := time.Parse("2006-01-02", name); err != nil {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
