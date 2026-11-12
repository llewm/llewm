//go:build windows

package memsync

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// backgroundCreationFlags start the background process in its own process
// group with a console that has no window. DETACHED_PROCESS would leave it with
// no console at all, and Windows then opens a new visible console window for
// every console program it starts, such as each git.exe and Git's transport
// helpers. With a windowless console of its own, those children inherit it and
// no window appears.
const backgroundCreationFlags = windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW

// StartBackground launches `llewm sync` for root detached from the caller,
// so hooks and tools never wait on the network. Its outcome is recorded as a
// sync attempt like any other.
func StartBackground(executable, root string) error {
	return StartBackgroundCommand(executable, root, "sync")
}

// StartBackgroundCommand launches another continuity command for root the same
// detached way, such as a usage catch-up at session start.
func StartBackgroundCommand(executable, root string, args ...string) error {
	command := backgroundCommand(executable, append([]string{"--project-root", root}, args...)...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func backgroundCommand(executable string, args ...string) *exec.Cmd {
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: backgroundCreationFlags}
	return command
}
