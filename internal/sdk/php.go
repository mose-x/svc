package sdk

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"svc/internal/config"
	"svc/internal/logger"
)

// PHPFetcher fetches PHP versions.
//
//   - Windows: uses windows.php.net (php-VERSION-nts-Win32-vsNN-x64.zip extracts
//     to php-VERSION-nts-Win32-vsNN-x64/ containing php.exe at root).
//     The vsNN tag (vs16 for 8.3, vs17 for 8.4+) is captured at runtime from
//     the version listing — never hardcoded.
//   - Unix (Linux/macOS): uses rodrigodotdev/php GitHub releases — a clean mirror
//     of static-php-cli builds (https://github.com/static-php/static-php-cli)
//     covering linux/macos × x86_64/aarch64. Each archive extracts to a single
//     `php` binary at the archive root (no bin/ folder, no phpize/php-config).
//     The upstream php.net source tarball is NOT used because it requires
//     compilation and ships no usable bin/ directory.
type PHPFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewPHPFetcher(cfg *config.Config, sm *config.SettingsManager) *PHPFetcher {
	return &PHPFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *PHPFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }

// StripArchiveTopDir:
//   - Windows: true — the php-Win32 zip extracts to php-VERSION-nts-Win32-vsNN-x64/
//     which must be stripped so php.exe lands at the version-dir root.
//   - Unix: false — the static-php-cli archive extracts to a single `php` file
//     (no enclosing directory); StripTopDir is a no-op for single-file extracts
//     but we return false to reflect the actual layout.
func (f *PHPFetcher) StripArchiveTopDir() bool { return runtime.GOOS == "windows" }

func (f *PHPFetcher) useEndpoint(defaultURL string) string {
	// applyGithubEndpoint mirrors both api.github.com and github.com; PHP
	// also mirrors windows.php.net / www.php.net / dl.static-php.dev.
	out := applyGithubEndpoint(f.sm, PHP, defaultURL)
	if f.sm == nil {
		return out
	}
	custom := f.sm.Get().Endpoints[string(PHP)]
	if custom == "" {
		return out
	}
	out = strings.Replace(out, "https://windows.php.net", custom, -1)
	out = strings.Replace(out, "https://www.php.net", custom, -1)
	out = strings.Replace(out, "https://dl.static-php.dev", custom, -1)
	return out
}

func (f *PHPFetcher) Type() SdkType { return PHP }

// GetBinDirs returns the relative bin directories inside the extracted SDK.
//   - Windows: [""]  — php.exe sits at the version-dir root after stripping.
//   - Unix:    [""]  — static-php-cli archive extracts to a single `php` file
//     at the version-dir root (no bin/ folder).
func (f *PHPFetcher) GetBinDirs() []string { return []string{""} }

func (f *PHPFetcher) GetExtraEnvVars() map[string]string { return nil }
func (f *PHPFetcher) VerifyCommand() (string, []string)  { return "php", []string{"--version"} }

// phpArchWarned guards warnPHPRunningOnWindowsArm64 so the warning is logged
// only once per process. Atomic because GetDownloadURL/FetchRemoteVersions
// can run concurrently (a bare bool would be a data race).
var phpArchWarned atomic.Bool

// warnPHPRunningOnWindowsArm64 logs a one-time warning when PHP is being
// fetched on Windows arm64. windows.php.net only publishes x64 builds (the
// version-listing regex matches "-x64.zip"), so on Windows arm64 the x64
// build is installed and runs via Windows-on-Arm x64 emulation.
func warnPHPRunningOnWindowsArm64() {
	if phpArchWarned.Load() {
		return
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		// CAS so concurrent callers log the warning exactly once.
		if phpArchWarned.CompareAndSwap(false, true) {
			logger.Warn("PHP does not publish native Windows arm64 builds; the x64 build will be installed and runs via Windows-on-Arm emulation")
		}
	}
}

// unixTarget returns the os-arch suffix used in rodrigodotdev/php asset names
// for the current Unix platform. Returns "" on Windows or unsupported arches.
// Asset naming (verified via the GitHub releases API):
//
//	php-VERSION-linux-x86_64.tar.gz
//	php-VERSION-linux-aarch64.tar.gz
//	php-VERSION-macos-x86_64.tar.gz
//	php-VERSION-macos-aarch64.tar.gz
func (f *PHPFetcher) unixTarget() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "linux-x86_64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "linux-aarch64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "macos-x86_64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "macos-aarch64"
	default:
		return ""
	}
}

// ----- Windows: windows.php.net -----

func (f *PHPFetcher) fetchWindowsVersions() ([]VersionInfo, error) {
	warnPHPRunningOnWindowsArm64()
	resp, err := f.httpClient.Get(f.useEndpoint("https://windows.php.net/downloads/releases/"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PHP version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch php versions: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read PHP version data: %w", err)
	}

	// Match php-X.Y.Z-nts-Win32-vsNN-x64.zip and capture both version and vs tag.
	// PHP 8.3 uses vs16, PHP 8.4+ uses vs17 — the tag must come from the actual
	// listing, not hardcoded, or newer versions 404.
	re := regexp.MustCompile(`php-(\d+\.\d+\.\d+)-nts-Win32-(vs\d+)-x64\.zip`)
	seen := make(map[string]bool)
	var versions []VersionInfo

	matches := re.FindAllStringSubmatch(string(body), -1)
	for _, m := range matches {
		ver := m[1]
		vsTag := m[2]
		if seen[ver] {
			continue
		}
		seen[ver] = true
		parts := strings.Split(ver, ".")
		major, _ := strconv.Atoi(parts[0])
		fileName := fmt.Sprintf("php-%s-nts-Win32-%s-x64.zip", ver, vsTag)
		versions = append(versions, VersionInfo{
			Version:     ver,
			Major:       major,
			DownloadURL: f.useEndpoint(fmt.Sprintf("https://windows.php.net/downloads/releases/%s", fileName)),
			FileName:    fileName,
		})
	}

	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// ----- Unix: rodrigodotdev/php (static-php-cli mirror) -----

// phpRelease matches the GitHub releases API response for rodrigodotdev/php.
type phpRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

func (f *PHPFetcher) fetchUnixVersions() ([]VersionInfo, error) {
	target := f.unixTarget()
	if target == "" {
		return nil, fmt.Errorf("PHP prebuilt binaries are not available for %s/%s (use Windows, or install PHP via your system package manager)", runtime.GOOS, runtime.GOARCH)
	}
	wantSuffix := fmt.Sprintf("-%s.tar.gz", target)

	var versions []VersionInfo
	page := 1
	for page <= 3 {
		url := f.useEndpoint(fmt.Sprintf("https://api.github.com/repos/rodrigodotdev/php/releases?per_page=30&page=%d", page))
		var releases []phpRelease
		if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
			return nil, fmt.Errorf("failed to fetch PHP version list (page %d): %w", page, err)
		}
		if len(releases) == 0 {
			break
		}

		for _, r := range releases {
			if r.Draft || r.Prerelease {
				continue
			}
			ver := strings.TrimPrefix(r.TagName, "v")
			parts := strings.Split(ver, ".")
			if len(parts) < 2 {
				continue
			}
			major, _ := strconv.Atoi(parts[0])
			date := ""
			if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
				date = t.Format("2006-01-02")
			}
			for _, a := range r.Assets {
				if strings.HasSuffix(a.Name, wantSuffix) {
					versions = append(versions, VersionInfo{
						Version:     ver,
						Major:       major,
						ReleaseDate: date,
						DownloadURL: f.useEndpoint(a.BrowserDownloadURL),
						FileName:    a.Name,
					})
					break
				}
			}
		}
		page++
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("no PHP prebuilt releases found for %s", target)
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// ----- Public API -----

func (f *PHPFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	if runtime.GOOS == "windows" {
		return f.fetchWindowsVersions()
	}
	return f.fetchUnixVersions()
}

func (f *PHPFetcher) GetDownloadURL(version string) (string, string, error) {
	warnPHPRunningOnWindowsArm64()
	// Cache short-circuit: skip a fresh FetchRemoteVersions round-trip when the
	// version list was already fetched (see PythonFetcher.GetDownloadURL).
	if url, name, ok := LookupCachedDownloadURL(PHP, version); ok {
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
	return "", "", fmt.Errorf("PHP version not found: %s", version)
}

func (f *PHPFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, PHP, "php"), nil
}
