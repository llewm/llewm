//go:build windows

package gitenv

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

var procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// hideConsole keeps Git from opening a visible console window when the caller
// has none of its own, as in a background sync or a hook started without one.
// Windows gives a console program started from such a process a new console,
// visible by default, for every Git command and transport helper. A caller
// with a console window, such as an interactive terminal, keeps the default,
// so Git shares that console and can still prompt on it.
func hideConsole(command *exec.Cmd) {
	if hasConsoleWindow() {
		return
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// hasConsoleWindow reports whether this process's console has a window,
// hidden or not. A process with no console, or a windowless one, has none.
func hasConsoleWindow() bool {
	if procGetConsoleWindow.Find() != nil {
		return true
	}
	window, _, _ := procGetConsoleWindow.Call()
	return window != 0
}
