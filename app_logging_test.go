package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/logger"
)

// initAppLogger points the logger singleton at a temp dir once per test
// process. logger.Init is sync.Once-guarded; the first call wins. Same
// caveat as internal/logmgr tests: the singleton keeps the file open for
// the process lifetime, so cleanup is best-effort.
func initAppLogger(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "applog-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	logger.Init(base)
	return filepath.Join(base, "logs")
}

// readLatestLog returns the content of the newest svc-*.log in dir.
func readLatestLog(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var latest string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "svc-") && strings.HasSuffix(e.Name(), ".log") {
			latest = filepath.Join(dir, e.Name())
		}
	}
	if latest == "" {
		t.Fatal("no svc-*.log file found")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(data)
}

// TestLogOpWritesOutcome pins that every frontend-facing operation lands in
// the log file with its outcome: successes as Info "op: ok", failures as
// Error "op failed: reason", and argument-less queries via logCall.
func TestLogOpWritesOutcome(t *testing.T) {
	logDir := initAppLogger(t)

	logOp("TestOpAlpha", nil)
	logOp("TestOpBeta", fmt.Errorf("boom"))
	logCall("TestQueryGamma")

	content := readLatestLog(t, logDir)
	for _, want := range []string{
		"[INFO] TestOpAlpha: ok",
		"[ERROR] TestOpBeta failed: boom",
		"[INFO] TestQueryGamma",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("log missing %q\ncontent:\n%s", want, content)
		}
	}
}
