//go:build linux

package recordsync

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(info os.FileInfo) (string, bool) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d", s.Dev, s.Ino, s.Mode, s.Size, s.Mtim.Sec, s.Mtim.Nsec, s.Ctim.Sec, s.Ctim.Nsec), true
}
