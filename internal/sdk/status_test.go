package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/config"
)

// newFakeConfig builds a Config rooted in a temp HOME without touching the
// real ~/.svc (HOME covers unix, USERPROFILE covers Windows CI).
func newFakeConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	cfg.SetHomeDir(home)
	return cfg
}

func TestBaseLocalStatusUnconfigured(t *testing.T) {
	cfg := newFakeConfig(t)
	// Bogus verify command: deterministically absent from PATH on every host,
	// so PathConfigured must be false without depending on host tooling.
	st := baseLocalStatus(cfg, Ruby, "svc-test-no-such-command-xyz")

	if st.SdkType != Ruby || st.DisplayName != SdkDisplayName(Ruby) {
		t.Errorf("SdkType/DisplayName = %q/%q; want ruby identity", st.SdkType, st.DisplayName)
	}
	if st.Configured {
		t.Error("Configured = true; want false with no active version")
	}
	if st.PathConfigured {
		t.Error("PathConfigured = true; want false for a command not in PATH")
	}
	if st.CurrentVersion != "" || len(st.InstalledVersions) != 0 {
		t.Errorf("versions = %q/%v; want empty", st.CurrentVersion, st.InstalledVersions)
	}
	if st.NeedsSwitch {
		t.Error("NeedsSwitch = true; want false with no active version")
	}
	if !strings.Contains(st.InstallPath, "ruby") {
		t.Errorf("InstallPath = %q; want it under the ruby SDK dir", st.InstallPath)
	}
}

func TestBaseLocalStatusConfigured(t *testing.T) {
	cfg := newFakeConfig(t)
	if err := cfg.SetActiveVersion(string(Ruby), "3.3.0"); err != nil {
		t.Fatal(err)
	}

	st := baseLocalStatus(cfg, Ruby, "svc-test-no-such-command-xyz")
	if !st.Configured {
		t.Error("Configured = false; want true with an active version")
	}
	if st.CurrentVersion != "3.3.0" {
		t.Errorf("CurrentVersion = %q; want 3.3.0", st.CurrentVersion)
	}
	if st.PathConfigured {
		t.Error("PathConfigured = true; want false once SVC manages the SDK")
	}
	// Active version missing from the installed list: dangling reference.
	if !st.NeedsSwitch {
		t.Error("NeedsSwitch = false; want true when the active version is not installed")
	}
}

func TestBaseLocalStatusNeedsSwitchFalseWhenInstalled(t *testing.T) {
	cfg := newFakeConfig(t)
	// GetInstalledVersions scans version subdirs of the SDK dir.
	if err := os.MkdirAll(cfg.SdkVersionDir(string(Ruby), "3.3.0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetActiveVersion(string(Ruby), "3.3.0"); err != nil {
		t.Fatal(err)
	}

	st := baseLocalStatus(cfg, Ruby, "svc-test-no-such-command-xyz")
	if st.NeedsSwitch {
		t.Errorf("NeedsSwitch = true; installed=%v active=%q", st.InstalledVersions, st.CurrentVersion)
	}
}
