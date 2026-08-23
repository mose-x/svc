package sdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"svc/internal/config"
)

// GolangFetcher is the Go version fetcher
type GolangFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewGolangFetcher(cfg *config.Config, sm *config.SettingsManager) *GolangFetcher {
	return &GolangFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *GolangFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *GolangFetcher) StripArchiveTopDir() bool          { return false }

func (f *GolangFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Golang)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://go.dev", custom, -1)
}

func (f *GolangFetcher) Type() SdkType {
	return Golang
}

func (f *GolangFetcher) GetBinDirs() []string {
	return []string{"go/bin"} // After extraction Go has a go/ subdirectory
}

func (f *GolangFetcher) GetExtraEnvVars() map[string]string {
	return map[string]string{
		"GOROOT": "go", // points to the go/ subdirectory
	}
}

func (f *GolangFetcher) VerifyCommand() (string, []string) {
	return "go", []string{"version"}
}

type goVersionJSON struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
	Files   []struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"` // "archive", "installer", "source"
		Size     int64  `json:"size"`
		SHA256   string `json:"sha256"`
	} `json:"files"`
}

func (f *GolangFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://go.dev/dl/?mode=json&include=all"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Go version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch go versions: HTTP %d", resp.StatusCode)
	}

	var raw []goVersionJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to parse Go version data: %w", err)
	}

	os := f.osParam()
	arch := f.archParam()

	var versions []VersionInfo
	for _, v := range raw {
		if !v.Stable {
			continue
		}

		ver := strings.TrimPrefix(v.Version, "go")
		parts := strings.Split(ver, ".")
		major := 0
		if len(parts) >= 1 {
			fmt.Sscanf(parts[0], "%d", &major)
		}

		for _, file := range v.Files {
			if file.Kind == "archive" && file.OS == os && file.Arch == arch {
				versions = append(versions, VersionInfo{
					Version:     ver,
					Major:       major,
					DownloadURL: f.useEndpoint(fmt.Sprintf("https://go.dev/dl/%s", file.Filename)),
					FileName:    file.Filename,
					IsLTS:       false,
				})
				break
			}
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

func (f *GolangFetcher) GetDownloadURL(version string) (string, string, error) {
	os := f.osParam()
	arch := f.archParam()

	resp, err := f.httpClient.Get(f.useEndpoint("https://go.dev/dl/?mode=json&include=all"))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("failed to get go download URL: HTTP %d", resp.StatusCode)
	}

	var raw []goVersionJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", "", err
	}

	goVersion := "go" + version
	for _, v := range raw {
		if v.Version == goVersion {
			for _, file := range v.Files {
				if file.Kind == "archive" && file.OS == os && file.Arch == arch {
					return f.useEndpoint(fmt.Sprintf("https://go.dev/dl/%s", file.Filename)), file.Filename, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("Go version not found: %s", version)
}

func (f *GolangFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Golang, "go"), nil
}

func (f *GolangFetcher) osParam() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	default:
		return ""
	}
}

func (f *GolangFetcher) archParam() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// FetchChecksum returns the SHA256 of the Go archive for the given version.
// The Go API at go.dev/dl/?mode=json includes SHA256 for every file.
func (f *GolangFetcher) FetchChecksum(version string) (string, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://go.dev/dl/?mode=json&include=all"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch failed: HTTP %d", resp.StatusCode)
	}

	var raw []goVersionJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", err
	}

	os := f.osParam()
	arch := f.archParam()
	goVersion := "go" + version

	for _, v := range raw {
		if v.Version == goVersion {
			for _, file := range v.Files {
				if file.Kind == "archive" && file.OS == os && file.Arch == arch {
					return file.SHA256, nil
				}
			}
		}
	}
	return "", nil
}
