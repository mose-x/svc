package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"svc/internal/fsutil"
	"svc/internal/logger"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
)

// copyToTargetAtomically copies sourceDir to a temp sibling of targetDir,
// aligns the import layout, runs the layer-2 critical-files check and the
// verify callback on the ALIGNED temp dir, then atomically replaces
// targetDir. Uses rename-old-first pattern: the old targetDir is renamed to
// .old BEFORE the new tmpDir is renamed into place, so if the second rename
// fails, the old version is restored from .old.
//
// The critical-files check runs AFTER AlignImportLayout (see
// alignAndCheckCriticalFiles): flat imports (directory / PATH / flat archive)
// only gain their expected wrapper dir (go/, dart-sdk/, ...) during alignment,
// so checking the pre-alignment layout would reject complete SDKs.
func copyToTargetAtomically(sourceDir, targetDir string, binDirs []string, sdkType sdk.SdkType, verify func(string) error) error {
	tmpDir := targetDir + ".new"
	oldDir := targetDir + ".old"
	os.RemoveAll(tmpDir)
	os.RemoveAll(oldDir)
	if err := fsutil.CopyDir(sourceDir, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to copy SDK: %w", err)
	}
	if err := alignAndCheckCriticalFiles(tmpDir, binDirs, sdkType); err != nil {
		os.RemoveAll(tmpDir)
		return err
	}
	if verify != nil {
		if err := verify(tmpDir); err != nil {
			os.RemoveAll(tmpDir)
			return err
		}
	}
	// Rename old targetDir to .old (if it exists). This preserves the old
	// version so it can be restored if the next Rename fails.
	if _, err := os.Stat(targetDir); err == nil {
		if renameErr := os.Rename(targetDir, oldDir); renameErr != nil {
			// H1: Never delete the live version as fallback. Abort so the
			// existing directory is preserved instead of losing data.
			os.RemoveAll(tmpDir)
			return fmt.Errorf("failed to backup existing directory for atomic replace: %w", renameErr)
		}
	}
	// Rename tmpDir into place. If this fails, restore from .old.
	if err := os.Rename(tmpDir, targetDir); err != nil {
		if _, statErr := os.Stat(oldDir); statErr == nil {
			os.Rename(oldDir, targetDir)
		}
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to move files into place: %w", err)
	}
	// Success — clean up .old.
	os.RemoveAll(oldDir)
	return nil
}

// alignAndCheckCriticalFiles aligns the import layout of dir (wrapping a flat
// layout into the fetcher's expected top-level dir when needed) and only THEN
// runs the layer-2 critical-files check on the aligned result.
//
// The check MUST run after alignment: directory / PATH imports of Go, Dart,
// Android, Perl (Windows) and Python (all platforms) — and the JDK macOS PATH
// import — arrive flat (bin/... with no go/ dart-sdk/ python/ ... wrapper),
// so checking the pre-alignment layout wrongly rejects complete SDKs as
// "SDK incomplete". Archive imports whose layout already carries the wrapper
// are unaffected: AlignImportLayout is a no-op for them.
func alignAndCheckCriticalFiles(dir string, binDirs []string, t sdk.SdkType) error {
	if err := pathmgr.AlignImportLayout(dir, binDirs); err != nil {
		logger.Warn("Failed to align import layout: %v", err)
	}
	return checkCriticalFiles(dir, t)
}

// criticalFilesFor returns the relative paths (from SDK root) of files that
// must exist for the SDK to be considered complete. Used by the import flow's
// layer-2 integrity check, which runs AFTER AlignImportLayout (see
// alignAndCheckCriticalFiles) so flat imports are judged on their aligned
// layout. The paths therefore match the post-alignment / download-install
// layout (e.g. "go/bin/go" with the wrapper dir present).
func criticalFilesFor(t sdk.SdkType) []string {
	switch t {
	case sdk.Golang:
		if runtime.GOOS == "windows" {
			return []string{"go/bin/go.exe", "go/bin/gofmt.exe"}
		}
		return []string{"go/bin/go", "go/bin/gofmt"}
	case sdk.NodeJS:
		if runtime.GOOS == "windows" {
			return []string{"node.exe", "npm.cmd"}
		}
		return []string{"bin/node", "bin/npm"}
	case sdk.JDK:
		if runtime.GOOS == "darwin" {
			return []string{"Contents/Home/bin/java", "Contents/Home/bin/javac"}
		}
		if runtime.GOOS == "windows" {
			return []string{"bin/java.exe", "bin/javac.exe"}
		}
		return []string{"bin/java", "bin/javac"}
	case sdk.Python:
		if runtime.GOOS == "windows" {
			return []string{"python/python.exe"}
		}
		return []string{"python/bin/python3"}
	case sdk.Rust:
		if runtime.GOOS == "windows" {
			return []string{"cargo/bin/cargo.exe", "rustc/bin/rustc.exe"}
		}
		return []string{"cargo/bin/cargo", "rustc/bin/rustc"}
	case sdk.Ruby:
		if runtime.GOOS == "windows" {
			return []string{"bin/ruby.exe"}
		}
		return []string{"bin/ruby"}
	case sdk.DotNet:
		if runtime.GOOS == "windows" {
			return []string{"dotnet.exe"}
		}
		return []string{"dotnet"}
	case sdk.PHP:
		if runtime.GOOS == "windows" {
			return []string{"php.exe"}
		}
		return []string{"php"}
	case sdk.Perl:
		if runtime.GOOS == "windows" {
			return []string{"perl/bin/perl.exe"}
		}
		return []string{"bin/perl"}
	case sdk.Maven:
		if runtime.GOOS == "windows" {
			return []string{"bin/mvn.cmd"}
		}
		return []string{"bin/mvn"}
	case sdk.Gradle:
		if runtime.GOOS == "windows" {
			return []string{"bin/gradle.bat"}
		}
		return []string{"bin/gradle"}
	case sdk.Flutter:
		if runtime.GOOS == "windows" {
			return []string{"bin/flutter.bat"}
		}
		return []string{"bin/flutter"}
	case sdk.Android:
		if runtime.GOOS == "windows" {
			return []string{"cmdline-tools/bin/sdkmanager.bat"}
		}
		return []string{"cmdline-tools/bin/sdkmanager"}
	case sdk.Dart:
		if runtime.GOOS == "windows" {
			return []string{"dart-sdk/bin/dart.exe"}
		}
		return []string{"dart-sdk/bin/dart"}
	default:
		return nil
	}
}

// checkCriticalFiles verifies that the critical files for the SDK type exist
// in sdkRoot. Returns an error listing the first missing file.
func checkCriticalFiles(sdkRoot string, t sdk.SdkType) error {
	for _, file := range criticalFilesFor(t) {
		if _, err := os.Stat(filepath.Join(sdkRoot, file)); err != nil {
			return fmt.Errorf("SDK incomplete, missing %s", file)
		}
	}
	return nil
}
