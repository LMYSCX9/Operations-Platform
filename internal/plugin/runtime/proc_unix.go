//go:build unix

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup 让插件独立成组，宿主终止时能连同其子进程一起回收（plugin-spec/02 §6）。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
