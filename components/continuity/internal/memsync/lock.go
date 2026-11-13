package memsync

import "os"

// fileLock owns a file and its OS lock. Its Unix Close explicitly unlocks
// because a concurrent fork can retain the description until the child execs.
type fileLock struct {
	*os.File
}
