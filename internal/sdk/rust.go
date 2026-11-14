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
	// The Rust dist tarball ships one directory per component, each with its
	// own bin/: cargo/bin (cargo), rustc/bin (rustc, rustdoc, rust-gdb,
	// rust-lldb, ...), rustfmt-preview/bin (rustfmt, cargo-fmt),
	// clippy-preview/bin (cargo-clippy, clippy-driver) and
	// rust-analyzer-preview/bin (rust-analyzer). A component missing from this
	// list never gets shims, so its commands stay off PATH and `cargo clippy`
	// fails with "no such command: `clippy`". cargo/bin is listed first so it
	// wins on name conflicts.
	return []string{
		"cargo/bin",
		"rustc/bin",
		"rustfmt-preview/bin",
		"clippy-preview/bin",
		"rust-analyzer-preview/bin",
	}
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

// MergeComponents repairs the per-component layout of the extracted Rust
// tarball so every binary can find what it links against:
//
//  1. rust-std-{target}/lib/rustlib/ is copied into cargo/lib/ and rustc/lib/
//     so rustc and cargo find the std library at their sysroot
//     (dirname(dirname(exe)) = component dir root, which now holds lib/rustlib/).
//  2. rustc's shared runtime is bridged into the sibling components
//     (LinkComponentRuntime) so rustfmt, clippy-driver and rust-analyzer can
//     load librustc_driver.
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
	return f.LinkComponentRuntime(versionDir, runtime.GOOS)
}

// LinkComponentRuntime bridges rustc's shared runtime into the sibling
// component directories so their binaries can load it. The tarball keeps one
// directory per component, but every binary outside rustc/ resolves the
// runtime relative to its own location:
//
//   - macOS:   LC_RPATH @loader_path/../lib  -> <component>/lib
//   - Linux:   RUNPATH  $ORIGIN/../lib       -> <component>/lib
//   - Windows: no rpath; the loader searches the .exe's own directory
//
// rustup's installer merges all components into a single prefix, which is why
// ../lib resolves there. svc extracts the tarball as-is, so rustfmt-preview/,
// clippy-preview/ and rust-analyzer-preview/ ship without the runtime they
// link against and die at launch (macOS: "Library not loaded:
// @rpath/librustc_driver-*.dylib"; Linux: "cannot open shared object file").
//
// Unix gets per-file symlinks into <component>/lib; Windows gets hardlinks of
// rustc/bin/*.dll into <component>/bin, falling back to a copy when the
// volume does not support hardlinks. Idempotent, so re-running on an
// already-installed version is safe. goos is a parameter rather than
// runtime.GOOS so tests can exercise both branches on any host.
func (f *RustFetcher) LinkComponentRuntime(versionDir string, goos string) error {
	if goos == "windows" {
		return linkWindowsRuntimeDLLs(versionDir)
	}
	return linkUnixRuntimeLibs(versionDir, goos)
}

// componentBinDirs returns the names of the subdirectories of versionDir that
// hold a bin/ directory, excluding rustc itself (rustc/lib and rustc/bin
// already contain the runtime, so rustc needs no bridge).
func componentBinDirs(versionDir string) ([]string, error) {
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read version dir: %w", err)
	}
	var dirs []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == "rustc" {
			continue
		}
		info, err := os.Stat(filepath.Join(versionDir, name, "bin"))
		if err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, name)
	}
	return dirs, nil
}

func linkUnixRuntimeLibs(versionDir string, goos string) error {
	rustcLib := filepath.Join(versionDir, "rustc", "lib")
	entries, err := os.ReadDir(rustcLib)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no rustc component installed: nothing to bridge
		}
		return fmt.Errorf("failed to read rustc/lib: %w", err)
	}

	var libs []string
	for _, entry := range entries {
		if !entry.IsDir() && isRustSharedLib(entry.Name(), goos) {
			libs = append(libs, entry.Name())
		}
	}
	if len(libs) == 0 {
		return nil
	}

	comps, err := componentBinDirs(versionDir)
	if err != nil {
		return err
	}
	for _, comp := range comps {
		libDir := filepath.Join(versionDir, comp, "lib")
		if err := os.MkdirAll(libDir, 0755); err != nil {
			return err
		}
		for _, lib := range libs {
			link := filepath.Join(libDir, lib)
			if _, err := os.Lstat(link); err == nil {
				continue
			}
			// Relative target so the version dir stays relocatable.
			target := filepath.Join("..", "..", "rustc", "lib", lib)
			if err := os.Symlink(target, link); err != nil {
				return fmt.Errorf("failed to link %s into %s: %w", lib, libDir, err)
			}
		}
	}
	return nil
}

func linkWindowsRuntimeDLLs(versionDir string) error {
	rustcBin := filepath.Join(versionDir, "rustc", "bin")
	entries, err := os.ReadDir(rustcBin)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no rustc component installed: nothing to bridge
		}
		return fmt.Errorf("failed to read rustc/bin: %w", err)
	}

	var dlls []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			dlls = append(dlls, entry.Name())
		}
	}
	if len(dlls) == 0 {
		return nil
	}

	comps, err := componentBinDirs(versionDir)
	if err != nil {
		return err
	}
	for _, comp := range comps {
		binDir := filepath.Join(versionDir, comp, "bin")
		for _, dll := range dlls {
			dst := filepath.Join(binDir, dll)
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			src := filepath.Join(rustcBin, dll)
			if err := os.Link(src, dst); err != nil {
				if err := fsutil.CopyFile(src, dst, 0644); err != nil {
					return fmt.Errorf("failed to place %s into %s: %w", dll, binDir, err)
				}
			}
		}
	}
	return nil
}

// isRustSharedLib reports whether name is a shared library of the rustc
// runtime. macOS uses .dylib; Linux uses .so and versioned .so.N.
func isRustSharedLib(name string, goos string) bool {
	if goos == "darwin" {
		return strings.HasSuffix(name, ".dylib")
	}
	return strings.HasPrefix(name, "lib") && strings.Contains(name, ".so")
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
