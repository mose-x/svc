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

// JdkFetcher JDK version fetcher (based on Adoptium/Eclipse Temurin)
type JdkFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewJdkFetcher(cfg *config.Config, sm *config.SettingsManager) *JdkFetcher {
	return &JdkFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *JdkFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *JdkFetcher) StripArchiveTopDir() bool          { return true }

func (f *JdkFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(JDK)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://api.adoptium.net", custom, -1)
}

func (f *JdkFetcher) Type() SdkType {
	return JDK
}

func (f *JdkFetcher) GetBinDirs() []string {
	// Adoptium Temurin archives extract to `jdk-VERSION+BUILD/` whose contents
	// differ by OS:
	//   - Linux/Windows: jdk-VERSION+BUILD/bin/  (flat layout)
	//   - macOS:         jdk-VERSION+BUILD/Contents/Home/bin/  (macOS bundle
	//                    layout, identical to /Library/Java/JavaVirtualMachines)
	// StripArchiveTopDir()=true strips the top-level version dir in both cases,
	// so the bin path returned here is relative to the stripped root.
	if runtime.GOOS == "darwin" {
		return []string{"Contents/Home/bin"}
	}
	return []string{"bin"}
}

func (f *JdkFetcher) GetExtraEnvVars() map[string]string {
	// JAVA_HOME points to the JDK root, which on macOS is the Contents/Home
	// dir (not the top-level bundle dir).
	if runtime.GOOS == "darwin" {
		return map[string]string{"JAVA_HOME": "Contents/Home"}
	}
	return map[string]string{"JAVA_HOME": ""} // Root directory
}

func (f *JdkFetcher) VerifyCommand() (string, []string) {
	return "java", []string{"-version"}
}

type adoptiumRelease struct {
	Version struct {
		Semver string `json:"semver"`
		Major  int    `json:"major"`
	} `json:"version"`
	Binary struct {
		Package struct {
			Link     string `json:"link"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			Checksum string `json:"checksum"`
		} `json:"package"`
	} `json:"binary"`
	ReleaseName string `json:"release_name"`
}

func (f *JdkFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	// Get available major versions
	releasesResp, err := f.httpClient.Get(f.useEndpoint("https://api.adoptium.net/v3/info/available_releases"))
	if err != nil {
		return nil, fmt.Errorf("failed to get available JDK versions: %w", err)
	}
	defer releasesResp.Body.Close()

	if releasesResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch jdk versions: HTTP %d", releasesResp.StatusCode)
	}

	var releases struct {
		AvailableReleases    []int `json:"available_releases"`
		AvailableLTSReleases []int `json:"available_lts_releases"`
	}
	if err := json.NewDecoder(releasesResp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse JDK version list: %w", err)
	}

	ltsSet := make(map[int]bool)
	for _, v := range releases.AvailableLTSReleases {
		ltsSet[v] = true
	}

	os := f.osParam()
	if os == "" {
		return nil, fmt.Errorf("current operating system is not supported")
	}

	var versions []VersionInfo
	for _, major := range releases.AvailableReleases {
		url := f.useEndpoint(fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%d/hotspot?architecture=%s&os=%s&image_type=jdk", major, jdkArch(runtime.GOARCH), os))
		resp, err := f.httpClient.Get(url)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var assets []adoptiumRelease
		if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		for _, asset := range assets {
			if asset.Binary.Package.Link == "" {
				continue
			}
			versions = append(versions, VersionInfo{
				Version:     asset.Version.Semver,
				Major:       asset.Version.Major,
				DownloadURL: asset.Binary.Package.Link,
				FileName:    asset.Binary.Package.Name,
				IsLTS:       ltsSet[asset.Version.Major],
			})
		}
	}

	// Sort in descending order
	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

func (f *JdkFetcher) GetDownloadURL(version string) (string, string, error) {
	os := f.osParam()
	if os == "" {
		return "", "", fmt.Errorf("current operating system is not supported")
	}

	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])

	url := f.useEndpoint(fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%d/hotspot?architecture=%s&os=%s&image_type=jdk", major, jdkArch(runtime.GOARCH), os))
	resp, err := f.httpClient.Get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("failed to get jdk download URL: HTTP %d", resp.StatusCode)
	}

	var assets []adoptiumRelease
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return "", "", err
	}

	for _, asset := range assets {
		if asset.Version.Semver == version {
			return asset.Binary.Package.Link, asset.Binary.Package.Name, nil
		}
	}

	return "", "", fmt.Errorf("JDK version not found: %s", version)
}

func (f *JdkFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, JDK, "java"), nil
}

func (f *JdkFetcher) osParam() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	case "darwin":
		return "mac"
	default:
		return ""
	}
}

// jdkArch maps runtime.GOARCH to the Adoptium API "architecture" query
// parameter (x64 for amd64, aarch64 for arm64). Pure so tests can exercise
// every architecture on any host.
func jdkArch(goarch string) string {
	switch goarch {
	case "arm64":
		return "aarch64"
	default:
		return "x64"
	}
}

// FetchChecksum returns the SHA256 of the JDK archive for the given version.
// The Adoptium API includes a checksum field in the binary package response.
func (f *JdkFetcher) FetchChecksum(version string) (string, error) {
	os := f.osParam()
	if os == "" {
		return "", nil
	}
	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])

	url := f.useEndpoint(fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%d/hotspot?architecture=%s&os=%s&image_type=jdk", major, jdkArch(runtime.GOARCH), os))
	resp, err := f.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch failed: HTTP %d", resp.StatusCode)
	}

	var assets []adoptiumRelease
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return "", err
	}

	for _, asset := range assets {
		if asset.Version.Semver == version {
			return asset.Binary.Package.Checksum, nil
		}
	}
	return "", nil
}
