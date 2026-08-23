package sdk

import (
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
