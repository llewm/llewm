//go:build !windows

package coordination

import (
	"os"
	"testing"
)

func makeStoragePublic(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
}
