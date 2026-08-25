package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/fsutil"
	"svc/internal/logger"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
)

// isSystemPath returns true if the path is a known OS system directory
// where SVC data should never be placed (would corrupt the OS).
func isSystemPath(path string) bool {
	cleaned := strings.ToLower(filepath.Clean(path))
	// Per-user temp dirs are not system locations: on macOS os.TempDir() is
	// /var/folders/..., which would otherwise be rejected by the /var root.
	if tmp := strings.ToLower(filepath.Clean(os.TempDir())); tmp != "" && (cleaned == tmp || strings.HasPrefix(cleaned, tmp+string(os.PathSeparator))) {
		return false
	}
	if runtime.GOOS == "windows" {
		systemRoots := []string{`c:\windows`, `c:\program files`, `c:\program files (x86)`, `c:\programdata`, `c:\system32`}
		for _, root := range systemRoots {
			if cleaned == root || strings.HasPrefix(cleaned, root+`\`) {
				return true
			}
		}
	} else {
		systemRoots := []string{"/usr", "/bin", "/sbin", "/etc", "/var", "/boot", "/dev", "/proc", "/sys", "/root", "/lib"}
		for _, root := range systemRoots {
			if cleaned == root || strings.HasPrefix(cleaned, root+"/") {
				return true
			}
		}
	}
	return false
}

// validateMigrationPaths checks that a migration from oldDir to newDir is
// safe before any filesystem mutation happens. It is a pure function (only
// reads via os.Stat) so it can be unit-tested without an App.
//
// Rules:
//  1. newDir must not already exist — as ANY type. Rejecting only
//     directories is unsafe: if newDir is an existing regular file, CopyDir
//     fails and the M10 cleanup (os.RemoveAll(newDir)) would delete the
//     user's pre-existing file.
//  2. Neither path may be nested inside the other. If newDir is inside
//     oldDir, the final os.RemoveAll(oldDir) would delete the freshly
//     migrated data (silent data loss); if oldDir is inside newDir the copy
//     would recurse into itself.
func validateMigrationPaths(oldDir, newDir string) error {
	oldDir = filepath.Clean(oldDir)
	newDir = filepath.Clean(newDir)

	if _, err := os.Stat(newDir); err == nil {
		return apperr.New(apperr.TargetExists, map[string]string{"path": newDir})
	}

	// Ancestor check. Windows paths are case-insensitive, so compare
	// lower-cased there (matching isSystemPath above).
	oldCmp, newCmp := oldDir, newDir
	if runtime.GOOS == "windows" {
		oldCmp = strings.ToLower(oldCmp)
		newCmp = strings.ToLower(newCmp)
	}
	sep := string(filepath.Separator)
	if newCmp == oldCmp || strings.HasPrefix(newCmp, oldCmp+sep) {
		return apperr.New(apperr.NestedDirs, map[string]string{"path": newDir})
	}
	if strings.HasPrefix(oldCmp, newCmp+sep) {
		return apperr.New(apperr.NestedDirs, map[string]string{"path": newDir})
	}
	return nil
}

func (m *Manager) GetDefaultInstallPath() string {
	return config.DefaultSvcDir()
}

func (m *Manager) GetInstallPath() string {
	return m.cfg.SvcDir()
}

func (m *Manager) MigrateInstallPath(newPath string) error {
	oldDir := m.cfg.SvcDir()
	newDir := filepath.Clean(newPath)

	// N2: Reject system directories and relative paths.
	if !filepath.IsAbs(newDir) {
		return apperr.New(apperr.PathNotAbsolute, map[string]string{"path": newDir})
	}
	if isSystemPath(newDir) {
		return apperr.New(apperr.SystemDir, map[string]string{"path": newDir})
	}

	logger.Info("Starting install path migration: %s -> %s", oldDir, newDir)

	if oldDir == newDir {
		logger.Info("Source and target are the same, skipping migration")
		return nil
	}

	// Reject an existing target (file OR directory) and nested source/target
	// paths before touching anything: see validateMigrationPaths.
	if err := validateMigrationPaths(oldDir, newDir); err != nil {
		logger.Error("Migration target rejected: %v", err)
		return err
	}

	// Backup old directory to desktop, failure does not block migration (only logs warning)
	backupPath, backupErr := pathmgr.BackupDir(oldDir)
	if backupErr != nil {
		logger.Warn("Failed to backup old install directory: %v", backupErr)
	} else {
		logger.Info("Old install directory backed up to: %s", backupPath)
	}

	logger.Info("Copying files from %s to %s", oldDir, newDir)
	if err := fsutil.CopyDir(oldDir, newDir); err != nil {
		logger.Error("Failed to copy directory: %v", err)
		// M10: Clean up partial copy so retry doesn't hit "target already exists".
		os.RemoveAll(newDir)
		return fmt.Errorf("failed to copy directory: %w", err)
	}
	logger.Info("File copy completed")

	installedSDKs := make(map[string]string)
	for _, sdkType := range sdk.AllSdkTypes() {
		activeVersion := m.cfg.GetActiveVersion(string(sdkType))
		if activeVersion != "" {
			installedSDKs[string(sdkType)] = activeVersion
		}
	}

	// Switch the config to the new directory. The shell rc source line points
	// to the fixed ~/.svc.rc location, so it never needs updating; only the
	// SVC_HOME variable inside .svc.rc changes.
	m.cfg.SetSvcDir(newDir)

	// Re-run shim setup at the new location: recreates the shims dir, refreshes
	// the shim binary, and regenerates .svc.rc with the new SVC_HOME.
	if err := m.shimMgr.EnsureSetup(); err != nil {
		logger.Error("Shim setup at new location failed, aborting migration: %v", err)
		os.RemoveAll(newDir)
		m.cfg.SetSvcDir(oldDir) // revert in-memory cfg so app keeps working until restart
		return fmt.Errorf("shim setup failed at new location: %w", err)
	}

	// Re-create shims for every active SDK at the new install path.
	logger.Info("Re-configuring %d active SDKs at new location", len(installedSDKs))
	for sdkTypeStr, activeVersion := range installedSDKs {
		f := m.registry.Get(sdk.SdkType(sdkTypeStr))
		if f == nil {
			continue
		}
		versionDir := m.cfg.SdkVersionDir(sdkTypeStr, activeVersion)
		logger.Info("Re-configuring: %s %s", sdkTypeStr, activeVersion)
		if err := m.pathMgr.ConfigureSdk(sdkTypeStr, versionDir, f.GetBinDirs(), f.GetExtraEnvVars()); err != nil {
			logger.Warn("Failed to re-configure %s: %v", sdkTypeStr, err)
		}
	}

	// N3: Persist the new installPath BEFORE deleting the old dir.
	// settings.json lives at ~/.svc/ (fixed location, not inside install dir).
	// If oldDir was ~/.svc itself, RemoveAll would delete settings.json;
	// saving first ensures the new path is recorded even if deletion fails.
	// If oldDir != ~/.svc, settings.json is untouched by RemoveAll.
	s := m.settings.Get()
	s.InstallPath = newDir
	if err := m.settings.Update(s); err != nil {
		// M4: Do NOT RemoveAll the old directory if persisting the new install
		// path failed. The new path must be recorded in settings.json before
		// the old dir is deleted; otherwise a restart would fall back to the
		// old (now-deleted) path and lose the install. Abort here instead.
		logger.Error("Failed to save new install path: %v", err)
		// Roll back in-memory state: cfg already points at newDir and
		// EnsureSetup above has already rewritten PATH / .svc.rc for the new
		// location. Restore cfg first, then re-run shim setup at the old
		// location so PATH / .svc.rc point back at the surviving install
		// (mirrors the rollback in the EnsureSetup failure branch above).
		m.cfg.SetSvcDir(oldDir)
		if rbErr := m.shimMgr.EnsureSetup(); rbErr != nil {
			logger.Error("Failed to restore shim setup at old directory %s: %v", oldDir, rbErr)
		}
		// Clean up the copied-but-unpersisted newDir so a retry is not
		// blocked by the "target already exists" guard (oldDir is intact).
		if rmErr := os.RemoveAll(newDir); rmErr != nil {
			logger.Warn("Failed to remove unpersisted migration target %s: %v", newDir, rmErr)
		}
		return fmt.Errorf("failed to save new install path: %w", err)
	}

	// Re-point the logger at the new install directory BEFORE deleting the
	// old one: the active log file and logDir still live under oldDir, which
	// os.RemoveAll is about to remove.
	if err := logger.Reinit(newDir); err != nil {
		logger.Warn("Failed to reinitialize logger at %s: %v", newDir, err)
	}

	logger.Info("Removing old install directory: %s", oldDir)
	if err := os.RemoveAll(oldDir); err != nil {
		logger.Warn("Failed to delete old install directory (%s): %v", oldDir, err)
	}

	// H4: If oldDir was ~/.svc itself, RemoveAll deleted settings.json.
	// Re-create it at the fixed ~/.svc/ location with the new InstallPath.
	if err := m.settings.Update(s); err != nil {
		logger.Error("Failed to re-create settings after migration: %v", err)
	}

	logger.Info("Install path migration completed successfully")
	return nil
}
