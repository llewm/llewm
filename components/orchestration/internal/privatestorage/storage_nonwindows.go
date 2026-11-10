//go:build !windows

package privatestorage

import (
	"errors"
	"os"
)

func MkdirAll(path string) error { return os.MkdirAll(path, 0700) }

func CheckDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("database directory must be private (mode 0700)")
	}
	return nil
}

func Create(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func Open(path string) (*os.File, error) { return os.Open(path) }

func checkFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("file must be a private regular file (mode 0600)")
	}
	return nil
}
