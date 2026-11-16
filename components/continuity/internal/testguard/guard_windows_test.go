package testguard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("LLEWM_TEST_GUARD_CHILD") == "1" {
		os.Exit(Run(m))
	}
	os.Exit(m.Run())
}

func TestEntrypointRequiresExplicitCount(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		args        []string
		wantFailure bool
	}{
		{"default count is not explicit", []string{"-test.run=^$"}, true},
		{"focused explicit count", []string{"-test.run=^$", "-test.count=1", "-test.timeout=1s"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			for _, entry := range os.Environ() {
				key := strings.SplitN(entry, "=", 2)[0]
				if !strings.EqualFold(key, override) && !strings.EqualFold(key, "LLEWM_TEST_GUARD_CHILD") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LLEWM_TEST_GUARD_CHILD=1")
			output, err := cmd.CombinedOutput()
			if tt.wantFailure {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || !strings.Contains(string(output), "explicit -count") {
					t.Fatalf("guard: %s %v", output, err)
				}
			} else if err != nil {
				t.Fatalf("focused run: %s %v", output, err)
			}
		})
	}
}
