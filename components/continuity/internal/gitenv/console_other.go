//go:build !windows

package gitenv

import "os/exec"

// hideConsole is needed only on Windows, where console programs started from
// a process without a console window would each open a new one.
func hideConsole(*exec.Cmd) {}
