//go:build windows

package gitenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

const consoleHelperEnv = "LLEWM_GITENV_CONSOLE_HELPER"

// TestConsoleHelperProcess is not a test: it reports whether Command, built in
// a process started with the flags under test, would hide Git's console.
func TestConsoleHelperProcess(t *testing.T) {
	if os.Getenv(consoleHelperEnv) == "" {
		return
	}
	command := Command(context.Background(), "--version")
	hidden := command.SysProcAttr != nil && command.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW != 0
	fmt.Printf("hidden=%t\n", hidden)
	os.Exit(0)
}

// Git hides its console exactly when the calling process has no console
// window: with no console (a DETACHED_PROCESS background sync) or a windowless
// one. A caller with a console window, even a hidden one, shares it with Git,
// so an interactive Git can still prompt.
func TestCommandHidesConsoleOnlyWithoutAConsoleWindow(t *testing.T) {
	for _, test := range []struct {
		name   string
		attr   *syscall.SysProcAttr
		hidden bool
	}{
		{"no console", &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}, true},
		{"windowless console", &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}, true},
		{"hidden console window", &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			helper := exec.Command(os.Args[0], "-test.run=^TestConsoleHelperProcess$")
			helper.Env = append(os.Environ(), consoleHelperEnv+"=1")
			helper.SysProcAttr = test.attr
			output, err := helper.Output()
			if err != nil {
				t.Fatalf("helper: %v", err)
			}
			if got, want := strings.TrimSpace(string(output)), fmt.Sprintf("hidden=%t", test.hidden); got != want {
				t.Fatalf("helper reported %q, want %q", got, want)
			}
		})
	}
}
