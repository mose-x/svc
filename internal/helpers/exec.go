package helpers

import (
	"os"
	"path/filepath"
	"runtime"
)

// FindExecutable looks for name (plus platform extensions on Windows) inside
// dir and returns the first regular file found, or "".
func FindExecutable(dir, name string) string {
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}
	for _, ext := range exts {
		p := filepath.Join(dir, name+ext)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// IsDir reports whether p exists and is a directory.
func IsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
