package auth

import (
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("test12345")
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if !VerifyPassword(hash, "test12345") {
		t.Fatal("正确口令校验失败")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("错误口令不应通过")
	}
	// 同一口令两次哈希应不同（随机盐）
	hash2, _ := HashPassword("test12345")
	if hash == hash2 {
		t.Fatal("哈希应带随机盐")
	}
}

func TestSessionCreateVerifyRevoke(t *testing.T) {
	m := NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	token, exp := m.Create(1, "admin", "admin")
	if time.Until(exp) <= 0 {
		t.Fatal("过期时间异常")
	}
	s, ok := m.Verify(token)
	if !ok || s.Username != "admin" {
		t.Fatal("会话校验失败")
	}
	if _, ok := m.Verify(token + "x"); ok {
		t.Fatal("被篡改的令牌不应通过")
	}
	m.Revoke(token)
	if _, ok := m.Verify(token); ok {
		t.Fatal("已注销令牌不应通过")
	}
}

func TestSessionExpiry(t *testing.T) {
	m := NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), -time.Second)
	token, _ := m.Create(1, "admin", "admin")
	if _, ok := m.Verify(token); ok {
		t.Fatal("过期令牌不应通过")
	}
}

func TestTokenHash(t *testing.T) {
	if TokenHash("a") == TokenHash("b") {
		t.Fatal("不同令牌哈希应不同")
	}
	if len(TokenHash("a")) != 64 {
		t.Fatal("应为 sha256 十六进制")
	}
}
