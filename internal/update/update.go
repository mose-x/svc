package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/downloader"
	"svc/internal/logger"
	"svc/internal/proxy"
	"svc/internal/sdk"
	"svc/internal/wailsrt"
)

type AppInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	License   string `json:"license"`
	RepoURL   string `json:"repoUrl"`
	UpdateURL string `json:"updateUrl"`
}

// Updater implements the in-app self-update flow: checking the GitHub
// releases endpoint, downloading the platform asset with checksum
// verification, and applying/rolling back the swap.
type Updater struct {
	info     AppInfo
	settings *config.SettingsManager
	dl       *downloader.Downloader
	proxy    *proxy.Service
	rt       wailsrt.Runtime
}

// NewUpdater wires an Updater. rt may be nil in tests that never trigger
// progress events or Quit (CheckUpdate, sha256 helpers).
func NewUpdater(info AppInfo, settings *config.SettingsManager, dl *downloader.Downloader, proxySvc *proxy.Service, rt wailsrt.Runtime) *Updater {
	return &Updater{info: info, settings: settings, dl: dl, proxy: proxySvc, rt: rt}
}

// findBackupPath returns the rollback backup for currentExe. It prefers the
// canonical <exe>.bak, but falls back to the NEWEST *.bak in the same
// directory. This matters after the rename migration renamed the executable:
// a backup created by an EARLIER self-update still carries the OLD executable
// name (e.g. "SDK Version Control.exe.bak"), so <exe>.bak would not exist and
// rollback would wrongly report "no backup found". Picking the newest *.bak
// recovers the real previous version. If nothing is found it returns the
// canonical <exe>.bak so the caller's error message stays accurate.
func findBackupPath(currentExe string) string {
	canonical := backupPath(currentExe)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	dir := filepath.Dir(currentExe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return canonical
	}
	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".bak") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = filepath.Join(dir, e.Name())
			newestTime = info.ModTime()
		}
	}
	if newest != "" {
		return newest
	}
	return canonical
}

// hasBackupForExe reports whether a rollback backup exists for the executable
// at exe (canonical <exe>.bak or the newest old-named *.bak next to it).
func hasBackupForExe(exe string) bool {
	if exe == "" {
		return false
	}
	info, err := os.Stat(findBackupPath(exe))
	return err == nil && !info.IsDir()
}

// HasBackup reports whether a rollback backup exists for the running binary.
// The frontend uses this to disable the rollback button instead of surfacing
// a "no backup found" error when the user clicks it anyway.
func (u *Updater) HasBackup() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return hasBackupForExe(exe)
}

// CleanStaleBackups removes old-named *.bak leftovers (e.g. a mac
// "SDKVersionControl.bak" from a pre-rename version) sitting next to
// currentExe, so an upgrade does not leave historical backups behind. It is
// safe for rollback: when the canonical <exe>.bak is absent, the NEWEST old
// .bak is renamed to the canonical name first (preserving the rollback
// source); any other old-named backups are deleted. Called at startup.
func CleanStaleBackups(currentExe string) {
	canonical := backupPath(currentExe)
	dir := filepath.Dir(currentExe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var olds []string
	var newest string
	var newestTime time.Time
	_, canonicalErr := os.Stat(canonical)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".bak") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if full == canonical {
			continue
		}
		olds = append(olds, full)
		if info, err := e.Info(); err == nil && (newest == "" || info.ModTime().After(newestTime)) {
			newest, newestTime = full, info.ModTime()
		}
	}
	if len(olds) == 0 {
		return
	}
	// Preserve rollback: promote the newest old backup to the canonical name
	// when there is no canonical backup yet.
	if canonicalErr != nil && newest != "" {
		if os.Rename(newest, canonical) == nil {
			logger.Info("CleanStaleBackups: promoted %s -> %s (preserve rollback)", newest, canonical)
			olds = removeString(olds, newest)
		}
	}
	for _, old := range olds {
		if os.Remove(old) == nil {
			logger.Info("CleanStaleBackups: removed stale backup %s", old)
		}
	}
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// ParseAppInfo decodes about.json content into AppInfo, falling back to safe
// defaults when the payload is missing or corrupt (dev builds without the
// embed, hand-edited files). Pure so main.go and app.go can share it.
func ParseAppInfo(data []byte) AppInfo {
	var info AppInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return AppInfo{
			Version:   "0.1.0",
			GoVersion: "1.25",
			License:   "MIT License",
			RepoURL:   "https://github.com/example/svc",
			UpdateURL: "",
		}
	}
	return info
}

// githubRelease models the relevant fields of the GitHub Releases API
// response (GET /repos/{owner}/{repo}/releases/latest). The updater reads
// tag_name for the version, body for the changelog, and assets[] for
// per-platform download URLs — no version.json manifest needed.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type UpdateInfo struct {
	HasUpdate     bool   `json:"hasUpdate"`
	LatestVersion string `json:"latestVersion"`
	Changelog     string `json:"changelog"`
	DownloadURL   string `json:"downloadUrl"`
	Filename      string `json:"filename"`
	Sha256        string `json:"sha256"`
}

// stableVersionReg matches a pure X.Y.Z version (exactly three numeric
// components, digits + dots only, no pre-release suffix like -rc1/-beta).
// Only releases whose tag (minus the leading "v") matches are valid update
// targets; suffixed/pre-release tags are skipped so the updater never offers a
// non-stable build to users.
var stableVersionReg = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func (u *Updater) CheckUpdate() (UpdateInfo, error) {
	if u.info.UpdateURL == "" {
		return UpdateInfo{}, fmt.Errorf("update URL is not configured")
	}

	client := &http.Client{Transport: u.proxy.BuildTransport(), Timeout: 15 * time.Second}

	// updateUrl points at the GitHub Releases API
	// (https://api.github.com/repos/<owner>/<repo>/releases/latest). To pick
	// the latest *stable* release we fetch the releases LIST (newest first by
	// created_at) and skip any tag carrying a pre-release suffix (-rc1, -beta,
	// ...): only pure vX.Y.Z tags are valid update targets. This prevents the
	// updater from ever offering an rc/build to users, independent of whether
	// the release was published as a GitHub pre-release. Derive the list URL
	// by stripping the trailing "/latest" so the existing about.json updateUrl
	// (ending in /latest) keeps working without a config change.
	listURL := strings.TrimSuffix(u.info.UpdateURL, "/latest")
	mirroredURL := u.proxy.ApplyGithubMirror(listURL)
	token := sdk.DecodeGithubToken(u.settings)
	useToken := token != "" && mirroredURL == listURL
	listURL = mirroredURL
	req, err := http.NewRequest(http.MethodGet, listURL, nil)
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "svc")
	if useToken {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	q := req.URL.Query()
	q.Set("per_page", "30")
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		// Network errors (DNS, connection refused, TLS, timeout) are common
		// when GitHub is unreachable. Surface them explicitly so the user
		// can tell "network problem" from "server problem" from "up to date".
		return UpdateInfo{}, fmt.Errorf("network error, unable to reach update server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{}, apperr.New(apperr.UpdateHttpStatus, map[string]string{"status": strconv.Itoa(resp.StatusCode)})
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return UpdateInfo{}, fmt.Errorf("failed to parse release info: %w", err)
	}

	// Pick the newest release whose tag (minus the leading "v") is a pure
	// X.Y.Z. Releases come back newest-first, so the first match is the
	// latest stable.
	var release *githubRelease
	for i := range releases {
		tag := strings.TrimPrefix(releases[i].TagName, "v")
		if stableVersionReg.MatchString(tag) {
			release = &releases[i]
			break
		}
	}
	if release == nil {
		// No pure-numeric release among the recent ones (e.g. only rc builds
		// are published). Treat as "no stable update available" rather than an
		// error: the user is current with respect to stable releases.
		return UpdateInfo{HasUpdate: false}, nil
	}

	remoteVersion := strings.TrimPrefix(release.TagName, "v")
	hasUpdate := sdk.CompareVersions(remoteVersion, u.info.Version) > 0

	if !hasUpdate {
		return UpdateInfo{HasUpdate: false, LatestVersion: remoteVersion}, nil
	}

	// Match the asset for the current platform from the release's asset list.
	asset, ok := matchPlatformAsset(release.Assets)
	// A new version exists but no matching asset was found. This is a real
	// error (release misconfigured, upload failed, or unsupported platform),
	// NOT the same as "up to date". Reporting HasUpdate=false here would
	// mislead the user into thinking they are current when they are not.
	if !ok {
		return UpdateInfo{}, apperr.New(apperr.NoAsset, map[string]string{
			"version": remoteVersion, "os": runtime.GOOS, "arch": runtime.GOARCH,
		})
	}

	// Resolve the expected sha256 from sha256sums.txt (also a release asset).
	// Verification is skipped if sha256sums.txt is absent (lenient fallback).
	sha := u.fetchAssetSha256(client, release.Assets, asset.Name)
	if sha == "" {
		logger.Warn("No sha256sums.txt found in release assets, checksum verification will be skipped")
	}

	return UpdateInfo{
		HasUpdate:     true,
		LatestVersion: remoteVersion,
		Changelog:     release.Body,
		DownloadURL:   asset.BrowserDownloadURL,
		Filename:      asset.Name,
		Sha256:        sha,
	}, nil
}

// matchPlatformAsset picks the release asset for the current OS/arch.
// Asset names follow the build convention svc-<ver>-<os>-<arch><ext>:
//
//	windows-x64.exe / windows-arm64.exe
//	macos-x64.bin   / macos-arm64.bin   (bare binary for in-place self-update, NOT .dmg)
//	linux-x64       / linux-arm64       (bare binary for in-place self-update, NOT .deb/.rpm)
//
// runtime.GOOS is windows/darwin/linux, but asset names use "macos" for darwin;
// runtime.GOARCH is amd64/arm64, but asset names use "x64" for amd64.
func matchPlatformAsset(assets []githubAsset) (githubAsset, bool) {
	osToken := map[string]string{
		"windows": "windows",
		"darwin":  "macos",
		"linux":   "linux",
	}[runtime.GOOS]
	archToken := map[string]string{
		"amd64": "x64",
		"arm64": "arm64",
	}[runtime.GOARCH]
	if osToken == "" || archToken == "" {
		return githubAsset{}, false
	}
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.Contains(name, osToken) || !strings.Contains(name, archToken) {
			continue
		}
		// macOS self-update uses the bare .bin, not the .dmg installer.
		if runtime.GOOS == "darwin" && strings.HasSuffix(name, ".dmg") {
			continue
		}
		// Windows ships both a bare .exe (for self-update) and an NSIS
		// installer named *-installer.exe (for first-time install).
		// Self-update must pick the bare exe — the installer can't be
		// swapped in place and running it would need admin/UAC. Use a
		// precise suffix match; an earlier check used "-setup." which
		// never matched the actual "-installer." asset name, so the
		// installer was selected and overwrote the running exe.
		if runtime.GOOS == "windows" && strings.HasSuffix(name, "-installer.exe") {
			continue
		}
		// Linux ships both a bare binary (for self-update) and .deb/.rpm
		// packages (for first-time install via dpkg/rpm). Self-update must
		// pick the bare binary — package managers can't be swapped in place
		// and would need root + package-manager state.
		if runtime.GOOS == "linux" && (strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".rpm")) {
			continue
		}
		return a, true
	}
	return githubAsset{}, false
}

// fetchAssetSha256 downloads the sha256sums.txt release asset (if present) and
// returns the hash recorded for filename. Empty string if the manifest is
// missing or the file isn't listed — DownloadUpdate then skips verification
// (lenient fallback for older releases without a checksum manifest).
func (u *Updater) fetchAssetSha256(client *http.Client, assets []githubAsset, filename string) string {
	var sumsURL string
	for _, a := range assets {
		if a.Name == "sha256sums.txt" {
			sumsURL = a.BrowserDownloadURL
			break
		}
	}
	if sumsURL == "" {
		return ""
	}
	resp, err := client.Get(sumsURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	// Lines: "<64-hex-hash>  <filename>"
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == filename {
			return fields[0]
		}
	}
	return ""
}

type UpdateProgress struct {
	Stage            string `json:"stage"`
	Percent          int    `json:"percent"`
	DownloadedBytes  int64  `json:"downloadedBytes"`
	TotalBytes       int64  `json:"totalBytes"`
	SpeedBytesPerSec int64  `json:"speedBytesPerSec"`
	Message          string `json:"message"`
}

// DownloadUpdate fetches the new binary to a temp path, then (if expectedSha256
// is non-empty) verifies the SHA256 before reporting success. On mismatch the
// downloaded file is deleted so ApplyUpdate cannot pick up a corrupt payload.
func (u *Updater) DownloadUpdate(downloadURL, expectedSha256 string) error {
	if downloadURL == "" {
		return fmt.Errorf("download URL is empty")
	}
	downloadURL = u.proxy.ApplyGithubMirror(downloadURL)

	tmpPath := getUpdateFilePath()
	os.Remove(tmpPath)

	proxyCfg := u.proxy.Config()
	threads := u.settings.Get().DownloadThreads
	if threads <= 0 {
		threads = 4
	}

	err := u.dl.Download(u.rt.Context(), downloadURL, tmpPath, func(downloaded, total, speed int64) {
		percent := 0
		if total > 0 {
			percent = int(downloaded * 100 / total)
		}
		msg := "Downloading..."
		if total > 0 {
			msg = fmt.Sprintf("Downloading %.1fMB / %.1fMB", float64(downloaded)/(1024*1024), float64(total)/(1024*1024))
		}
		u.emitUpdateProgress(UpdateProgress{
			Stage:            "downloading",
			Percent:          percent,
			DownloadedBytes:  downloaded,
			TotalBytes:       total,
			SpeedBytesPerSec: speed,
			Message:          msg,
		})
	}, proxyCfg, threads)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Verify integrity if the server published a SHA256 for this asset.
	// Older releases without the field skip verification (lenient fallback).
	if expectedSha256 != "" {
		u.emitUpdateProgress(UpdateProgress{
			Stage:   "verifying",
			Percent: 100,
			Message: "Verifying integrity...",
		})
		actual, err := sha256OfFile(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to hash downloaded file: %w", err)
		}
		if !sha256Matches(actual, expectedSha256) {
			os.Remove(tmpPath)
			return apperr.New(apperr.ChecksumMismatch, map[string]string{"expected": expectedSha256, "got": actual})
		}
	}

	u.emitUpdateProgress(UpdateProgress{
		Stage:   "done",
		Percent: 100,
		Message: "Download complete",
	})

	return nil
}

// sha256OfFile streams the file through a SHA256 hasher and returns the hex digest.
func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sha256Matches reports whether a computed hex digest equals the expected one,
// case-insensitively. Release manifests (sha256sums.txt and similar) sometimes
// publish UPPERCASE hex while sha256OfFile always returns lowercase; a plain
// case-sensitive != would then reject a perfectly valid download.
func sha256Matches(actual, expected string) bool {
	return strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected))
}

func (u *Updater) emitUpdateProgress(p UpdateProgress) {
	u.rt.EventsEmit("update:progress", p)
}
