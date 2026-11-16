// Package testguard catches costly Windows test invocations before fixtures run.
// It is used only by test entry points, never by the installed application.
package testguard

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

const override = "LLEWM_TEST_ALLOW_UNSAFE_WINDOWS"

// Run parses testing flags before checking the known long-running packages.
// Package concurrency (-p) belongs to the go command and is not visible here.
func Run(m *testing.M) int {
	if !flag.Parsed() {
		flag.Parse()
	}
	countSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "test.count" {
			countSet = true
		}
	})
	timeout, err := time.ParseDuration(flag.Lookup("test.timeout").Value.String())
	if err == nil {
		err = check(runtime.GOOS, countSet, flag.Lookup("test.run").Value.String(), flag.Lookup("test.list").Value.String(), timeout, os.Getenv(override) == "1")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Windows test invocation: %v\nUse go test ./... -count=1 -timeout=40m -p=2, or root task test. Focused -run tests may use shorter timeouts. For a deliberate diagnostic only, set %s=1 for that invocation.\n", err, override)
		return 2
	}
	return m.Run()
}

func check(goos string, countSet bool, run, list string, timeout time.Duration, allow bool) error {
	if goos != "windows" || allow || list != "" {
		return nil
	}
	if !countSet {
		return fmt.Errorf("explicit -count is required to disable the expensive test-result cache")
	}
	if (run == "" || run == "." || run == ".*") && timeout > 0 && timeout < 40*time.Minute {
		return fmt.Errorf("the full suite needs -timeout=40m or longer (or an explicit unlimited timeout)")
	}
	return nil
}
