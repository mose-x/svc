//go:build !windows

package clirt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"svc/internal/helpers"
)

// signalChildEnv marks the re-executed child half of the tests below.
const signalChildEnv = "SVC_CLIRT_SIGNAL_CHILD"

// TestSignalChildProcess is not a test. It is the child half of
// TestSecondInterruptTerminatesTheProcess, re-executed through the standard Go
// helper-process pattern: install the signal context, report readiness, report
// the cancellation, then block on stdin so the runtime's deadlock detector
// stays quiet while the parent sends a second signal.
func TestSignalChildProcess(t *testing.T) {
	if os.Getenv(signalChildEnv) == "" {
		t.Skip("helper process entry point for TestSecondInterruptTerminatesTheProcess")
	}

	ctx, stop := SignalContext(context.Background())
	defer stop()

	fmt.Fprintln(os.Stdout, "ready")
	<-ctx.Done()
	fmt.Fprintln(os.Stdout, "canceled")
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Fprintln(os.Stdout, "stdin-closed")
}

// TestFirstInterruptCancelsTheContext is the A6 half of the signal contract:
// Ctrl-C during a long install must cancel the operation the way the GUI's
// cancel button does, not leave a half-written version directory behind.
func TestFirstInterruptCancelsTheContext(t *testing.T) {
	ctx, stop := SignalContext(context.Background())
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to ourselves: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGINT did not cancel the context within 10s")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
}

// TestFirstTermCancelsTheContext covers the other signal in the set, so
// `kill <pid>` and a container stop cancel instead of abandoning a write.
func TestFirstTermCancelsTheContext(t *testing.T) {
	ctx, stop := SignalContext(context.Background())
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to ourselves: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not cancel the context within 10s")
	}
}

// TestSecondInterruptTerminatesTheProcess is the "press twice and mean it"
// half. signal.NotifyContext restores the default signal disposition once it
// has delivered the first signal, so the second one kills the process outright
// -- which is the only escape hatch a user has when a download refuses to
// observe cancellation. It has to be checked in a child: asserting it in the
// test process would end the test run.
func TestSecondInterruptTerminatesTheProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// helpers.CreateCmd, not exec.Command: the no-flash rule covers test
	// children too, and internal/detach/nospawn_guard_test.go scans this tree.
	cmd := helpers.CreateCmd(exe, "-test.run=TestSignalChildProcess")
	cmd.Env = append(os.Environ(), signalChildEnv+"=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	waitForLine(t, lines, "ready")
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("first SIGINT: %v", err)
	}
	waitForLine(t, lines, "canceled")

	// Give the child's NotifyContext goroutine time to restore the default
	// disposition before the second signal arrives.
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("second SIGINT: %v", err)
	}

	waitErr := cmd.Wait()
	_ = stdin.Close()
	if waitErr == nil {
		t.Fatal("the child survived a second SIGINT and exited cleanly; the default signal disposition was not restored")
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("cmd.Wait() = %v, want an *exec.ExitError from a signal", waitErr)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("the child's wait status is %T, want syscall.WaitStatus", exitErr.Sys())
	}
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("the child exited with %v, want termination by SIGINT", status)
	}
}

// waitForLine blocks until want arrives on lines, or fails the test.
func waitForLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line := <-lines:
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("the child never printed %q", want)
		}
	}
}
