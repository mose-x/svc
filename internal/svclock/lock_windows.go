//go:build windows

package svclock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows has no flock(2); LockFileEx over the whole file with a zero
// Overlapped (which makes the call synchronous) is the equivalent, and is the
// same shape shimmanager uses for shims.json.lock.

// lockFileNB takes an exclusive non-blocking lock. FAIL_IMMEDIATELY makes it
// return ERROR_LOCK_VIOLATION instead of waiting when the lock is held.
func lockFileNB(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		0xFFFFFFFF, 0xFFFFFFFF,
		&windows.Overlapped{},
	)
}

// lockFile takes an exclusive lock, blocking until it is granted.
func lockFile(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		0xFFFFFFFF, 0xFFFFFFFF,
		&windows.Overlapped{},
	)
}

// unlockFile releases the lock. Closing the handle alone would also release it,
// but unlocking explicitly mirrors the Unix flock/funlock pair.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		0xFFFFFFFF, 0xFFFFFFFF,
		&windows.Overlapped{},
	)
}

// isBusyErr distinguishes "someone else holds it" from "the call broke".
func isBusyErr(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
