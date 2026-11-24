package sqlparse

import "unicode/utf16"

// tdsParser 解析 SQL Server TDS 客户端包。
// 包结构：type(1) status(1) length(2 BE) spid(2) packetID(1) window(1) + payload。
// type 0x01 为 SQL Batch（UTF-16LE），status 置 EOM(0x01) 表示消息结束。
type tdsParser struct {
	buf   []byte
	batch []byte
}

func (t *tdsParser) feed(p []byte, max int, emit func(kind, sql string)) {
	t.buf = append(t.buf, p...)
	for {
		if len(t.buf) < 8 {
			return
		}
		pktLen := be16(t.buf[2:4])
		if pktLen < 8 || pktLen > 1<<20 {
			t.buf = nil
			t.batch = nil
			return
		}
		if len(t.buf) < pktLen {
			return
		}
		typ, status := t.buf[0], t.buf[1]
		payload := t.buf[8:pktLen]
		t.buf = t.buf[pktLen:]

		if typ == 0x01 {
			t.batch = append(t.batch, payload...)
			if status&0x01 != 0 { // EOM
				sql := decodeUTF16LE(t.batch)
				t.batch = nil
				emit("batch", cleanSQL([]byte(sql), max))
			}
		}
	}
}

// decodeUTF16LE 把 UTF-16LE 字节解码成 UTF-8（非法代理项用替换符）。
func decodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}
