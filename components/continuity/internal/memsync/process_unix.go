//go:build unix

package memsync

import (
	"os/exec"
	"syscall"
)

// processTree is the process group stopGently configured; the kernel tracks
// membership, so starting and releasing need nothing beyond the command.
type processTree struct{}

// stopGently makes canceling command send SIGTERM to its whole process group,
// so Git and the transport helpers it starts (git-remote-https, ssh) all exit
// and remove their lock files. SIGKILL, the default, would leave locks behind
// and orphan the helpers, which hold Git's output open.
func stopGently(command *exec.Cmd) *processTree {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
	return &processTree{}
}

func (t *processTree) start(command *exec.Cmd) error { return command.Start() }

func (t *processTree) release() {}
