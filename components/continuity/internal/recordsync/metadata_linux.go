//go:build linux

package recordsync

import (
	"os"
	"syscall"
)

func fileIdentity(info os.FileInfo) ([8]uint64, bool) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [8]uint64{}, false
	}
	return [8]uint64{s.Dev, s.Ino, uint64(s.Mode), uint64(s.Size), uint64(s.Mtim.Sec), uint64(s.Mtim.Nsec), uint64(s.Ctim.Sec), uint64(s.Ctim.Nsec)}, true
}
