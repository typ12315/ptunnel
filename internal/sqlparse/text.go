package sqlparse

import "bytes"

// textParser 用于 Oracle / 达梦（TNS 私有编码）的启发式提取：
// 在客户端上行数据中寻找以 SQL 关键字开头、由可打印字符组成的片段。
// 不是严格协议解析，可能漏报/误报；仅用于审计参考。
type textParser struct{ buf []byte }

var sqlKeywords = [][]byte{
	[]byte("select "), []byte("insert "), []byte("update "), []byte("delete "),
	[]byte("merge "), []byte("create "), []byte("alter "), []byte("drop "),
	[]byte("truncate "), []byte("declare "), []byte("begin "), []byte("with "),
	[]byte("grant "), []byte("revoke "), []byte("call "),
}

const (
	textKeepTail  = 64
	textMaxBuffer = 512 * 1024
)

func (t *textParser) feed(p []byte, max int, emit func(kind, sql string)) {
	buf := append(t.buf, p...)
	for {
		idx := findSQLStart(buf)
		if idx < 0 {
			if len(buf) > textKeepTail {
				buf = append([]byte(nil), buf[len(buf)-textKeepTail:]...)
			}
			t.buf = buf
			return
		}
		end := idx
		limit := idx + max + 64
		for end < len(buf) {
			c := buf[end]
			if c == 0 || c == 0x7f || (c < 0x20 && c != '\t') {
				break
			}
			if end >= limit {
				break
			}
			end++
		}
		terminated := end < len(buf)
		if !terminated && end < limit {
			// 语句还没结束，保留从关键字开始的内容等待更多数据
			buf = append([]byte(nil), buf[idx:]...)
			if len(buf) > textMaxBuffer {
				buf = nil
			}
			t.buf = buf
			return
		}
		if end-idx >= 8 {
			emit("heuristic", cleanSQL(buf[idx:end], max))
		}
		buf = buf[end:]
		if len(buf) == 0 {
			t.buf = buf
			return
		}
	}
}

// findSQLStart 返回第一个「关键字 + 空格」且左侧不是字母/数字/下划线/空格/括号的起始位置。
func findSQLStart(buf []byte) int {
	if len(buf) < 8 {
		return -1
	}
	lower := asciiLower(buf)
	for i := 0; i+7 <= len(lower); i++ {
		// 粗筛：只从关键字首字母位置尝试，减少比较次数
		if lower[i] != 's' && lower[i] != 'i' && lower[i] != 'u' && lower[i] != 'd' &&
			lower[i] != 'm' && lower[i] != 'c' && lower[i] != 'a' && lower[i] != 't' &&
			lower[i] != 'b' && lower[i] != 'w' && lower[i] != 'g' && lower[i] != 'r' {
			continue
		}
		for _, kw := range sqlKeywords {
			if !bytes.HasPrefix(lower[i:], kw) {
				continue
			}
			if i > 0 && isSQLWordByte(lower[i-1]) {
				continue
			}
			return i
		}
	}
	return -1
}

func isSQLWordByte(c byte) bool {
	return c == '_' || c == ' ' || c == '\t' || c == '(' || c == ',' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
