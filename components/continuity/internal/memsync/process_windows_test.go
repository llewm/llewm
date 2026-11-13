//go:build windows

package memsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The helper process runs the test binary in one of these roles, set by
// memsyncProcessHelperEnv, writing its observations into the directory named
// by memsyncProcessHelperDir.
const (
	memsyncProcessHelperEnv = "LLEWM_MEMSYNC_PROCESS_HELPER"
	memsyncProcessHelperDir = "LLEWM_MEMSYNC_PROCESS_HELPER_DIR"
)

// TestProcessTreeHelperProcess is not a test: it is the body of the helper
// processes the tests below start.
//
//   - root starts an intermediate, waits for it to exit, writes "ready" and
//     sleeps, standing in for a canceled Git.
//   - intermediate writes its own ID to "intermediate", starts a sleeper that
//     writes its ID to "grandchild", and exits, orphaning the sleeper.
//   - sleep writes its ID to the file named by its argument and sleeps.
func TestProcessTreeHelperProcess(t *testing.T) {
	t.Parallel()
	role := os.Getenv(memsyncProcessHelperEnv)
	if role == "" {
		return
	}
	dir := os.Getenv(memsyncProcessHelperDir)
	switch role {
	case "root":
		intermediate := helperCommand(context.Background(), dir, "intermediate")
		if err := intermediate.Run(); err != nil {
			os.Exit(3)
		}
		writeHelperFile(dir, "ready", "ok")
	case "intermediate":
		writeHelperFile(dir, "intermediate", strconv.Itoa(os.Getpid()))
		sleeper := helperCommand(context.Background(), dir, "sleep", "grandchild")
		if err := sleeper.Start(); err != nil {
			os.Exit(3)
		}
		waitHelperFile(dir, "grandchild")
		os.Exit(0)
	case "sleep":
		writeHelperFile(dir, os.Args[len(os.Args)-1], strconv.Itoa(os.Getpid()))
	}
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}

func helperCommand(ctx context.Context, dir, role string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestProcessTreeHelperProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), memsyncProcessHelperEnv+"="+role, memsyncProcessHelperDir+"="+dir)
	return command
}

func writeHelperFile(dir, name, value string) {
	temporary := filepath.Join(dir, name+".tmp")
	if os.WriteFile(temporary, []byte(value), 0o600) != nil || os.Rename(temporary, filepath.Join(dir, name)) != nil {
		os.Exit(3)
	}
}

func waitHelperFile(dir, name string) string {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

// openHelper opens the live helper whose ID was written to name. The helper
// writes its ID and then sleeps, so the ID names it when it is opened.
func openHelper(t *testing.T, dir, name string) windows.Handle {
	t.Helper()
	value := waitHelperFile(dir, name)
	pid, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		t.Fatalf("helper %s did not report its process ID (%q)", name, value)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open helper %s (%d): %v", name, pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(handle, 1)
		windows.CloseHandle(handle)
	})
	return handle
}

func exited(handle windows.Handle, wait time.Duration) bool {
	event, err := windows.WaitForSingleObject(handle, uint32(wait/time.Millisecond))
	return err == nil && event == windows.WAIT_OBJECT_0
}

// startTree starts the root helper as a canceled Git would run: through
// gitCommand with stopGently's process tree.
func startTree(t *testing.T, ctx context.Context, dir string) *gitCommand {
	t.Helper()
	command := &gitCommand{Cmd: helperCommand(ctx, dir, "root")}
	command.tree = stopGently(command.Cmd)
	command.WaitDelay = 3 * time.Second
	if err := command.Start(); err != nil {
		t.Fatalf("start process tree: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	if waitHelperFile(dir, "ready") == "" {
		t.Fatal("process tree never became ready")
	}
	return command
}

// A helper whose parent has exited is still a descendant of Git and must end
// with it; its stale parent ID cannot lead an ID walk back to Git.
func TestStopGentlyTerminatesOrphanedDescendant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := startTree(t, ctx, dir)
	grandchild := openHelper(t, dir, "grandchild")
	if exited(grandchild, 0) {
		t.Fatal("grandchild exited before cancellation")
	}

	cancel()
	started := time.Now()
	_ = command.Wait()
	if !exited(grandchild, 10*time.Second) {
		t.Fatal("orphaned grandchild survived cancellation of its Git")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

// A process outside the tree survives cancellation even when it started after
// Git and reuses the ID of a descendant that already exited, the replacement a
// process-ID walk would have terminated.
func TestStopGentlyLeavesUnrelatedProcessesRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := startTree(t, ctx, dir)
	stale := waitHelperFile(dir, "intermediate")
	grandchild := openHelper(t, dir, "grandchild")

	// Start unrelated sleepers until one takes the exited intermediate's ID.
	// Reuse is likely but not guaranteed; the survival check holds either way.
	var unrelated []windows.Handle
	reused := false
	for attempt := 0; attempt < 64 && !reused; attempt++ {
		name := fmt.Sprintf("unrelated-%d", attempt)
		if err := helperCommand(context.Background(), dir, "sleep", name).Start(); err != nil {
			t.Fatalf("start unrelated process: %v", err)
		}
		reused = waitHelperFile(dir, name) == stale
		handle := openHelper(t, dir, name)
		if reused || len(unrelated) == 0 {
			unrelated = append(unrelated, handle)
		} else {
			_ = windows.TerminateProcess(handle, 1)
		}
	}
	t.Logf("unrelated process reused the exited descendant's ID: %t", reused)

	cancel()
	_ = command.Wait()
	if !exited(grandchild, 10*time.Second) {
		t.Fatal("descendant survived cancellation")
	}
	for _, handle := range unrelated {
		if exited(handle, 500*time.Millisecond) {
			t.Fatal("cancellation terminated a process outside Git's tree")
		}
	}
}

// Without cancellation the tree is only released, as a process group is.
func TestStopGentlyNormalExitReleasesTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	command := &gitCommand{Cmd: helperCommand(context.Background(), dir, "sleep", "self")}
	command.tree = stopGently(command.Cmd)
	if err := command.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if command.tree.job == 0 {
		t.Fatal("Git was not assigned to a job")
	}
	self := openHelper(t, dir, "self")
	_ = windows.TerminateProcess(self, 0)
	if err := command.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if command.tree.job != 0 {
		t.Fatal("job handle not released after Wait")
	}
}
