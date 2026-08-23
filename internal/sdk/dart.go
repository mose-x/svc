package sdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"svc/internal/config"
	"svc/internal/logger"
)

type DartFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewDartFetcher(cfg *config.Config, sm *config.SettingsManager) *DartFetcher {
	return &DartFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *DartFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *DartFetcher) StripArchiveTopDir() bool          { return false }

func (f *DartFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Dart)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://storage.googleapis.com", custom, -1)
}
func (f *DartFetcher) Type() SdkType                      { return Dart }
func (f *DartFetcher) GetBinDirs() []string               { return []string{"dart-sdk/bin"} }
func (f *DartFetcher) GetExtraEnvVars() map[string]string { return nil }
func (f *DartFetcher) VerifyCommand() (string, []string)  { return "dart", []string{"--version"} }

type gcsListResponse struct {
	Prefixes      []string `json:"prefixes"`
	NextPageToken string   `json:"nextPageToken"`
}

// dartArch maps runtime.GOARCH to the Dart SDK archive arch token. Dart
// publishes arm64 builds for every platform, so arm64 is returned for all
// OSes (not just darwin). Pure so tests can exercise every combo on any host.
func dartArch(goos, goarch string) string {
	switch goarch {
	case "arm64":
		return "arm64"
	default:
		return "x64"
	}
}

// dartOSName maps runtime.GOOS to the Dart SDK archive OS token. Pure for
// cross-platform testability.
func dartOSName(goos string) string {
	switch goos {
	case "linux":
		return "linux"
	case "darwin":
		return "macos"
	default:
		return "windows"
	}
}

// dartFileName builds the local download file name for a Dart SDK archive.
// The version is part of the name: FileName becomes the temp-file name in
// TmpDir during install, and a version-less name made installs of different
// versions collide on the same file (and made the download ambiguous in
// logs). Pure for testability.
func dartFileName(osName, arch, version string) string {
	return fmt.Sprintf("dartsdk-%s-%s-%s-release.zip", osName, arch, version)
}

func (f *DartFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	var versions []VersionInfo
	pageToken := ""
	pageCount := 0

	for {
		// M10: Guard against an infinite pagination loop (e.g. a misconfigured
		// mirror returning the same pageToken forever). 100 pages × 200 results
		// = 20000 versions — far more than Dart has ever published.
		pageCount++
		if pageCount > 100 {
			logger.Warn("Dart version fetch exceeded 100 pages, stopping pagination")
			break
		}
		apiURL := f.useEndpoint("https://storage.googleapis.com/storage/v1/b/dart-archive/o?prefix=channels/stable/release/&delimiter=/&maxResults=200")
		if pageToken != "" {
			apiURL += "&pageToken=" + url.QueryEscape(pageToken)
		}
		resp, err := f.httpClient.Get(apiURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch Dart version list: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to fetch dart versions: HTTP %d", resp.StatusCode)
		}
		var result gcsListResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to parse Dart version data: %w", err)
		}
		resp.Body.Close()

		for _, prefix := range result.Prefixes {
			// prefix format: "channels/stable/release/3.6.0/"
			parts := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
			if len(parts) < 4 {
				continue
			}
			ver := parts[3]
			if ver == "latest" {
				continue
			}
			// Filter dev/beta
			if strings.Contains(ver, "-") {
				continue
			}
			vParts := strings.Split(ver, ".")
			if len(vParts) < 2 {
				continue
			}
			major, _ := strconv.Atoi(vParts[0])

			osName := dartOSName(runtime.GOOS)
			arch := dartArch(runtime.GOOS, runtime.GOARCH)

			versions = append(versions, VersionInfo{
				Version:     ver,
				Major:       major,
				DownloadURL: f.useEndpoint(fmt.Sprintf("https://storage.googleapis.com/dart-archive/channels/stable/release/%s/sdk/dartsdk-%s-%s-release.zip", ver, osName, arch)),
				FileName:    dartFileName(osName, arch, ver),
			})
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

func (f *DartFetcher) GetDownloadURL(version string) (string, string, error) {
	osName := dartOSName(runtime.GOOS)
	arch := dartArch(runtime.GOOS, runtime.GOARCH)
	url := f.useEndpoint(fmt.Sprintf("https://storage.googleapis.com/dart-archive/channels/stable/release/%s/sdk/dartsdk-%s-%s-release.zip", version, osName, arch))
	return url, dartFileName(osName, arch, version), nil
}

func (f *DartFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Dart, "dart"), nil
}
