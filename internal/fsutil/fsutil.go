// Package fsutil hosts filesystem primitives shared across packages that
// cannot import each other (sdk <-> pathmgr <-> helpers layering). It imports
// only the standard library, so any package can use it without import cycles.
package fsutil

import (
	"io"
	"os"
	"path/filepath"
)

// CopyDir recursively copies the directory tree at src to dst, preserving
// file modes. dst is created as needed.
func CopyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		return CopyFile(path, dstPath, info.Mode())
	})
}

// CopyFile copies a single file from src to dst with the given mode.
func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	// M2: Surface Close errors (e.g. disk full during flush) — previously
	// dropped via defer out.Close(), hiding the real cause of a corrupt copy.
	_, err = io.Copy(out, in)
	if cerr := out.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}
