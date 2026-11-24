package sqlparse

import (
	"bytes"
	"encoding/binary"
)

// mongoParser 解析 MongoDB 客户端消息（OP_MSG / OP_QUERY），提取「命令 + 集合名」。
// Mongo 没有 SQL，审计里记录的是命令（find/insert/update/delete/aggregate...）。
type mongoParser struct {
	buf []byte
}

func (m *mongoParser) feed(p []byte, max int, emit func(kind, sql string)) {
	m.buf = append(m.buf, p...)
	for {
		if len(m.buf) < 16 {
			return
		}
		msgLen := int(int32(binary.LittleEndian.Uint32(m.buf[0:4])))
		if msgLen < 16 || msgLen > 48<<20 {
			m.buf = nil
			return
		}
		if len(m.buf) < msgLen {
			return
		}
		opCode := int(int32(binary.LittleEndian.Uint32(m.buf[12:16])))
		body := m.buf[16:msgLen]
		m.buf = m.buf[msgLen:]

		switch opCode {
		case 2013: // OP_MSG
			if len(body) < 5 || body[4] != 0 { // 只处理 kind 0 的 body 段
				continue
			}
			cmd, coll := bsonCommand(body[5:])
			if cmd != "" {
				line := cmd
				if coll != "" {
					line = cmd + " " + coll
				}
				emit("command", cleanSQL([]byte(line), max))
			}
		case 2004: // OP_QUERY（老驱动）
			if len(body) < 4 {
				continue
			}
			i := bytes.IndexByte(body[4:], 0)
			if i < 0 {
				continue
			}
			coll := string(body[4 : 4+i])
			rest := body[4+i+1:]
			if len(rest) < 8 {
				continue
			}
			cmd, _ := bsonCommand(rest[8:])
			if cmd != "" {
				line := cmd
				if coll != "" {
					line = cmd + " " + coll
				}
				emit("command", cleanSQL([]byte(line), max))
			}
		}
	}
}

// bsonCommand 取 BSON 文档第一个元素的名称（命令名）与其字符串值（通常是集合名）。
func bsonCommand(doc []byte) (string, string) {
	if len(doc) < 5 {
		return "", ""
	}
	n := int(int32(binary.LittleEndian.Uint32(doc[0:4])))
	if n < 5 || n > len(doc) {
		return "", ""
	}
	end := n - 1 // 最后一个 0x00
	pos := 4
	if pos >= end {
		return "", ""
	}
	typ := doc[pos]
	pos++
	i := bytes.IndexByte(doc[pos:end], 0)
	if i < 0 {
		return "", ""
	}
	name := string(doc[pos : pos+i])
	pos += i + 1
	var val string
	if typ == 0x02 && pos+4 <= end { // string
		sl := int(int32(binary.LittleEndian.Uint32(doc[pos:])))
		if sl > 0 && pos+4+sl <= end {
			val = string(bytes.TrimRight(doc[pos+4:pos+4+sl], "\x00"))
		}
	}
	return name, val
}
