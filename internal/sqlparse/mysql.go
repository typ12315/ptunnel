package sqlparse

// mysqlParser 解析 MySQL 客户端命令包：3 字节长度 + 1 字节序号 + 命令体。
// 只关心 COM_QUERY(0x03) 与 COM_STMT_PREPARE(0x16)；握手响应与 SSL 请求会被跳过。
type mysqlParser struct {
	buf   []byte
	phase int // 0 等待握手响应，1 命令阶段，2 已停止（SSL/异常）
}

func (m *mysqlParser) feed(p []byte, max int, emit func(kind, sql string)) {
	if m.phase == 2 {
		return
	}
	m.buf = append(m.buf, p...)
	for {
		if len(m.buf) < 4 {
			return
		}
		n := int(m.buf[0]) | int(m.buf[1])<<8 | int(m.buf[2])<<16
		if n <= 0 || n > 64<<20 {
			m.stop()
			return
		}
		if len(m.buf) < 4+n {
			return
		}
		payload := m.buf[4 : 4+n]
		m.buf = m.buf[4+n:]

		if m.phase == 0 {
			m.phase = 1
			// 握手响应前 4 字节是 capability flags；带 CLIENT_SSL(0x0800) 说明后面是 TLS 流
			if len(payload) >= 4 && le32(payload) == 32 && le32(payload)&0x0800 != 0 {
				m.stop()
				return
			}
			continue
		}
		if len(payload) == 0 {
			continue
		}
		switch payload[0] {
		case 0x03:
			emit("query", cleanSQL(payload[1:], max))
		case 0x16:
			emit("prepare", cleanSQL(payload[1:], max))
		}
	}
}

func (m *mysqlParser) stop() {
	m.phase = 2
	m.buf = nil
}
