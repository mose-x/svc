package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"svc/internal/logger"
)

const (
	svcDirName   = ".svc"
	configFile   = "config.json"
	tmpDirName   = "tmp"
	cacheDirName = "cache"
	envShFile    = "env.sh"
	shimsDirName = "shims"
	shimsCfgFile = "shims.json"
	rcFileName   = ".svc.rc"
)

// Config manages the ~/.svc directory and application configuration
type Config struct {
	mu      sync.RWMutex
	homeDir string
	svcDir  string
	data    *ConfigData
}

// ConfigData holds data persisted to config.json
type ConfigData struct {
	ActiveVersions map[string]string `json:"activeVersions"` // sdkType -> version
}

// NewConfig creates a configuration manager
func NewConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	svcDir := filepath.Join(home, svcDirName)
	c := &Config{
		homeDir: home,
		svcDir:  svcDir,
		data: &ConfigData{
			ActiveVersions: make(map[string]string),
		},
	}
	if err := c.init(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) init() error {
	// Create the ~/.svc directory
	dirs := []string{
		c.svcDir,
		filepath.Join(c.svcDir, tmpDirName),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	// Create each SDK subdirectory
	for _, name := range []string{
		"nodejs", "jdk", "go", "python", "rust", "ruby", "dotnet", "php", "perl",
		"maven", "gradle",
		"flutter", "android", "dart",
	} {
		if err := os.MkdirAll(filepath.Join(c.svcDir, name), 0755); err != nil {
			return err
		}
	}
	// Load the config file
	return c.load()
}

func (c *Config) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	path := filepath.Join(c.svcDir, configFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.save()
		}
		return err
	}
	if err := json.Unmarshal(data, c.data); err != nil {
		// A corrupt config.json must not be fatal: NewConfig errors make the
		// GUI os.Exit(1) with no recovery path. Back the corrupt file up for
		// manual inspection and fall back to defaults, mirroring the M9
		// handling in settings.go.
		backup := filepath.Join(c.svcDir, fmt.Sprintf("config.corrupt-%d.json", time.Now().UnixNano()))
		if mvErr := os.Rename(path, backup); mvErr != nil {
			logger.Warn("Failed to back up corrupt config file %s: %v", path, mvErr)
		} else {
			logger.Warn("Corrupt config file backed up to %s; using default config", backup)
		}
		c.data = &ConfigData{ActiveVersions: make(map[string]string)}
		return nil
	}
	return nil
}

func (c *Config) save() error {
	// NOTE: caller must hold c.mu. save() is called from load(),
	// SetActiveVersion, ClearActiveVersion — all of which take the write
	// lock before mutating c.data. Acquiring the lock here would deadlock.
	data, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.svcDir, configFile)
	// O9: atomic write via temp file + os.Rename. A partial write to
	// config.json (e.g. process killed mid-write) would corrupt the file
	// and break every shim lookup at startup. Writing to a sibling temp file
	// and renaming over the target is atomic on POSIX (rename(2)) and on
	// Windows (MoveFileEx with MOVEFILE_REPLACE_EXISTING).
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// HomeDir returns the user's home directory
func (c *Config) HomeDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.homeDir
}

// SvcDir returns the ~/.svc path
func (c *Config) SvcDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.svcDir
}

// SetSvcDir sets a custom install directory
func (c *Config) SetSvcDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.svcDir = dir
}

// SetHomeDir overrides the user home directory (mainly for tests, so
// RcFilePath — which derives from homeDir, not svcDir — lands in a temp dir
// instead of the process working directory).
func (c *Config) SetHomeDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.homeDir = dir
}

// DefaultSvcDir returns the default install directory
func DefaultSvcDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home, err = os.Getwd()
		if err != nil {
			return svcDirName
		}
	}
	return filepath.Join(home, svcDirName)
}

// TmpDir returns the temporary download directory
func (c *Config) TmpDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, tmpDirName)
}

// CacheDir returns the directory holding the version cache and the
// cross-process refresh stamps. Every consumer derives the path from here
// rather than joining "cache" itself, so the name exists in exactly one place.
func (c *Config) CacheDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, cacheDirName)
}

// SdkDir returns the storage directory of the specified SDK
func (c *Config) SdkDir(sdkType string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, sdkType)
}

// SdkVersionDir returns the install directory of the specified SDK version
func (c *Config) SdkVersionDir(sdkType string, version string) string {
	// Delegates to SdkDir which acquires the read lock — do NOT also take
	// the lock here (nested RLock deadlocks on some sync.RWMutex impls).
	return filepath.Join(c.SdkDir(sdkType), version)
}

// GetActiveVersion returns the currently active version
func (c *Config) GetActiveVersion(sdkType string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.ActiveVersions[sdkType]
}

// SetActiveVersion sets the currently active version
func (c *Config) SetActiveVersion(sdkType string, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ActiveVersions[sdkType] = version
	return c.save()
}

// ClearActiveVersion clears the active version for the specified SDK type.
// M4: returns the save() error instead of ignoring it, so a failed persist
// is surfaced to the caller rather than silently dropping the entry in-memory.
func (c *Config) ClearActiveVersion(sdkType string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data.ActiveVersions, sdkType)
	return c.save()
}

// GetInstalledVersions returns all locally installed versions.
//
// M7: Now returns ([]string, error) so callers can distinguish "no versions
// installed yet" from a genuine read failure. A missing SDK directory is the
// normal "no versions" state and returns (nil, nil); any other read error
// (permission denied, path is a file, ...) is propagated as (nil, err) so the
// caller does not mistake a transient failure for "0 remaining" and wrongly
// tear down the shim layer.
func (c *Config) GetInstalledVersions(sdkType string) ([]string, error) {
	dir := c.SdkDir(sdkType)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Error("Failed to read SDK directory (%s): %v", dir, err)
			return nil, err
		}
		// SDK dir doesn't exist yet -> genuinely no versions installed.
		return nil, nil
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	return versions, nil
}

// EnvShPath returns the env.sh file path (used on Linux/macOS)
func (c *Config) EnvShPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, envShFile)
}

// ShimsDir returns the shims directory path (~/.svc/shims)
func (c *Config) ShimsDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, shimsDirName)
}

// ShimsConfigPath returns the shims.json file path (~/.svc/shims.json)
func (c *Config) ShimsConfigPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.svcDir, shimsCfgFile)
}

// RcFilePath returns the .svc.rc file path (~/ .svc.rc, fixed location)
func (c *Config) RcFilePath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filepath.Join(c.homeDir, rcFileName)
}

// IsWindows reports whether the current OS is Windows
func IsWindows() bool {
	return runtime.GOOS == "windows"
}
