package main

import (
	"os"
	"testing"
)

// TestMain keeps hook tests from starting real detached processes: the test
// binary is os.Executable, and a session start would launch it again.
func TestMain(m *testing.M) {
	startBackgroundUsage = func(string, string) error { return nil }
	os.Exit(m.Run())
}
