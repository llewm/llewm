//go:build !windows

package permtest

import (
	"os"
	"strconv"
)

func expose(path string) error { return os.Chmod(path, 0o644) }

func deny(path string) (func() error, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0); err != nil {
		return nil, err
	}
	return func() error { return os.Chmod(path, info.Mode().Perm()) }, nil
}

func privilege() string { return "euid " + strconv.Itoa(os.Geteuid()) }
