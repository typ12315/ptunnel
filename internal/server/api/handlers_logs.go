package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ptunnel/internal/store"
)

func parseTime(v string) *time.Time {
	if v == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t
	}
	if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
		t := time.Unix(sec, 0)
		return &t
	}
	return nil
}

func connFilterFrom(c *gin.Context) store.ConnFilter {
	f := store.ConnFilter{
		ClientID:   c.Query("client_id"),
		ProxyID:    c.Query("proxy_id"),
		SrcIP:      c.Query("src_ip"),
		From:       parseTime(c.Query("from")),
		To:         parseTime(c.Query("to")),
		ActiveOnly: c.Query("active") == "1",
	}
	f.RemotePort, _ = strconv.Atoi(c.Query("remote_port"))
	f.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	f.Size, _ = strconv.Atoi(c.DefaultQuery("size", "50"))
	return f
}

func (s *Server) handleListConnections(c *gin.Context) {
	items, total, err := s.st.ListConnLogs(connFilterFrom(c))
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询连接审计失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

func (s *Server) handleActiveConnections(c *gin.Context) {
	items, err := s.st.ActiveConnLogs(500)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询活跃连接失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) handleGetConnLog(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	item, err := s.st.GetConnLog(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "not_found", "记录不存在")
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) handleExportConnections(c *gin.Context) {
	f := connFilterFrom(c)
	f.Page = 1
	f.Size = 500
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=connections-%s.csv", time.Now().Format("20060102-150405")))
	c.Writer.WriteHeader(http.StatusOK)
	// UTF-8 BOM，便于 Excel 直接打开
	_, _ = c.Writer.Write([]byte("\xEF\xBB\xBF"))

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"开始时间", "结束时间", "连接时长(ms)", "来源IP", "来源端口", "来源MAC", "服务端端口", "客户端", "代理", "目标地址", "上行字节", "下行字节", "结束原因"})

	exported := 0
	for page := 1; page <= 200 && exported < 100000; page++ {
		f.Page = page
		items, _, err := s.st.ListConnLogs(f)
		if err != nil || len(items) == 0 {
			break
		}
		for _, it := range items {
			ended := ""
			if it.EndedAt != nil {
				ended = it.EndedAt.Format("2006-01-02 15:04:05")
			}
			_ = w.Write([]string{
				it.StartedAt.Format("2006-01-02 15:04:05"),
				ended,
				strconv.FormatInt(it.DurationMS, 10),
				it.SrcIP,
				strconv.Itoa(it.SrcPort),
				it.SrcMAC,
				strconv.Itoa(it.RemotePort),
				it.ClientName,
				it.ProxyName,
				it.TargetAddr,
				strconv.FormatInt(it.BytesIn, 10),
				strconv.FormatInt(it.BytesOut, 10),
				it.CloseReason,
			})
			exported++
		}
		w.Flush()
		c.Writer.Flush()
		if len(items) < f.Size {
			break
		}
	}
	w.Flush()
}

func (s *Server) handleDeleteConnections(c *gin.Context) {
	before := parseTime(c.Query("before"))
	if before == nil {
		fail(c, http.StatusBadRequest, "invalid_param", "缺少 before 参数")
		return
	}
	var total int64
	for {
		n, err := s.st.DeleteConnLogsBefore(*before, 5000)
		if err != nil {
			fail(c, http.StatusInternalServerError, "db_error", "清理失败")
			return
		}
		total += n
		if n == 0 {
			break
		}
	}
	s.audit(c, "conn.delete", "conn_log", "", fmt.Sprintf("清理 %s 之前的连接审计 %d 条", before.Format(time.RFC3339), total))
	c.JSON(http.StatusOK, gin.H{"deleted": total})
}

func (s *Server) handleListAudit(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	items, total, err := s.st.ListAudit(c.Query("action"), c.Query("actor"), page, size)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询操作审计失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

func (s *Server) handleOverview(c *gin.Context) {
	clients, err := s.st.ListClients()
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询失败")
		return
	}
	online := s.mgr.Count()
	pending := 0
	for _, cl := range clients {
		proxies, _ := s.st.ListProxies(cl.ID)
		for _, p := range proxies {
			if p.Approval == "pending" {
				pending++
			}
		}
	}
	stats := s.ph.Snapshot()
	var active, totalConns, rejected, bytesIn, bytesOut int64
	for _, st := range stats {
		active += st.ActiveConns
		totalConns += st.TotalConns
		rejected += st.Rejected
		bytesIn += st.BytesIn
		bytesOut += st.BytesOut
	}
	startOfDay := time.Now()
	startOfDay = time.Date(startOfDay.Year(), startOfDay.Month(), startOfDay.Day(), 0, 0, 0, 0, startOfDay.Location())
	todayConns, todayIn, todayOut, _ := s.st.SumSince(startOfDay)

	c.JSON(http.StatusOK, gin.H{
		"clients_total":   len(clients),
		"clients_online":  online,
		"proxies_pending": pending,
		"listeners":       s.ports.Count(),
		"active_conns":    active,
		"total_conns":     totalConns,
		"rejected_conns":  rejected,
		"live_bytes_in":   bytesIn,
		"live_bytes_out":  bytesOut,
		"today_conns":     todayConns,
		"today_bytes_in":  todayIn,
		"today_bytes_out": todayOut,
		"uptime_seconds":  int(time.Since(s.startedAt).Seconds()),
		"audit_queue":     s.aw.QueueLen(),
		"audit_dropped":   s.aw.Dropped(),
		"online_clients":  s.mgr.Snapshot(),
		"proxy_stats":     stats,
	})
}

func (s *Server) handleSeries(c *gin.Context) {
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	to := time.Now()
	from := to.Add(-time.Duration(hours) * time.Hour)
	points, err := s.st.TrafficSeries(c.Query("proxy_id"), from, to, 5*time.Minute)
	if err != nil {
		fail(c, http.StatusInternalServerError, "db_error", "查询流量曲线失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": points})
}
