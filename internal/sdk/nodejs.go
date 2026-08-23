package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"svc/internal/config"
)

// NodejsFetcher Node.js version fetcher
type NodejsFetcher struct {
	cfg        *config.Config
	sm         *config.SettingsManager
	httpClient *http.Client
}

// nodeVersionJSON matches the structure of nodejs.org/dist/index.json
type nodeVersionJSON struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	LTS     any    `json:"lts"` // false or a string like "Iron"
	Major   int    `json:"-"`   // Parsed from Version
}

func NewNodejsFetcher(cfg *config.Config, sm *config.SettingsManager) *NodejsFetcher {
	return &NodejsFetcher{cfg: cfg, sm: sm, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (f *NodejsFetcher) SetHTTPClient(client *http.Client) { f.httpClient = client }
func (f *NodejsFetcher) StripArchiveTopDir() bool          { return true }

func (f *NodejsFetcher) useEndpoint(defaultURL string) string {
	if f.sm == nil {
		return defaultURL
	}
	custom := f.sm.Get().Endpoints[string(NodeJS)]
	if custom == "" {
		return defaultURL
	}
	return strings.Replace(defaultURL, "https://nodejs.org", custom, -1)
}

func (f *NodejsFetcher) Type() SdkType {
	return NodeJS
}

func (f *NodejsFetcher) GetBinDirs() []string {
	if config.IsWindows() {
		return []string{""} // node.exe is in the root directory on Windows
	}
	return []string{"bin"}
}

func (f *NodejsFetcher) GetExtraEnvVars() map[string]string {
	return nil
}

func (f *NodejsFetcher) VerifyCommand() (string, []string) {
	return "node", []string{"--version"}
}

func (f *NodejsFetcher) FetchRemoteVersions() ([]VersionInfo, error) {
	resp, err := f.httpClient.Get(f.useEndpoint("https://nodejs.org/dist/index.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Node.js version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch nodejs versions: HTTP %d", resp.StatusCode)
	}

	var raw []nodeVersionJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to parse Node.js version data: %w", err)
	}

	var versions []VersionInfo
	for _, v := range raw {
		ver := strings.TrimPrefix(v.Version, "v")
		parts := strings.Split(ver, ".")
		if len(parts) < 1 {
			continue
		}
		major, _ := strconv.Atoi(parts[0])
		if major < 16 { // Filter out versions that are too old
			continue
		}

		isLTS := false
		if v.LTS != nil {
			if s, ok := v.LTS.(string); ok && s != "" {
				isLTS = true
			}
		}

		url, fileName := f.buildDownloadURL(ver)
		if url == "" {
			continue
		}

		versions = append(versions, VersionInfo{
			Version:     ver,
			Major:       major,
			DownloadURL: url,
			FileName:    fileName,
			IsLTS:       isLTS,
			ReleaseDate: v.Date,
		})
	}

	// Sort in descending order
	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

// nodejsPlatformArch maps a (goos, goarch) pair to the Node.js archive suffix
// (e.g. "win-x64") and extension (e.g. "zip"). Returns ("", "") for unknown
// combos. Pure so tests can exercise all 6 platform combos on any host.
func nodejsPlatformArch(goos, goarch string) (suffix, ext string) {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "win-x64", "zip"
	case "windows/arm64":
		return "win-arm64", "zip"
	case "linux/amd64":
		return "linux-x64", "tar.xz"
	case "linux/arm64":
		return "linux-arm64", "tar.xz"
	case "darwin/amd64":
		return "darwin-x64", "tar.gz"
	case "darwin/arm64":
		return "darwin-arm64", "tar.gz"
	default:
		return "", ""
	}
}

func (f *NodejsFetcher) buildDownloadURL(version string) (string, string) {
	suffix, ext := nodejsPlatformArch(runtime.GOOS, runtime.GOARCH)
	if suffix == "" {
		return "", ""
	}

	fileName := fmt.Sprintf("node-v%s-%s.%s", version, suffix, ext)
	url := f.useEndpoint(fmt.Sprintf("https://nodejs.org/dist/v%s/%s", version, fileName))
	return url, fileName
}

// DetectNodeExternalManager reports whether nodePath (a node binary or any
// path under its install tree) belongs to an external Node version manager
// that SVC must not import: nvm-rust (nvm-rs, ~/.nvm.rust) or classic nvm
// (~/.nvm, including nvm-windows under %APPDATA%\nvm). Returns the manager
// name, or "" for standalone copies. Pure path check so tests can exercise
// all platforms on any host.
func DetectNodeExternalManager(nodePath string) string {
	if nodePath == "" {
		return ""
	}
	// ReplaceAll (not just filepath.ToSlash) so Windows backslash paths are
	// normalized on non-Windows test hosts too.
	p := strings.ToLower(strings.ReplaceAll(filepath.ToSlash(nodePath), "\\", "/"))
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case ".nvm.rust":
			return "nvm-rust"
		case ".nvm":
			return "nvm"
		}
	}
	// nvm-windows keeps versions under %APPDATA%\nvm (no dot prefix).
	if strings.Contains(p, "appdata/roaming/nvm/") {
		return "nvm"
	}
	return ""
}

// resolveNodeExternalManager detects the external manager owning the node
// binary at binPath, resolving symlinks first so a shim or alias link that
// points into a manager home (e.g. ~/.nvm.rust/active -> vX.Y.Z) still
// matches. Returns "" for standalone copies.
func resolveNodeExternalManager(binPath string) string {
	if binPath == "" {
		return ""
	}
	if mgr := DetectNodeExternalManager(binPath); mgr != "" {
		return mgr
	}
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		return DetectNodeExternalManager(resolved)
	}
	return ""
}

func (f *NodejsFetcher) GetLocalStatus() (*SdkStatus, error) {
	installed, _ := f.cfg.GetInstalledVersions(string(NodeJS))
	active := f.cfg.GetActiveVersion(string(NodeJS))
	configured := active != ""

	// Check if currentVersion is still valid (exists in installed versions)
	needsSwitch := false
	if active != "" {
		found := false
		for _, v := range installed {
			if v == active {
				found = true
				break
			}
		}
		needsSwitch = !found
	}

	// Locate the PATH copy (SVC shims excluded). When it lives inside an
	// external version manager's home (nvm-rust / nvm), report the manager
	// so the UI tells the user to keep using that tool instead of offering
	// an import that would fight the manager's own shim setup.
	pathBinary := ""
	externalManager := ""
	if !configured {
		pathBinary = ResolveSystemCommand("node")
		externalManager = resolveNodeExternalManager(pathBinary)
	}

	return &SdkStatus{
		SdkType:           NodeJS,
		DisplayName:       SdkDisplayName(NodeJS),
		Configured:        configured,
		PathConfigured:    pathBinary != "",
		ExternalManager:   externalManager,
		PathBinary:        pathBinary,
		CurrentVersion:    active,
		InstalledVersions: installed,
		InstallPath:       f.cfg.SdkDir(string(NodeJS)),
		NeedsSwitch:       needsSwitch,
	}, nil
}

func (f *NodejsFetcher) GetDownloadURL(version string) (string, string, error) {
	url, fileName := f.buildDownloadURL(version)
	if url == "" {
		return "", "", fmt.Errorf("current platform is not supported")
	}
	return url, fileName, nil
}

// CompareVersions compares two semantic versions, returns -1/0/1.
//
// Build metadata after "+" (e.g. "17.0.20+8") is discarded before comparing:
// strconv.Atoi fails on a "+..." suffix and the ignored error used to zero the
// whole segment, making "17.0.20+8" compare equal to "17.0.9+9" (20 -> 0 and
// 9+9 -> 0). SemVer specifies that build metadata has no precedence, so
// dropping it is the correct comparison semantics.
func CompareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}

// FetchChecksum returns the SHA256 of the Node.js archive for the given version.
// Node.js publishes SHASUMS256.txt alongside each release in the dist directory.
func (f *NodejsFetcher) FetchChecksum(version string) (string, error) {
	sumURL := f.useEndpoint(fmt.Sprintf("https://nodejs.org/dist/v%s/SHASUMS256.txt", version))
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

	_, fileName := f.buildDownloadURL(version)
	if fileName == "" {
		return "", nil
	}

	// SHASUMS256.txt format: "<hash>  <filename>"
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == fileName {
			return fields[0], nil
		}
	}
	return "", nil
}
