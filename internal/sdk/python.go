package sdk

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"svc/internal/config"
)

// PythonFetcher Python version fetcher using astral-sh/python-build-standalone
// prebuilt binaries. python.org only ships source tarballs for Linux/macOS
// (no prebuilt bin/ dir), so we use python-build-standalone which provides
// prebuilt CPython for all three platforms with pip included.
type PythonFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewPythonFetcher(cfg *config.Config, sm *config.SettingsManager) *PythonFetcher {
	return &PythonFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *PythonFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }

// StripArchiveTopDir returns false because the python-build-standalone
// install_only archive extracts to a top-level `python/` directory whose
// name is part of GetBinDirs() (e.g. "python/bin"). Stripping would break
// the bin path resolution.
func (f *PythonFetcher) StripArchiveTopDir() bool { return false }

func (f *PythonFetcher) useEndpoint(defaultURL string) string {
	return applyGithubEndpoint(f.sm, Python, defaultURL)
}

func (f *PythonFetcher) Type() SdkType {
	return Python
}

// GetBinDirs returns the relative bin directories inside the extracted SDK.
// python-build-standalone extracts to `python/` containing:
//   - Unix: python/bin/  (python, python3, pip, pip3, etc.)
//   - Windows: python/   (python.exe, pythonw.exe directly in root; Scripts/
//     is empty in install_only archive — pip is usable via `python -m pip`)
func (f *PythonFetcher) GetBinDirs() []string {
	if config.IsWindows() {
		return []string{"python"}
	}
	return []string{"python/bin"}
}

func (f *PythonFetcher) GetExtraEnvVars() map[string]string {
	return nil
}

func (f *PythonFetcher) VerifyCommand() (string, []string) {
	// macOS 12.3+ and modern Linux distros ship only `python3` (Python 2's
	// `python` was removed). python-build-standalone Unix archives include
	// both `python` and `python3` in bin/, so `python3` verifies both the
	// system copy and an app-installed copy. Windows keeps `python`:
	// python-build-standalone Windows archives ship python.exe only, and
	// Windows users expect `python` (not python3.exe).
	if runtime.GOOS == "windows" {
		return "python", []string{"--version"}
	}
	return "python3", []string{"--version"}
}

// IsSystemPythonPath reports whether binPath points at a system-managed Python
// binary that cannot be safely imported. Importing such a copy would CopyDir
// an OS directory (e.g. /usr or C:\Windows) into the app's managed store, so
// the app should refuse and guide the user to install via the app instead.
// Covered cases:
//   - macOS /usr/bin/python3 (SSV-protected, also CLT framework path)
//   - Linux /usr/bin/python3 (distro package, owned by package manager)
//   - Windows Microsoft Store python.exe stub (WindowsApps alias)
func IsSystemPythonPath(binPath string) bool {
	if binPath == "" {
		return false
	}
	p := filepath.ToSlash(binPath)
	lower := strings.ToLower(p)
	switch runtime.GOOS {
	case "darwin":
		// H7: prefixes are lowercase; match against the lowercased path so
		// /System/ and /Library/ (capitalized on macOS) are caught.
		for _, prefix := range []string{"/usr/bin/", "/bin/", "/sbin/", "/system/", "/library/"} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	case "linux":
		for _, prefix := range []string{"/usr/bin/", "/bin/", "/sbin/", "/usr/lib/"} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	case "windows":
		if strings.HasPrefix(lower, "c:/windows/system32/") ||
			strings.HasPrefix(lower, "c:/windows/syswow64/") {
			return true
		}
		// Microsoft Store alias stub (e.g. %LOCALAPPDATA%\Microsoft\WindowsApps\python.exe)
		if strings.Contains(lower, "windowsapps") {
			return true
		}
	}
	return false
}

// IsWindowsStorePython reports whether binPath is a Microsoft Store Python
// alias stub (e.g. %LOCALAPPDATA%\Microsoft\WindowsApps\python.exe). The stub
// is not a real interpreter: executing it opens the Microsoft Store, so the
// app must skip detection entirely -- no version probe, no import offer, and
// no entry shown in the SDK list. Pure path check (no GOOS gate) so tests can
// exercise it on any host; callers only invoke it on Windows.
func IsWindowsStorePython(binPath string) bool {
	if binPath == "" {
		return false
	}
	// ReplaceAll (not just filepath.ToSlash) so Windows backslash paths are
	// normalized on non-Windows test hosts too.
	lower := strings.ToLower(strings.ReplaceAll(filepath.ToSlash(binPath), "\\", "/"))
	return strings.Contains(lower, "appdata/local/microsoft/windowsapps")
}

// platformTarget returns the python-build-standalone target triple used in
// asset filenames for the current OS/arch.
func (f *PythonFetcher) platformTarget() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "x86_64-unknown-linux-gnu"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "aarch64-unknown-linux-gnu"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "x86_64-apple-darwin"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "aarch64-apple-darwin"
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "x86_64-pc-windows-msvc"
	case runtime.GOOS == "windows" && runtime.GOARCH == "arm64":
		return "aarch64-pc-windows-msvc"
	default:
		return ""
	}
}

// assetNameSuffix is the suffix appended after the target triple to select
// the standard install_only build (excludes debug/freethreaded/stripped).
func (f *PythonFetcher) assetNameSuffix() string {
	return "-install_only.tar.gz"
}

// pythonRelease matches the GitHub releases API response for
// astral-sh/python-build-standalone.
type pythonRelease struct {
	TagName     string        `json:"tag_name"`
	PublishedAt string        `json:"published_at"`
	Assets      []pythonAsset `json:"assets"`
}

type pythonAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// FetchRemoteVersions fetches Python versions from python-build-standalone
// GitHub releases. Each release (tagged by date) contains builds for multiple
// Python versions; we aggregate them and deduplicate by Python version,
// keeping the latest release's build for each version.
//
// Pages are fetched CONCURRENTLY (5 pages in parallel) instead of serially:
// python-build-standalone publishes ~50 dated releases covering all Python
// versions, so we need 5 pages (per_page=10). Serial fetching takes 5 round
// trips; parallel collapses that to 1. Each page is independent (the dedup
// prefers the lowest page number, since releases are returned newest-first),
// so concurrent fetch is safe.
func (f *PythonFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	target := f.platformTarget()
	if target == "" {
		return nil, fmt.Errorf("current platform is not supported by python-build-standalone")
	}
	suffix := target + f.assetNameSuffix()

	// Regex to extract the Python version from asset names like:
	//   cpython-3.12.3+20240415-x86_64-unknown-linux-gnu-install_only.tar.gz
	verRe := regexp.MustCompile(`^cpython-(\d+\.\d+\.\d+)\+\d+-` + regexp.QuoteMeta(suffix) + `$`)

	const pageCount = 5
	const perPage = 10
	// per_page=10 (not the usual 30): python-build-standalone releases each
	// carry 100+ assets, so per_page=30 produces a multi-MB JSON that GitHub's
	// API gateway times out on (HTTP 504). 10 keeps each response small enough
	// to stay under the gateway timeout.

	type pageResult struct {
		page     int
		releases []pythonRelease
		err      error
	}
	results := make([]pageResult, pageCount)

	var wg sync.WaitGroup
	for p := 1; p <= pageCount; p++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			// fetchGithubReleasesPage applies GitHub token auth (60->5000/h)
			// and the mirrors.json / UI mirror fallback chain; useEndpoint
			// above mirrors api.github.com via the per-SDK custom endpoint.
			url := f.useEndpoint(fmt.Sprintf("https://api.github.com/repos/astral-sh/python-build-standalone/releases?per_page=%d&page=%d", perPage, page))
			var releases []pythonRelease
			if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
				results[page-1] = pageResult{page: page, err: err}
				return
			}
			results[page-1] = pageResult{page: page, releases: releases}
		}(p)
	}
	wg.Wait()

	// Deduplicate by Python version. Releases are returned newest-first across
	// pages, so a lower page number is "newer". Track the page each version came
	// from and replace an existing entry only when the new page is lower (newer),
	// so the same Python version built in multiple dated releases resolves to
	// the build from the most recent release that contains it.
	seen := make(map[string]VersionInfo) // pythonVersion -> chosen VersionInfo
	seenPage := make(map[string]int)     // pythonVersion -> page it came from
	for _, r := range results {
		if r.err != nil {
			// A single page failure (e.g. transient 504 on one page) does not
			// fail the whole call: the other pages may carry the versions the
			// user wants. We only fail below if NO page produced any version.
			continue
		}
		for _, rel := range r.releases {
			for _, asset := range rel.Assets {
				m := verRe.FindStringSubmatch(asset.Name)
				if m == nil {
					continue
				}
				pyVer := m[1]
				parts := strings.Split(pyVer, ".")
				major, _ := strconv.Atoi(parts[0])
				date := ""
				if t, err := time.Parse(time.RFC3339, rel.PublishedAt); err == nil {
					date = t.Format("2006-01-02")
				}
				cand := VersionInfo{
					Version:     pyVer,
					Major:       major,
					DownloadURL: f.useEndpoint(asset.BrowserDownloadURL),
					FileName:    asset.Name,
					ReleaseDate: date,
				}
				if prevPage, exists := seenPage[pyVer]; !exists || r.page < prevPage {
					seen[pyVer] = cand
					seenPage[pyVer] = r.page
				}
			}
		}
	}

	// Fail only when every page errored AND we got nothing. A partial result
	// (some pages ok, some failed) is returned so the user still sees versions.
	if len(seen) == 0 {
		// Surface the first non-nil error for diagnostics; if all pages returned
		// empty (no error), report "no releases found".
		for _, r := range results {
			if r.err != nil {
				return nil, fmt.Errorf("failed to fetch Python version list: %w", r.err)
			}
		}
		return nil, fmt.Errorf("no python-build-standalone releases found")
	}

	var versions []VersionInfo
	for _, v := range seen {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})
	return versions, nil
}

func (f *PythonFetcher) GetDownloadURL(version string) (string, string, error) {
	// Cache short-circuit: if the version list was already fetched (e.g. the
	// user opened the version panel, which populated the cache, then clicked
	// Install), reuse the cached URL instead of re-fetching 5 GitHub pages.
	// Falls through to FetchRemoteVersions on a miss (first install).
	if url, name, ok := LookupCachedDownloadURL(Python, version); ok {
		return url, name, nil
	}
	versions, err := f.FetchRemoteVersions()
	if err != nil {
		return "", "", err
	}
	for _, v := range versions {
		if v.Version == version {
			return v.DownloadURL, v.FileName, nil
		}
	}
	return "", "", fmt.Errorf("Python version not found: %s", version)
}

func (f *PythonFetcher) GetLocalStatus() (*SdkStatus, error) {
	installed, _ := f.cfg.GetInstalledVersions(string(Python))
	active := f.cfg.GetActiveVersion(string(Python))
	configured := active != ""

	needsSwitch := false
	if active != "" {
		found := false
		for _, v := range installed {
			if v == active {
				found = true
				break
			}
		}
		needsSwitch = !found
	}

	// Use the platform-specific verify command so macOS/Linux detect the
	// system python3 (not the absent `python`). When a PATH copy is found,
	// also resolve its real binary path and flag system-managed copies
	// (e.g. /usr/bin/python3) that cannot be safely imported -- the UI then
	// hides the import button and guides the user to install instead.
	cmdName, _ := f.VerifyCommand()
	pathConfigured := false
	systemProtected := false
	systemPath := ""
	pathBinary := ""
	if !configured {
		pathBinary = ResolveSystemCommand(cmdName)
		// Windows ships a Microsoft Store stub (WindowsApps\python.exe) that
		// opens the Store when executed. Skip detection, import and display
		// for it entirely -- probing its version would pop up the Store.
		if runtime.GOOS == "windows" && IsWindowsStorePython(pathBinary) {
			pathBinary = ""
		}
		pathConfigured = pathBinary != ""
		if pathConfigured && IsSystemPythonPath(pathBinary) {
			systemProtected = true
			systemPath = pathBinary
		}
	}

	return &SdkStatus{
		SdkType:           Python,
		DisplayName:       SdkDisplayName(Python),
		Configured:        configured,
		PathConfigured:    pathConfigured,
		SystemProtected:   systemProtected,
		SystemPath:        systemPath,
		PathBinary:        pathBinary,
		CurrentVersion:    active,
		InstalledVersions: installed,
		InstallPath:       f.cfg.SdkDir(string(Python)),
		NeedsSwitch:       needsSwitch,
	}, nil
}
