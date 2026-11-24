package sqlparse

import "bytes"

// pgParser 解析 PostgreSQL 前端消息。
// 启动包之后是「1 字节类型 + 4 字节长度 + 负载」，SQL 出现在 Query('Q') 与 Parse('P')。
// 若客户端请求 SSL（SSLRequest），之后流量加密，解析器停止。
type pgParser struct {
	buf   []byte
	state int // 0 等待启动包，1 消息循环，2 已停止
}

func (p *pgParser) feed(data []byte, max int, emit func(kind, sql string)) {
	if p.state == 2 {
		return
	}
	p.buf = append(p.buf, data...)
	for {
		if p.state == 0 {
			if len(p.buf) < 8 {
				return
			}
			n := be32(p.buf[:4])
			if n < 8 || n > 10000 {
				p.stop()
				return
			}
			if len(p.buf) < n {
				return
			}
			code := be32(p.buf[4:8])
			switch code {
			case 80877103, 80877104: // SSLRequest / GSSENCRequest：后续为加密流
				p.stop()
				return
			case 80877102: // CancelRequest
				p.stop()
				return
			}
			p.buf = p.buf[n:]
			p.state = 1
			continue
		}
		if len(p.buf) < 5 {
			return
		}
		typ := p.buf[0]
		n := be32(p.buf[1:5])
		if n < 4 || n > 64<<20 {
			p.stop()
			return
		}
		total := 1 + n
		if len(p.buf) < total {
			return
		}
		payload := p.buf[5:total]
		p.buf = p.buf[total:]
		switch typ {
		case 'Q':
			emit("query", cleanSQL(payload, max))
		case 'P':
			// Parse: statement-name\0 SQL\0 param-types
			if i := bytes.IndexByte(payload, 0); i >= 0 {
				rest := payload[i+1:]
				if j := bytes.IndexByte(rest, 0); j >= 0 {
					emit("prepare", cleanSQL(rest[:j], max))
				}
			}
		}
	}
}

func (p *pgParser) stop() {
	p.state = 2
	p.buf = nil
}
