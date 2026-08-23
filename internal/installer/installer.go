package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"svc/internal/config"
	"svc/internal/downloader"
	"svc/internal/extractor"
	"svc/internal/helpers"
	"svc/internal/logger"
	"svc/internal/pathmgr"
	"svc/internal/proxy"
	"svc/internal/sdk"
	"svc/internal/shimmanager"
	"svc/internal/wailsrt"

	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
)

// Service implements SDK installation lifecycle operations: status queries,
// remote version discovery, install/switch/cancel, and download URL
// resolution. It is the largest service; all Wails install bindings on App
// delegate here.
type Service struct {
	cfg      *config.Config
	registry *sdk.Registry
	dl       *downloader.Downloader
	pathMgr  pathmgr.PathManager
	shimMgr  *shimmanager.Manager
	settings *config.SettingsManager
	proxy    *proxy.Service
	rt       wailsrt.Runtime
	cancels  *cancelTracker
	locks    *fetcherLocks
}

// New wires an install Service. registry/rt must be non-nil in production
// (registry is created in startup; rt wraps the Wails context).
func New(cfg *config.Config, registry *sdk.Registry, dl *downloader.Downloader, pathMgr pathmgr.PathManager, shimMgr *shimmanager.Manager, settings *config.SettingsManager, proxySvc *proxy.Service, rt wailsrt.Runtime) *Service {
	return &Service{
		cfg:      cfg,
		registry: registry,
		dl:       dl,
		pathMgr:  pathMgr,
		shimMgr:  shimMgr,
		settings: settings,
		proxy:    proxySvc,
		rt:       rt,
		cancels:  newCancelTracker(),
		locks:    &fetcherLocks{},
	}
}

// CancelAll requests cancellation of every in-flight install (app shutdown).
func (s *Service) CancelAll() {
	s.cancels.cancelAll()
}

func (s *Service) emitProgress(sdkType sdk.SdkType, version, stage string, percent int, message string, downloadedBytes, totalBytes, speedBytesPerSec int64, downloadURL string) {
	s.rt.EventsEmit("install:progress", sdk.InstallProgress{
		SdkType:          sdkType,
		Version:          version,
		Stage:            stage,
		Percent:          percent,
		Message:          message,
		DownloadedBytes:  downloadedBytes,
		TotalBytes:       totalBytes,
		SpeedBytesPerSec: speedBytesPerSec,
		DownloadURL:      downloadURL,
	})
}

// verifyFileSHA256 computes the SHA256 hash of a file and compares it to the
// expected hex-encoded checksum. Returns nil if they match, an error otherwise.
func verifyFileSHA256(filePath, expectedSHA256 string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to compute checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expectedSHA256) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA256, actual)
	}
	return nil
}

func (s *Service) GetAllSdkStatus() []sdk.SdkStatus {
	if s.registry == nil {
		return nil
	}
	var statuses []sdk.SdkStatus
	for _, f := range s.registry.All() {
		status, err := f.GetLocalStatus()
		if err != nil {
			statuses = append(statuses, sdk.SdkStatus{
				SdkType:     f.Type(),
				DisplayName: sdk.SdkDisplayName(f.Type()),
				Configured:  false,
			})
			continue
		}
		completePathInfo(status, f)
		// Filter the transient "<version>.old"/"<version>.new" byproducts of
		// the rename-old-first atomic replace (left behind briefly by a
		// crashed install or a slow cleanup): they are not installable
		// versions and must not appear in the version list shown to the user.
		// Done here at the display layer because the config-layer enumerator
		// (config.GetInstalledVersions) is shared with consumers that must
		// see the raw directory listing.
		status.InstalledVersions = filterResidualVersionDirs(status.InstalledVersions)
		statuses = append(statuses, *status)
	}
	return statuses
}

func (s *Service) GetSdkStatus(sdkType string) (*sdk.SdkStatus, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return nil, err
	}
	f := s.registry.Get(sdk.SdkType(sdkType))
	if f == nil {
		return nil, fmt.Errorf("unknown SDK type: %s", sdkType)
	}
	status, err := f.GetLocalStatus()
	if err != nil {
		return nil, err
	}
	completePathInfo(status, f)
	return status, nil
}

// completePathInfo fills the derived PATH-copy fields on a status: the
// detected PATH version and the resolved binary path (the yellow "!" on the
// detail page shows the latter). Fetchers that already resolved either field
// (python, nodejs) keep their value.
func completePathInfo(status *sdk.SdkStatus, f sdk.VersionFetcher) {
	if status == nil || !status.PathConfigured {
		return
	}
	if status.PathVersion == "" {
		cmd, args := f.VerifyCommand()
		status.PathVersion = helpers.ExtractVersionFromOutput(cmd, args)
	}
	if status.PathBinary == "" {
		cmd, _ := f.VerifyCommand()
		status.PathBinary = sdk.ResolveSystemCommand(cmd)
	}
}

func (s *Service) CheckSystemConflicts(sdkType string) ([]string, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return nil, err
	}
	f := s.registry.Get(sdk.SdkType(sdkType))
	if f == nil {
		return nil, fmt.Errorf("unknown SDK type: %s", sdkType)
	}

	var keys []string
	for k := range f.GetExtraEnvVars() {
		keys = append(keys, k)
	}

	return s.pathMgr.DetectSystemConflicts(sdkType, keys), nil
}

func (s *Service) GetRemoteVersions(sdkType string) ([]sdk.VersionInfo, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return nil, err
	}
	t := sdk.SdkType(sdkType)
	f := s.registry.Get(t)
	if f == nil {
		return nil, fmt.Errorf("unknown SDK type: %s", sdkType)
	}

	// Cache-first: return the cached list immediately (memory → disk) so the UI
	// gets a usable list without waiting for a network round-trip. A background
	// goroutine then refreshes the cache from the remote and emits
	// "install:versions-refreshed" so the UI can silently swap in the fresh list.
	//
	// On a cache MISS (first run, never fetched before) we fetch synchronously
	// so the user sees a real list on the first open instead of an empty panel
	// that only fills in seconds later via the event.
	if cached, ok := sdk.GetCachedVersions(t); ok {
		s.refreshVersionsInBackground(t, f)
		return cached, nil
	}

	versions, err := s.fetchAndCacheVersions(t, f)
	if err != nil {
		return nil, err
	}
	return versions, nil
}

// fetchAndCacheVersions fetches the remote version list through f, stores it in
// the version cache (memory + disk), and returns it. Used both by the
// synchronous cache-miss path and by the background refresh goroutine.
func (s *Service) fetchAndCacheVersions(t sdk.SdkType, f sdk.VersionFetcher) ([]sdk.VersionInfo, error) {
	proxyCfg := s.proxy.Config()
	client := downloader.BuildClient(proxyCfg)
	client.Timeout = 30 * time.Second
	// Serialize SetHTTPClient + the fetch that must observe it under the
	// per-SDK lock: registry fetchers are shared singletons holding the
	// client in a bare field, so a concurrent install of the same SDK would
	// race this background refresh on that field otherwise.
	lk := s.locks.get(string(t))
	lk.Lock()
	f.SetHTTPClient(client)
	versions, err := f.FetchRemoteVersions()
	lk.Unlock()
	if err != nil {
		return nil, err
	}
	sdk.SetCachedVersions(t, versions)
	return versions, nil
}

// refreshVersionsInBackground fetches a fresh version list off the UI thread and
// emits "install:versions-refreshed" with {sdkType, versions} when the fresh
// list differs from the cached one. Errors are logged but not surfaced: this is
// a silent refresh, and the UI already has the cached list to show.
//
// Throttled per-SDK via ShouldRefreshVersions (30s cooldown): rapidly switching
// SDK panels triggers GetRemoteVersions on each switch, and without throttling
// every cache hit would spawn a goroutine + emit an event, flooding the UI.
//
// The goroutine is fire-and-forget; it is not cancelled on app shutdown
// (Wails tears down the runtime, so a late EventsEmit is a no-op).
func (s *Service) refreshVersionsInBackground(t sdk.SdkType, f sdk.VersionFetcher) {
	if !sdk.ShouldRefreshVersions(t) {
		return
	}
	go func() {
		fresh, err := s.fetchAndCacheVersions(t, f)
		if err != nil {
			logger.Warn("Background version refresh failed for %s: %v", t, err)
			return
		}
		// Emit even when the list is identical to the cached one: the UI uses
		// the event as a "refresh done" signal too (e.g. to clear a stale
		// loading state from a manual refresh click). Cheap and idempotent.
		s.rt.EventsEmit("install:versions-refreshed", map[string]any{
			"sdkType":  string(t),
			"versions": fresh,
		})
	}()
}

func (s *Service) InstallSdk(sdkTypeStr string, version string) error {
	if s.registry == nil {
		return fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkTypeStr); err != nil {
		return err
	}
	if err := helpers.ValidatePathSegment(version); err != nil {
		return err
	}
	sdkType := sdk.SdkType(sdkTypeStr)
	f := s.registry.Get(sdkType)
	if f == nil {
		return fmt.Errorf("unknown SDK type: %s", sdkTypeStr)
	}

	logger.Info("Starting installation: %s %s", sdkTypeStr, version)

	// Inject the proxy-aware client BEFORE resolving the download URL: 8 SDKs
	// (jdk/go/gradle/python/ruby/php/perl/android) issue HTTP API calls inside
	// GetDownloadURL, and python/ruby/php/perl/android even re-run
	// FetchRemoteVersions (multi-page GitHub API). Without this injection they
	// used the bare constructor client and bypassed the user's proxy, so
	// resolving the URL failed in proxy-only networks even though the actual
	// file download (below) correctly used the proxy.
	proxyCfg := s.proxy.Config()
	client := downloader.BuildClient(proxyCfg)
	client.Timeout = 30 * time.Second
	// Serialize SetHTTPClient + the call that must observe it under the
	// per-SDK lock (shared fetcher singletons hold the client in a bare
	// field; the background version refresh would race it otherwise).
	lk := s.locks.get(sdkTypeStr)
	lk.Lock()
	f.SetHTTPClient(client)
	downloadURL, fileName, err := f.GetDownloadURL(version)
	lk.Unlock()
	if err != nil {
		return fmt.Errorf("failed to get download URL: %w", err)
	}
	if err := helpers.ValidatePathSegment(fileName); err != nil {
		return fmt.Errorf("invalid download filename: %w", err)
	}
	downloadURL = s.proxy.ApplyGithubMirror(downloadURL)

	tmpFile := filepath.Join(s.cfg.TmpDir(), fileName)
	logger.Info("Download URL: %s", downloadURL)
	s.emitProgress(sdkType, version, "downloading", 0, "Downloading...", 0, 0, 0, downloadURL)

	defer os.Remove(tmpFile)

	installCtx, prevDone, _, cleanup := s.cancels.register(s.rt.Context(), sdkTypeStr)
	defer cleanup()

	// Wait (bounded) for the cancelled previous install of the SAME SDK to
	// fully exit before this install starts downloading. Cancelling the old
	// context is not enough: its download may still be writing the shared tmp
	// file (same SDK + version -> same file name), and its deferred cleanup
	// may delete that file after this install re-creates it. Waiting on done
	// closes both windows. The timeout bounds the worst case (e.g. an old
	// install stuck in a phase that does not observe cancellation).
	if prevDone != nil && !waitForInstallExit(prevDone, installExitWaitTimeout) {
		logger.Warn("Previous install of %s did not exit within %v after cancel; proceeding anyway",
			sdkTypeStr, installExitWaitTimeout)
	}

	threads := s.settings.Get().DownloadThreads
	if threads <= 0 {
		threads = 4
	}
	err = s.dl.Download(installCtx, downloadURL, tmpFile, func(downloaded, total, speed int64) {
		if total > 0 {
			percent := int(downloaded * 100 / total)
			msg := fmt.Sprintf("Downloading... %d%%", percent)
			s.emitProgress(sdkType, version, "downloading", percent, msg, downloaded, total, speed, downloadURL)
		} else {
			s.emitProgress(sdkType, version, "downloading", 0, "Downloading...", downloaded, 0, speed, downloadURL)
		}
	}, proxyCfg, threads)
	if err != nil {
		s.emitProgress(sdkType, version, "error", 0, fmt.Sprintf("Download failed: %v", err), 0, 0, 0, downloadURL)
		return fmt.Errorf("download failed: %w", err)
	}

	// Verify download integrity via SHA256 if the fetcher supports it (M1).
	if cf, ok := f.(sdk.ChecksumFetcher); ok {
		// Re-assert the client under the per-SDK lock: FetchChecksum reads
		// the same bare client field, which a concurrent background refresh
		// may have swapped since the GetDownloadURL call above.
		lk := s.locks.get(sdkTypeStr)
		lk.Lock()
		f.SetHTTPClient(client)
		expected, err := cf.FetchChecksum(version)
		lk.Unlock()
		if err != nil {
			logger.Warn("Failed to fetch checksum for %s %s: %v (skipping verification)", sdkTypeStr, version, err)
		} else if expected != "" {
			s.emitProgress(sdkType, version, "verifying", 0, "Verifying checksum...", 0, 0, 0, downloadURL)
			if err := verifyFileSHA256(tmpFile, expected); err != nil {
				s.emitProgress(sdkType, version, "error", 0, fmt.Sprintf("Checksum verification failed: %v", err), 0, 0, 0, downloadURL)
				return fmt.Errorf("checksum verification failed: %w", err)
			}
			logger.Info("Checksum verified for %s %s", sdkTypeStr, version)
		} else {
			logger.Warn("No checksum available for %s %s, skipping verification", sdkTypeStr, version)
		}
	}

	// Observe cancellation between phases: after the download completes the
	// install context used to be ignored, so a cancel issued during the slow
	// extract/replace phases had no effect. Check before each expensive phase
	// and bail out cleanly (deferred cleanup removes tmpFile).
	if err := installCtx.Err(); err != nil {
		s.emitProgress(sdkType, version, "error", 0, "Installation cancelled", 0, 0, 0, downloadURL)
		return fmt.Errorf("installation cancelled: %w", err)
	}

	logger.Info("Download completed, extracting...")
	s.emitProgress(sdkType, version, "extracting", 0, "Extracting...", 0, 0, 0, downloadURL)
	versionDir := s.cfg.SdkVersionDir(string(sdkType), version)

	// Extract to a temporary sibling directory first, then atomically replace
	// the old version on success. This preserves the previously-working version
	// if extraction fails (M4: data loss on extraction failure).
	tmpVersionDir := versionDir + ".new"
	os.RemoveAll(tmpVersionDir)
	if err := os.MkdirAll(tmpVersionDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	ext, err := extractor.NewExtractor(fileName)
	if err != nil {
		os.RemoveAll(tmpVersionDir)
		return fmt.Errorf("unsupported archive format: %w", err)
	}
	if err := ext.Extract(tmpFile, tmpVersionDir); err != nil {
		os.RemoveAll(tmpVersionDir)
		return fmt.Errorf("extraction failed: %w", err)
	}
	// Strip the archive's top-level directory only when the fetcher opts in.
	// SDKs whose GetBinDirs() already includes the top-level dir name (e.g.
	// Go "go/bin", Dart "dart-sdk/bin", Android "cmdline-tools/bin", Perl
	// "perl/bin"+"c/bin") set StripArchiveTopDir()=false so the top dir is
	// preserved and their bin paths resolve correctly.
	if f.StripArchiveTopDir() {
		if err := extractor.StripTopDir(tmpVersionDir); err != nil {
			os.RemoveAll(tmpVersionDir)
			return fmt.Errorf("extraction failed: %w", err)
		}
	}

	// Rust-specific: merge rust-std component's lib/rustlib into cargo/ and
	// rustc/ so that sysroot resolution finds the std library.
	if rustFetcher, ok := f.(*sdk.RustFetcher); ok {
		if err := rustFetcher.MergeComponents(tmpVersionDir); err != nil {
			os.RemoveAll(tmpVersionDir)
			return fmt.Errorf("failed to merge Rust components: %w", err)
		}
	}

	// Check cancellation again before the atomic replace: extracting a large
	// archive can take a long time, and the user may have cancelled during.
	if err := installCtx.Err(); err != nil {
		os.RemoveAll(tmpVersionDir)
		s.emitProgress(sdkType, version, "error", 0, "Installation cancelled", 0, 0, 0, downloadURL)
		return fmt.Errorf("installation cancelled: %w", err)
	}

	// Atomically replace the old version directory using rename-old-first
	// pattern: rename old to .old, rename new into place, cleanup .old.
	// If the second rename fails, restore from .old (prevents data loss).
	oldVersionDir := versionDir + ".old"
	os.RemoveAll(oldVersionDir)
	if _, err := os.Stat(versionDir); err == nil {
		if renameErr := os.Rename(versionDir, oldVersionDir); renameErr != nil {
			// H1: Never delete the live version as fallback. Abort so the
			// existing version is preserved; the caller can retry or free
			// space instead of losing data.
			os.RemoveAll(tmpVersionDir)
			return fmt.Errorf("failed to backup old version for atomic replace: %w", renameErr)
		}
	}
	if err := os.Rename(tmpVersionDir, versionDir); err != nil {
		if _, statErr := os.Stat(oldVersionDir); statErr == nil {
			os.Rename(oldVersionDir, versionDir)
		}
		os.RemoveAll(tmpVersionDir)
		return fmt.Errorf("failed to move extracted files into place: %w", err)
	}
	if err := os.RemoveAll(oldVersionDir); err != nil {
		// Non-fatal (the new version is already in place), but log it: a
		// leftover .old directory wastes disk and shows up in dir listings.
		logger.Warn("Failed to clean up old version directory %s: %v", oldVersionDir, err)
	}

	logger.Info("Extraction completed, configuring environment...")
	s.emitProgress(sdkType, version, "configuring_path", 0, "Configuring environment...", 0, 0, 0, downloadURL)

	// M13: Set active version BEFORE ConfigureSdk. If SetActiveVersion fails,
	// the config doesn't record this version — don't configure PATH for an
	// unrecorded version (would leave inconsistent state on failure).
	if err := s.cfg.SetActiveVersion(string(sdkType), version); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if err := s.pathMgr.ConfigureSdk(string(sdkType), versionDir, f.GetBinDirs(), f.GetExtraEnvVars()); err != nil {
		return fmt.Errorf("failed to configure PATH: %w", err)
	}

	// Refresh .svc.rc so env var lines (JAVA_HOME, GOROOT, etc.) reflect the
	// newly-active version. ConfigureSdk ran updateRcFile before SetActiveVersion,
	// so its env vars pointed at the previous active version; this refresh fixes
	// that. Non-fatal: the shim runtime reads config.json directly, so a stale
	// .svc.rc only affects tools that source it directly.
	if err := s.shimMgr.RefreshRcFile(); err != nil {
		logger.Warn("Failed to refresh .svc.rc after install: %v", err)
	}

	logger.Info("Installation complete: %s %s", sdkTypeStr, version)
	s.emitProgress(sdkType, version, "done", 100, "Installation complete!", 0, 0, 0, downloadURL)
	return nil
}

func (s *Service) CancelInstall(sdkType string) {
	s.cancels.cancel(sdkType)
}

func (s *Service) GetInstallDir(sdkType string) string {
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return ""
	}
	return s.cfg.SdkDir(sdkType)
}

func (s *Service) SwitchVersion(sdkTypeStr string, version string) error {
	if s.registry == nil {
		return fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkTypeStr); err != nil {
		return err
	}
	if err := helpers.ValidatePathSegment(version); err != nil {
		return err
	}
	sdkType := sdk.SdkType(sdkTypeStr)
	f := s.registry.Get(sdkType)
	if f == nil {
		return fmt.Errorf("unknown SDK type: %s", sdkTypeStr)
	}

	logger.Info("Switching %s version to: %s", sdkTypeStr, version)

	versionDir := s.cfg.SdkVersionDir(sdkTypeStr, version)
	if _, err := os.Stat(versionDir); err != nil {
		logger.Error("Version directory does not exist: %s", versionDir)
		return fmt.Errorf("version directory does not exist: %s", version)
	}

	// M8: Set active version BEFORE ConfigureSdk (matching InstallSdk's M13
	// fix). If SetActiveVersion fails, the config doesn't record this version —
	// don't configure PATH for an unrecorded version (would leave inconsistent
	// state on failure).
	if err := s.cfg.SetActiveVersion(sdkTypeStr, version); err != nil {
		logger.Error("Failed to save config for %s %s: %v", sdkTypeStr, version, err)
		return fmt.Errorf("failed to save config: %w", err)
	}

	if err := s.pathMgr.ConfigureSdk(sdkTypeStr, versionDir, f.GetBinDirs(), f.GetExtraEnvVars()); err != nil {
		logger.Error("Failed to configure PATH for %s %s: %v", sdkTypeStr, version, err)
		return fmt.Errorf("failed to configure PATH: %w", err)
	}

	// Refresh .svc.rc so env var lines reflect the newly-switched version.
	if err := s.shimMgr.RefreshRcFile(); err != nil {
		logger.Warn("Failed to refresh .svc.rc after switch: %v", err)
	}

	logger.Info("Successfully switched %s to version %s", sdkTypeStr, version)
	return nil
}

func (s *Service) GetSdkDownloadURL(sdkType string, version string) (string, error) {
	if s.registry == nil {
		return "", fmt.Errorf("application not fully initialized")
	}
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return "", err
	}
	if err := helpers.ValidatePathSegment(version); err != nil {
		return "", err
	}
	f := s.registry.Get(sdk.SdkType(sdkType))
	if f == nil {
		return "", fmt.Errorf("unknown SDK type: %s", sdkType)
	}
	// Same proxy injection as InstallSdk: GetDownloadURL may issue HTTP API
	// calls that must honor the user's proxy configuration.
	proxyCfg := s.proxy.Config()
	client := downloader.BuildClient(proxyCfg)
	client.Timeout = 30 * time.Second
	// Serialize SetHTTPClient + the call that must observe it under the
	// per-SDK lock (shared fetcher singletons hold the client in a bare
	// field; the background version refresh would race it otherwise).
	lk := s.locks.get(sdkType)
	lk.Lock()
	f.SetHTTPClient(client)
	url, _, err := f.GetDownloadURL(version)
	lk.Unlock()
	if err != nil {
		return "", err
	}
	return s.proxy.ApplyGithubMirror(url), nil
}

func (s *Service) DetectPathVersion(sdkType string) string {
	if s.registry == nil {
		return ""
	}
	f := s.registry.Get(sdk.SdkType(sdkType))
	if f == nil {
		return ""
	}
	cmd, args := f.VerifyCommand()
	return helpers.ExtractVersionFromOutput(cmd, args)
}

// filterResidualVersionDirs drops leftover atomic-replace directories
// ("<version>.old" / "<version>.new") from an installed-versions list. They
// are transient byproducts of the rename-old-first replacement (or a crashed
// install), not installable versions, and must not appear in the version list
// shown to the user. Pure function so the display-layer filter is testable.
func filterResidualVersionDirs(versions []string) []string {
	filtered := make([]string, 0, len(versions))
	for _, v := range versions {
		if strings.HasSuffix(v, ".old") || strings.HasSuffix(v, ".new") {
			continue
		}
		filtered = append(filtered, v)
	}
	return filtered
}
