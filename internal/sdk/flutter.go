package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"svc/internal/config"
	"svc/internal/logger"
)

type FlutterFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

func NewFlutterFetcher(cfg *config.Config, sm *config.SettingsManager) *FlutterFetcher {
	return &FlutterFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *FlutterFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *FlutterFetcher) StripArchiveTopDir() bool          { return true }

func (f *FlutterFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(Flutter)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://storage.googleapis.com", custom, -1)
}
func (f *FlutterFetcher) Type() SdkType        { return Flutter }
func (f *FlutterFetcher) GetBinDirs() []string { return []string{"bin"} }
func (f *FlutterFetcher) GetExtraEnvVars() map[string]string {
	return map[string]string{"FLUTTER_ROOT": ""}
}
func (f *FlutterFetcher) VerifyCommand() (string, []string) { return "flutter", []string{"--version"} }

// flutterArchWarned guards warnFlutterUnsupportedArm64 so the warning is
// logged only once per process (avoids spamming on every GetDownloadURL
// call). Atomic because GetDownloadURL/FetchRemoteVersions can run
// concurrently (a bare bool would be a data race).
var flutterArchWarned atomic.Bool

// warnFlutterUnsupportedArm64 logs a one-time warning when Flutter is being
// fetched on a non-darwin arm64 host. Flutter does not publish arm64-specific
// builds for Windows or Linux — only macOS has an arm64 build. The x64 build
// is selected on Win/Linux arm64 and runs via emulation, which is slower and
// may have edge-case bugs (notably on Windows-on-Arm translation).
func warnFlutterUnsupportedArm64() {
	if flutterArchWarned.Load() {
		return
	}
	if runtime.GOARCH == "arm64" && runtime.GOOS != "darwin" {
		// CAS so concurrent callers log the warning exactly once.
		if flutterArchWarned.CompareAndSwap(false, true) {
			logger.Warn("Flutter does not publish arm64 builds for %s; the x64 build will be installed and runs via emulation", runtime.GOOS)
		}
	}
}

func (f *FlutterFetcher) buildOSName() string {
	switch runtime.GOOS {
	case "linux":
		return "linux"
	case "darwin":
		return "macos"
	default:
		return "windows"
	}
}

// flutterArchSuffix returns the architecture segment inserted into the macOS
// Flutter archive name. macOS arm64 uses an "_arm64" suffix (e.g.
// flutter_macos_arm64_<ver>-stable.zip); all other platforms/arches use "".
// Flutter does not publish arm64-specific builds for Windows or Linux.
// Pure so tests can exercise every (goos, goarch) combo on any host.
func flutterArchSuffix(goos, goarch string) string {
	if goos == "darwin" && goarch == "arm64" {
		return "_arm64"
	}
	return ""
}

func (f *FlutterFetcher) buildExt() string {
	if runtime.GOOS == "linux" {
		return "tar.xz"
	}
	return "zip"
}

func isStableFlutterTag(tag string) bool {
	return !strings.Contains(tag, "beta") && !strings.Contains(tag, "dev") && !strings.Contains(tag, "pre")
}

// ----- Official releases JSON (the real stable release source) -----
//
// Flutter publishes stable releases as git tags only — the GitHub Releases
// API for flutter/flutter is always empty, so the previous
// api.github.com/repos/flutter/flutter/releases based listing returned no
// versions at all. The authoritative source is the per-platform releases
// metadata published at
// https://storage.googleapis.com/flutter_infra_release/releases/releases_{windows|macos|linux}.json
// with the shape:
//
//	{
//	  "base_url": "https://storage.googleapis.com/flutter_infra_release/releases",
//	  "current_release": {"stable": "<hash>", ...},
//	  "releases": [
//	    {"hash": "...", "channel": "stable"|"beta"|"dev", "version": "3.47.1",
//	     "dart_sdk_version": "...", "dart_sdk_arch": "x64"|"arm64",
//	     "release_date": "2026-08-19T22:09:02.684964Z",
//	     "archive": "stable/windows/flutter_windows_3.47.1-stable.zip",
//	     "sha256": "..."},
//	    ...
//	  ]
//	}
//
// Download URL = base_url + "/" + archive. The macOS file lists one x64 and
// one arm64 entry per version (dart_sdk_arch), so entries are deduplicated
// per version, preferring the entry matching the host architecture.

// flutterReleasesJSONURL returns the official per-platform releases metadata
// URL for the given GOOS. Pure so the mapping is testable on any host.
func flutterReleasesJSONURL(goos string) string {
	const base = "https://storage.googleapis.com/flutter_infra_release/releases/releases_"
	switch goos {
	case "darwin":
		return base + "macos.json"
	case "linux":
		return base + "linux.json"
	default:
		return base + "windows.json"
	}
}

// flutterRelease is one entry of the releases array in the releases JSON.
type flutterRelease struct {
	Channel     string `json:"channel"`
	Version     string `json:"version"`
	DartSDKArch string `json:"dart_sdk_arch"`
	ReleaseDate string `json:"release_date"`
	Archive     string `json:"archive"`
	SHA256      string `json:"sha256"`
}

// flutterReleasesDoc is the decoded releases_{platform}.json document.
type flutterReleasesDoc struct {
	BaseURL  string           `json:"base_url"`
	Releases []flutterRelease `json:"releases"`
}

// flutterDesiredArch returns the dart_sdk_arch value of the release entry a
// host with the given (goos, goarch) should install. Only macOS has arm64
// builds; Windows/Linux arm64 hosts run the x64 build via emulation. Pure
// for testability.
func flutterDesiredArch(goos, goarch string) string {
	if goos == "darwin" && goarch == "arm64" {
		return "arm64"
	}
	return "x64"
}

// fetchReleasesDoc downloads and decodes the per-platform releases JSON.
func (f *FlutterFetcher) fetchReleasesDoc() (*flutterReleasesDoc, error) {
	if f.httpClient == nil {
		return nil, fmt.Errorf("flutter fetcher has no HTTP client configured")
	}
	url := f.useEndpoint(flutterReleasesJSONURL(runtime.GOOS))
	resp, err := f.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Flutter releases metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch Flutter releases metadata: HTTP %d", resp.StatusCode)
	}
	// Bound the read: the real files are well under 1 MB; a misbehaving
	// mirror must not be able to exhaust memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Flutter releases metadata: %w", err)
	}
	var doc flutterReleasesDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse Flutter releases metadata: %w", err)
	}
	return &doc, nil
}

// flutterStableVersions extracts the stable-channel version list from a
// releases document. Only channel=="stable" entries are kept; duplicate
// versions (the macOS file lists one x64 and one arm64 entry per release)
// are collapsed, preferring the entry whose dart_sdk_arch matches the host.
// The result is sorted by semantic version, descending. Pure so parsing,
// filtering, dedup and URL construction are testable offline with fixture
// JSON on any host. Download URLs are NOT endpoint-mirrored here; the
// caller applies useEndpoint.
func flutterStableVersions(doc *flutterReleasesDoc, goos, goarch string) []VersionInfo {
	if doc == nil {
		return nil
	}
	wantArch := flutterDesiredArch(goos, goarch)

	type picked struct {
		rel   flutterRelease
		exact bool // dart_sdk_arch matched the host
	}
	best := make(map[string]*picked)
	var order []string
	for _, r := range doc.Releases {
		if r.Channel != "stable" || r.Version == "" {
			continue
		}
		match := r.DartSDKArch == wantArch
		cur, seen := best[r.Version]
		if !seen {
			best[r.Version] = &picked{rel: r, exact: match}
			order = append(order, r.Version)
			continue
		}
		// Upgrade to the arch-matching entry when we first saw the other one.
		if match && !cur.exact {
			cur.rel = r
			cur.exact = true
		}
	}

	versions := make([]VersionInfo, 0, len(order))
	for _, ver := range order {
		r := best[ver].rel
		parts := strings.Split(ver, ".")
		major, _ := strconv.Atoi(parts[0])
		date := ""
		if t, err := time.Parse(time.RFC3339, r.ReleaseDate); err == nil {
			date = t.Format("2006-01-02")
		}
		info := VersionInfo{Version: ver, Major: major, ReleaseDate: date}
		if r.Archive != "" && doc.BaseURL != "" {
			info.DownloadURL = strings.TrimRight(doc.BaseURL, "/") + "/" + r.Archive
			info.FileName = path.Base(r.Archive)
		}
		versions = append(versions, info)
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i].Version, versions[j].Version) > 0 })
	return versions
}

// flutterLookupStableArchive finds the stable release entry for version in
// the document and returns its full download URL (base_url + "/" + archive)
// and file name, preferring the entry matching the host architecture.
// Returns ok=false when the version is absent or has no archive path. Pure
// for testability.
func flutterLookupStableArchive(doc *flutterReleasesDoc, version, goos, goarch string) (string, string, bool) {
	if doc == nil || doc.BaseURL == "" {
		return "", "", false
	}
	wantArch := flutterDesiredArch(goos, goarch)
	var fallback *flutterRelease
	for i := range doc.Releases {
		r := &doc.Releases[i]
		if r.Channel != "stable" || r.Version != version || r.Archive == "" {
			continue
		}
		if r.DartSDKArch == wantArch {
			return strings.TrimRight(doc.BaseURL, "/") + "/" + r.Archive, path.Base(r.Archive), true
		}
		if fallback == nil {
			fallback = r
		}
	}
	if fallback != nil {
		return strings.TrimRight(doc.BaseURL, "/") + "/" + fallback.Archive, path.Base(fallback.Archive), true
	}
	return "", "", false
}

// flutterLookupChecksum returns the sha256 recorded in the releases document
// for the given stable version (preferring the host-arch entry), or "" when
// unknown. Pure for testability.
func flutterLookupChecksum(doc *flutterReleasesDoc, version, goos, goarch string) string {
	if doc == nil {
		return ""
	}
	wantArch := flutterDesiredArch(goos, goarch)
	fallback := ""
	for _, r := range doc.Releases {
		if r.Channel != "stable" || r.Version != version {
			continue
		}
		if r.DartSDKArch == wantArch {
			return r.SHA256
		}
		if fallback == "" {
			fallback = r.SHA256
		}
	}
	return fallback
}

func (f *FlutterFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	warnFlutterUnsupportedArm64()
	doc, err := f.fetchReleasesDoc()
	if err != nil {
		return nil, err
	}
	versions := flutterStableVersions(doc, runtime.GOOS, runtime.GOARCH)
	// Mirror the authoritative download URLs through the per-SDK endpoint.
	for i := range versions {
		if versions[i].DownloadURL != "" {
			versions[i].DownloadURL = f.useEndpoint(versions[i].DownloadURL)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no stable Flutter versions found in releases metadata")
	}
	return versions, nil
}

// buildPatternURL constructs the download URL and file name from the known
// Flutter archive naming pattern. Used as the fallback when the releases
// metadata cannot be fetched or does not list the requested version (older
// releases are sometimes pruned from the JSON).
func (f *FlutterFetcher) buildPatternURL(version string) (string, string) {
	osName := f.buildOSName()
	ext := f.buildExt()
	archSuffix := flutterArchSuffix(runtime.GOOS, runtime.GOARCH)
	url := f.useEndpoint(fmt.Sprintf("https://storage.googleapis.com/flutter_infra_release/releases/stable/%s/flutter_%s%s_%s-stable.%s", osName, osName, archSuffix, version, ext))
	return url, fmt.Sprintf("flutter_%s%s_%s-stable.%s", osName, archSuffix, version, ext)
}

func (f *FlutterFetcher) GetDownloadURL(version string) (string, string, error) {
	warnFlutterUnsupportedArm64()
	// Cache short-circuit: skip a fresh metadata round-trip when the version
	// list was already fetched (see PythonFetcher.GetDownloadURL).
	if url, name, ok := LookupCachedDownloadURL(Flutter, version); ok {
		return url, name, nil
	}
	// Prefer the authoritative archive path recorded in the official releases
	// metadata (it carries the real per-version file name, which matters for
	// macOS arm64). A fetch failure is not fatal: fall back to the pattern.
	if doc, err := f.fetchReleasesDoc(); err == nil {
		if url, name, ok := flutterLookupStableArchive(doc, version, runtime.GOOS, runtime.GOARCH); ok {
			return f.useEndpoint(url), name, nil
		}
	}
	url, name := f.buildPatternURL(version)
	return url, name, nil
}

// FetchChecksum returns the SHA256 of the Flutter archive for the given
// version. The official releases JSON embeds a sha256 field per release
// entry, so no separate checksum file is needed. Returns ("", nil) when the
// version is unknown to the metadata (verification is then skipped).
func (f *FlutterFetcher) FetchChecksum(version string) (string, error) {
	doc, err := f.fetchReleasesDoc()
	if err != nil {
		return "", err
	}
	return flutterLookupChecksum(doc, version, runtime.GOOS, runtime.GOARCH), nil
}

func (f *FlutterFetcher) GetLocalStatus() (*SdkStatus, error) {
	return baseLocalStatus(f.cfg, Flutter, "flutter"), nil
}
