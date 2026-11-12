//go:build windows

package memsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const backgroundHelperEnv = "LLEWM_MEMSYNC_BACKGROUND_HELPER"

var procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// TestBackgroundHelperProcess is not a test: started as a background command,
// it records whether it has a console and whether that console has a window.
func TestBackgroundHelperProcess(t *testing.T) {
	report := os.Getenv(backgroundHelperEnv)
	if report == "" {
		return
	}
	_, consoleErr := windows.GetConsoleCP()
	window, _, _ := procGetConsoleWindow.Call()
	state := "console=" + boolWord(consoleErr == nil) + " window=" + boolWord(window != 0)
	_ = os.WriteFile(report+".tmp", []byte(state), 0o600)
	_ = os.Rename(report+".tmp", report)
	os.Exit(0)
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// A background command has a console of its own without a window, so the
// console programs it starts (git.exe and its helpers) inherit that console
// instead of each opening a new visible one, as they did under DETACHED_PROCESS.
func TestBackgroundCommandHasAWindowlessConsole(t *testing.T) {
	report := filepath.Join(t.TempDir(), "state")
	command := backgroundCommand(os.Args[0], "-test.run=^TestBackgroundHelperProcess$")
	command.Env = append(os.Environ(), backgroundHelperEnv+"="+report)
	if err := command.Start(); err != nil {
		t.Fatalf("start background helper: %v", err)
	}
	defer command.Wait()
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(report)
		if err == nil {
			if got := strings.TrimSpace(string(data)); got != "console=yes window=no" {
				t.Fatalf("background process reported %q, want a console without a window", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background helper never reported")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
