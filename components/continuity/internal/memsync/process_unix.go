//go:build unix

package memsync

import (
	"os/exec"
	"syscall"
)

// stopGently makes canceling command send SIGTERM to its whole process group,
// so Git and the transport helpers it starts (git-remote-https, ssh) all exit
// and remove their lock files. SIGKILL, the default, would leave locks behind
// and orphan the helpers, which hold Git's output open.
func stopGently(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
}
