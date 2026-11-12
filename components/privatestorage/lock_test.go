package privatestorage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockProcessHelper(t *testing.T) {
	path := os.Getenv("LLEWM_LOCK_TEST_PATH")
	if path == "" {
		return
	}
	f, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("locked")
	// Parent kills this process while the lock is still held.
	var input [1]byte
	os.Stdin.Read(input[:])
}

func TestLockCancellationAndCrashRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cursor.lock")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestLockProcessHelper$")
	cmd.Env = append(os.Environ(), "LLEWM_LOCK_TEST_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "locked" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child did not lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child startup timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = Lock(ctx, path)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy lock: %v", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	// The file persists; only the OS lock disappears after an unclean exit.
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	recovered, err := Lock(ctx, path)
	if err != nil {
		t.Fatalf("crashed process left a stale lock: %v", err)
	}
	recovered.Close()
}
