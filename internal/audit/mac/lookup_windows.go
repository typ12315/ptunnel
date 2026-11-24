//go:build windows

package mac

import (
	"os/exec"
	"regexp"
	"strings"
)

var macPattern = regexp.MustCompile(`(?i)\b([0-9a-f]{2}[-:]){5}[0-9a-f]{2}\b`)

// lookupOS 使用 arp -a 查询邻居表（Windows 无需 CGO）。
func lookupOS(ip string) (string, string) {
	cmd := exec.Command("arp", "-a", ip)
	out, err := cmd.Output()
	if err != nil {
		return "", "none"
	}
	return parseArpOutput(string(out), ip), "arp"
}

func parseArpOutput(out, ip string) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, ip) {
			continue
		}
		if m := macPattern.FindString(line); m != "" {
			return strings.ToUpper(strings.ReplaceAll(m, "-", ":"))
		}
	}
	return ""
}
