//go:build !windows

package svclock

import (
	"errors"
	"os"
	"syscall"
)

// lockFileNB takes an exclusive non-blocking flock, the same primitive
// shimmanager uses for shims.json.lock. It fails with EWOULDBLOCK/EAGAIN when
// another process (or another open file description in this one) holds it.
func lockFileNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockFile takes an exclusive flock, blocking until it is granted.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the flock.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// isBusyErr distinguishes "someone else holds it" from "the call broke". Both
// constants are checked because their equality is a per-platform property, not
// a Go guarantee.
func isBusyErr(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
