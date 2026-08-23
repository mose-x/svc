package sdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"svc/internal/config"
)

// PerlFetcher fetches Perl versions.
//
//   - Windows: uses Strawberry Perl portable edition from strawberryperl.com
//     (strawberry-perl-VERSION-64bit-portable.zip). The zip has NO top-level
//     wrapper folder — files extract directly to the root with `perl/bin/`
//     (perl.exe, cpan, prove, perldoc, ...) and `c/bin/` (gcc, g++, gmake, ...).
//   - Unix (Linux/macOS): uses skaji/relocatable-perl GitHub releases — fully
//     relocatable Perl tarballs covering linux/darwin × amd64/arm64. Each
//     archive extracts to `perl-{os}-{arch}/` containing `bin/` (perl, cpan,
//     prove, perldoc, cpanm, ...) and `lib/`. The upstream cpan.org source
//     tarball is NOT used because it requires compilation and ships no
//     usable bin/ directory.
type PerlFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewPerlFetcher(cfg *config.Config, sm *config.SettingsManager) *PerlFetcher {
	return &PerlFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *PerlFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }

// StripArchiveTopDir:
//   - Windows: false — Strawberry Perl portable zip has NO top-level wrapper
//     folder; `perl/bin/` and `c/bin/` are at the version-dir root after
//     extraction. Stripping would remove them.
//   - Unix: true — skaji/relocatable-perl tarball extracts to
//     `perl-{os}-{arch}/` which must be stripped so `bin/` lands at the
//     version-dir root.
func (f *PerlFetcher) StripArchiveTopDir() bool { return runtime.GOOS != "windows" }

func (f *PerlFetcher) useEndpoint(defaultURL string) string {
	// applyGithubEndpoint mirrors both api.github.com and github.com; Perl
	// also mirrors strawberryperl.com for the Windows portable builds.
	out := applyGithubEndpoint(f.sm, Perl, defaultURL)
	if f.sm == nil {
		return out
	}
	custom := f.sm.Get().Endpoints[string(Perl)]
	if custom == "" {
		return out
	}
	return strings.Replace(out, "https://strawberryperl.com", custom, -1)
}

func (f *PerlFetcher) Type() SdkType { return Perl }

// GetBinDirs returns the relative bin directories inside the extracted SDK.
//   - Windows: ["perl/bin", "c/bin"] — Strawberry Perl ships commands across
//     two bin dirs. perl/bin holds perl.exe, cpan, prove, perldoc, ...;
//     c/bin holds the bundled MinGW toolchain (gcc, g++, gmake, dmake, ...).
//     perl/bin is listed first so it wins on name conflicts.
//   - Unix: ["bin"] — skaji/relocatable-perl ships all commands (perl, cpan,
//     prove, perldoc, cpanm, ...) in a single bin/ dir after the top-level
//     `perl-{os}-{arch}/` folder is stripped.
func (f *PerlFetcher) GetBinDirs() []string {
	if runtime.GOOS == "windows" {
		return []string{"perl/bin", "c/bin"}
	}
	return []string{"bin"}
}

func (f *PerlFetcher) GetExtraEnvVars() map[string]string { return nil }
func (f *PerlFetcher) VerifyCommand() (string, []string)  { return "perl", []string{"--version"} }

// ----- Windows: Strawberry Perl portable -----

// strawberryRelease matches the JSON response from strawberryperl.com/releases.json.
type strawberryRelease struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Edition struct {
		Portable struct {
			URL    string `json:"url"`
			Sha256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"portable"`
	} `json:"edition"`
}

func (f *PerlFetcher) fetchWindowsVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://strawberryperl.com/releases.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Perl version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch perl versions: HTTP %d", resp.StatusCode)
	}

	var releases []strawberryRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse Perl version data: %w", err)
	}

	var versions []VersionInfo
	for _, r := range releases {
		if r.Edition.Portable.URL == "" {
			continue
		}
		ver := r.Version
		parts := strings.Split(ver, ".")
		if len(parts) < 2 {
			continue
		}
		major, _ := strconv.Atoi(parts[0])
		// Extract filename from the GitHub URL (last path segment).
		urlParts := strings.Split(r.Edition.Portable.URL, "/")
		fileName := urlParts[len(urlParts)-1]
		versions = append(versions, VersionInfo{
			Version:     ver,
			Major:       major,
			ReleaseDate: r.Date,
			DownloadURL: f.useEndpoint(r.Edition.Portable.URL),
			FileName:    fileName,
		})
	}

	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// ----- Unix: skaji/relocatable-perl -----

// perlRelease matches the GitHub releases API response for
// skaji/relocatable-perl.
type perlRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// unixTarget returns the os-arch suffix used in skaji/relocatable-perl asset
// names for the current Unix platform. Returns "" on Windows or unsupported
// arches. Asset naming (verified via the GitHub releases API):
//
//	perl-linux-amd64.tar.gz  /  perl-linux-amd64.tar.xz
//	perl-linux-arm64.tar.gz  /  perl-linux-arm64.tar.xz
//	perl-darwin-amd64.tar.gz /  perl-darwin-amd64.tar.xz
//	perl-darwin-arm64.tar.gz /  perl-darwin-arm64.tar.xz
func (f *PerlFetcher) unixTarget() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "linux-amd64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "linux-arm64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "darwin-amd64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "darwin-arm64"
	default:
		return ""
	}
}

func (f *PerlFetcher) fetchUnixVersions() ([]VersionInfo, error) {
	target := f.unixTarget()
	if target == "" {
		return nil, fmt.Errorf("Perl prebuilt binaries are not available for %s/%s (use Windows, or install Perl via your system package manager)", runtime.GOOS, runtime.GOARCH)
	}
	// Prefer .tar.gz (broader toolchain support; .tar.xz needs an extra dep).
	wantSuffix := fmt.Sprintf("-%s.tar.gz", target)

	var versions []VersionInfo
	page := 1
	for page <= 3 {
		url := f.useEndpoint(fmt.Sprintf("https://api.github.com/repos/skaji/relocatable-perl/releases?per_page=30&page=%d", page))
		var releases []perlRelease
		if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
			return nil, fmt.Errorf("failed to fetch Perl version list (page %d): %w", page, err)
		}
		if len(releases) == 0 {
			break
		}

		for _, r := range releases {
			if r.Draft || r.Prerelease {
				continue
			}
			// skaji/relocatable-perl tags are 4-part versions like "5.44.0.0"
			// (MAJOR.MINOR.PATCH.BUILD) with no `v` prefix.
			ver := r.TagName
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
		return nil, fmt.Errorf("no Perl prebuilt releases found for %s", target)
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// ----- Public API -----

func (f *PerlFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	if runtime.GOOS == "windows" {
		return f.fetchWindowsVersions()
	}
	return f.fetchUnixVersions()
}

func (f *PerlFetcher) GetDownloadURL(version string) (string, string, error) {
	// Cache short-circuit: skip a fresh FetchRemoteVersions round-trip when the
	// version list was already fetched (see PythonFetcher.GetDownloadURL).
	if url, name, ok := LookupCachedDownloadURL(Perl, version); ok {
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
	return "", "", fmt.Errorf("Perl version not found: %s", version)
}

func (f *PerlFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Perl, "perl"), nil
}

// FetchChecksum returns the SHA256 of the Perl archive for the given version.
// Windows: strawberryperl.com/releases.json includes sha256 per release.
// Unix: skaji/relocatable-perl GitHub assets don't publish checksums — skip.
func (f *PerlFetcher) FetchChecksum(version string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", nil
	}
	resp, err := f.httpClient.Get(f.useEndpoint("https://strawberryperl.com/releases.json"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch failed: HTTP %d", resp.StatusCode)
	}

	var releases []strawberryRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}

	for _, r := range releases {
		if r.Version == version {
			return r.Edition.Portable.Sha256, nil
		}
	}
	return "", nil
}
