//go:build !windows && !linux

package mac

// lookupOS 其他平台暂不支持邻居表查询。
func lookupOS(string) (string, string) { return "", "none" }
