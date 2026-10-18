//go:build windows

package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lock(ctx context.Context, filename string) (func(), error) {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("lock Wok browser cache: %w", err)
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("Wok browser cache lock is not a regular file")
	}
	handle := windows.Handle(f.Fd())
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
		if err == nil {
			return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, new(windows.Overlapped)); _ = f.Close() }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			f.Close()
			return nil, fmt.Errorf("lock Wok browser cache: %w", err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
