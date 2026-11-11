package privatestorage

import (
	"context"
	"errors"
	"os"
	"time"
)

// Lock serializes cooperating writers to a private file until ctx expires.
// Closing the returned file (including process exit) releases the OS lock.
// The file must remain in place, so waiters always lock the same object.
func Lock(ctx context.Context, path string) (*os.File, error) {
	f, err := Create(path)
	if errors.Is(err, os.ErrExist) {
		f, err = Open(path)
	}
	if err != nil {
		return nil, err
	}
	if err = CheckFile(f); err != nil {
		f.Close()
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		busy, lockErr := tryLock(f)
		if lockErr != nil {
			f.Close()
			return nil, lockErr
		}
		if !busy {
			return f, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
