//go:build unix

package memsync

import (
	"os/exec"
	"syscall"
)

// StartBackground launches `llewm sync` for root detached from the caller,
// so hooks and tools never wait on the network. Its outcome is recorded as a
// sync attempt like any other.
func StartBackground(executable, root string) error {
	return StartBackgroundCommand(executable, root, "sync")
}

// StartBackgroundCommand launches another continuity command for root the same
// detached way, such as a usage catch-up at session start.
func StartBackgroundCommand(executable, root string, args ...string) error {
	command := exec.Command(executable, append([]string{"--project-root", root}, args...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
