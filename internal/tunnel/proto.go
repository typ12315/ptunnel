// Package tunnel 定义服务端与客户端之间的加密隧道协议（TLS + yamux）。
package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ALPN 隧道协议标识。
const ALPN = "ptunnel/1"

// StreamVersion 数据流头版本。
const StreamVersion byte = 1

const maxFrameSize = 4 << 20 // 单条控制消息上限 4MB

// LocalProxy 客户端本地声明的代理。
type LocalProxy struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	RemotePort int    `json:"remote_port"`
	Target     string `json:"target"`
}

// Hello 客户端上线握手。
type Hello struct {
	ClientID string       `json:"client_id"`
	Version  string       `json:"version"`
	OS       string       `json:"os"`
	Arch     string       `json:"arch"`
	Proxies  []LocalProxy `json:"proxies"`
}

// HelloAck 服务端握手确认。
type HelloAck struct {
	ServerTime     int64 `json:"server_time"`
	HeartbeatSec   int   `json:"heartbeat_sec"`
	IdleTimeoutSec int   `json:"idle_timeout_sec"`
}

// ProxySpec 服务端下发的代理配置。
type ProxySpec struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Protocol       string   `json:"protocol"`
	BindAddr       string   `json:"bind_addr"`
	RemotePort     int      `json:"remote_port"`
	TargetAddr     string   `json:"target_addr"`
	Enabled        bool     `json:"enabled"`
	MaxConns       int      `json:"max_conns"`
	AllowCIDRs     []string `json:"allow_cidrs"`
	IdleTimeoutSec int      `json:"idle_timeout_sec"`
}

// ConfigPush 全量配置下发。
type ConfigPush struct {
	Revision int64       `json:"revision"`
	Proxies  []ProxySpec `json:"proxies"`
}

// ApplyError 单条代理应用失败。
type ApplyError struct {
	ProxyID string `json:"proxy_id"`
	Message string `json:"message"`
}

// ApplyResult 客户端应用结果。
type ApplyResult struct {
	Revision int64        `json:"revision"`
	Errors   []ApplyError `json:"errors,omitempty"`
}

// ProxyStats 单代理运行统计。
type ProxyStats struct {
	ProxyID     string `json:"proxy_id"`
	ActiveConns int    `json:"active_conns"`
	BytesIn     int64  `json:"bytes_in"`
	BytesOut    int64  `json:"bytes_out"`
}

// Stats 客户端周期上报。
type Stats struct {
	Proxies []ProxyStats `json:"proxies"`
}

// Ping / Pong 心跳。
type Ping struct {
	Seq int64 `json:"seq"`
	TS  int64 `json:"ts"`
}

// Pong 心跳响应。
type Pong struct {
	Seq int64 `json:"seq"`
	TS  int64 `json:"ts"`
}

// Kick 服务端要求客户端下线。
type Kick struct {
	Reason string `json:"reason"`
}

// ServerShutdown 服务端优雅关闭通知。
type ServerShutdown struct {
	RetryAfterSec int `json:"retry_after_sec"`
}

// 控制消息类型。
const (
	MsgHello          = "hello"
	MsgHelloAck       = "hello_ack"
	MsgConfigPush     = "config_push"
	MsgApplyResult    = "apply_result"
	MsgStats          = "stats"
	MsgPing           = "ping"
	MsgPong           = "pong"
	MsgKick           = "kick"
	MsgServerShutdown = "server_shutdown"
)

type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Control 控制流读写封装（写加锁，支持并发发送）。
type Control struct {
	conn net.Conn
	wmu  sync.Mutex
}

// NewControl 包装一条流为控制通道。
func NewControl(c net.Conn) *Control { return &Control{conn: c} }

// Send 发送一条控制消息。
func (c *Control) Send(msgType string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = b
	}
	body, err := json.Marshal(envelope{Type: msgType, Payload: raw})
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(body)))
	copy(buf[4:], body)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.conn.Write(buf)
	return err
}

// Receive 读取一条控制消息。
func (c *Control) Receive() (string, json.RawMessage, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(c.conn, lenBuf[:]); err != nil {
		return "", nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > maxFrameSize {
		return "", nil, fmt.Errorf("非法控制帧长度: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return "", nil, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, fmt.Errorf("解析控制帧失败: %w", err)
	}
	return env.Type, env.Payload, nil
}

// SetDeadline 设置读超时。
func (c *Control) SetDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close 关闭控制流。
func (c *Control) Close() error { return c.conn.Close() }

// 数据流状态字节：客户端拨号结果。
const (
	StreamStatusOK    byte = 0
	StreamStatusError byte = 1
)

// WriteStreamStatus 写入拨号结果（成功时不带消息）。
func WriteStreamStatus(w io.Writer, ok bool, msg string) error {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	buf := make([]byte, 0, 3+len(msg))
	if ok {
		buf = append(buf, StreamStatusOK, 0, 0)
	} else {
		buf = append(buf, StreamStatusError, byte(len(msg)), byte(len(msg)>>8))
		buf = append(buf, msg...)
	}
	_, err := w.Write(buf)
	return err
}

// ReadStreamStatus 读取拨号结果。
func ReadStreamStatus(r io.Reader) (bool, string, error) {
	var head [3]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return false, "", err
	}
	if head[0] == StreamStatusOK {
		return true, "", nil
	}
	n := int(head[1]) | int(head[2])<<8
	msg := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, msg); err != nil {
			return false, "", err
		}
	}
	return false, string(msg), nil
}

// WriteStreamHeader 写入数据流头。
func WriteStreamHeader(w io.Writer, proxyID, srcAddr string) error {
	if len(proxyID) > 65535 || len(srcAddr) > 65535 {
		return fmt.Errorf("流头字段过长")
	}
	buf := make([]byte, 0, 5+len(proxyID)+len(srcAddr))
	buf = append(buf, StreamVersion)
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], uint16(len(proxyID)))
	buf = append(buf, tmp[:]...)
	buf = append(buf, proxyID...)
	binary.BigEndian.PutUint16(tmp[:], uint16(len(srcAddr)))
	buf = append(buf, tmp[:]...)
	buf = append(buf, srcAddr...)
	_, err := w.Write(buf)
	return err
}

// ReadStreamHeader 读取数据流头。
func ReadStreamHeader(r io.Reader) (proxyID, srcAddr string, err error) {
	var head [3]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return "", "", err
	}
	if head[0] != StreamVersion {
		return "", "", fmt.Errorf("不支持的流头版本: %d", head[0])
	}
	idLen := int(binary.BigEndian.Uint16(head[1:3]))
	idBuf := make([]byte, idLen)
	if _, err = io.ReadFull(r, idBuf); err != nil {
		return "", "", err
	}
	var addrLenBuf [2]byte
	if _, err = io.ReadFull(r, addrLenBuf[:]); err != nil {
		return "", "", err
	}
	addrLen := int(binary.BigEndian.Uint16(addrLenBuf[:]))
	addrBuf := make([]byte, addrLen)
	if _, err = io.ReadFull(r, addrBuf); err != nil {
		return "", "", err
	}
	return string(idBuf), string(addrBuf), nil
}
