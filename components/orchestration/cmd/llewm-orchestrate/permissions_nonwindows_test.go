//go:build !windows

package main

import (
	"os"
	"testing"
)

func setTokenPrivate(t *testing.T, path string, private bool) {
	t.Helper()
	mode := os.FileMode(0644)
	if private {
		mode = 0600
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
