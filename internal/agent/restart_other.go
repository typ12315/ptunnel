//go:build !linux && !darwin

package agent

import "fmt"

// supportsSelfUpgrade 其他平台（如 Windows）无法替换正在运行的二进制。
func supportsSelfUpgrade() bool { return false }

// restartSelf 在不受支持的平台上直接返回错误。
func restartSelf(_ string) error {
	return fmt.Errorf("当前平台不支持进程内重启")
}
