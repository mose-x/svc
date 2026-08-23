package pathmgr

// PathEntry describes a PATH entry
type PathEntry struct {
	Path      string `json:"path"`
	IsManaged bool   `json:"isManaged"` // whether under .svc
	SdkType   string `json:"sdkType"`   // identified SDK type (empty = unknown)
	Source    string `json:"source"`    // "user", "system", or "external"
	// SystemProtected marks OS-managed directories (/usr/bin, C:\Windows, ...)
	// that must never be imported; the UI shows a "system-managed" tag instead
	// of the import button.
	SystemProtected bool `json:"systemProtected"`
	// ExternalManager names the external version manager owning this copy
	// ("nvm-rust" / "nvm" for Node.js); the UI tells the user to keep using
	// that manager instead of importing. Empty for standalone copies.
	ExternalManager string `json:"externalManager"`
}

// ShimConfigurer is implemented by shimmanager.Manager. When injected into a
// PathManager via SetShimManager, all SDK path/env configuration is routed
// through the shims model (one shims dir in PATH + .svc.rc), instead of
// writing per-SDK entries to shell rc files or the registry PATH.
type ShimConfigurer interface {
	ConfigureSdk(sdkType string, versionDir string, binDirs []string, extraEnvVars map[string]string) error
	RemoveSdk(sdkType string, extraEnvVars map[string]string) error
	EnsureSetup() error
}

// PathManager manages system PATH environment variables
type PathManager interface {
	// ConfigureSdk adds the specified SDK version's bin directories to PATH (persistent)
	ConfigureSdk(sdkType string, versionDir string, binDirs []string, extraEnvVars map[string]string) error
	// RemoveSdk removes the specified SDK from PATH
	RemoveSdk(sdkType string, extraEnvVars map[string]string) error
	// GetCurrentConfig returns the PATH entries currently managed by SVC
	GetCurrentConfig() (map[string]string, error)
	// GetAllPathEntries returns all PATH entries
	GetAllPathEntries() ([]PathEntry, error)
	// CleanExternalPaths cleans non-SVC-managed external PATH entries matching the same SDK type and version
	// sourcePath is the specific source path of the import; matched entries are removed first
	CleanExternalPaths(sdkType string, version string, sourcePath string)
	// DetectSystemConflicts checks whether the system level contains env var configs matching the SDK
	// Returns the list of conflicting entries; empty list means no conflicts
	DetectSystemConflicts(sdkType string, envKeys []string) []string
	// SetShimManager injects a shim-based configurator. Once set, ConfigureSdk
	// and RemoveSdk delegate to it instead of touching shell rc / registry PATH.
	SetShimManager(mgr ShimConfigurer)
}
