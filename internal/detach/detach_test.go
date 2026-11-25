package detach

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// detachedProcessFlag is DETACHED_PROCESS. It is spelled out here rather than
// referenced from the package because the whole point of the assertion is that
// production code never sets it.
const detachedProcessFlag = 0x00000008

// TestCreationFlagsAcrossPlatforms covers all six release targets on any host.
// The Windows assertion is the product red line: a background child that gets a
// console flashes a window on the user's screen, and DETACHED_PROCESS -- the
// "obvious" fix -- is documented by MSDN as mutually exclusive with
// CREATE_NO_WINDOW, so combining them either fails or hands the child a
// brand-new console.
func TestCreationFlagsAcrossPlatforms(t *testing.T) {
	platforms := []struct{ goos, goarch string }{
		{"windows", "amd64"}, {"windows", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"linux", "amd64"}, {"linux", "arm64"},
	}
	for _, p := range platforms {
		t.Run(p.goos+"/"+p.goarch, func(t *testing.T) {
			got := CreationFlags(p.goos)
			if p.goos != "windows" {
				if got != 0 {
					t.Errorf("CreationFlags(%q) = %#x, want 0 (creation flags are Windows-only)", p.goos, got)
				}
				return
			}
			if got&createNoWindow == 0 {
				t.Errorf("CreationFlags(windows) = %#x, missing CREATE_NO_WINDOW", got)
			}
			if got&detachedProcessFlag != 0 {
				t.Errorf("CreationFlags(windows) = %#x, must NOT contain DETACHED_PROCESS: it is mutually exclusive with CREATE_NO_WINDOW", got)
			}
			if got != createNoWindow {
				t.Errorf("CreationFlags(windows) = %#x, want exactly CREATE_NO_WINDOW (%#x)", got, createNoWindow)
			}
		})
	}
}

func TestConfigureStdioInheritsNothing(t *testing.T) {
	cmd := &exec.Cmd{}
	// Pre-pollute the descriptors: a fresh exec.Cmd has them nil anyway, and
	// the assertion is only meaningful if configureStdio actively clears them.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cleanup, err := configureStdio(cmd, "")
	if err != nil {
		t.Fatalf("configureStdio: %v", err)
	}
	defer cleanup()

	// nil is os/exec's "connect to the null device". Anything else would let a
	// background child write into the pipe the user fed to another program.
	if cmd.Stdin != nil {
		t.Error("the child would inherit stdin")
	}
	if cmd.Stdout != nil {
		t.Error("the child would inherit stdout")
	}
	if cmd.Stderr != nil {
		t.Error("the child would inherit stderr")
	}
}

func TestConfigureStdioRedirectsToLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "refresh.log")
	if err := os.WriteFile(logPath, []byte("earlier run\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := &exec.Cmd{}
	cleanup, err := configureStdio(cmd, logPath)
	if err != nil {
		t.Fatalf("configureStdio: %v", err)
	}
	if cmd.Stdout == nil || cmd.Stderr == nil {
		t.Fatal("stdout/stderr were not redirected to the log file")
	}
	if cmd.Stdout != cmd.Stderr {
		t.Error("stdout and stderr must land in the same log file")
	}
	if cmd.Stdin != nil {
		t.Error("stdin must stay detached even when a log file is set")
	}
	cleanup()

	// O_APPEND, not truncate: the log accumulates across background runs.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "earlier run\n") {
		t.Errorf("the log file was truncated instead of appended to: %q", data)
	}
}

func TestConfigureStdioReportsUnopenableLog(t *testing.T) {
	cmd := &exec.Cmd{}
	// O_CREATE makes the file but not the missing parent directory.
	missing := filepath.Join(t.TempDir(), "no-such-dir", "refresh.log")
	cleanup, err := configureStdio(cmd, missing)
	if err == nil {
		cleanup()
		t.Fatal("configureStdio with an unopenable log file = nil error, want an error")
	}
	if cleanup != nil {
		t.Error("a failed configureStdio must not return a cleanup func")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error should name the log file, got: %v", err)
	}
}

func TestSpawnRejectsBadInput(t *testing.T) {
	if err := Spawn("", nil, Options{}); err == nil {
		t.Error("Spawn with an empty executable = nil error, want an error")
	}
	if err := Spawn(filepath.Join(t.TempDir(), "does-not-exist"), nil, Options{}); err == nil {
		t.Error("Spawn with a missing executable = nil error, want an error")
	}
	if err := Spawn(os.Args[0], nil, Options{LogFile: filepath.Join(t.TempDir(), "no-dir", "x.log")}); err == nil {
		t.Error("Spawn with an unopenable log file = nil error, want an error")
	}
}

// TestDetachedChild is not a test. It is the child half of
// TestSpawnRunsTheChildAndRedirectsItsOutput, re-executed through the standard
// Go helper-process pattern. It writes a sentinel to stdout, one to stderr, then
// a file the parent polls -- in that order, so once the file exists the two
// writes have already been issued (os.Stdout is unbuffered in Go).
func TestDetachedChild(t *testing.T) {
	sentinel := os.Getenv("SVC_DETACH_CHILD_SENTINEL")
	if sentinel == "" {
		t.Skip("helper process entry point for TestSpawnRunsTheChildAndRedirectsItsOutput")
	}
	defer os.Exit(0)

	fmt.Fprintln(os.Stdout, "detach-child-stdout")
	fmt.Fprintln(os.Stderr, "detach-child-stderr")
	if err := os.WriteFile(sentinel, []byte("ran"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write the sentinel:", err)
		os.Exit(1)
	}
}

// TestSpawnRunsTheChildAndRedirectsItsOutput is the end-to-end half: a real
// child process, spawned through Spawn, whose output lands in the log file and
// nowhere else.
func TestSpawnRunsTheChildAndRedirectsItsOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "child-ran")
	logPath := filepath.Join(dir, "refresh.log")

	t.Setenv("SVC_DETACH_CHILD_SENTINEL", sentinel)
	if err := Spawn(exe, []string{"-test.run=TestDetachedChild"}, Options{LogFile: logPath}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitForFile(t, sentinel, 30*time.Second)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read the child log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "detach-child-stdout") {
		t.Errorf("the child's stdout did not reach the log file:\n%s", log)
	}
	if !strings.Contains(log, "detach-child-stderr") {
		t.Errorf("the child's stderr did not reach the log file:\n%s", log)
	}
}

// waitForFile blocks until path exists, or fails the test. Spawn deliberately
// exposes no way to wait for the child -- an unobservable exit status is the
// point -- so the tests synchronise on something the child writes instead.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the child never created %s within %s", path, timeout)
}
