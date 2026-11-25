// Package svclock serializes the svc operations that mutate shared state.
//
// config.json, shims.json and the shell rc are all read-modify-write. shims.json
// already has a sidecar lock inside shimmanager, but config.json is guarded only
// by an in-process sync.RWMutex: with the GUI open and `svc install` running in a
// terminal, the second writer silently clobbers the first. This package is the
// cross-process answer -- one coarse operation lock per SVC dir, taken by every
// mutating command in both shells.
//
// Coarse on purpose. svc mutations are infrequent and users expect them
// serialized; per-file locking would need a lock ordering and a deadlock story
// for no practical gain.
//
// The lock is advisory, exactly like shimmanager's: it holds because every
// writer goes through it. Read-only commands must NOT take it, so `svc list`
// next to a long install still answers instantly.
//
// It is also NOT re-entrant. Each acquisition opens its own file description,
// so a second Acquire/TryAcquire in the same process contends with the first
// instead of joining it -- blocking forever under Acquire, or reporting ErrBusy
// under TryAcquire (TestTryAcquireIsMutuallyExclusive pins this). Shells must
// therefore take the lock once per user-visible operation, at the outermost
// entry point, and never around an inner call that takes it again.
package svclock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName is the sidecar lock file inside the SVC dir. A dedicated file
// rather than config.json itself, for the same reason shimmanager locks
// shims.json.lock: config.json is replaced atomically via rename, so a lock
// held on the old inode would not exclude a writer renaming over it.
const LockFileName = "svc.lock"

// ErrBusy reports that another svc process holds the operation lock. Shells map
// it to their "operation already running" exit code; `--no-wait` surfaces it
// immediately instead of blocking.
var ErrBusy = errors.New("another svc operation is in progress")

// Lock is a held operation lock. The zero value is not usable; obtain one from
// TryAcquire or Acquire. Release is safe to call more than once, so a deferred
// release can sit next to an explicit early one.
type Lock struct {
	file     *os.File
	released bool
}

// LockPath returns the lock file path for a SVC dir. Pure, so the layout is
// testable without touching the filesystem.
func LockPath(dir string) string {
	return filepath.Join(dir, LockFileName)
}

// IsBusy reports whether err is (or wraps) ErrBusy.
func IsBusy(err error) bool { return errors.Is(err, ErrBusy) }

// TryAcquire takes the lock without waiting. When another process holds it, the
// returned error wraps ErrBusy -- check with IsBusy, since it carries the lock
// path for the user-facing message.
func TryAcquire(dir string) (*Lock, error) {
	file, err := openLockFile(dir)
	if err != nil {
		return nil, err
	}
	if err := lockFileNB(file); err != nil {
		file.Close()
		if isBusyErr(err) {
			return nil, fmt.Errorf("%w (lock file %s)", ErrBusy, LockPath(dir))
		}
		return nil, fmt.Errorf("svclock: lock %s: %w", LockPath(dir), err)
	}
	return &Lock{file: file}, nil
}

// Acquire takes the lock, waiting for as long as it takes. onWait, when
// non-nil, runs exactly once and only if the lock is genuinely contended, so a
// shell can explain the pause without printing anything on the fast path.
func Acquire(dir string, onWait func()) (*Lock, error) {
	lock, err := TryAcquire(dir)
	if err == nil {
		return lock, nil
	}
	if !IsBusy(err) {
		return nil, err
	}
	if onWait != nil {
		onWait()
	}

	file, err := openLockFile(dir)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("svclock: wait for %s: %w", LockPath(dir), err)
	}
	return &Lock{file: file}, nil
}

// Release drops the lock and closes the file. Calls after the first are no-ops
// returning nil.
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("svclock: unlock: %w", err)
	}
	return closeErr
}

// With runs fn while holding the lock and always releases it afterwards. Both
// shells should prefer this form: a panic or an early return inside fn cannot
// leave the lock held for the lifetime of the process.
func With(dir string, onWait func(), fn func() error) error {
	lock, err := Acquire(dir, onWait)
	if err != nil {
		return err
	}
	defer lock.Release()
	return fn()
}

// openLockFile creates the SVC dir if needed and opens the sidecar lock file.
// The dir is created rather than required so a first-run `svc install` does not
// have to race the GUI to make ~/.svc exist.
func openLockFile(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("svclock: create %s: %w", dir, err)
	}
	file, err := os.OpenFile(LockPath(dir), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("svclock: open %s: %w", LockPath(dir), err)
	}
	return file, nil
}
