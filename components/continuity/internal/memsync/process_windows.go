//go:build windows

package memsync

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// processTree is the job object holding Git and every process it starts.
// Windows has no SIGTERM or process-group signal, and the default cancellation
// kills only git.exe, orphaning transport helpers (git-remote-https, ssh) that
// keep the remote connection and Git's output open.
//
// Membership is tracked by the kernel, never by process ID: a descendant joins
// the job when it is created, stays in it after its parent exits, and an
// unrelated process cannot join it however IDs are recycled. Git starts
// suspended and runs only once it is in the job, so nothing it starts escapes.
type processTree struct {
	command *exec.Cmd
	mu      sync.Mutex
	job     windows.Handle // zero when no job holds the command
}

// stopGently makes canceling command terminate Git and all its descendants.
func stopGently(command *exec.Cmd) *processTree {
	tree := &processTree{command: command}
	command.Cancel = tree.terminate
	return tree
}

// start starts command suspended, assigns it to a new job and then resumes it.
// Where no job can be created or assigned, Git still runs and cancellation
// falls back to killing git.exe alone, as before. A Git that cannot be resumed
// is killed and reaped rather than left suspended.
func (t *processTree) start(command *exec.Cmd) error {
	if t == nil {
		return command.Start()
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return command.Start()
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := command.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}
	// exec.Cmd holds the process handle, so this ID names Git until it is reaped.
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|processSuspendResume, false, uint32(command.Process.Pid))
	if err == nil {
		defer windows.CloseHandle(process)
		if windows.AssignProcessToJobObject(job, process) == nil {
			t.mu.Lock()
			t.job = job
			t.mu.Unlock()
		} else {
			windows.CloseHandle(job)
		}
		err = resumeProcess(process)
	} else {
		windows.CloseHandle(job)
	}
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.release()
		return fmt.Errorf("start Git: resume suspended process: %w", err)
	}
	return nil
}

// terminate ends every process in the job, or git.exe alone without one.
func (t *processTree) terminate() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 && windows.TerminateJobObject(t.job, 1) == nil {
		return nil
	}
	return t.command.Process.Kill()
}

// release closes the job once Git is reaped. Like a process group on unix, a
// normal exit leaves any remaining member running; only cancellation ends them.
func (t *processTree) release() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

// processSuspendResume is PROCESS_SUSPEND_RESUME, which x/sys does not name.
const processSuspendResume = 0x0800

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// resumeProcess resumes every thread of the suspended process through its own
// handle. It reads no thread IDs, so a recycled ID cannot resume another
// process, and it avoids a system-wide thread snapshot on every Git start.
func resumeProcess(process windows.Handle) error {
	if err := procNtResumeProcess.Find(); err != nil {
		return err
	}
	if status, _, _ := procNtResumeProcess.Call(uintptr(process)); status != 0 {
		return windows.NTStatus(status)
	}
	return nil
}
