package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"svc/internal/config"
)

// TestValidateMigrationPaths is the table-driven guard test for migration
// target validation: existing targets (directory OR plain file) and nested
// source/target pairs must be rejected; disjoint absolute paths must pass.
func TestValidateMigrationPaths(t *testing.T) {
	base := t.TempDir()

	oldDir := filepath.Join(base, "old")
	if err := os.MkdirAll(filepath.Join(oldDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	existingDir := filepath.Join(base, "existing-dir")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatal(err)
	}
	existingFile := filepath.Join(base, "existing-file")
	if err := os.WriteFile(existingFile, []byte("user data"), 0644); err != nil {
		t.Fatal(err)
	}
	// For the "source nested inside target" case: use paths that do not
	// exist on disk so the lexical nesting branch (not the existence check)
	// is what rejects them.
	ghostOuter := filepath.Join(base, "ghost-outer")

	tests := []struct {
		name    string
		oldDir  string
		newDir  string
		wantErr string // substring expected in the error; empty means success
	}{
		{"normal nonexistent target ok", oldDir, filepath.Join(base, "new-target"), ""},
		{"existing directory rejected", oldDir, existingDir, "[svc:target-exists]"},
		{"existing plain file rejected", oldDir, existingFile, "[svc:target-exists]"},
		{"target nested in source rejected", oldDir, filepath.Join(oldDir, "nested"), "[svc:nested-dirs]"},
		{"target deep-nested in source rejected", oldDir, filepath.Join(oldDir, "sub", "deep"), "[svc:nested-dirs]"},
		{"same path rejected", filepath.Join(base, "old2"), filepath.Join(base, "old2"), "[svc:nested-dirs]"},
		{"source nested in target rejected", filepath.Join(ghostOuter, "inner"), ghostOuter, "[svc:nested-dirs]"},
		{"sibling with shared prefix ok", oldDir, filepath.Join(base, "old2"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMigrationPaths(tt.oldDir, tt.newDir)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestMigrateInstallPath_RejectsExistingFile verifies the end-to-end guard:
// migrating onto an existing regular file must fail BEFORE any copy/cleanup,
// so the user's pre-existing file survives (previously the M10 cleanup could
// RemoveAll it after CopyDir failed).
func TestMigrateInstallPath_RejectsExistingFile(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "old")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target-file")
	if err := os.WriteFile(target, []byte("user data"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.SetSvcDir(oldDir)
	app := NewManager(cfg, nil, nil, nil, nil)

	err := app.MigrateInstallPath(target)
	if err == nil {
		t.Fatal("expected error when migrating onto an existing file, got nil")
	}
	if !strings.Contains(err.Error(), "[svc:target-exists]") {
		t.Errorf("unexpected error: %v", err)
	}
	// The pre-existing user file must be untouched.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("pre-existing target file disappeared: %v", err)
	}
	if string(data) != "user data" {
		t.Errorf("target file content changed: %q", data)
	}
}

// TestMigrateInstallPath_RejectsNestedTarget verifies the end-to-end guard:
// migrating into a subdirectory of the current install dir must be rejected
// (the final RemoveAll(oldDir) would otherwise delete the migrated data).
func TestMigrateInstallPath_RejectsNestedTarget(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "old")
	if err := os.MkdirAll(filepath.Join(oldDir, "keep"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.SetSvcDir(oldDir)
	app := NewManager(cfg, nil, nil, nil, nil)

	err := app.MigrateInstallPath(filepath.Join(oldDir, "nested"))
	if err == nil {
		t.Fatal("expected error for nested target, got nil")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("unexpected error: %v", err)
	}
	// Old directory content must be intact.
	if _, err := os.Stat(filepath.Join(oldDir, "keep")); err != nil {
		t.Errorf("old directory content lost: %v", err)
	}
}

func TestIsSystemPath_AllowsTempDir(t *testing.T) {
	// macOS per-user temp lives under /var/folders and must NOT be treated
	// as a system directory (regression: CI macOS runners failed migration
	// tests because t.TempDir() matched the /var system root).
	if isSystemPath(filepath.Join(os.TempDir(), "svc-target")) {
		t.Errorf("path under os.TempDir() (%s) wrongly flagged as system path", os.TempDir())
	}
	if runtime.GOOS == "windows" {
		if !isSystemPath(`C:\Windows\System32`) {
			t.Error(`C:\Windows\System32 must be a system path`)
		}
	} else {
		if !isSystemPath("/usr") {
			t.Error("/usr must be a system path")
		}
	}
}
func TestIsSystemPath(t *testing.T) {
	// Relative paths should be rejected by migration (H1 fix), but isSystemPath
	// itself only checks system dirs — the IsAbs check is separate.
	if runtime.GOOS == "windows" {
		systemPaths := []string{`C:\Windows`, `C:\Windows\System32`, `C:\Program Files`, `c:\program files (x86)`, `C:\ProgramData\foo`}
		for _, p := range systemPaths {
			if !isSystemPath(p) {
				t.Errorf("isSystemPath(%q) = false; want true", p)
			}
		}
		validPaths := []string{`C:\Users\mose\.svc`, `D:\SDKs`, `C:\dev\svc`}
		for _, p := range validPaths {
			if isSystemPath(p) {
				t.Errorf("isSystemPath(%q) = true; want false", p)
			}
		}
	} else {
		systemPaths := []string{"/usr", "/usr/local", "/bin", "/etc", "/var/log", "/sbin", "/boot"}
		for _, p := range systemPaths {
			if !isSystemPath(p) {
				t.Errorf("isSystemPath(%q) = false; want true", p)
			}
		}
		validPaths := []string{"/home/user/.svc", "/opt/sdks", "/Users/mose/.svc"}
		for _, p := range validPaths {
			if isSystemPath(p) {
				t.Errorf("isSystemPath(%q) = true; want false", p)
			}
		}
	}
}

func TestFilePathIsAbs(t *testing.T) {
	var absPaths, relPaths []string
	if runtime.GOOS == "windows" {
		absPaths = []string{`C:\Users\mose\.svc`, `D:\SDKs`, `C:\dev`}
		relPaths = []string{"foo", `..\bar`, `.\baz`, ""}
	} else {
		absPaths = []string{"/usr/local", "/home/user/.svc", "/opt/sdks"}
		relPaths = []string{"foo", "../bar", "./baz", ""}
	}
	for _, p := range absPaths {
		if !filepath.IsAbs(p) {
			t.Errorf("filepath.IsAbs(%q) = false; want true", p)
		}
	}
	for _, p := range relPaths {
		if filepath.IsAbs(p) {
			t.Errorf("filepath.IsAbs(%q) = true; want false", p)
		}
	}
}
