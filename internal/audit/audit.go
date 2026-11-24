// Package audit 负责连接审计的采集与批量落库。
package audit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"ptunnel/internal/audit/mac"
	"ptunnel/internal/store"
)

// Config 审计写入配置。
type Config struct {
	Mode          string // full / sample / off
	SampleRate    int
	QueueSize     int
	RetentionDays int
	MACLookup     bool
}

type itemKind int

const (
	itemStart itemKind = iota
	itemEnd
	itemMAC
)

type item struct {
	kind  itemKind
	log   *store.ConnLog
	end   *store.ConnEnd
	conn  string
	mac   string
	macBy string
}

// Writer 异步批量写入连接审计，队列满时丢弃并计数，绝不阻塞转发路径。
type Writer struct {
	st      *store.Store
	cfg     Config
	log     *slog.Logger
	ch      chan item
	dropped atomic.Int64
	seq     atomic.Int64
	once    sync.Once
	stop    chan struct{}
}

// NewWriter 创建审计写入器。
func NewWriter(st *store.Store, cfg Config, logger *slog.Logger) *Writer {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 1
	}
	if cfg.Mode == "" {
		cfg.Mode = "full"
	}
	return &Writer{
		st:   st,
		cfg:  cfg,
		log:  logger,
		ch:   make(chan item, cfg.QueueSize),
		stop: make(chan struct{}),
	}
}

// Start 启动写协程。
func (w *Writer) Start() {
	go w.loop()
}

// Close 停止写协程并落盘剩余数据。
func (w *Writer) Close() {
	w.once.Do(func() { close(w.stop) })
}

// Dropped 返回因队列满被丢弃的记录数。
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// QueueLen 返回当前积压数量。
func (w *Writer) QueueLen() int { return len(w.ch) }

// Enabled 是否记录明细。
func (w *Writer) Enabled() bool { return w.cfg.Mode != "off" }

// StartConn 记录连接建立；包含被拒绝的连接。
// 返回 true 表示开始记录已成功入队，调用方才应写结束记录，避免产生悬挂的活跃记录。
func (w *Writer) StartConn(l *store.ConnLog) bool {
	if !w.Enabled() {
		return false
	}
	if w.cfg.Mode == "sample" {
		if w.seq.Add(1)%int64(w.cfg.SampleRate) != 0 {
			return false
		}
	}
	if !w.push(item{kind: itemStart, log: l}) {
		return false
	}
	if w.cfg.MACLookup && l.SrcIP != "" {
		// 命中缓存时直接补写，避免每条连接都启动一个 goroutine
		if hw, source, ok := mac.LookupCached(l.SrcIP); ok {
			if hw != "" {
				w.push(item{kind: itemMAC, conn: l.ConnID, mac: hw, macBy: source})
			}
		} else {
			go w.resolveMAC(l.ConnID, l.SrcIP)
		}
	}
	return true
}

// EndConn 记录连接结束。
func (w *Writer) EndConn(e *store.ConnEnd) {
	if !w.Enabled() {
		return
	}
	w.push(item{kind: itemEnd, end: e})
}

func (w *Writer) resolveMAC(connID, ip string) {
	hw, source := mac.Lookup(ip)
	if hw == "" {
		return
	}
	w.push(item{kind: itemMAC, conn: connID, mac: hw, macBy: source})
}

// push 入队，返回是否成功；队列满时丢弃并计数，绝不阻塞。
func (w *Writer) push(it item) bool {
	select {
	case w.ch <- it:
		return true
	default:
		w.dropped.Add(1)
		return false
	}
}

func (w *Writer) loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var starts []*store.ConnLog
	var ends []*store.ConnEnd
	var macs []item

	flush := func() {
		if len(starts) > 0 {
			if err := w.st.InsertConnLogs(starts); err != nil {
				w.log.Error("写入连接审计失败", "err", err, "count", len(starts))
			}
			starts = starts[:0]
		}
		if len(ends) > 0 {
			if err := w.st.FinishConnLogs(ends); err != nil {
				w.log.Error("回填连接审计失败", "err", err, "count", len(ends))
			}
			ends = ends[:0]
		}
		for _, m := range macs {
			if err := w.st.UpdateConnLogMAC(m.conn, m.mac, m.macBy); err != nil {
				w.log.Warn("补写 MAC 失败", "err", err)
			}
		}
		macs = macs[:0]
	}

	pending := func() int { return len(starts) + len(ends) + len(macs) }

	for {
		select {
		case <-w.stop:
			for {
				select {
				case it := <-w.ch:
					collect(it, &starts, &ends, &macs)
				default:
					flush()
					return
				}
			}
		case it := <-w.ch:
			collect(it, &starts, &ends, &macs)
			if pending() >= 200 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func collect(it item, starts *[]*store.ConnLog, ends *[]*store.ConnEnd, macs *[]item) {
	switch it.kind {
	case itemStart:
		*starts = append(*starts, it.log)
	case itemEnd:
		*ends = append(*ends, it.end)
	case itemMAC:
		*macs = append(*macs, it)
	}
}

// RunRetention 每日 03:00 清理过期明细，直到 ctx 取消。
func (w *Writer) RunRetention(ctx context.Context) {
	if w.cfg.RetentionDays <= 0 {
		return
	}
	// 每小时检查一次，仅当跨过 03:00 且今天尚未清理时执行，避免同一天重复清理。
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	var lastRun string
	purge := func() {
		lastRun = time.Now().Format("2006-01-02")
		cutoff := time.Now().AddDate(0, 0, -w.cfg.RetentionDays)
		for i := 0; i < 100; i++ {
			n, err := w.st.DeleteConnLogsBefore(cutoff, 5000)
			if err != nil {
				w.log.Warn("清理连接审计失败", "err", err)
				break
			}
			if n == 0 {
				break
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.Hour() != 3 || lastRun == now.Format("2006-01-02") {
				continue
			}
			purge()
		}
	}
}
