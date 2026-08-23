package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir = %v; want nil", err)
	}

	if got, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(got) != "alpha" {
		t.Errorf("a.txt = %q, %v; want %q, nil", got, err, "alpha")
	}
	if got, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(got) != "beta" {
		t.Errorf("sub/b.txt = %q, %v; want %q, nil", got, err, "beta")
	}
	// Modes are preserved (skip on Windows where chmod semantics differ).
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dst, "sub", "b.txt")); err != nil || info.Mode().Perm() != 0600 {
			t.Errorf("sub/b.txt mode = %v, %v; want 0600 preserved", info.Mode(), err)
		}
	}
}

func TestCopyDirMissingSrc(t *testing.T) {
	err := CopyDir(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst"))
	if err == nil {
		t.Error("CopyDir(missing src) = nil; want an error")
	}
}

func TestCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst.txt")
	if err := CopyFile(src, dst, 0644); err != nil {
		t.Fatalf("CopyFile = %v; want nil", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "content" {
		t.Errorf("dst = %q, %v; want %q, nil", got, err, "content")
	}
}

func TestCopyFileErrors(t *testing.T) {
	tmp := t.TempDir()
	if err := CopyFile(filepath.Join(tmp, "missing"), filepath.Join(tmp, "dst"), 0644); err == nil {
		t.Error("CopyFile(missing src) = nil; want an error")
	}
	src := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(src, filepath.Join(tmp, "nodir", "dst.txt"), 0644); err == nil {
		t.Error("CopyFile(missing dst dir) = nil; want an error")
	}
}
