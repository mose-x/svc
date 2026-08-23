package sdk

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"svc/internal/config"
	"svc/internal/fsutil"
)

type RustFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewRustFetcher(cfg *config.Config, sm *config.SettingsManager) *RustFetcher {
	return &RustFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *RustFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *RustFetcher) StripArchiveTopDir() bool          { return true }

func (f *RustFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Rust)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://static.rust-lang.org", custom, -1)
}

func (f *RustFetcher) Type() SdkType { return Rust }
func (f *RustFetcher) GetBinDirs() []string {
	// Rust tarball ships commands across multiple bin dirs. cargo/bin is the
	// merged entry (hardlinks to the others), listed first so it wins on
	// conflicts. rustc/bin and rustfmt-preview/bin hold the real files, listed
	// as fallback in case hardlinks were lost during cross-filesystem extract.
	return []string{"cargo/bin", "rustc/bin", "rustfmt-preview/bin"}
}
func (f *RustFetcher) GetExtraEnvVars() map[string]string {
	return nil
}

// rustTarget maps a (goos, goarch) pair to the Rust target triple used in
// download URLs and filenames. Extracted as a pure function so tests can
// exercise all 6 platform combos on any host without overriding runtime.GOOS /
// runtime.GOARCH.
func rustTarget(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "x86_64-pc-windows-msvc"
	case "windows/arm64":
		return "aarch64-pc-windows-msvc"
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu"
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu"
	case "darwin/amd64":
		return "x86_64-apple-darwin"
	case "darwin/arm64":
		return "aarch64-apple-darwin"
	default:
		return "x86_64-pc-windows-msvc"
	}
}

func (f *RustFetcher) VerifyCommand() (string, []string) {
	// --version prints "rustc 1.75.0 (...)" which extractVersionFromOutput
	// can parse for a version number. --print sysroot prints a bare path
	// (no version) which breaks version extraction for system-detected Rust.
	// The sysroot health itself is ensured by MergeComponents (A1), not by
	// this command — --print sysroot always succeeds even if lib/rustlib is
	// missing, so it doesn't actually detect the problem it was meant to.
	return "rustc", []string{"--version"}
}

// MergeComponents copies rust-std-{target}/lib/rustlib/ into cargo/lib/ and
// rustc/lib/ so that rustc and cargo find the std library at their sysroot
// (dirname(dirname(exe)) = component dir root, which now contains lib/rustlib/).
//
// The Rust tarball ships per-component directories (cargo/, rustc/,
// rustfmt-preview/, rust-std-{target}/) as siblings. Without this merge,
// rustc at rustc/bin/rustc computes sysroot=rustc/ but std is at
// rust-std-{target}/lib/rustlib/ — a sibling, not under rustc/.
func (f *RustFetcher) MergeComponents(versionDir string) error {
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return fmt.Errorf("failed to read version dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "rust-std-") {
			continue
		}
		srcRustlib := filepath.Join(versionDir, entry.Name(), "lib", "rustlib")
		if info, err := os.Stat(srcRustlib); err != nil || !info.IsDir() {
			continue
		}
		for _, comp := range []string{"cargo", "rustc"} {
			dstRustlib := filepath.Join(versionDir, comp, "lib", "rustlib")
			if _, err := os.Stat(dstRustlib); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dstRustlib), 0755); err != nil {
				return err
			}
			if err := fsutil.CopyDir(srcRustlib, dstRustlib); err != nil {
				return fmt.Errorf("failed to copy rustlib to %s: %w", comp, err)
			}
		}
	}
	return nil
}

func (f *RustFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	var versions []VersionInfo
	page := 1
	for page <= 3 {
		// Rust's per-SDK endpoint mirrors static.rust-lang.org (the download
		// host), NOT github, so the releases API URL is passed unmirrored;
		// fetchGithubReleasesPage still applies the GitHub token and the
		// GithubMirror reverse-proxy fallback for rate-limit / reachability.
		url := fmt.Sprintf("https://api.github.com/repos/rust-lang/rust/releases?per_page=30&page=%d", page)
		var releases []ghRelease
		if err := fetchGithubReleasesPage(f.sm, f.httpClient, url, &releases); err != nil {
			return nil, fmt.Errorf("failed to fetch Rust version list (page %d): %w", page, err)
		}
		if len(releases) == 0 {
			break
		}

		for _, r := range releases {
			if r.Draft || r.Prerelease {
				continue
			}
			tag := r.TagName
			if strings.Contains(tag, "beta") || strings.Contains(tag, "nightly") || strings.Contains(tag, "alpha") {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
			major, _ := strconv.Atoi(parts[0])
			date := ""
			if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
				date = t.Format("2006-01-02")
			}
			versions = append(versions, VersionInfo{
				Version:     strings.TrimPrefix(tag, "v"),
				Major:       major,
				ReleaseDate: date,
				DownloadURL: f.buildDownloadURL(strings.TrimPrefix(tag, "v")),
				FileName:    f.buildFileName(strings.TrimPrefix(tag, "v")),
			})
		}
		page++
	}

	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})
	return versions, nil
}

func (f *RustFetcher) GetDownloadURL(version string) (string, string, error) {
	return f.buildDownloadURL(version), f.buildFileName(version), nil
}

func (f *RustFetcher) buildDownloadURL(version string) string {
	target := rustTarget(runtime.GOOS, runtime.GOARCH)
	return f.useEndpoint(fmt.Sprintf("https://static.rust-lang.org/dist/rust-%s-%s.tar.gz", version, target))
}

func (f *RustFetcher) buildFileName(version string) string {
	target := rustTarget(runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("rust-%s-%s.tar.gz", version, target)
}

func (f *RustFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Rust, "rustc"), nil
}

// FetchChecksum returns the SHA256 of the Rust tarball for the given version.
// Rust publishes .sha256 sidecar files alongside each download.
func (f *RustFetcher) FetchChecksum(version string) (string, error) {
	url, _, err := f.GetDownloadURL(version)
	if err != nil {
		return "", err
	}
	sumURL := url + ".sha256"
	resp, err := f.httpClient.Get(sumURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch failed: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// The .sha256 file format: "<hash>  <filename>" (or just the hash)
	fields := strings.Fields(string(body))
	if len(fields) > 0 {
		return fields[0], nil
	}
	return "", nil
}
