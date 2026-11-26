package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/config"
)

// TestActiveVersionDeleted covers the sentinel parser both shells use to tell
// "you deleted the version you were on" apart from a genuine failure.
func TestActiveVersionDeleted(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantSdk string
		wantOK  bool
	}{
		{"nil is not the sentinel", nil, "", false},
		{"an unrelated error is not the sentinel", errors.New("version directory does not exist: 1.0.0"), "", false},
		{"the sentinel names its SDK", fmt.Errorf("%s%s", ActiveVersionDeletedPrefix, "nodejs"), "nodejs", true},
		{"a wrapped-looking sentinel still parses", errors.New(ActiveVersionDeletedPrefix + "go"), "go", true},
		{"the bare prefix parses to an empty SDK", errors.New(ActiveVersionDeletedPrefix), "", true},
		{"the prefix must be at the start", errors.New("failed: " + ActiveVersionDeletedPrefix + "go"), "", false},
		{"a case difference is not the sentinel", errors.New(strings.ToLower(ActiveVersionDeletedPrefix) + "go"), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSdk, gotOK := ActiveVersionDeleted(c.err)
			if gotOK != c.wantOK || gotSdk != c.wantSdk {
				t.Errorf("ActiveVersionDeleted(%v) = (%q, %v), want (%q, %v)", c.err, gotSdk, gotOK, c.wantSdk, c.wantOK)
			}
		})
	}
}

// testConfig builds a real *config.Config rooted in a temp home and svc dir.
//
// The zero value is enough for the read-only paths (GetInstalledVersions only
// touches svcDir) but NOT for SetActiveVersion/ClearActiveVersion: those call
// save(), which dereferences the data map that only NewConfig initializes.
func testConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatalf("config.NewConfig: %v", err)
	}
	dir := t.TempDir()
	cfg.SetHomeDir(home)
	cfg.SetSvcDir(dir)
	return cfg, dir
}

// seedVersions creates <dir>/<sdkType>/<version> for each version so the
// manager sees them as installed.
func seedVersions(t *testing.T, dir string, sdkType string, versions ...string) {
	t.Helper()
	for _, version := range versions {
		if err := os.MkdirAll(filepath.Join(dir, sdkType, version), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestUninstallVersionFlagsTheActiveVersion is the producer half: removing the
// active version clears the active config AND returns the sentinel, so the shell
// can report a success-with-warning instead of a failure.
//
// Two versions are installed so noVersionsLeft stays false: the last-version
// path tears the shim layer down through pathMgr, which this manager does not
// have, and shim teardown is not what is under test here.
func TestUninstallVersionFlagsTheActiveVersion(t *testing.T) {
	cfg, dir := testConfig(t)
	seedVersions(t, dir, "nodejs", "20.0.0", "22.0.0")
	if err := cfg.SetActiveVersion("nodejs", "22.0.0"); err != nil {
		t.Fatalf("set the active version: %v", err)
	}

	m := NewManager(cfg, nil, nil, nil, nil)
	err := m.UninstallVersion("nodejs", "22.0.0")

	sdkType, ok := ActiveVersionDeleted(err)
	if !ok {
		t.Fatalf("UninstallVersion of the active version = %v, want the %q sentinel", err, ActiveVersionDeletedPrefix)
	}
	if sdkType != "nodejs" {
		t.Errorf("the sentinel names %q, want nodejs", sdkType)
	}
	if got := cfg.GetActiveVersion("nodejs"); got != "" {
		t.Errorf("the active version is still %q; deleting it must clear the config", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodejs", "22.0.0")); !os.IsNotExist(err) {
		t.Errorf("the version directory still exists (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodejs", "20.0.0")); err != nil {
		t.Errorf("the other version was removed too: %v", err)
	}
}

// TestUninstallVersionOfAnInactiveVersionIsSilent is the other side: removing a
// version that was not active is a plain success, and a shell that treated every
// uninstall as a warning would cry wolf on the common case.
func TestUninstallVersionOfAnInactiveVersionIsSilent(t *testing.T) {
	cfg, dir := testConfig(t)
	seedVersions(t, dir, "nodejs", "20.0.0", "22.0.0")
	if err := cfg.SetActiveVersion("nodejs", "22.0.0"); err != nil {
		t.Fatalf("set the active version: %v", err)
	}

	m := NewManager(cfg, nil, nil, nil, nil)
	if err := m.UninstallVersion("nodejs", "20.0.0"); err != nil {
		t.Fatalf("UninstallVersion of an inactive version = %v, want nil", err)
	}
	if got := cfg.GetActiveVersion("nodejs"); got != "22.0.0" {
		t.Errorf("the active version became %q, want it untouched", got)
	}
}

// TestUninstallVersionOfAMissingVersionFails covers the refusal path. The error
// is deliberately NOT an apperr marker -- it predates the marker convention and
// the GUI renders it raw -- so the CLI checks the version exists before calling
// in, and this test pins that the service still refuses rather than succeeding
// on a directory that was never there.
func TestUninstallVersionOfAMissingVersionFails(t *testing.T) {
	cfg, _ := testConfig(t)
	m := NewManager(cfg, nil, nil, nil, nil)

	err := m.UninstallVersion("nodejs", "99.0.0")
	if err == nil {
		t.Fatal("uninstalling a version that does not exist = nil error, want a refusal")
	}
	if _, ok := ActiveVersionDeleted(err); ok {
		t.Errorf("a missing version was reported as an active-version deletion: %v", err)
	}
}

// TestActiveVersionDeletedPrefixMatchesTheFrontend pins the sentinel to the
// literal the GUI matches on. The two shells cannot share a constant across the
// Wails boundary -- an error arrives in the frontend as its message text -- so
// the string IS the contract, and a rename on either side alone would turn a
// warning toast into an error dialog (or, in the CLI, a success into exit 1).
func TestActiveVersionDeletedPrefixMatchesTheFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "components", "Panel", "DetailPanel.tsx"))
	if err != nil {
		t.Fatalf("read DetailPanel.tsx: %v", err)
	}
	want := "'" + ActiveVersionDeletedPrefix + "'"
	if !strings.Contains(string(raw), want) {
		t.Errorf("DetailPanel.tsx no longer matches on %s; ActiveVersionDeletedPrefix and the GUI have drifted", want)
	}
}
