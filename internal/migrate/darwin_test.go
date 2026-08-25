//go:build darwin

package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildLegacyRenameSh pins the structure of the macOS migration script:
// PID wait, bundle rename with fallback, Info.plist display-name update,
// relaunch.
func TestBuildLegacyRenameSh(t *testing.T) {
	const pid = 4242
	exe := "/Applications/SDK Version Control.app/Contents/MacOS/SDK Version Control"
	script := buildLegacyRenameSh(pid, exe)

	checks := []struct {
		desc string
		want string
	}{
		{"embeds the current exe path", exe},
		{"embeds the PID to wait on", `PID="4242"`},
		{"waits via kill -0 polling", `kill -0 "$PID"`},
		{"wait bounded by timeout", "timeout=60"},
		{"bundle renamed to svc.app", `NEW_BUNDLE="$APPDIR/svc.app"`},
		{"bundle rename fallback", `NEW_BUNDLE="$BUNDLE"`},
		{"inner executable renamed to svc", `mv "$MACOS_NEW/$INNER_OLD" "$MACOS_NEW/svc"`},
		{"updates CFBundleName", `plutil -replace CFBundleName -string "svc"`},
		{"updates CFBundleExecutable", `plutil -replace CFBundleExecutable -string "svc"`},
		{"relaunch guarded by svc existence", `if [ -f "$MACOS_NEW/svc" ]; then`},
		{"relaunches via open", `open "$NEW_BUNDLE"`},
	}
	for _, c := range checks {
		t.Run(c.desc, func(t *testing.T) {
			if !strings.Contains(script, c.want) {
				t.Errorf("script missing %q\n--- script ---\n%s", c.want, script)
			}
		})
	}
}

// TestIsLegacyDarwinInstall checks detection via the bundle folder name using
// a fake executable path layout.
func TestIsLegacyDarwinInstall(t *testing.T) {
	// The test binary itself is not inside a legacy bundle.
	if isLegacyDarwinInstall() {
		t.Error("test binary should not be detected as a legacy install")
	}
	// Sanity: the legacy constant matches the documented old bundle name.
	if legacyAppBundleName != "SDK Version Control.app" {
		t.Errorf("legacyAppBundleName = %q", legacyAppBundleName)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if strings.EqualFold(filepath.Base(bundle), legacyAppBundleName) {
		t.Errorf("unexpected: test runs inside legacy bundle %s", bundle)
	}
}

func TestLegacySiblingBundlePath(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		want string
	}{
		{
			"fresh svc.app yields the legacy sibling",
			"/Applications/svc.app/Contents/MacOS/svc",
			"/Applications/SDK Version Control.app",
		},
		{
			"running inside the legacy bundle yields nothing (rename migration owns it)",
			"/Applications/SDK Version Control.app/Contents/MacOS/SDK Version Control",
			"",
		},
		{
			"legacy bundle name is matched case-insensitively",
			"/Applications/sdk version control.app/Contents/MacOS/svc",
			"",
		},
		{
			"non-bundle executable (dev build) yields nothing",
			"/Users/dev/code/svc/tmp/build/bin/svc",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacySiblingBundlePath(tt.exe); got != tt.want {
				t.Errorf("legacySiblingBundlePath(%q) = %q; want %q", tt.exe, got, tt.want)
			}
		})
	}
}

func TestRemoveLegacySiblingBundleFor(t *testing.T) {
	base := t.TempDir()

	// Fake fresh install: <base>/svc.app/Contents/MacOS/svc
	exe := filepath.Join(base, "svc.app", "Contents", "MacOS", "svc")
	if err := os.MkdirAll(filepath.Dir(exe), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("removes the legacy sibling", func(t *testing.T) {
		sibling := filepath.Join(base, legacyAppBundleName)
		if err := os.MkdirAll(filepath.Join(sibling, "Contents"), 0755); err != nil {
			t.Fatal(err)
		}
		removeLegacySiblingBundleFor(exe)
		if _, err := os.Stat(sibling); !os.IsNotExist(err) {
			t.Errorf("legacy bundle still present after removal (stat err = %v)", err)
		}
	})

	t.Run("no sibling is a no-op", func(t *testing.T) {
		removeLegacySiblingBundleFor(exe) // must not panic or error
	})

	t.Run("non-bundle exe is a no-op", func(t *testing.T) {
		sibling := filepath.Join(base, legacyAppBundleName)
		if err := os.MkdirAll(sibling, 0755); err != nil {
			t.Fatal(err)
		}
		removeLegacySiblingBundleFor(filepath.Join(base, "plain-dir", "svc"))
		if _, err := os.Stat(sibling); err != nil {
			t.Errorf("sibling touched for a non-bundle exe: %v", err)
		}
	})
}
