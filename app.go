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

// logOp records the outcome of a frontend-facing operation. Every Wails
// binding funnels through here so all user operations (success AND failure)
// land in the log file, on every platform. Sensitive arguments (tokens)
// must never be interpolated — callers pass only the operation name.
func logOp(op string, err error) {
	if err != nil {
		logger.Error("%s failed: %v", op, err)
		return
	}
	logger.Info("%s: ok", op)
}

// logCall records an argument-less query-type operation (it never fails).
func logCall(op string) { logger.Info("%s", op) }

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
	entries, err := a.pathMgr.GetAllPathEntries()
	logOp("GetPathEntries", err)
	return entries, err
}

// CheckProxy verifies that the configured proxy can reach the given URL.
func (a *App) CheckProxy(targetURL string) error {
	err := a.proxySvc.CheckProxy(targetURL)
	logOp(fmt.Sprintf("CheckProxy(%s)", targetURL), err)
	return err
}

// GetSettings returns the settings with the GitHub token masked.
func (a *App) GetSettings() config.AppSettings {
	logCall("GetSettings")
	return a.settingsSvc.Get()
}

// SaveSettings persists a settings snapshot (owned fields are preserved).
func (a *App) SaveSettings(s config.AppSettings) error {
	err := a.settingsSvc.Save(s)
	logOp("SaveSettings", err)
	return err
}

// SaveGithubToken stores (or clears, when empty) the GitHub PAT.
func (a *App) SaveGithubToken(token string) error {
	// The token value itself must never reach the log.
	op := "SaveGithubToken"
	if token == "" {
		op = "ClearGithubToken"
	}
	err := a.settingsSvc.SaveGithubToken(token)
	logOp(op, err)
	return err
}

// GetDefaultEndpoints lists the built-in endpoint presets.
func (a *App) GetDefaultEndpoints() []sdk.EndpointInfo {
	logCall("GetDefaultEndpoints")
	return a.settingsSvc.GetDefaultEndpoints()
}

// GetEndpoints returns the custom endpoint overrides.
func (a *App) GetEndpoints() map[string]string {
	logCall("GetEndpoints")
	return a.settingsSvc.GetEndpoints()
}

// SaveEndpoints replaces the custom endpoint overrides.
func (a *App) SaveEndpoints(endpoints map[string]string) error {
	err := a.settingsSvc.SaveEndpoints(endpoints)
	logOp(fmt.Sprintf("SaveEndpoints(%d)", len(endpoints)), err)
	return err
}

// GetLogFiles lists the application log files.
func (a *App) GetLogFiles() ([]logger.LogFileInfo, error) {
	files, err := logmgr.GetLogFiles()
	logOp("GetLogFiles", err)
	return files, err
}

// GetLogContent returns the content of one log file.
func (a *App) GetLogContent(filename string) (string, error) {
	content, err := logmgr.GetLogContent(filename)
	logOp(fmt.Sprintf("GetLogContent(%s)", filename), err)
	return content, err
}

// CleanLogs removes all log files.
func (a *App) CleanLogs() error {
	err := logmgr.CleanLogs()
	logOp("CleanLogs", err)
	return err
}

// DeleteLogFile removes a single log file.
func (a *App) DeleteLogFile(filename string) error {
	err := logmgr.DeleteLogFile(filename)
	logOp(fmt.Sprintf("DeleteLogFile(%s)", filename), err)
	return err
}

// GetLogDir returns the active log directory.
func (a *App) GetLogDir() string {
	logCall("GetLogDir")
	return logmgr.GetLogDir()
}

// UninstallVersion deletes an installed SDK version.
func (a *App) UninstallVersion(sdkType string, version string) error {
	err := a.storageMgr.UninstallVersion(sdkType, version)
	logOp(fmt.Sprintf("UninstallVersion(%s, %s)", sdkType, version), err)
	return err
}

// GetStorageInfo reports per-SDK disk usage.
func (a *App) GetStorageInfo() []storage.StorageInfo {
	logCall("GetStorageInfo")
	return a.storageMgr.GetStorageInfo()
}

// GetTmpCacheSize reports the temp cache size in bytes.
func (a *App) GetTmpCacheSize() int64 {
	logCall("GetTmpCacheSize")
	return a.storageMgr.GetTmpCacheSize()
}

// CleanTmpCache empties the temp cache directory.
func (a *App) CleanTmpCache() error {
	err := a.storageMgr.CleanTmpCache()
	logOp("CleanTmpCache", err)
	return err
}

// CleanInactiveVersions removes all non-active versions of an SDK.
func (a *App) CleanInactiveVersions(sdkType string) error {
	err := a.storageMgr.CleanInactiveVersions(sdkType)
	logOp(fmt.Sprintf("CleanInactiveVersions(%s)", sdkType), err)
	return err
}

// GetDefaultInstallPath returns the default install path.
func (a *App) GetDefaultInstallPath() string {
	logCall("GetDefaultInstallPath")
	return a.storageMgr.GetDefaultInstallPath()
}

// GetInstallPath returns the current install path.
func (a *App) GetInstallPath() string {
	logCall("GetInstallPath")
	return a.storageMgr.GetInstallPath()
}

// MigrateInstallPath moves the SVC install to a new directory.
func (a *App) MigrateInstallPath(newPath string) error {
	err := a.storageMgr.MigrateInstallPath(newPath)
	logOp(fmt.Sprintf("MigrateInstallPath(%s)", newPath), err)
	return err
}

// GetAppInfo returns the embedded about.json metadata.
func (a *App) GetAppInfo() update.AppInfo {
	logCall("GetAppInfo")
	return a.appInfo
}

// CheckUpdate queries the release endpoint for a newer stable version.
func (a *App) CheckUpdate() (update.UpdateInfo, error) {
	info, err := a.updater.CheckUpdate()
	logOp("CheckUpdate", err)
	return info, err
}

// DownloadUpdate fetches the update asset and verifies its checksum.
func (a *App) DownloadUpdate(downloadURL, expectedSha256 string) error {
	err := a.updater.DownloadUpdate(downloadURL, expectedSha256)
	logOp(fmt.Sprintf("DownloadUpdate(%s)", downloadURL), err)
	return err
}

// ApplyUpdate swaps in the downloaded update and restarts.
func (a *App) ApplyUpdate() error {
	err := a.updater.ApplyUpdate()
	logOp("ApplyUpdate", err)
	return err
}

// RollbackUpdate restores the previous binary from the .bak backup.
func (a *App) RollbackUpdate() error {
	err := a.updater.RollbackUpdate()
	logOp("RollbackUpdate", err)
	return err
}

// HasUpdateBackup reports whether a rollback backup (.bak) exists for the
// current binary. The frontend disables the rollback button when false so a
// click can never surface the "no backup found" error.
func (a *App) HasUpdateBackup() bool {
	logCall("HasUpdateBackup")
	if a.updater == nil {
		return false
	}
	return a.updater.HasBackup()
}

// GetPackageManagers lists the package managers available for an SDK.
func (a *App) GetPackageManagers(sdkType string) []sdk.PackageManagerInfo {
	logCall(fmt.Sprintf("GetPackageManagers(%s)", sdkType))
	return a.pkgmgrSvc.GetPackageManagers(sdkType)
}

// InstallPackageManager installs a package manager (yarn/pnpm/...).
func (a *App) InstallPackageManager(name string) error {
	err := a.pkgmgrSvc.InstallPackageManager(name)
	logOp(fmt.Sprintf("InstallPackageManager(%s)", name), err)
	return err
}

// UpdatePackageManager updates a package manager to the latest version.
func (a *App) UpdatePackageManager(name string) error {
	err := a.pkgmgrSvc.UpdatePackageManager(name)
	logOp(fmt.Sprintf("UpdatePackageManager(%s)", name), err)
	return err
}

// GetNpmRegistry returns the active Node.js installation's npm registry.
func (a *App) GetNpmRegistry() (string, error) {
	reg, err := a.pkgmgrSvc.GetNpmRegistry()
	logOp("GetNpmRegistry", err)
	return reg, err
}

// SetNpmRegistry sets the npm registry (persisted to the user-level ~/.npmrc).
func (a *App) SetNpmRegistry(url string) error {
	err := a.pkgmgrSvc.SetNpmRegistry(url)
	logOp(fmt.Sprintf("SetNpmRegistry(%s)", url), err)
	return err
}

// GetGlobalPackages lists globally installed npm packages of the active Node.js.
func (a *App) GetGlobalPackages(sdkType string) ([]sdk.GlobalPackage, error) {
	pkgs, err := a.pkgmgrSvc.GetGlobalPackages(sdkType)
	logOp(fmt.Sprintf("GetGlobalPackages(%s)", sdkType), err)
	return pkgs, err
}

// InstallGlobalPackage installs an npm package globally.
func (a *App) InstallGlobalPackage(name string) error {
	err := a.pkgmgrSvc.InstallGlobalPackage(name)
	logOp(fmt.Sprintf("InstallGlobalPackage(%s)", name), err)
	return err
}

// UninstallGlobalPackage removes a globally installed npm package.
func (a *App) UninstallGlobalPackage(name string) error {
	err := a.pkgmgrSvc.UninstallGlobalPackage(name)
	logOp(fmt.Sprintf("UninstallGlobalPackage(%s)", name), err)
	return err
}

// UpdateGlobalPackage updates a global npm package to the latest version.
func (a *App) UpdateGlobalPackage(name string) error {
	err := a.pkgmgrSvc.UpdateGlobalPackage(name)
	logOp(fmt.Sprintf("UpdateGlobalPackage(%s)", name), err)
	return err
}

// GetAllSdkStatus returns the local status of every known SDK.
func (a *App) GetAllSdkStatus() []sdk.SdkStatus {
	logCall("GetAllSdkStatus")
	if a.installerSvc == nil {
		return nil
	}
	return a.installerSvc.GetAllSdkStatus()
}

// GetSdkStatus returns the local status of one SDK.
func (a *App) GetSdkStatus(sdkType string) (*sdk.SdkStatus, error) {
	if a.installerSvc == nil {
		logOp(fmt.Sprintf("GetSdkStatus(%s)", sdkType), errUninitialized)
		return nil, errUninitialized
	}
	st, err := a.installerSvc.GetSdkStatus(sdkType)
	logOp(fmt.Sprintf("GetSdkStatus(%s)", sdkType), err)
	return st, err
}

// CheckSystemConflicts reports system PATH/env conflicts for an SDK.
func (a *App) CheckSystemConflicts(sdkType string) ([]string, error) {
	if a.installerSvc == nil {
		return nil, errUninitialized
	}
	conflicts, err := a.installerSvc.CheckSystemConflicts(sdkType)
	logOp(fmt.Sprintf("CheckSystemConflicts(%s)", sdkType), err)
	return conflicts, err
}

// GetRemoteVersions returns the installable versions of an SDK.
func (a *App) GetRemoteVersions(sdkType string) ([]sdk.VersionInfo, error) {
	if a.installerSvc == nil {
		logOp(fmt.Sprintf("GetRemoteVersions(%s)", sdkType), errUninitialized)
		return nil, errUninitialized
	}
	versions, err := a.installerSvc.GetRemoteVersions(sdkType)
	logOp(fmt.Sprintf("GetRemoteVersions(%s)", sdkType), err)
	return versions, err
}

// InstallSdk downloads, verifies and installs an SDK version.
func (a *App) InstallSdk(sdkType string, version string) error {
	if a.installerSvc == nil {
		logOp(fmt.Sprintf("InstallSdk(%s, %s)", sdkType, version), errUninitialized)
		return errUninitialized
	}
	err := a.installerSvc.InstallSdk(sdkType, version)
	logOp(fmt.Sprintf("InstallSdk(%s, %s)", sdkType, version), err)
	return err
}

// CancelInstall requests cancellation of an in-flight install.
func (a *App) CancelInstall(sdkType string) {
	logCall(fmt.Sprintf("CancelInstall(%s)", sdkType))
	if a.installerSvc != nil {
		a.installerSvc.CancelInstall(sdkType)
	}
}

// GetInstallDir returns the install directory of an SDK.
func (a *App) GetInstallDir(sdkType string) string {
	logCall(fmt.Sprintf("GetInstallDir(%s)", sdkType))
	if a.installerSvc == nil {
		return ""
	}
	return a.installerSvc.GetInstallDir(sdkType)
}

// SwitchVersion activates an installed SDK version.
func (a *App) SwitchVersion(sdkType string, version string) error {
	if a.installerSvc == nil {
		logOp(fmt.Sprintf("SwitchVersion(%s, %s)", sdkType, version), errUninitialized)
		return errUninitialized
	}
	err := a.installerSvc.SwitchVersion(sdkType, version)
	logOp(fmt.Sprintf("SwitchVersion(%s, %s)", sdkType, version), err)
	return err
}

// GetSdkDownloadURL resolves the download URL of an SDK version.
func (a *App) GetSdkDownloadURL(sdkType string, version string) (string, error) {
	if a.installerSvc == nil {
		return "", errUninitialized
	}
	url, err := a.installerSvc.GetSdkDownloadURL(sdkType, version)
	logOp(fmt.Sprintf("GetSdkDownloadURL(%s, %s)", sdkType, version), err)
	return url, err
}

// DetectPathVersion detects the version of an SDK found on the system PATH.
func (a *App) DetectPathVersion(sdkType string) string {
	logCall(fmt.Sprintf("DetectPathVersion(%s)", sdkType))
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
	path, err := a.importerSvc.SelectLocalFile()
	logOp("SelectLocalFile", err)
	return path, err
}

// SelectLocalDir opens a directory dialog to pick an SDK directory.
func (a *App) SelectLocalDir() (string, error) {
	if a.importerSvc == nil {
		return "", errUninitialized
	}
	path, err := a.importerSvc.SelectLocalDir()
	logOp("SelectLocalDir", err)
	return path, err
}

// ImportLocalSdk imports an SDK from a local archive or directory.
func (a *App) ImportLocalSdk(sdkType string, localPath string) error {
	if a.importerSvc == nil {
		logOp(fmt.Sprintf("ImportLocalSdk(%s)", sdkType), errUninitialized)
		return errUninitialized
	}
	err := a.importerSvc.ImportLocalSdk(sdkType, localPath)
	logOp(fmt.Sprintf("ImportLocalSdk(%s, %s)", sdkType, localPath), err)
	return err
}

// ImportSdk imports an SDK from an external directory.
func (a *App) ImportSdk(externalPath string, sdkType string) error {
	if a.importerSvc == nil {
		logOp(fmt.Sprintf("ImportSdk(%s)", sdkType), errUninitialized)
		return errUninitialized
	}
	err := a.importerSvc.ImportSdk(externalPath, sdkType)
	logOp(fmt.Sprintf("ImportSdk(%s, %s)", externalPath, sdkType), err)
	return err
}

// ImportPathSdk imports an SDK detected on the system PATH.
func (a *App) ImportPathSdk(sdkType string) error {
	if a.importerSvc == nil {
		logOp(fmt.Sprintf("ImportPathSdk(%s)", sdkType), errUninitialized)
		return errUninitialized
	}
	err := a.importerSvc.ImportPathSdk(sdkType)
	logOp(fmt.Sprintf("ImportPathSdk(%s)", sdkType), err)
	return err
}
