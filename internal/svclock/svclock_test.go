package svclock

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"svc/internal/helpers"
)

func TestLockPath(t *testing.T) {
	got := LockPath(filepath.Join("home", ".svc"))
	want := filepath.Join("home", ".svc", "svc.lock")
	if got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
	if got := LockPath(""); got != "svc.lock" {
		t.Errorf("LockPath(\"\") = %q, want the bare file name", got)
	}
}

func TestIsBusy(t *testing.T) {
	if IsBusy(nil) {
		t.Error("IsBusy(nil) = true, want false")
	}
	if !IsBusy(ErrBusy) {
		t.Error("IsBusy(ErrBusy) = false, want true")
	}
	if !IsBusy(fmt.Errorf("outer: %w", ErrBusy)) {
		t.Error("IsBusy must see through wrapping")
	}
	if IsBusy(io.ErrUnexpectedEOF) {
		t.Error("IsBusy(unrelated error) = true, want false")
	}
}

func TestTryAcquireRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", ".svc")

	lock, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if info, err := os.Stat(LockPath(dir)); err != nil || info.IsDir() {
		t.Errorf("the lock file was not created at %s: %v", LockPath(dir), err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}

	// The same lock must be takeable again straight after a release.
	again, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestTryAcquireIsMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}

	// A second open file description in the same process is a different lock
	// owner on both flock(2) and LockFileEx, so this is a real contention test
	// and not just a re-entrancy check.
	_, err = TryAcquire(dir)
	if !IsBusy(err) {
		t.Errorf("second TryAcquire while held = %v, want ErrBusy", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire after the holder released: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestTryAcquireReportsRealErrors(t *testing.T) {
	// A regular file where the SVC dir should be: MkdirAll fails. That is a
	// filesystem error, not contention, and must not be reported as busy --
	// a shell would otherwise tell the user to retry later.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := TryAcquire(filepath.Join(blocker, ".svc"))
	if err == nil {
		t.Fatal("TryAcquire through a blocked path = nil error, want an error")
	}
	if IsBusy(err) {
		t.Errorf("a filesystem failure was reported as busy: %v", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	lock, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second Release = %v, want nil", err)
	}

	// A nil *Lock must be releasable so `defer lock.Release()` is safe even on
	// the path where Acquire failed and returned nil.
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Errorf("(*Lock)(nil).Release() = %v, want nil", err)
	}
}

func TestAcquireSkipsOnWaitWhenUncontended(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir, func() {
		t.Error("onWait ran for an uncontended lock; it must only fire on a real wait")
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestAcquireWaitsForAContendedLock(t *testing.T) {
	dir := t.TempDir()
	first, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	// Atomic because the counter is written from the acquiring goroutine and
	// read from the test goroutine; -race would flag a plain int.
	var waitCalls atomic.Int32
	acquired := make(chan error, 1)
	go func() {
		lock, err := Acquire(dir, func() { waitCalls.Add(1) })
		if err == nil {
			err = lock.Release()
		}
		acquired <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for waitCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if waitCalls.Load() != 1 {
		t.Errorf("onWait ran %d times, want exactly 1", waitCalls.Load())
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Errorf("the waiting Acquire failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Acquire never returned after the holder released")
	}
}

func TestWithReleasesOnEveryPath(t *testing.T) {
	dir := t.TempDir()

	sentinel := fmt.Errorf("operation failed")
	if err := With(dir, nil, func() error { return nil }); err != nil {
		t.Errorf("With on success = %v, want nil", err)
	}
	if err := With(dir, nil, func() error { return sentinel }); err != sentinel {
		t.Errorf("With did not propagate the fn error: got %v, want %v", err, sentinel)
	}

	// Both runs must have released, or these would report busy.
	for i := 0; i < 2; i++ {
		lock, err := TryAcquire(dir)
		if err != nil {
			t.Fatalf("TryAcquire %d after With returned: %v", i, err)
		}
		if err := lock.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	}

	// A panic inside fn must not leave the lock held for the process lifetime.
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("With swallowed the panic instead of letting it through")
			}
		}()
		_ = With(dir, nil, func() error { panic("boom") })
	}()
	lock, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire after a panicking With: %v", err)
	}
	lock.Release()

	// With must surface contention from Acquire rather than running fn.
	held, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	ran := false
	done := make(chan error, 1)
	go func() {
		done <- With(dir, nil, func() error { ran = true; return nil })
	}()
	time.Sleep(100 * time.Millisecond)
	held.Release()
	if err := <-done; err != nil {
		t.Errorf("With after the holder released: %v", err)
	}
	if !ran {
		t.Error("With never ran fn once the lock became available")
	}
}

// TestConcurrentWritersDoNotLoseUpdates is the scenario the package exists for:
// many processes doing read-modify-write on one file, which is exactly how
// config.json's active-version map is updated. Without the lock the final count
// comes up short; with it, every increment survives. Run under -race.
func TestConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.WriteFile(target, []byte("0"), 0644); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	const increments = 25

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < increments; j++ {
				err := With(dir, nil, func() error {
					raw, err := os.ReadFile(target)
					if err != nil {
						return err
					}
					count, err := strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil {
						return err
					}
					return os.WriteFile(target, []byte(strconv.Itoa(count+1)), 0644)
				})
				if err != nil {
					failures.Add(1)
					t.Errorf("With: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("%d writer goroutines failed", failures.Load())
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(raw)), strconv.Itoa(writers*increments); got != want {
		t.Errorf("final counter = %s, want %s: increments were lost", got, want)
	}
}

// TestHelperProcess is not a test. It is the child half of
// TestCrossProcessExclusion, re-executed through the standard Go helper-process
// pattern: it takes the lock in SVCLOCK_CHILD_DIR, prints LOCKED so the parent
// stops waiting, then holds the lock until the parent closes its stdin.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("SVCLOCK_CHILD") != "1" {
		t.Skip("helper process entry point for TestCrossProcessExclusion")
	}
	defer os.Exit(0)

	dir := os.Getenv("SVCLOCK_CHILD_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "helper: SVCLOCK_CHILD_DIR is empty")
		os.Exit(1)
	}
	lock, err := TryAcquire(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: could not take the lock:", err)
		os.Exit(1)
	}
	defer lock.Release()

	fmt.Println("LOCKED")
	// Block until the parent closes our stdin; exiting then drops the lock.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestCrossProcessExclusion proves the lock works across process boundaries,
// which is the entire point: the in-process cases above could in principle be
// satisfied by something that never reaches the kernel.
func TestCrossProcessExclusion(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()

	// helpers.CreateCmd rather than exec.Command: on Windows it sets
	// CREATE_NO_WINDOW, and the project's hard rule is that no spawned process
	// may ever flash a console window -- tests included.
	cmd := helpers.CreateCmd(exe, "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "SVCLOCK_CHILD=1", "SVCLOCK_CHILD_DIR="+dir)
	childStdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	// Closing stdin is what lets the child exit, so do it even on a failure
	// path -- otherwise a red test leaves a stray process holding the lock.
	t.Cleanup(func() { _ = childStdin.Close() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("the helper produced no output: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.HasPrefix(line, "LOCKED") {
		t.Fatalf("the helper printed %q, want LOCKED (stderr: %s)", line, stderr.String())
	}

	if _, err := TryAcquire(dir); !IsBusy(err) {
		t.Errorf("TryAcquire while another PROCESS holds the lock = %v, want ErrBusy", err)
	}

	if err := childStdin.Close(); err != nil {
		t.Errorf("close the helper stdin: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the helper exited with an error: %v (stderr: %s)", err, stderr.String())
	}

	// The lock dies with the holder, so a crashed svc can never wedge the next
	// one -- the property that makes an advisory lock safe here.
	lock, err := TryAcquire(dir)
	if err != nil {
		t.Fatalf("TryAcquire after the holder exited: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}
