package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ptunnel/internal/sqllog"
)

// handleListSQLLogs 查询某天的 SQL 审计日志（默认今天），支持 IP/MAC/应用/关键字过滤。
func (s *Server) handleListSQLLogs(c *gin.Context) {
	if s.sqlw == nil {
		c.JSON(http.StatusOK, gin.H{"items": []sqllog.Record{}, "total": 0, "enabled": false,
			"message": "SQL 语句审计未启用（audit.sql_log_enabled: false）"})
		return
	}
	f := sqllog.Filter{
		Date:    c.Query("date"),
		IP:      c.Query("ip"),
		MAC:     c.Query("mac"),
		App:     c.Query("app"),
		Keyword: c.Query("keyword"),
	}
	f.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	f.Size, _ = strconv.Atoi(c.DefaultQuery("size", "50"))

	s.sqlw.Flush() // 让当天刚写入的语句立即可查
	items, total, err := s.sqlw.List(f)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_query", "查询失败: "+err.Error())
		return
	}
	date := strings.TrimSpace(f.Date)
	if date == "" {
		date = s.sqlw.Today()
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"total":   total,
		"date":    date,
		"enabled": true,
		"dropped": s.sqlw.Dropped(),
	})
}

// handleSQLLogDates 返回有日志的日期列表（新→旧），供前端日期选择。
func (s *Server) handleSQLLogDates(c *gin.Context) {
	if s.sqlw == nil {
		c.JSON(http.StatusOK, gin.H{"items": []string{}, "enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   s.sqlw.Dates(),
		"today":   s.sqlw.Today(),
		"enabled": true,
	})
}
