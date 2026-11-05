package pathmgr

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"svc/internal/config"
)

const (
	systemEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey   = `Environment`
)

// readUserPathValue reads the user PATH together with its registry value type.
// A missing value is a legitimate fresh-profile state and returns existed=false
// with no error. Any other problem — access denied, or an unsupported type such
// as REG_MULTI_SZ/REG_DWORD — is returned as an error so callers abort instead
// of clobbering a value they could not understand. Mirrors the shimmanager
// helper of the same name; kept local to avoid a cross-package dependency.
func readUserPathValue(k registry.Key) (val string, valType uint32, existed bool, err error) {
	// GetValue with a nil buffer only queries the size and type.
	_, valType, err = k.GetValue("Path", nil)
	if errors.Is(err, registry.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to probe user PATH type: %w", err)
	}
	if valType != registry.SZ && valType != registry.EXPAND_SZ {
		return "", 0, false, fmt.Errorf("user PATH has unsupported registry value type %d; refusing to modify it", valType)
	}
	val, _, err = k.GetStringValue("Path")
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to read user PATH: %w", err)
	}
	return val, valType, true, nil
}

// writeUserPathValue writes the user PATH preserving its value type. The type
// decision lives in the pure choosePathValueType function (unit-tested on every
// platform); writing REG_SZ over a REG_EXPAND_SZ PATH would silently stop %VAR%
// expansion, which is the O8 regression this prevents.
func writeUserPathValue(k registry.Key, val string, origType uint32, existed bool) error {
	if choosePathValueType(origType, existed, val) == regTypeExpandSZ {
		return k.SetExpandStringValue("Path", val)
	}
	return k.SetStringValue("Path", val)
}

// WindowsPathManager handles PATH management on Windows.
// When a ShimConfigurer is injected (via SetShimManager), all SDK configuration
// is routed through the shims model: a single shims dir entry is added to the
// user PATH in the registry (once), and per-SDK env vars are written to the
// registry by the shim manager. This avoids writing one PATH entry per SDK.
type WindowsPathManager struct {
	cfg  *config.Config
	shim ShimConfigurer
}

// NewPathManager creates a PathManager for Windows
func NewPathManager(cfg *config.Config) PathManager {
	return &WindowsPathManager{cfg: cfg}
}

// SetShimManager injects a shim-based configurator.
func (m *WindowsPathManager) SetShimManager(mgr ShimConfigurer) {
	m.shim = mgr
}

func (m *WindowsPathManager) ConfigureSdk(sdkType string, versionDir string, binDirs []string, extraEnvVars map[string]string) error {
	// Shims model: delegate entirely to the shim manager. The shim manager
	// creates shims, updates shims.json, writes env vars to the registry, and
	// keeps .svc.rc in sync. The user PATH already contains only the shims dir.
	if m.shim != nil {
		return m.shim.ConfigureSdk(sdkType, versionDir, binDirs, extraEnvVars)
	}

	// Legacy fallback (no shim manager injected): write per-SDK PATH entries.
	// Add each binDir to PATH (first one is primary).
	for _, binDir := range binDirs {
		binPath := versionDir
		if binDir != "" {
			binPath = filepath.Join(versionDir, binDir)
		}
		if err := m.addToUserPath(binPath, sdkType); err != nil {
			return fmt.Errorf("failed to add to user PATH: %w", err)
		}
	}

	for key, relPath := range extraEnvVars {
		value := versionDir
		if relPath != "" {
			value = filepath.Join(versionDir, relPath)
		}
		if err := m.setUserEnvVar(key, value); err != nil {
			return fmt.Errorf("failed to set user %s: %w", key, err)
		}
	}

	broadcastEnvChange()
	return nil
}

func (m *WindowsPathManager) RemoveSdk(sdkType string, extraEnvVars map[string]string) error {
	// Shims model: delegate entirely to the shim manager.
	if m.shim != nil {
		return m.shim.RemoveSdk(sdkType, extraEnvVars)
	}

	// Legacy fallback.
	if err := m.removeSvcPathsFromUserPath(sdkType); err != nil {
		return err
	}
	for key := range extraEnvVars {
		m.removeUserEnvVar(key)
	}
	broadcastEnvChange()
	return nil
}

func (m *WindowsPathManager) GetCurrentConfig() (map[string]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.READ)
	if err != nil {
		return nil, err
	}
	defer k.Close()

	result := make(map[string]string)
	if path, _, err := k.GetStringValue("Path"); err == nil {
		for _, p := range strings.Split(path, ";") {
			if hasSvcSegment(p) {
				result["PATH"] = p
			}
		}
	}
	return result, nil
}

func (m *WindowsPathManager) addToUserPath(binPath string, sdkType string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	currentPath, valType, existed, err := readUserPathValue(k)
	if err != nil {
		return err
	}

	sdkDir := m.cfg.SdkDir(sdkType)
	parts := strings.Split(currentPath, ";")
	var filtered []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasPathPrefix(p, sdkDir) {
			continue
		}
		filtered = append(filtered, p)
	}

	filtered = append([]string{binPath}, filtered...)
	newPath := strings.Join(filtered, ";")

	return writeUserPathValue(k, newPath, valType, existed)
}

func (m *WindowsPathManager) removeSvcPathsFromUserPath(sdkType string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	currentPath, valType, existed, err := readUserPathValue(k)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}

	sdkDir := m.cfg.SdkDir(sdkType)
	parts := strings.Split(currentPath, ";")
	var filtered []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasPathPrefix(p, sdkDir) {
			continue
		}
		filtered = append(filtered, p)
	}

	newPath := strings.Join(filtered, ";")
	return writeUserPathValue(k, newPath, valType, existed)
}

func (m *WindowsPathManager) setUserEnvVar(key, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(key, value)
}

func (m *WindowsPathManager) removeUserEnvVar(key string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
	if err != nil {
		return
	}
	defer k.Close()
	k.DeleteValue(key)
}

// CleanExternalPaths cleans non-SVC-managed external PATH entries matching the same SDK type and version
func (m *WindowsPathManager) CleanExternalPaths(sdkType string, version string, sourcePath string) {
	if err := m.cleanExternalFromKey(sdkType, version, sourcePath); err == nil {
		broadcastEnvChange()
	}
}

func (m *WindowsPathManager) cleanExternalFromKey(sdkType string, version string, sourcePath string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	currentPath, valType, existed, err := readUserPathValue(k)
	if err != nil {
		return err
	}
	if !existed {
		return fmt.Errorf("no external paths removed")
	}

	svcDir := m.cfg.SvcDir()
	sourcePathClean := filepath.Clean(sourcePath)
	parts := strings.Split(currentPath, ";")
	var filtered []string
	removed := false
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasSvcSegment(p) {
			filtered = append(filtered, p)
			continue
		}
		if hasPathPrefix(p, svcDir) {
			filtered = append(filtered, p)
			continue
		}
		if sourcePathClean != "" && strings.EqualFold(filepath.Clean(p), sourcePathClean) {
			removed = true
			continue
		}
		if sourcePathClean != "" {
			sourceRoot := DetectSdkRoot(sourcePathClean, sdkType)
			pRoot := DetectSdkRoot(p, sdkType)
			if sourceRoot != "" && pRoot != "" && strings.EqualFold(filepath.Clean(pRoot), filepath.Clean(sourceRoot)) {
				removed = true
				continue
			}
		}
		detected := detectSdkTypesByBin(p)
		if len(detected) == 0 {
			if t := detectSdkTypeFromPath(p); t != "" {
				detected = []string{t}
			}
		}
		if sliceContains(detected, sdkType) && version != "" && strings.Contains(p, version) {
			removed = true
			continue
		}
		filtered = append(filtered, p)
	}

	if !removed {
		return fmt.Errorf("no external paths removed")
	}

	newPath := strings.Join(filtered, ";")
	return writeUserPathValue(k, newPath, valType, existed)
}

func (m *WindowsPathManager) GetAllPathEntries() ([]PathEntry, error) {
	var entries []PathEntry
	seen := make(map[string]bool)

	// Read user-level first (primary config location)
	userEntries := m.readPathFromKey(registry.CURRENT_USER, userEnvKey)
	for _, e := range userEntries {
		if !seen[e.Path] {
			entries = append(entries, e)
			seen[e.Path] = true
		}
	}

	// Also read system-level (for display, marked as non-managed)
	systemEntries := m.readPathFromKey(registry.LOCAL_MACHINE, systemEnvKey)
	for _, e := range systemEntries {
		if !seen[e.Path] {
			entries = append(entries, e)
			seen[e.Path] = true
		}
	}

	return DeduplicateEntries(entries), nil
}

func (m *WindowsPathManager) readPathFromKey(root registry.Key, keyPath string) []PathEntry {
	k, err := registry.OpenKey(root, keyPath, registry.READ)
	if err != nil {
		return nil
	}
	defer k.Close()

	pathVal, _, err := k.GetStringValue("Path")
	if err != nil {
		return nil
	}

	var entries []PathEntry
	for _, p := range strings.Split(pathVal, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasSvcSegment(p) {
			entries = append(entries, PathEntry{
				Path:      p,
				IsManaged: true,
				SdkType:   detectSdkTypeFromPath(p),
			})
			continue
		}
		entries = append(entries, buildUnmanagedEntries(p, m.cfg)...)
	}
	return entries
}

// DetectSystemConflicts checks whether the HKLM system-level registry contains matching env var configs for the SDK
func (m *WindowsPathManager) DetectSystemConflicts(sdkType string, envKeys []string) []string {
	var conflicts []string

	k, err := registry.OpenKey(registry.LOCAL_MACHINE, systemEnvKey, registry.READ)
	if err == nil {
		defer k.Close()

		if pathVal, _, err := k.GetStringValue("Path"); err == nil {
			sdkDir := m.cfg.SdkDir(sdkType)
			for _, p := range strings.Split(pathVal, ";") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if hasPathPrefix(p, sdkDir) {
					conflicts = append(conflicts, fmt.Sprintf("PATH: %s", p))
					continue
				}
				detected := detectSdkTypesByBin(p)
				if len(detected) == 0 {
					if t := detectSdkTypeFromPath(p); t != "" {
						detected = []string{t}
					}
				}
				if sliceContains(detected, sdkType) {
					conflicts = append(conflicts, fmt.Sprintf("PATH: %s", p))
				}
			}
		}

		for _, key := range envKeys {
			if val, _, err := k.GetStringValue(key); err == nil && val != "" {
				conflicts = append(conflicts, fmt.Sprintf("%s=%s", key, val))
			}
		}
	}

	return conflicts
}

// broadcastEnvChange broadcasts the environment variable change notification
func broadcastEnvChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	HWND_BROADCAST := uintptr(0xFFFF)
	WM_SETTINGCHANGE := uintptr(0x001A)
	SMTO_ABORTIFHUNG := uintptr(0x0002)

	envStr, _ := syscall.UTF16PtrFromString("Environment")
	sendMessageTimeout.Call(
		HWND_BROADCAST,
		WM_SETTINGCHANGE,
		0,
		uintptr(unsafe.Pointer(envStr)),
		SMTO_ABORTIFHUNG,
		5000,
		0,
	)
}
