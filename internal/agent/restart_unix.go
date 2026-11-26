//go:build linux || darwin

package agent

import (
	"os"
	"syscall"
)

// supportsSelfUpgrade 这些平台支持替换运行中的二进制并原地重启进程。
func supportsSelfUpgrade() bool { return true }

// restartSelf 以新二进制替换当前进程镜像，PID 保持不变。
func restartSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
