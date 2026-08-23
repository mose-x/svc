package sdk

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"svc/internal/config"
)

type AndroidFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewAndroidFetcher(cfg *config.Config, sm *config.SettingsManager) *AndroidFetcher {
	return &AndroidFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *AndroidFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *AndroidFetcher) StripArchiveTopDir() bool          { return false }

func (f *AndroidFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Android)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://dl.google.com", custom, -1)
}
func (f *AndroidFetcher) Type() SdkType { return Android }
func (f *AndroidFetcher) GetBinDirs() []string {
	// The cmdline-tools zip extracts to cmdline-tools/bin/ directly — there is
	// no "latest/" layer inside the archive. StripArchiveTopDir()=false keeps
	// the cmdline-tools/ top dir so this path resolves.
	return []string{"cmdline-tools/bin"}
}
func (f *AndroidFetcher) GetExtraEnvVars() map[string]string {
	return map[string]string{"ANDROID_HOME": "", "ANDROID_SDK_ROOT": ""}
}
func (f *AndroidFetcher) VerifyCommand() (string, []string) {
	return "sdkmanager", []string{"--version"}
}

// Android repository XML structure
type androidRepository struct {
	XMLName  xml.Name         `xml:"sdk-repository"`
	Packages []androidPackage `xml:"remotePackage"`
}

type androidPackage struct {
	Path     string `xml:"path,attr"`
	Revision struct {
		Major int `xml:"major"`
		Minor int `xml:"minor"`
		Micro int `xml:"micro"`
	} `xml:"revision"`
	Archives struct {
		Archive []struct {
			OS       string `xml:"host-os"`
			HostArch string `xml:"host-arch"`
			URL      string `xml:"complete>url"`
			Size     int64  `xml:"complete>size"`
		} `xml:"archive"`
	} `xml:"archives"`
}

// androidHostArchKey returns the host-arch value used by the Android
// repository XML for the current runtime architecture. Returns "" for
// unknown architectures so callers can fall back to the legacy behaviour
// (any archive matching host-os).
func androidHostArchKey(goarch string) string {
	switch goarch {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x64"
	}
	return ""
}

// androidCmdlineToolsPrerelease reports whether a "cmdline-tools;<ver>"
// package path names a pre-release build. Pre-release packages carry a
// suffix in the version segment (e.g. "cmdline-tools;14.0-alpha01",
// "...;15.0-rc1"); any "-" after the prefix marks a non-stable build. Pure
// so the filter is testable without XML fixtures.
func androidCmdlineToolsPrerelease(path string) bool {
	ver, ok := strings.CutPrefix(path, "cmdline-tools;")
	if !ok {
		return false
	}
	return strings.Contains(ver, "-")
}

func (f *AndroidFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://dl.google.com/android/repository/repository2-3.xml"))
	if err != nil {
		return nil, fmt.Errorf("android repository request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("android repository returned HTTP %d", resp.StatusCode)
	}

	// M9: Bound the response read to prevent an oversized/malicious server
	// from exhausting memory. 100 MB is far larger than any legitimate
	// repository2-3.xml (~5 MB).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read android repository response: %w", err)
	}

	var repo androidRepository
	if err := xml.Unmarshal(body, &repo); err != nil {
		return nil, fmt.Errorf("failed to parse android repository XML: %w", err)
	}

	osKey := "windows"
	if runtime.GOOS == "linux" {
		osKey = "linux"
	}
	if runtime.GOOS == "darwin" {
		osKey = "macosx"
	}
	// E3: filter archives by host-arch so e.g. an arm64-only macosx build is
	// not selected on an amd64 macosx host. Empty HostArch matches any arch
	// (legacy XML or archive genuinely omitting host-arch).
	wantArch := androidHostArchKey(runtime.GOARCH)

	seen := make(map[string]bool)
	var versions []VersionInfo
	for _, pkg := range repo.Packages {
		if !strings.HasPrefix(pkg.Path, "cmdline-tools;") {
			continue
		}
		// Skip pre-release packages ("cmdline-tools;14.0-alpha01",
		// "...-beta02", "...-rc1"): only stable cmdline-tools builds should
		// be offered for install.
		if androidCmdlineToolsPrerelease(pkg.Path) {
			continue
		}
		ver := fmt.Sprintf("%d.%d.%d", pkg.Revision.Major, pkg.Revision.Minor, pkg.Revision.Micro)
		if seen[ver] {
			continue
		}
		seen[ver] = true

		// Find the download URL matching the current platform + arch.
		// Prefer an exact (os, arch) match; fall back to the first archive
		// matching the os with an empty HostArch (legacy/backward compat).
		downloadURL := ""
		fileName := ""
		legacyIdx := -1
		for i := range pkg.Archives.Archive {
			a := &pkg.Archives.Archive[i]
			if a.OS != osKey && a.OS != "" {
				continue
			}
			if a.HostArch == wantArch {
				downloadURL = f.useEndpoint("https://dl.google.com/android/repository/" + a.URL)
				parts := strings.Split(a.URL, "/")
				fileName = parts[len(parts)-1]
				break
			}
			if a.HostArch == "" && legacyIdx == -1 {
				legacyIdx = i
			}
		}
		if downloadURL == "" && legacyIdx != -1 {
			a := &pkg.Archives.Archive[legacyIdx]
			downloadURL = f.useEndpoint("https://dl.google.com/android/repository/" + a.URL)
			parts := strings.Split(a.URL, "/")
			fileName = parts[len(parts)-1]
		}
		if downloadURL == "" {
			continue
		}

		versions = append(versions, VersionInfo{
			Version:     ver,
			Major:       pkg.Revision.Major,
			DownloadURL: downloadURL,
			FileName:    fileName,
		})
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("no Android cmdline-tools versions found in repository")
	}

	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

func (f *AndroidFetcher) GetDownloadURL(version string) (string, string, error) {
	// Cache short-circuit: skip a fresh FetchRemoteVersions round-trip when the
	// version list was already fetched (see PythonFetcher.GetDownloadURL).
	if url, name, ok := LookupCachedDownloadURL(Android, version); ok {
		return url, name, nil
	}
	// E4: the legacy hardcoded fallback to a stale cmdline-tools build
	// (e.g. build 14742923, "14.0") was removed in PR #74. A fetch failure
	// MUST surface as an error so the caller does not silently install the
	// wrong version. This is intentional and the only code path — the
	// fallback is unreachable and was removed entirely.
	versions, err := f.FetchRemoteVersions()
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch Android cmdline-tools versions: %w", err)
	}
	for _, v := range versions {
		if v.Version == version {
			return v.DownloadURL, v.FileName, nil
		}
	}
	return "", "", fmt.Errorf("Android cmdline-tools version not found: %s", version)
}

func (f *AndroidFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Android, "sdkmanager"), nil
}
