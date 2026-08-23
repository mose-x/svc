package sdk

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"svc/internal/config"
)

// RubyFetcher fetches Ruby versions.
//
//   - Windows: uses oneclick/rubyinstaller2 GitHub releases (RubyInstaller-*
//     7z archives that extract to rubyinstaller-VERSION-1-x64/ containing bin/).
//   - Unix (Linux/macOS): uses jdx/ruby GitHub releases (portable Ruby
//     tarballs that extract to ruby-VERSION/ containing bin/). The upstream
//     ruby-lang.org source tarball is NOT used because it requires compilation
//     and ships no usable bin/ directory.
type RubyFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewRubyFetcher(cfg *config.Config, sm *config.SettingsManager) *RubyFetcher {
	return &RubyFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *RubyFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }

// StripArchiveTopDir returns true for both backends:
//   - RubyInstaller 7z extracts to rubyinstaller-VERSION-1-x64/ → strip → bin/ at root.
//   - jdx/ruby tarball extracts to ruby-VERSION/ → strip → bin/ at root.
func (f *RubyFetcher) StripArchiveTopDir() bool { return true }

func (f *RubyFetcher) useEndpoint(defaultURL string) string {
	// applyGithubEndpoint mirrors both api.github.com and github.com (the
	// former matters for the releases API, which the old code missed). Ruby
	// also mirrors cache.ruby-lang.org for source tarballs.
	out := applyGithubEndpoint(f.sm, Ruby, defaultURL)
	if f.sm == nil {
		return out
	}
	custom := f.sm.Get().Endpoints[string(Ruby)]
	if custom == "" {
		return out
	}
	return strings.Replace(out, "https://cache.ruby-lang.org", custom, -1)
}

func (f *RubyFetcher) Type() SdkType { return Ruby }

// GetBinDirs returns ["bin"] for both backends — both extract (after stripping
// the top-level version dir) to a layout where ruby/gem/bundle/irb/etc. live
// directly in bin/.
func (f *RubyFetcher) GetBinDirs() []string { return []string{"bin"} }

func (f *RubyFetcher) GetExtraEnvVars() map[string]string { return nil }
func (f *RubyFetcher) VerifyCommand() (string, []string)  { return "ruby", []string{"--version"} }

// ----- Shared GitHub release type -----

type ghRelease struct {
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// ----- Windows: RubyInstaller2 -----

type rubyInstallerRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

func (f *RubyFetcher) fetchRubyInstallerVersions() ([]VersionInfo, error) {
	var versions []VersionInfo
	page := 1
	for page <= 3 {
		url := f.useEndpoint(fmt.Sprintf("https://api.github.com/repos/oneclick/rubyinstaller2/releases?per_page=30&page=%d", page))
		var releases []rubyInstallerRelease
		if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
			return nil, fmt.Errorf("failed to fetch Ruby version list (page %d): %w", page, err)
		}
		if len(releases) == 0 {
			break
		}

		for _, r := range releases {
			if r.Draft || r.Prerelease {
				continue
			}
			tag := strings.TrimPrefix(r.TagName, "RubyInstaller-")
			tag = strings.TrimPrefix(tag, "v")
			// RubyInstaller tags look like "3.2.2-1"; keep only the upstream
			// Ruby version part ("3.2.2") so it matches jdx/ruby tags.
			ver := tag
			if idx := strings.Index(tag, "-"); idx > 0 {
				ver = tag[:idx]
			}
			parts := strings.Split(ver, ".")
			if len(parts) < 2 {
				continue
			}
			major, _ := strconv.Atoi(parts[0])
			date := ""
			if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
				date = t.Format("2006-01-02")
			}
			dlURL, fname := f.rubyInstallerAsset(ver, r.Assets)
			versions = append(versions, VersionInfo{
				Version:     ver,
				Major:       major,
				ReleaseDate: date,
				DownloadURL: dlURL,
				FileName:    fname,
			})
		}
		page++
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// rubyInstallerAsset picks the x64 7z asset from a RubyInstaller release.
func (f *RubyFetcher) rubyInstallerAsset(ver string, assets []ghAsset) (string, string) {
	// RubyInstaller names assets like:
	//   rubyinstaller-3.2.2-1-x64.7z
	//   rubyinstaller-3.2.2-1-x64.7z.asc   (signature sidecar)
	//   rubyinstaller-3.2.2-1-x64-javadoc.7z
	// We want the main archive. The match MUST be a suffix check: a substring
	// check ("-x64.7z" anywhere) also matches the .7z.asc signature file,
	// which used to be downloaded in place of the real archive. The suffix
	// also naturally excludes the -javadoc archive (ends in -javadoc.7z).
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.HasSuffix(lower, "-x64.7z") {
			return f.useEndpoint(a.BrowserDownloadURL), a.Name
		}
	}
	// Fallback: construct URL from version
	fname := fmt.Sprintf("rubyinstaller-%s-1-x64.7z", ver)
	return f.useEndpoint(fmt.Sprintf("https://github.com/oneclick/rubyinstaller2/releases/download/RubyInstaller-%s-1/%s", ver, fname)), fname
}

// ----- Unix: jdx/ruby portable binaries -----

type jdxRubyRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// jdxRubyTarget returns the jdx/ruby asset name suffix for the current
// OS/arch. Asset names follow the pattern `ruby-VERSION.TARGET.tar.gz`:
//   - macos (universal)
//   - x86_64_linux / x86_64_linux.no_yjit
//   - arm64_linux / arm64_linux.no_yjit
func (f *RubyFetcher) jdxRubyTarget() string {
	switch {
	case runtime.GOOS == "darwin":
		return "macos"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "x86_64_linux"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "arm64_linux"
	default:
		return ""
	}
}

func (f *RubyFetcher) fetchJdxRubyVersions() ([]VersionInfo, error) {
	target := f.jdxRubyTarget()
	if target == "" {
		return nil, fmt.Errorf("Ruby portable binaries are not available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	var versions []VersionInfo
	page := 1
	for page <= 3 {
		url := f.useEndpoint(fmt.Sprintf("https://api.github.com/repos/jdx/ruby/releases?per_page=30&page=%d", page))
		var releases []jdxRubyRelease
		if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
			return nil, fmt.Errorf("failed to fetch Ruby version list (page %d): %w", page, err)
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
			// Pick the asset matching our target. Skip the .no_yjit variant
			// unless it's the only one available (we prefer YJIT-enabled).
			wantSuffix := fmt.Sprintf(".%s.tar.gz", target)
			noYjitSuffix := fmt.Sprintf(".%s.no_yjit.tar.gz", target)
			var picked ghAsset
			var pickedNoYjit ghAsset
			for _, a := range r.Assets {
				if strings.HasSuffix(a.Name, noYjitSuffix) {
					pickedNoYjit = a
				} else if strings.HasSuffix(a.Name, wantSuffix) {
					picked = a
					break
				}
			}
			if picked.Name == "" {
				if pickedNoYjit.Name == "" {
					continue
				}
				picked = pickedNoYjit
			}
			versions = append(versions, VersionInfo{
				Version:     ver,
				Major:       major,
				ReleaseDate: date,
				DownloadURL: f.useEndpoint(picked.BrowserDownloadURL),
				FileName:    picked.Name,
			})
		}
		page++
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// ----- Public API -----

func (f *RubyFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	if runtime.GOOS == "windows" {
		return f.fetchRubyInstallerVersions()
	}
	return f.fetchJdxRubyVersions()
}

func (f *RubyFetcher) GetDownloadURL(version string) (string, string, error) {
	// Cache short-circuit: skip a fresh FetchRemoteVersions round-trip when the
	// version list was already fetched (see PythonFetcher.GetDownloadURL).
	if url, name, ok := LookupCachedDownloadURL(Ruby, version); ok {
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
	return "", "", fmt.Errorf("Ruby version not found: %s", version)
}

func (f *RubyFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Ruby, "ruby"), nil
}
