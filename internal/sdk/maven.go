package sdk

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"svc/internal/config"
)

// MavenFetcher Maven version fetcher
type MavenFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewMavenFetcher(cfg *config.Config, sm *config.SettingsManager) *MavenFetcher {
	return &MavenFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *MavenFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *MavenFetcher) StripArchiveTopDir() bool          { return true }

func (f *MavenFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Maven)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://archive.apache.org", custom, -1)
}

func (f *MavenFetcher) Type() SdkType {
	return Maven
}

func (f *MavenFetcher) GetBinDirs() []string {
	return []string{"bin"}
}

func (f *MavenFetcher) GetExtraEnvVars() map[string]string {
	return map[string]string{
		"M2_HOME": "", // Root directory
	}
}

func (f *MavenFetcher) VerifyCommand() (string, []string) {
	return "mvn", []string{"--version"}
}

func (f *MavenFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://archive.apache.org/dist/maven/maven-3/"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Maven version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch maven versions: HTTP %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Maven version page: %w", err)
	}

	var versions []VersionInfo
	doc.Find("pre a").Each(func(i int, s *goquery.Selection) {
		href := strings.TrimSuffix(s.Text(), "/")
		if href == "" || href == ".." || href == "source" {
			return
		}
		// Filter out alpha/beta/rc versions
		if strings.Contains(href, "alpha") || strings.Contains(href, "beta") || strings.Contains(href, "rc") || strings.Contains(href, "RC") {
			return
		}
		parts := strings.Split(href, ".")
		major, _ := strconv.Atoi(parts[0])
		if major < 3 {
			return
		}

		fileName := fmt.Sprintf("apache-maven-%s-bin.zip", href)
		url := f.useEndpoint(fmt.Sprintf("https://archive.apache.org/dist/maven/maven-3/%s/binaries/%s", href, fileName))

		versions = append(versions, VersionInfo{
			Version:     href,
			Major:       major,
			DownloadURL: url,
			FileName:    fileName,
		})
	})

	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

func (f *MavenFetcher) GetDownloadURL(version string) (string, string, error) {
	fileName := fmt.Sprintf("apache-maven-%s-bin.zip", version)
	url := f.useEndpoint(fmt.Sprintf("https://archive.apache.org/dist/maven/maven-3/%s/binaries/%s", version, fileName))
	return url, fileName, nil
}

func (f *MavenFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Maven, "mvn"), nil
}
