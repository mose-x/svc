package sdk

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveSystemCommandNotFoundReturnsEmpty verifies that
// ResolveSystemCommand returns "" when the command is not found in PATH (M3
// fix). Previously it returned the bare command name, which caused
// ImportPathSdk to copy the entire CWD.
func TestResolveSystemCommandNotFoundReturnsEmpty(t *testing.T) {
	got := ResolveSystemCommand("definitely_not_a_real_command_xyz123")
	if got != "" {
		t.Errorf("ResolveSystemCommand(nonexistent) = %q; want \"\"", got)
	}
}

// TestResolveSystemCommandExcludesShimsDir verifies the shims exclusion
// helpers used by ResolveSystemCommand correctly identify SVC shims paths.
func TestResolveSystemCommandExcludesShimsDir(t *testing.T) {
	shimsDir := SvcShimsDir()
	if shimsDir == "" {
		t.Fatal("SvcShimsDir() returned empty string")
	}

	// IsShimsDirEntry: a PATH entry equal to shimsDir should be excluded
	if !IsShimsDirEntry(shimsDir, shimsDir) {
		t.Errorf("IsShimsDirEntry(shimsDir, shimsDir) = false; want true")
	}
	// A different directory should NOT be excluded
	otherDir := filepath.Join(t.TempDir(), "other")
	if IsShimsDirEntry(otherDir, shimsDir) {
		t.Errorf("IsShimsDirEntry(otherDir, shimsDir) = true; want false")
	}

	// IsShimsPath: a binary inside shimsDir should be detected
	shimBinary := filepath.Join(shimsDir, "go.exe")
	if !IsShimsPath(shimBinary, shimsDir) {
		t.Errorf("IsShimsPath(%s, %s) = false; want true", shimBinary, shimsDir)
	}
	// A binary outside shimsDir should NOT be detected
	externalBinary := filepath.Join(otherDir, "go.exe")
	if IsShimsPath(externalBinary, shimsDir) {
		t.Errorf("IsShimsPath(%s, %s) = true; want false", externalBinary, shimsDir)
	}
}

// TestResolveInPathDirs pins the scan semantics of the ResolveSystemCommand
// core: PATH order wins, shims dir is skipped, dirs and empty entries ignored.
func TestResolveInPathDirs(t *testing.T) {
	home := t.TempDir()
	dirA := filepath.Join(home, "a")
	dirB := filepath.Join(home, "b")
	shims := filepath.Join(home, "shims")
	for _, d := range []string{dirA, dirB, shims} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	exts := []string{""}
	// Platform separator: ";" on Windows, where drive letters put a literal
	// ":" inside every absolute path and a hardcoded ":" would shred them.
	sep := string(os.PathListSeparator)

	t.Run("path order wins", func(t *testing.T) {
		write(dirA, "tool")
		write(dirB, "tool")
		got := resolveInPathDirs(dirA+sep+dirB, sep, exts, "tool", shims)
		if got != filepath.Join(dirA, "tool") {
			t.Errorf("got %q; want the first PATH dir's copy", got)
		}
	})

	t.Run("later dir found when first lacks the binary", func(t *testing.T) {
		got := resolveInPathDirs(dirB, sep, exts, "tool", shims)
		if got != filepath.Join(dirB, "tool") {
			t.Errorf("got %q; want %q", got, filepath.Join(dirB, "tool"))
		}
	})

	t.Run("shims dir skipped", func(t *testing.T) {
		write(shims, "shimmed")
		write(dirB, "shimmed")
		got := resolveInPathDirs(shims+sep+dirB, sep, exts, "shimmed", shims)
		if got != filepath.Join(dirB, "shimmed") {
			t.Errorf("got %q; want the shims dir skipped", got)
		}
	})

	t.Run("empty and whitespace entries skipped", func(t *testing.T) {
		got := resolveInPathDirs(" "+sep+" "+dirB+" "+sep, sep, exts, "tool", shims)
		if got != filepath.Join(dirB, "tool") {
			t.Errorf("got %q; want %q", got, filepath.Join(dirB, "tool"))
		}
	})

	t.Run("directory with the command name is not a match", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(dirA, "adir"), 0755); err != nil {
			t.Fatal(err)
		}
		if got := resolveInPathDirs(dirA, sep, exts, "adir", shims); got != "" {
			t.Errorf("got %q; want empty for a directory", got)
		}
	})

	t.Run("missing command returns empty", func(t *testing.T) {
		if got := resolveInPathDirs(dirA, sep, exts, "nope", shims); got != "" {
			t.Errorf("got %q; want empty", got)
		}
	})
}
