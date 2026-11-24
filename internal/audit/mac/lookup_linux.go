//go:build linux

package mac

import (
	"os"
	"strings"
)

// lookupOS 解析 /proc/net/arp（IPv4）。
func lookupOS(ip string) (string, string) {
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return "", "none"
	}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 {
			continue // 表头
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != ip {
			continue
		}
		flags := fields[2]
		if flags != "0x2" { // 0x2 = ATF_COM，条目有效
			continue
		}
		hw := strings.ToUpper(fields[3])
		if hw == "00:00:00:00:00:00" {
			continue
		}
		return hw, "arp"
	}
	return "", "none"
}
