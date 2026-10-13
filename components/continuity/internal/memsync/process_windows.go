//go:build windows

package memsync

import "os/exec"

// stopGently keeps the default cancellation on Windows, which has no SIGTERM;
// WaitDelay still bounds how long a canceled Git holds its output open.
func stopGently(command *exec.Cmd) {}
