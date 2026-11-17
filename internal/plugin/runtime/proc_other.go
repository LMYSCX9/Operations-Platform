//go:build !unix

package runtime

import (
	"os"
	"os/exec"
)

// Windows 无进程组信号语义，只能终止主进程；插件仍须自行清理子进程。
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
