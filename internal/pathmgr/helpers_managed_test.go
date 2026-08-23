package pathmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"svc/internal/config"
)

func TestIsProtectedSystemDir(t *testing.T) {
	tests := []struct {
		name string
		goos string
		dir  string
		want bool
	}{
		// macOS
		{"darwin /usr/bin", "darwin", "/usr/bin", true},
		{"darwin /bin", "darwin", "/bin", true},
		{"darwin /sbin", "darwin", "/sbin", true},
		{"darwin system cryptex", "darwin", "/System/Cryptexes/App/usr/bin", true},
		{"darwin CLT usr bin", "darwin", "/Library/Developer/CommandLineTools/usr/bin", true},
		{"darwin homebrew stays importable", "darwin", "/usr/local/bin", false},
		{"darwin opt homebrew", "darwin", "/opt/homebrew/bin", false},
		{"darwin prefix boundary", "darwin", "/usr/binx", false},
		// Linux
		{"linux /usr/bin", "linux", "/usr/bin", true},
		{"linux /bin", "linux", "/bin", true},
		{"linux /usr/lib", "linux", "/usr/lib", true},
		{"linux /usr/local/bin stays importable", "linux", "/usr/local/bin", false},
		{"linux snap", "linux", "/snap/bin", false},
		// Windows
		{"windows system32", "windows", `C:\Windows\System32`, true},
		{"windows store stubs", "windows", `C:\Users\mose\AppData\Local\Microsoft\WindowsApps`, true},
		{"windows program files node", "windows", `C:\Program Files\nodejs`, false},
		// Edge cases
		{"empty dir", "darwin", "", false},
		{"unknown goos", "plan9", "/usr/bin", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsProtectedSystemDir(tt.goos, tt.dir); got != tt.want {
				t.Errorf("IsProtectedSystemDir(%q, %q) = %v; want %v", tt.goos, tt.dir, got, tt.want)
			}
		})
	}
}

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

func TestBuildUnmanagedEntriesFlagsSystemDirs(t *testing.T) {
	// Use a real protected dir for the host OS; create nothing inside it.
	var dir string
	switch runtime.GOOS {
	case "darwin", "linux":
		dir = "/usr/bin"
	case "windows":
		dir = `C:\Windows\System32`
	}
	// /usr/bin (and System32) contain SDK binaries on every CI image, but
	// even when detection finds none the protected flag must never be set on
	// importable dirs, so assert the flag directly via IsProtectedSystemDir
	// and only assert entry flags when SDK binaries were detected.
	entries := buildUnmanagedEntries(dir, nil)
	if !IsProtectedSystemDir(runtime.GOOS, dir) {
		t.Fatalf("expected %q to be protected on %s", dir, runtime.GOOS)
	}
	for _, e := range entries {
		if e.SdkType != "" && !e.SystemProtected {
			t.Errorf("entry %+v from protected dir must be flagged systemProtected", e)
		}
	}
}

func TestDetectEntryExternalManager(t *testing.T) {
	home := t.TempDir()

	nvmRustDir := filepath.Join(home, ".nvm.rust", "shims")
	writeNodeDir(t, nvmRustDir)
	if got := detectEntryExternalManager(nvmRustDir, "nodejs"); got != "nvm-rust" {
		t.Errorf("nvm-rust dir: got %q; want %q", got, "nvm-rust")
	}

	nvmDir := filepath.Join(home, ".nvm", "versions", "node", "v20.11.1", "bin")
	writeNodeDir(t, nvmDir)
	if got := detectEntryExternalManager(nvmDir, "nodejs"); got != "nvm" {
		t.Errorf("nvm dir: got %q; want %q", got, "nvm")
	}

	plainDir := filepath.Join(home, "standalone", "bin")
	writeNodeDir(t, plainDir)
	if got := detectEntryExternalManager(plainDir, "nodejs"); got != "" {
		t.Errorf("standalone dir: got %q; want empty", got)
	}

	if got := detectEntryExternalManager(nvmRustDir, "python"); got != "" {
		t.Errorf("non-node sdk: got %q; want empty", got)
	}

	if got := detectEntryExternalManager(filepath.Join(home, "no-node"), "nodejs"); got != "" {
		t.Errorf("missing node binary: got %q; want empty", got)
	}
}
