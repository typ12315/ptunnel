package sqlparse

import (
	"bytes"
	"strconv"
	"strings"
)

// redisParser 解析 Redis RESP 请求（客户端命令都是数组），也兼容 inline 命令。
type redisParser struct {
	buf []byte
}

func (r *redisParser) feed(p []byte, max int, emit func(kind, sql string)) {
	r.buf = append(r.buf, p...)
	for {
		if len(r.buf) == 0 {
			return
		}
		if r.buf[0] != '*' {
			// inline 命令：读到换行为止
			i := bytes.IndexByte(r.buf, '\n')
			if i < 0 {
				if len(r.buf) > 256*1024 {
					r.buf = nil
				}
				return
			}
			line := strings.TrimRight(string(r.buf[:i]), "\r")
			r.buf = r.buf[i+1:]
			if line != "" {
				emit("command", cleanSQL([]byte(line), max))
			}
			continue
		}
		// *<N>\r\n
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(r.buf[1:i])))
		if err != nil || n <= 0 || n > 1024 {
			r.buf = nil // 非法数据，放弃解析
			return
		}
		pos := i + 1
		args := make([]string, 0, n)
		ok := true
		for k := 0; k < n; k++ {
			if pos >= len(r.buf) || r.buf[pos] != '$' {
				ok = false
				break
			}
			j := bytes.IndexByte(r.buf[pos:], '\n')
			if j < 0 {
				ok = false
				break
			}
			ln, err := strconv.Atoi(strings.TrimSpace(string(r.buf[pos+1 : pos+j])))
			if err != nil || ln < 0 || ln > 1<<20 {
				ok = false
				break
			}
			start := pos + j + 1
			if start+ln > len(r.buf) {
				ok = false
				break
			}
			args = append(args, string(r.buf[start:start+ln]))
			pos = start + ln
			// 跳过 \r\n
			if pos+1 < len(r.buf) && r.buf[pos] == '\r' && r.buf[pos+1] == '\n' {
				pos += 2
			} else if pos < len(r.buf) && r.buf[pos] == '\n' {
				pos++
			}
		}
		if !ok {
			if len(r.buf) > 1<<20 {
				r.buf = nil
			}
			return
		}
		emit("command", cleanSQL([]byte(strings.Join(args, " ")), max))
		r.buf = r.buf[pos:]
	}
}
