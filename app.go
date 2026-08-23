package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"svc/internal/config"
	"svc/internal/downloader"
	"svc/internal/importer"
	"svc/internal/installer"
	"svc/internal/logger"
	"svc/internal/logmgr"
	"svc/internal/migrate"
	"svc/internal/pathmgr"
	"svc/internal/pkgmgr"
	"svc/internal/proxy"
	"svc/internal/sdk"
	"svc/internal/settings"
	"svc/internal/shimmanager"
	"svc/internal/storage"
	"svc/internal/update"
	"svc/internal/wailsrt"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed about.json
var aboutJSON []byte

// errUninitialized is returned by bindings whose backing service is only
// created in startup (registry-dependent). The message matches the original
// inline error text.
var errUninitialized = fmt.Errorf("application not fully initialized")

// App struct - Wails bound core structure. It is a thin facade: all business
// logic lives in internal service packages; App wires them together and
// exposes their methods to the Wails frontend.
type App struct {
	ctx          context.Context
	cfg          *config.Config
	registry     *sdk.Registry
	downloader   *downloader.Downloader
	pathMgr      pathmgr.PathManager
	shimMgr      *shimmanager.Manager
	settings     *config.SettingsManager
	proxySvc     *proxy.Service
	settingsSvc  *settings.Service
	storageMgr   *storage.Manager
	updater      *update.Updater
	pkgmgrSvc    *pkgmgr.Service
	installerSvc *installer.Service
	importerSvc  *importer.Service
	appInfo      update.AppInfo
}

// runtimeAdapter implements wailsrt.Runtime on top of the Wails runtime so
// the internal service packages stay free of any Wails dependency.
type runtimeAdapter struct{ ctx context.Context }

func (r *runtimeAdapter) Context() context.Context { return r.ctx }

func (r *runtimeAdapter) EventsEmit(eventName string, data ...any) {
	wailsRuntime.EventsEmit(r.ctx, eventName, data...)
}

func (r *runtimeAdapter) OpenFileDialog(title string, filters []wailsrt.FileFilter) (string, error) {
	fs := make([]wailsRuntime.FileFilter, len(filters))
	for i, f := range filters {
		fs[i] = wailsRuntime.FileFilter{DisplayName: f.DisplayName, Pattern: f.Pattern}
	}
	return wailsRuntime.OpenFileDialog(r.ctx, wailsRuntime.OpenDialogOptions{Title: title, Filters: fs})
}

func (r *runtimeAdapter) OpenDirectoryDialog(title string) (string, error) {
	return wailsRuntime.OpenDirectoryDialog(r.ctx, wailsRuntime.OpenDialogOptions{Title: title})
}

func (r *runtimeAdapter) Quit() { wailsRuntime.Quit(r.ctx) }

// NewApp creates an App instance
func NewApp() *App {
	cfg, err := config.NewConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize config: %v\n", err)
		os.Exit(1)
	}

	logger.Init(cfg.SvcDir())
	logger.Info("Application starting...")

	shimMgr := shimmanager.New(cfg)
	pathMgr := pathmgr.NewPathManager(cfg)
	pathMgr.SetShimManager(shimMgr)

	sm := config.NewSettingsManager(cfg.HomeDir())
	app := &App{
		cfg:         cfg,
		settings:    sm,
		proxySvc:    proxy.New(sm),
		settingsSvc: settings.New(sm),
		pathMgr:     pathMgr,
		shimMgr:     shimMgr,
		downloader:  downloader.NewDownloader(),
	}
	app.appInfo = update.ParseAppInfo(aboutJSON)
	return app
}

// startup called on application launch
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	s := a.settings.Get()
	if s.InstallPath != "" {
		logger.Info("Using custom install path: %s", s.InstallPath)
		a.cfg.SetSvcDir(s.InstallPath)
	}
	a.registry = sdk.NewRegistry(a.cfg, a.settings)
	logger.Info("SDK registry initialized with %d SDK types", len(a.registry.All()))
	a.storageMgr = storage.NewManager(a.cfg, a.registry, a.pathMgr, a.shimMgr, a.settings)
	rt := &runtimeAdapter{ctx: ctx}
	a.updater = update.NewUpdater(a.appInfo, a.settings, a.downloader, a.proxySvc, rt)
	a.pkgmgrSvc = pkgmgr.New(a.cfg, a.registry)
	a.installerSvc = installer.New(a.cfg, a.registry, a.downloader, a.pathMgr, a.shimMgr, a.settings, a.proxySvc, rt)
	a.importerSvc = importer.New(a.cfg, a.registry, a.pathMgr, a.shimMgr, rt)

	// Seed ~/.svc/mirrors.json (the editable "easter egg" GitHub mirror list)
	// and point the version cache at ~/.svc/cache. Both must be (re)initialised
	// after any SetSvcDir override above so the files land in the active SVC dir.
	sdk.InitMirrorsFile(a.cfg.SvcDir())
	sdk.InitVersionCacheDir(filepath.Join(a.cfg.SvcDir(), "cache"))

	// One-time shims setup: creates ~/.svc/shims, installs the shim binary,
	// and adds the single .svc.rc source line (Unix) / shims PATH entry (Windows).
	// This is the only place SVC ever touches the shell rc or registry PATH.
	if err := a.shimMgr.EnsureSetup(); err != nil {
		logger.Warn("Shim setup failed (run 'svc init' to retry): %v", err)
	}

	// Clean up temp directory
	if entries, err := os.ReadDir(a.cfg.TmpDir()); err == nil && len(entries) > 0 {
		logger.Info("Cleaning up %d temporary files from previous run", len(entries))
		for _, e := range entries {
			os.RemoveAll(filepath.Join(a.cfg.TmpDir(), e.Name()))
		}
	}

	logger.Info("Application startup complete")

	// Remove stale old-named rollback backups (e.g. mac SDKVersionControl.bak)
	// left by pre-rename versions; the canonical backup is kept for rollback.
	if exe, err := os.Executable(); err == nil {
		update.CleanStaleBackups(exe)
	}

	// Re-point any svc/legacy shortcut whose icon/target still references the
	// old renamed folder (Windows only; no-op elsewhere).
	migrate.RepairShortcutIcons()

	// One-time rename migration for pre-rename self-updated installs. No-op
	// off Windows and for installs that already run under the svc name.
	migrate.MaybeShowLegacyMigrationPrompt(ctx, rt)
}

// shutdown called on application exit
func (a *App) shutdown(ctx context.Context) {
	logger.Info("Application shutting down...")
	if a.installerSvc != nil {
		a.installerSvc.CancelAll()
	}
	logger.Info("Application shutdown complete")
}

// GetPathEntries retrieves all PATH entries
func (a *App) GetPathEntries() ([]pathmgr.PathEntry, error) {
	return a.pathMgr.GetAllPathEntries()
}

// CheckProxy verifies that the configured proxy can reach the given URL.
func (a *App) CheckProxy(targetURL string) error {
	return a.proxySvc.CheckProxy(targetURL)
}

// GetSettings returns the settings with the GitHub token masked.
func (a *App) GetSettings() config.AppSettings {
	return a.settingsSvc.Get()
}

// SaveSettings persists a settings snapshot (owned fields are preserved).
func (a *App) SaveSettings(s config.AppSettings) error {
	return a.settingsSvc.Save(s)
}

// SaveGithubToken stores (or clears, when empty) the GitHub PAT.
func (a *App) SaveGithubToken(token string) error {
	return a.settingsSvc.SaveGithubToken(token)
}

// GetDefaultEndpoints lists the built-in endpoint presets.
func (a *App) GetDefaultEndpoints() []sdk.EndpointInfo {
	return a.settingsSvc.GetDefaultEndpoints()
}

// GetEndpoints returns the custom endpoint overrides.
func (a *App) GetEndpoints() map[string]string {
	return a.settingsSvc.GetEndpoints()
}

// SaveEndpoints replaces the custom endpoint overrides.
func (a *App) SaveEndpoints(endpoints map[string]string) error {
	return a.settingsSvc.SaveEndpoints(endpoints)
}

// GetLogFiles lists the application log files.
func (a *App) GetLogFiles() ([]logger.LogFileInfo, error) {
	return logmgr.GetLogFiles()
}

// GetLogContent returns the content of one log file.
func (a *App) GetLogContent(filename string) (string, error) {
	return logmgr.GetLogContent(filename)
}

// CleanLogs removes all log files.
func (a *App) CleanLogs() error {
	return logmgr.CleanLogs()
}

// DeleteLogFile removes a single log file.
func (a *App) DeleteLogFile(filename string) error {
	return logmgr.DeleteLogFile(filename)
}

// GetLogDir returns the active log directory.
func (a *App) GetLogDir() string {
	return logmgr.GetLogDir()
}

// UninstallVersion deletes an installed SDK version.
func (a *App) UninstallVersion(sdkType string, version string) error {
	return a.storageMgr.UninstallVersion(sdkType, version)
}

// GetStorageInfo reports per-SDK disk usage.
func (a *App) GetStorageInfo() []storage.StorageInfo {
	return a.storageMgr.GetStorageInfo()
}

// GetTmpCacheSize reports the temp cache size in bytes.
func (a *App) GetTmpCacheSize() int64 {
	return a.storageMgr.GetTmpCacheSize()
}

// CleanTmpCache empties the temp cache directory.
func (a *App) CleanTmpCache() error {
	return a.storageMgr.CleanTmpCache()
}

// CleanInactiveVersions removes all non-active versions of an SDK.
func (a *App) CleanInactiveVersions(sdkType string) error {
	return a.storageMgr.CleanInactiveVersions(sdkType)
}

// GetDefaultInstallPath returns the default install path.
func (a *App) GetDefaultInstallPath() string {
	return a.storageMgr.GetDefaultInstallPath()
}

// GetInstallPath returns the current install path.
func (a *App) GetInstallPath() string {
	return a.storageMgr.GetInstallPath()
}

// MigrateInstallPath moves the SVC install to a new directory.
func (a *App) MigrateInstallPath(newPath string) error {
	return a.storageMgr.MigrateInstallPath(newPath)
}

// GetAppInfo returns the embedded about.json metadata.
func (a *App) GetAppInfo() update.AppInfo {
	return a.appInfo
}

// CheckUpdate queries the release endpoint for a newer stable version.
func (a *App) CheckUpdate() (update.UpdateInfo, error) {
	return a.updater.CheckUpdate()
}

// DownloadUpdate fetches the update asset and verifies its checksum.
func (a *App) DownloadUpdate(downloadURL, expectedSha256 string) error {
	return a.updater.DownloadUpdate(downloadURL, expectedSha256)
}

// ApplyUpdate swaps in the downloaded update and restarts.
func (a *App) ApplyUpdate() error {
	return a.updater.ApplyUpdate()
}

// RollbackUpdate restores the previous binary from the .bak backup.
func (a *App) RollbackUpdate() error {
	return a.updater.RollbackUpdate()
}

// HasUpdateBackup reports whether a rollback backup (.bak) exists for the
// current binary. The frontend disables the rollback button when false so a
// click can never surface the "no backup found" error.
func (a *App) HasUpdateBackup() bool {
	if a.updater == nil {
		return false
	}
	return a.updater.HasBackup()
}

// GetPackageManagers lists the package managers available for an SDK.
func (a *App) GetPackageManagers(sdkType string) []sdk.PackageManagerInfo {
	return a.pkgmgrSvc.GetPackageManagers(sdkType)
}

// InstallPackageManager installs a package manager (yarn/pnpm/...).
func (a *App) InstallPackageManager(name string) error {
	return a.pkgmgrSvc.InstallPackageManager(name)
}

// UpdatePackageManager updates a package manager to the latest version.
func (a *App) UpdatePackageManager(name string) error {
	return a.pkgmgrSvc.UpdatePackageManager(name)
}

// GetAllSdkStatus returns the local status of every known SDK.
func (a *App) GetAllSdkStatus() []sdk.SdkStatus {
	if a.installerSvc == nil {
		return nil
	}
	return a.installerSvc.GetAllSdkStatus()
}

// GetSdkStatus returns the local status of one SDK.
func (a *App) GetSdkStatus(sdkType string) (*sdk.SdkStatus, error) {
	if a.installerSvc == nil {
		return nil, errUninitialized
	}
	return a.installerSvc.GetSdkStatus(sdkType)
}

// CheckSystemConflicts reports system PATH/env conflicts for an SDK.
func (a *App) CheckSystemConflicts(sdkType string) ([]string, error) {
	if a.installerSvc == nil {
		return nil, errUninitialized
	}
	return a.installerSvc.CheckSystemConflicts(sdkType)
}

// GetRemoteVersions returns the installable versions of an SDK.
func (a *App) GetRemoteVersions(sdkType string) ([]sdk.VersionInfo, error) {
	if a.installerSvc == nil {
		return nil, errUninitialized
	}
	return a.installerSvc.GetRemoteVersions(sdkType)
}

// InstallSdk downloads, verifies and installs an SDK version.
func (a *App) InstallSdk(sdkType string, version string) error {
	if a.installerSvc == nil {
		return errUninitialized
	}
	return a.installerSvc.InstallSdk(sdkType, version)
}

// CancelInstall requests cancellation of an in-flight install.
func (a *App) CancelInstall(sdkType string) {
	if a.installerSvc != nil {
		a.installerSvc.CancelInstall(sdkType)
	}
}

// GetInstallDir returns the install directory of an SDK.
func (a *App) GetInstallDir(sdkType string) string {
	if a.installerSvc == nil {
		return ""
	}
	return a.installerSvc.GetInstallDir(sdkType)
}

// SwitchVersion activates an installed SDK version.
func (a *App) SwitchVersion(sdkType string, version string) error {
	if a.installerSvc == nil {
		return errUninitialized
	}
	return a.installerSvc.SwitchVersion(sdkType, version)
}

// GetSdkDownloadURL resolves the download URL of an SDK version.
func (a *App) GetSdkDownloadURL(sdkType string, version string) (string, error) {
	if a.installerSvc == nil {
		return "", errUninitialized
	}
	return a.installerSvc.GetSdkDownloadURL(sdkType, version)
}

// DetectPathVersion detects the version of an SDK found on the system PATH.
func (a *App) DetectPathVersion(sdkType string) string {
	if a.installerSvc == nil {
		return ""
	}
	return a.installerSvc.DetectPathVersion(sdkType)
}

// SelectLocalFile opens a file dialog to pick an SDK archive.
func (a *App) SelectLocalFile() (string, error) {
	if a.importerSvc == nil {
		return "", errUninitialized
	}
	return a.importerSvc.SelectLocalFile()
}

// SelectLocalDir opens a directory dialog to pick an SDK directory.
func (a *App) SelectLocalDir() (string, error) {
	if a.importerSvc == nil {
		return "", errUninitialized
	}
	return a.importerSvc.SelectLocalDir()
}

// ImportLocalSdk imports an SDK from a local archive or directory.
func (a *App) ImportLocalSdk(sdkType string, localPath string) error {
	if a.importerSvc == nil {
		return errUninitialized
	}
	return a.importerSvc.ImportLocalSdk(sdkType, localPath)
}

// ImportSdk imports an SDK from an external directory.
func (a *App) ImportSdk(externalPath string, sdkType string) error {
	if a.importerSvc == nil {
		return errUninitialized
	}
	return a.importerSvc.ImportSdk(externalPath, sdkType)
}

// ImportPathSdk imports an SDK detected on the system PATH.
func (a *App) ImportPathSdk(sdkType string) error {
	if a.importerSvc == nil {
		return errUninitialized
	}
	return a.importerSvc.ImportPathSdk(sdkType)
}
