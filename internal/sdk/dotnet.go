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

type DotNetFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewDotNetFetcher(cfg *config.Config, sm *config.SettingsManager) *DotNetFetcher {
	return &DotNetFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *DotNetFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *DotNetFetcher) StripArchiveTopDir() bool          { return true }

func (f *DotNetFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(DotNet)]
	if custom == "" {
		return defaultURL
	}
	defaultURL = strings.Replace(defaultURL, "https://dotnetcli.blob.core.windows.net", custom, -1)
	return strings.Replace(defaultURL, "https://dotnetcli.azureedge.net", custom, -1)
}
func (f *DotNetFetcher) Type() SdkType        { return DotNet }
func (f *DotNetFetcher) GetBinDirs() []string { return []string{""} }
func (f *DotNetFetcher) GetExtraEnvVars() map[string]string {
	return map[string]string{"DOTNET_ROOT": ""}
}
func (f *DotNetFetcher) VerifyCommand() (string, []string) { return "dotnet", []string{"--version"} }

type dotnetReleaseIndex struct {
	ReleasesIndex []dotnetIndexChannel `json:"releases-index"`
}

// dotnetIndexChannel is one channel entry of releases-index.json. The index
// carries latest-sdk (the newest SDK version of the channel, e.g.
// "10.0.400") directly, so a single request is enough for version discovery.
// The index's latest-release field is a RUNTIME version (e.g. "10.0.11");
// building SDK download URLs from it produces 404s.
type dotnetIndexChannel struct {
	ChannelVersion string `json:"channel-version"`
	LatestSDK      string `json:"latest-sdk"`
}

// dotnetChannelMajor parses the major version number from a channel-version
// string like "8.0". Returns 0 when the major part is not numeric. Pure so
// the LTS rule is testable without network access.
func dotnetChannelMajor(channelVersion string) int {
	majorStr := channelVersion
	if i := strings.Index(channelVersion, "."); i >= 0 {
		majorStr = channelVersion[:i]
	}
	major, _ := strconv.Atoi(strings.TrimSpace(majorStr))
	return major
}

// dotnetIsLTS implements the official .NET LTS rule: even major versions are
// LTS, odd majors are Standard-Term Support. The index's support-phase field
// is NOT usable for this purpose: its real values are only
// preview/active/maintenance/eol -- it never equals "lts", so the previous
// `support-phase == "lts"` comparison marked every channel as non-LTS. Pure
// for testability.
func dotnetIsLTS(channelVersion string) bool {
	major := dotnetChannelMajor(channelVersion)
	return major > 0 && major%2 == 0
}

// dotnetIsPrereleaseSDK reports whether an SDK version string denotes a
// pre-release build (contains "preview" or "rc", case-insensitive, e.g.
// "11.0.100-preview.7.26381.103"). Such versions are excluded from the
// installable list. Pure for testability.
func dotnetIsPrereleaseSDK(version string) bool {
	lower := strings.ToLower(version)
	return strings.Contains(lower, "preview") || strings.Contains(lower, "rc")
}

func (f *DotNetFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://dotnetcli.blob.core.windows.net/dotnet/release-metadata/releases-index.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch .NET version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch dotnet versions: HTTP %d", resp.StatusCode)
	}

	var index dotnetReleaseIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("failed to parse .NET version data: %w", err)
	}

	// The index carries latest-sdk per channel, so version discovery needs
	// exactly this one request. Channels without an SDK release yet (empty
	// latest-sdk) and pre-release SDKs (preview/rc) are skipped.
	var versions []VersionInfo
	for _, ch := range index.ReleasesIndex {
		sdk := strings.TrimSpace(ch.LatestSDK)
		if sdk == "" || dotnetIsPrereleaseSDK(sdk) {
			continue
		}
		versions = append(versions, VersionInfo{
			Version:     sdk,
			Major:       dotnetChannelMajor(ch.ChannelVersion),
			IsLTS:       dotnetIsLTS(ch.ChannelVersion),
			DownloadURL: f.buildURL(sdk),
			FileName:    f.buildFileName(sdk),
		})
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no .NET SDK versions found in release metadata")
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions, nil
}

// dotnetRID maps a (goos, goarch) pair to the .NET Runtime ID (RID) used in
// download URLs and filenames. Pure so tests can exercise all 6 platform combos
// on any host.
func dotnetRID(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "win-x64"
	case "windows/arm64":
		return "win-arm64"
	case "linux/amd64":
		return "linux-x64"
	case "linux/arm64":
		return "linux-arm64"
	case "darwin/amd64":
		return "osx-x64"
	case "darwin/arm64":
		return "osx-arm64"
	default:
		return "win-x64"
	}
}

func (f *DotNetFetcher) buildRID() string {
	return dotnetRID(runtime.GOOS, runtime.GOARCH)
}

func (f *DotNetFetcher) buildExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

func (f *DotNetFetcher) buildURL(version string) string {
	return f.useEndpoint(fmt.Sprintf("https://dotnetcli.azureedge.net/dotnet/Sdk/%s/dotnet-sdk-%s-%s.%s", version, version, f.buildRID(), f.buildExt()))
}

func (f *DotNetFetcher) buildFileName(version string) string {
	return fmt.Sprintf("dotnet-sdk-%s-%s.%s", version, f.buildRID(), f.buildExt())
}

func (f *DotNetFetcher) GetDownloadURL(version string) (string, string, error) {
	return f.buildURL(version), f.buildFileName(version), nil
}

func (f *DotNetFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, DotNet, "dotnet"), nil
}
