package tunnel

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestStreamHeaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStreamHeader(&buf, "proxy-123", "192.168.1.10:54321"); err != nil {
		t.Fatalf("写入流头失败: %v", err)
	}
	id, addr, err := ReadStreamHeader(&buf)
	if err != nil {
		t.Fatalf("读取流头失败: %v", err)
	}
	if id != "proxy-123" || addr != "192.168.1.10:54321" {
		t.Fatalf("流头不匹配: id=%q addr=%q", id, addr)
	}
}

func TestStreamHeaderEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStreamHeader(&buf, "", ""); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	id, addr, err := ReadStreamHeader(&buf)
	if err != nil || id != "" || addr != "" {
		t.Fatalf("空字段解析异常: id=%q addr=%q err=%v", id, addr, err)
	}
}

func TestStreamStatusRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStreamStatus(&buf, true, ""); err != nil {
		t.Fatal(err)
	}
	ok, msg, err := ReadStreamStatus(&buf)
	if err != nil || !ok || msg != "" {
		t.Fatalf("成功状态解析异常: ok=%v msg=%q err=%v", ok, msg, err)
	}

	buf.Reset()
	if err := WriteStreamStatus(&buf, false, "连接被拒绝"); err != nil {
		t.Fatal(err)
	}
	ok, msg, err = ReadStreamStatus(&buf)
	if err != nil || ok || msg != "连接被拒绝" {
		t.Fatalf("失败状态解析异常: ok=%v msg=%q err=%v", ok, msg, err)
	}
}

func TestControlSendReceive(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	c := NewControl(client)
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_ = c.Send(MsgHello, Hello{ClientID: "abc", Version: "1.0"})
	}()

	sc := NewControl(server)
	typ, payload, err := sc.Receive()
	if err != nil {
		t.Fatalf("接收失败: %v", err)
	}
	if typ != MsgHello {
		t.Fatalf("消息类型不匹配: %s", typ)
	}
	if !bytes.Contains(payload, []byte("abc")) {
		t.Fatalf("载荷不匹配: %s", payload)
	}
}

func TestControlRejectsOversizeFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		// 构造一个声明长度超限的帧
		_, _ = client.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}()
	sc := NewControl(server)
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := sc.Receive(); err == nil {
		t.Fatal("超长帧应当报错")
	}
}
