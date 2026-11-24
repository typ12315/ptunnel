// Package auth 提供口令哈希与会话令牌。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// HashPassword 使用 Argon2id 生成口令哈希。
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const (
		memory  = 64 * 1024
		time    = 3
		threads = 2
		keyLen  = 32
	)
	sum := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory, time, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword 校验口令。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Session 会话信息。
type Session struct {
	JTI      string `json:"jti"`
	UserID   int64  `json:"uid"`
	Username string `json:"usr"`
	Role     string `json:"role"`
	Expires  int64  `json:"exp"`
}

// SessionManager 签发与校验无状态会话（HMAC），并维护吊销白名单。
type SessionManager struct {
	key   []byte
	ttl   time.Duration
	mu    sync.RWMutex
	alive map[string]int64
}

// NewSessionManager 创建会话管理器。
func NewSessionManager(key []byte, ttl time.Duration) *SessionManager {
	return &SessionManager{key: key, ttl: ttl, alive: map[string]int64{}}
}

// LoadOrCreateKey 从设置表加载签名密钥，不存在则生成。
func LoadOrCreateKey(get func(string, string) string, set func(string, string) error) ([]byte, error) {
	if v := get("session_key", ""); v != "" {
		if b, err := hex.DecodeString(v); err == nil && len(b) >= 32 {
			return b, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := set("session_key", hex.EncodeToString(key)); err != nil {
		return nil, err
	}
	return key, nil
}

// Create 签发会话令牌。
func (m *SessionManager) Create(userID int64, username, role string) (string, time.Time) {
	jti := randomID()
	exp := time.Now().Add(m.ttl)
	s := Session{JTI: jti, UserID: userID, Username: username, Role: role, Expires: exp.Unix()}
	body, _ := json.Marshal(s)
	payload := base64.RawURLEncoding.EncodeToString(body)
	sig := m.sign(payload)
	m.mu.Lock()
	if len(m.alive) > 1024 {
		now := time.Now().Unix()
		for jti, exp := range m.alive {
			if exp < now {
				delete(m.alive, jti)
			}
		}
	}
	m.alive[jti] = exp.Unix()
	m.mu.Unlock()
	return payload + "." + sig, exp
}

// Verify 校验令牌。
func (m *SessionManager) Verify(token string) (*Session, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, false
	}
	if !hmac.Equal([]byte(m.sign(parts[0])), []byte(parts[1])) {
		return nil, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, false
	}
	if time.Now().Unix() > s.Expires {
		return nil, false
	}
	m.mu.RLock()
	_, ok := m.alive[s.JTI]
	m.mu.RUnlock()
	return &s, ok
}

// Revoke 注销令牌。
func (m *SessionManager) Revoke(token string) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return
	}
	var s Session
	if json.Unmarshal(body, &s) != nil {
		return
	}
	m.mu.Lock()
	delete(m.alive, s.JTI)
	m.mu.Unlock()
}

// RevokeUser 强制指定用户全部会话失效。
func (m *SessionManager) RevokeUser(userID int64) {
	// 简化实现：清空白名单。管理员规模很小，可接受。
	m.mu.Lock()
	m.alive = map[string]int64{}
	m.mu.Unlock()
}

func (m *SessionManager) sign(payload string) string {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandomToken 生成随机令牌明文。
func RandomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenHash 计算令牌摘要（仅存哈希）。
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
