package pathmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"svc/internal/config"
	"svc/internal/sdk"
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

// writeNodeDir creates dir with a node executable detectSdkTypesByBin sees on
// every OS (the detector stats extension-less names too).
func writeNodeDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("fake"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestBuildUnmanagedEntriesFiltersManagedSdks(t *testing.T) {
	home := t.TempDir()
	nodeDir := filepath.Join(home, "nodejs", "bin")
	writeNodeDir(t, nodeDir)

	t.Run("unmanaged sdk listed", func(t *testing.T) {
		cfg := newFakeConfig(t)
		entries := buildUnmanagedEntries(nodeDir, cfg)
		if len(entries) != 1 || entries[0].SdkType != "nodejs" || entries[0].IsManaged {
			t.Fatalf("entries = %+v; want one unmanaged nodejs entry", entries)
		}
	})

	t.Run("svc-managed sdk dropped", func(t *testing.T) {
		cfg := newFakeConfig(t)
		if err := cfg.SetActiveVersion("nodejs", "22.11.0"); err != nil {
			t.Fatal(err)
		}
		if entries := buildUnmanagedEntries(nodeDir, cfg); len(entries) != 0 {
			t.Fatalf("entries = %+v; want the SVC-managed SDK filtered out", entries)
		}
	})

	t.Run("other sdk active does not filter nodejs", func(t *testing.T) {
		cfg := newFakeConfig(t)
		if err := cfg.SetActiveVersion("python", "3.13.0"); err != nil {
			t.Fatal(err)
		}
		if entries := buildUnmanagedEntries(nodeDir, cfg); len(entries) != 1 {
			t.Fatalf("entries = %+v; want nodejs kept when only python is managed", entries)
		}
	})

	t.Run("nil cfg keeps entry", func(t *testing.T) {
		if entries := buildUnmanagedEntries(nodeDir, nil); len(entries) != 1 {
			t.Fatalf("entries = %+v; want one entry with nil config", entries)
		}
	})

	t.Run("dir without sdk binaries yields plain entry", func(t *testing.T) {
		plain := filepath.Join(home, "plain")
		if err := os.MkdirAll(plain, 0755); err != nil {
			t.Fatal(err)
		}
		entries := buildUnmanagedEntries(plain, nil)
		if len(entries) != 1 || entries[0].SdkType != "" {
			t.Fatalf("entries = %+v; want one plain entry without sdk type", entries)
		}
	})
}

func TestBuildUnmanagedEntriesSkipsSystemDirs(t *testing.T) {
	// Use a real protected dir for the host OS. OS-managed copies can never
	// be imported, so they must not appear in the PATH modal data at all
	// (the SDK list/detail still reports them as system-managed).
	var dir string
	switch runtime.GOOS {
	case "darwin", "linux":
		dir = "/usr/bin"
	case "windows":
		dir = `C:\Windows\System32`
	}
	if !sdk.IsProtectedSystemDir(runtime.GOOS, dir) {
		t.Fatalf("expected %q to be protected on %s", dir, runtime.GOOS)
	}
	if entries := buildUnmanagedEntries(dir, nil); len(entries) != 0 {
		t.Fatalf("entries = %+v; want protected dir fully skipped", entries)
	}
}

// TestBuildUnmanagedEntriesFlagsExternalManager verifies manager-owned node
// dirs (nvm / nvm-rust) surface the ExternalManager flag via the central
// sdk.ClassifyPathCopy classification.
func TestBuildUnmanagedEntriesFlagsExternalManager(t *testing.T) {
	home := t.TempDir()

	nvmRustDir := filepath.Join(home, ".nvm.rust", "shims")
	writeNodeDir(t, nvmRustDir)
	entries := buildUnmanagedEntries(nvmRustDir, nil)
	if len(entries) != 1 || entries[0].ExternalManager != "nvm-rust" {
		t.Fatalf("entries = %+v; want one nodejs entry managed by nvm-rust", entries)
	}

	nvmDir := filepath.Join(home, ".nvm", "versions", "node", "v20.11.1", "bin")
	writeNodeDir(t, nvmDir)
	entries = buildUnmanagedEntries(nvmDir, nil)
	if len(entries) != 1 || entries[0].ExternalManager != "nvm" {
		t.Fatalf("entries = %+v; want one nodejs entry managed by nvm", entries)
	}

	plainDir := filepath.Join(home, "standalone", "bin")
	writeNodeDir(t, plainDir)
	entries = buildUnmanagedEntries(plainDir, nil)
	if len(entries) != 1 || entries[0].ExternalManager != "" || entries[0].SystemProtected {
		t.Fatalf("entries = %+v; want a plain importable nodejs entry", entries)
	}
}
