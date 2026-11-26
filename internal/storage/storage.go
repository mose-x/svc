package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"svc/internal/config"
	"svc/internal/helpers"
	"svc/internal/logger"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
	"svc/internal/shimmanager"
)

// Manager owns the SVC on-disk data lifecycle: version uninstall, storage
// accounting, cache cleaning, and install-path migration.
type Manager struct {
	cfg      *config.Config
	registry *sdk.Registry
	pathMgr  pathmgr.PathManager
	shimMgr  *shimmanager.Manager
	settings *config.SettingsManager
}

// NewManager wires a Manager with its dependencies. registry/pathMgr/shimMgr/
// settings may be nil in unit tests that only exercise cfg-backed paths.
func NewManager(cfg *config.Config, registry *sdk.Registry, pathMgr pathmgr.PathManager, shimMgr *shimmanager.Manager, settings *config.SettingsManager) *Manager {
	return &Manager{cfg: cfg, registry: registry, pathMgr: pathMgr, shimMgr: shimMgr, settings: settings}
}

// ActiveVersionDeletedPrefix marks the sentinel UninstallVersion returns when
// the version it removed was the active one.
//
// A prefixed string rather than a typed error or an apperr code, for a reason
// worth keeping: the GUI receives errors across the Wails boundary as their
// message text, and DetailPanel.tsx matches exactly this prefix to turn the case
// into a warning toast plus a refresh instead of an error dialog. A drift guard
// in storage_test.go pins the Go constant to that literal, so neither side can
// rename it on its own.
const ActiveVersionDeletedPrefix = "ACTIVE_VERSION_DELETED:"

// ActiveVersionDeleted reports whether err is the ActiveVersionDeletedPrefix
// sentinel and returns the SDK type it names.
//
// Deleting the version the user was on is not a failure -- the uninstall did
// exactly what was asked -- so both shells report success with a warning rather
// than letting it become a non-zero exit code or an error dialog.
func ActiveVersionDeleted(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	if !strings.HasPrefix(text, ActiveVersionDeletedPrefix) {
		return "", false
	}
	return strings.TrimPrefix(text, ActiveVersionDeletedPrefix), true
}

type StorageInfo struct {
	SdkType      string `json:"sdkType"`
	DisplayName  string `json:"displayName"`
	SdkDir       string `json:"sdkDir"`
	TotalSize    int64  `json:"totalSize"`
	VersionCount int    `json:"versionCount"`
	ActiveVer    string `json:"activeVer"`
}

func (m *Manager) UninstallVersion(sdkType string, version string) error {
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return err
	}
	if err := helpers.ValidatePathSegment(version); err != nil {
		return err
	}

	logger.Info("Uninstalling %s version: %s", sdkType, version)

	active := m.cfg.GetActiveVersion(sdkType)
	wasActive := active == version

	versionDir := m.cfg.SdkVersionDir(sdkType, version)
	if _, err := os.Stat(versionDir); os.IsNotExist(err) {
		logger.Error("Version directory does not exist: %s", versionDir)
		return fmt.Errorf("version directory does not exist: %s", version)
	}

	if err := os.RemoveAll(versionDir); err != nil {
		logger.Error("Failed to delete version directory %s: %v", versionDir, err)
		return fmt.Errorf("failed to delete version directory: %w", err)
	}

	// If we deleted the active version, clear the active version config
	if wasActive {
		logger.Info("Deleted active version, clearing active version config")
		if err := m.cfg.ClearActiveVersion(sdkType); err != nil {
			logger.Error("Failed to clear active version config: %v", err)
		}
	}

	// Tear down the shim layer for this SDK type when the last version is
	// gone. RemoveSdk deletes shim files (go.exe/gofmt.exe/...), clears the
	// SDK's entries from shims.json, and drops its env-var lines from
	// .svc.rc / the registry. Without this, uninstalled SDKs leave orphan
	// shims that resolve to "no active version" and keep PathConfigured=true
	// (the shim files are still found by IsCommandAvailable).
	//
	// Only remove when NO versions remain: if other versions of the same
	// SDK are still installed, their shims must stay (shims route by SDK
	// type, not by version — the active version is resolved at run time).
	//
	// M7: Propagate the GetInstalledVersions error instead of treating any
	// read failure as "0 remaining". A transient read error (permission,
	// path is a file, ...) must NOT trigger shim teardown, otherwise
	// installed-but-unreadable SDKs lose their shims.
	left, err := m.noVersionsLeft(sdkType)
	if err != nil {
		logger.Warn("Failed to check remaining versions for %s, skipping shim teardown: %v", sdkType, err)
	} else if left {
		extraEnvVars := m.getExtraEnvVars(sdkType)
		if err := m.pathMgr.RemoveSdk(sdkType, extraEnvVars); err != nil {
			logger.Warn("Failed to remove shims for %s: %v", sdkType, err)
		}
	}

	if wasActive {
		// Return special error to signal frontend to refresh and show warning
		return fmt.Errorf("%s%s", ActiveVersionDeletedPrefix, sdkType)
	}

	logger.Info("Successfully uninstalled %s version %s", sdkType, version)
	return nil
}

// noVersionsLeft reports whether no installed versions remain for sdkType.
// Used after UninstallVersion to decide whether to tear down the shim layer
// for the whole SDK type (shims route by type, so they are only obsolete
// once the last version is gone).
//
// M7: Returns (bool, error) so callers can distinguish a genuine "no
// versions" result from a read failure. A read error (permission, path is a
// file, ...) is propagated; callers must NOT treat it as "0 remaining" or
// they would wrongly tear down shims for SDKs whose versions are merely
// unreadable.
func (m *Manager) noVersionsLeft(sdkType string) (bool, error) {
	remaining, err := m.cfg.GetInstalledVersions(sdkType)
	if err != nil {
		return false, err
	}
	return len(remaining) == 0, nil
}

// getExtraEnvVars returns the extra env vars (JAVA_HOME, GOROOT, ...) declared
// by the SDK fetcher, mirroring the InstallSdk/ImportPathSdk call sites so the
// RemoveSdk path stays symmetric with the ConfigureSdk path.
func (m *Manager) getExtraEnvVars(sdkType string) map[string]string {
	if m.registry == nil {
		return nil
	}
	f := m.registry.Get(sdk.SdkType(sdkType))
	if f == nil {
		return nil
	}
	return f.GetExtraEnvVars()
}

func (m *Manager) GetStorageInfo() []StorageInfo {
	var infos []StorageInfo
	for _, t := range sdk.AllSdkTypes() {
		sdkDir := m.cfg.SdkDir(string(t))
		entries, err := os.ReadDir(sdkDir)
		if err != nil {
			continue
		}

		var totalSize int64
		var versionCount int
		for _, e := range entries {
			if e.IsDir() {
				versionCount++
				totalSize += dirSize(filepath.Join(sdkDir, e.Name()))
			}
		}

		if versionCount > 0 {
			infos = append(infos, StorageInfo{
				SdkType:      string(t),
				DisplayName:  sdk.SdkDisplayName(t),
				SdkDir:       sdkDir,
				TotalSize:    totalSize,
				VersionCount: versionCount,
				ActiveVer:    m.cfg.GetActiveVersion(string(t)),
			})
		}
	}
	return infos
}

func (m *Manager) GetTmpCacheSize() int64 {
	return dirSize(m.cfg.TmpDir())
}

func (m *Manager) CleanTmpCache() error {
	tmpDir := m.cfg.TmpDir()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		logger.Error("Failed to read cache directory %s: %v", tmpDir, err)
		return fmt.Errorf("failed to read cache directory: %w", err)
	}
	logger.Info("Cleaning temporary cache: %d items in %s", len(entries), tmpDir)
	var firstErr error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(tmpDir, e.Name())); err != nil {
			logger.Error("Failed to remove cache entry %s: %v", e.Name(), err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("failed to clean temporary cache: %w", firstErr)
	}
	logger.Info("Temporary cache cleaned")
	return nil
}

func (m *Manager) CleanInactiveVersions(sdkType string) error {
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return err
	}

	active := m.cfg.GetActiveVersion(sdkType)
	sdkDir := m.cfg.SdkDir(sdkType)
	entries, err := os.ReadDir(sdkDir)
	if err != nil {
		logger.Error("Failed to read directory %s: %v", sdkDir, err)
		return fmt.Errorf("failed to read directory: %w", err)
	}

	var cleaned int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == active {
			continue
		}
		logger.Info("Removing inactive version: %s %s", sdkType, e.Name())
		if err := os.RemoveAll(filepath.Join(sdkDir, e.Name())); err != nil {
			logger.Error("Failed to delete %s: %v", e.Name(), err)
			return fmt.Errorf("failed to delete %s: %w", e.Name(), err)
		}
		cleaned++
	}
	// If no versions remain (e.g. active was empty and everything was
	// inactive), tear down the shim layer for this SDK type — same rationale
	// as in UninstallVersion: orphan shims keep PathConfigured=true and
	// resolve to "no active version" at run time.
	// M7: on read error, do NOT tear down shims (cannot confirm zero left).
	left, err := m.noVersionsLeft(sdkType)
	if cleaned > 0 && err == nil && left {
		extraEnvVars := m.getExtraEnvVars(sdkType)
		if err := m.pathMgr.RemoveSdk(sdkType, extraEnvVars); err != nil {
			logger.Warn("Failed to remove shims for %s: %v", sdkType, err)
		}
	}
	if err != nil {
		logger.Warn("Failed to check remaining versions for %s, skipped shim teardown: %v", sdkType, err)
	}
	logger.Info("Cleaned %d inactive versions for %s", cleaned, sdkType)
	return nil
}

func dirSize(path string) int64 {
	var size int64
	filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}
