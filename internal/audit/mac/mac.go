// Package mac 尽力从系统邻居表获取 IP 对应的 MAC 地址。
// 注意：仅当对端与服务端处于同一二层网络时才可能取到。
package mac

import (
	"sync"
	"time"
)

type entry struct {
	mac    string
	source string
	expire time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]entry{}
	ttl   = 60 * time.Second
)

// Lookup 返回 MAC 与来源（arp/ndp/none），结果缓存 60 秒。
func Lookup(ip string) (string, string) {
	if ip == "" {
		return "", "none"
	}
	if hw, source, ok := LookupCached(ip); ok {
		return hw, source
	}

	hw, source := lookupOS(ip)
	if hw == "" {
		source = "none"
	}
	mu.Lock()
	cache[ip] = entry{mac: hw, source: source, expire: time.Now().Add(ttl)}
	mu.Unlock()
	return hw, source
}

// LookupCached 仅返回未过期的缓存结果；ok=false 表示需要重新查询。
func LookupCached(ip string) (string, string, bool) {
	if ip == "" {
		return "", "none", true
	}
	mu.Lock()
	defer mu.Unlock()
	if e, ok := cache[ip]; ok && time.Now().Before(e.expire) {
		return e.mac, e.source, true
	}
	return "", "", false
}
