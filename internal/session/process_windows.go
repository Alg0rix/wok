//go:build windows

package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// detach starts the child outside this console's process group, so closing
// the terminal or pressing Ctrl+C does not stop the browser or controller.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
}

// connectionRefused also accepts Winsock's refusal code, which does not
// match syscall.ECONNREFUSED on Windows.
func connectionRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED)
}

// Lock serializes commands within a profile. The OS releases it on process exit.
func (s *Store) Lock(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	handle := windows.Handle(f.Fd())
	for {
		overlapped := new(windows.Overlapped)
		err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, new(windows.Overlapped)); _ = f.Close() }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("profile busy: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
