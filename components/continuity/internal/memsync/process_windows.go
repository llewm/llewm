//go:build windows

package memsync

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stopGently makes canceling command end Git and every process it started.
// Windows has no SIGTERM or process-group signal, and the default cancellation
// kills only git.exe, orphaning transport helpers (git-remote-https, ssh) that
// keep the remote connection and Git's output open. Git is killed first so it
// starts nothing new, then its descendants are terminated.
func stopGently(command *exec.Cmd) {
	command.Cancel = func() error {
		err := command.Process.Kill()
		terminateDescendants(uint32(command.Process.Pid))
		return err
	}
}

// terminateDescendants terminates the processes descending from root, found
// by parent process ID. exec.Cmd still holds root's handle, so its ID cannot be
// reused; a descendant must also have started no earlier than root, so a
// recycled ID of an exited intermediate process is never followed to an
// unrelated older process. A second pass catches a helper started by a
// descendant while the first pass ran. Failures are ignored: Git's own exit
// and WaitDelay still bound the command.
func terminateDescendants(root uint32) {
	rootStart, ok := processStart(root)
	if !ok {
		return
	}
	for pass := 0; pass < 2; pass++ {
		for _, pid := range descendants(root) {
			handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				continue
			}
			var creation, exit, kernel, user windows.Filetime
			if windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user) == nil && creation.Nanoseconds() >= rootStart {
				_ = windows.TerminateProcess(handle, 1)
			}
			windows.CloseHandle(handle)
		}
	}
}

// processStart returns the creation time of pid in nanoseconds.
func processStart(pid uint32) (int64, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	return creation.Nanoseconds(), true
}

// descendants lists the IDs of every process below root in one snapshot.
func descendants(root uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	children := map[uint32][]uint32{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID != entry.ParentProcessID {
			children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		}
	}
	var found []uint32
	seen := map[uint32]bool{root: true}
	for queue := []uint32{root}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if !seen[child] {
				seen[child] = true
				found = append(found, child)
				queue = append(queue, child)
			}
		}
	}
	return found
}
