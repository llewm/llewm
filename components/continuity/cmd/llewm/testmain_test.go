package main

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/testguard"
)

// MCP scenarios reuse this executable as a server, with lifetime bounded by
// their context. Keep the child invocation compatible with the test guard.
func mcpTestCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$", "-test.count=1", "-test.timeout=0")
}

// TestMain keeps hook tests from starting real detached processes: the test
// binary is os.Executable, and a session start would launch it again.
func TestMain(m *testing.M) {
	startBackgroundUsage = func(string, string) error { return nil }
	os.Exit(testguard.Run(m))
}
