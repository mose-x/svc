package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// newTestConfig builds a Config rooted at dir without touching the real
// ~/.svc home. Used by the GetInstalledVersions tests below (M7).
func newTestConfig(t *testing.T, dir string) *Config {
	t.Helper()
	return &Config{
		svcDir: dir,
		data:   &ConfigData{ActiveVersions: make(map[string]string)},
	}
}

// TestGetInstalledVersions_MissingDir covers the "no versions installed yet"
// case: a non-existent SDK directory is the normal empty state and must
// return (nil, nil), NOT an error, so callers do not mistake "not installed"
// for a read failure (M7).
func TestGetInstalledVersions_MissingDir(t *testing.T) {
	c := newTestConfig(t, t.TempDir())
	got, err := c.GetInstalledVersions("nodejs")
	if err != nil {
		t.Fatalf("expected no error for missing dir (IsNotExist), got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil versions for missing dir, got %v", got)
	}
}

// TestGetInstalledVersions_ListsVersionDirs verifies directory entries are
// returned (sorted, since os.ReadDir sorts by name) and regular files in the
// SDK dir are ignored (only subdirectories are versions).
func TestGetInstalledVersions_ListsVersionDirs(t *testing.T) {
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	sdkDir := filepath.Join(dir, "nodejs")
	for _, v := range []string{"v18.0.0", "v20.10.0"} {
		if err := os.MkdirAll(filepath.Join(sdkDir, v), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file must be ignored (not treated as a version).
	if err := os.WriteFile(filepath.Join(sdkDir, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetInstalledVersions("nodejs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"v18.0.0", "v20.10.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestGetInstalledVersions_PropagatesReadError is the core M7 regression test:
// a genuine read failure (permission denied) must be propagated as (nil, err)
// rather than swallowed as "0 remaining" (which would wrongly tear down the
// shim layer). Uses chmod 0000 to trigger EACCES on Unix; skipped on Windows
// (where triggering a non-IsNotExist ReadDir error is not reliable) and when
// running as root (chmod does not block root).
func TestGetInstalledVersions_PropagatesReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("triggering a non-IsNotExist ReadDir error is not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; chmod 0000 does not block root from reading")
	}
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	sdkDir := filepath.Join(dir, "nodejs")
	if err := os.MkdirAll(sdkDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sdkDir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sdkDir, 0755) }) // restore so TempDir cleanup works
	got, err := c.GetInstalledVersions("nodejs")
	if err == nil {
		t.Fatalf("expected a non-nil error for unreadable dir, got nil; versions=%v", got)
	}
	if got != nil {
		t.Errorf("expected nil versions on error, got %v", got)
	}
}

// TestLoad_CorruptConfigFallsBackToDefaults is the corrupt-config recovery
// test: a corrupt config.json must NOT be fatal (the GUI os.Exit(1)s on
// NewConfig errors). The corrupt file is backed up as
// config.corrupt-<unixnano>.json and default config is returned, mirroring
// the M9 handling in settings.go.
func TestLoad_CorruptConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(`{"activeVersions":`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.load(); err != nil {
		t.Fatalf("expected nil error for corrupt config (fallback to defaults), got %v", err)
	}
	// Defaults must be in place (no partial data from the corrupt file).
	if v := c.GetActiveVersion("nodejs"); v != "" {
		t.Errorf("expected empty active version after corrupt load, got %q", v)
	}
	// The corrupt file must have been moved out of the way.
	if _, err := os.Stat(filepath.Join(dir, configFile)); !os.IsNotExist(err) {
		t.Errorf("corrupt config.json should have been renamed away, stat err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.corrupt-") && strings.HasSuffix(e.Name(), ".json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no config.corrupt-*.json backup found in %s", dir)
	}
	// Writes must work afterwards (save() recreates config.json).
	if err := c.SetActiveVersion("nodejs", "v20.0.0"); err != nil {
		t.Fatalf("SetActiveVersion after corrupt load failed: %v", err)
	}
	if v := c.GetActiveVersion("nodejs"); v != "v20.0.0" {
		t.Errorf("GetActiveVersion after recovery: got %q, want v20.0.0", v)
	}
}

// TestLoad_ValidJSON verifies a well-formed config.json loads into data.
func TestLoad_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	valid := []byte(`{"activeVersions":{"nodejs":"v18.0.0","go":"1.22.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, configFile), valid, 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := c.GetActiveVersion("nodejs"); v != "v18.0.0" {
		t.Errorf("nodejs: got %q, want v18.0.0", v)
	}
	if v := c.GetActiveVersion("go"); v != "1.22.0" {
		t.Errorf("go: got %q, want 1.22.0", v)
	}
}

// TestLoad_MissingFileCreatesConfig verifies a missing config.json is
// created with defaults (first-run path) instead of erroring.
func TestLoad_MissingFileCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	if err := c.load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, configFile)); err != nil {
		t.Errorf("config.json was not created on first load: %v", err)
	}
}

// TestCacheDir covers the layout of the version-cache directory and, more
// importantly, that it follows SetSvcDir. appcore.Bootstrap applies an install
// path override with SetSvcDir and only then points the version cache at
// CacheDir(); a cache path computed from the old svcDir would leave the CLI
// reading one home while its detached refresh child writes another.
func TestCacheDir(t *testing.T) {
	dir := t.TempDir()
	c := newTestConfig(t, dir)
	if got, want := c.CacheDir(), filepath.Join(dir, "cache"); got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}

	custom := filepath.Join(t.TempDir(), "elsewhere")
	c.SetSvcDir(custom)
	if got, want := c.CacheDir(), filepath.Join(custom, "cache"); got != want {
		t.Errorf("after SetSvcDir, CacheDir() = %q, want %q", got, want)
	}
	// The name is a plain directory join, so it must never escape the SVC dir.
	if !strings.HasPrefix(c.CacheDir(), custom) {
		t.Errorf("CacheDir() = %q escapes the SVC dir %q", c.CacheDir(), custom)
	}
	if runtime.GOOS == "windows" && strings.Contains(c.CacheDir(), "/") {
		t.Errorf("CacheDir() = %q mixes separators on windows", c.CacheDir())
	}
}
