package memsync

import (
	"os"
	"testing"
)

// TestMain isolates every test from the engineer's global and system Git
// configuration once for the whole package, before any test runs: memory
// commits must work without a global identity. A per-test t.Setenv would
// forbid t.Parallel; tests that need other values still set them with t.Setenv
// and stay serial, and Go runs them before resuming the parallel tests.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}
