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
//  1. rust-std-{target}/lib/rustlib/{target}/lib/ is made reachable from
//     cargo/lib/rustlib/ and rustc/lib/rustlib/ so rustc finds the std library
//     at its sysroot (dirname(dirname(exe)) = component dir root).
//  2. rustc's shared runtime is bridged into the sibling components
//     (LinkComponentRuntime) so rustfmt, clippy-driver and rust-analyzer can
//     load librustc_driver.
//
// The Rust tarball ships per-component directories (cargo/, rustc/,
// rustfmt-preview/, rust-std-{target}/) as siblings. Without this merge,
// rustc at rustc/bin/rustc computes sysroot=rustc/ but std is at
// rust-std-{target}/lib/rustlib/ — a sibling, not under rustc/. Every compile
// then dies with E0463 "can't find crate for `std`".
func (f *RustFetcher) MergeComponents(versionDir string) error {
	return f.mergeComponents(versionDir, runtime.GOOS)
}

// mergeComponents is the goos-parameterised core of MergeComponents so tests can
// assert the Unix (symlink) and Windows (copy) std placement on any host.
func (f *RustFetcher) mergeComponents(versionDir string, goos string) error {
	if _, err := os.ReadDir(versionDir); err != nil {
		return fmt.Errorf("failed to read version dir: %w", err)
	}

	if srcRustlib := findStdRustlib(versionDir); srcRustlib != "" {
		if err := mergeStdRustlib(versionDir, srcRustlib, goos); err != nil {
			return err
		}
	}
	return f.LinkComponentRuntime(versionDir, goos)
}

// CheckLayout reports the layout problems of an installed version that
// RepairLayout would fix; an empty result means the layout matches what a
// fresh install produces. Implements sdk.LayoutHealer so switching to a
// version installed before a layout fix existed can self-heal instead of
// needing a reinstall.
func (f *RustFetcher) CheckLayout(versionDir string) ([]string, error) {
	return f.checkLayout(versionDir, runtime.GOOS)
}

// RepairLayout restores the extracted layout (std merge + runtime bridge) by
// re-running MergeComponents, which is idempotent — healthy versions are left
// untouched.
func (f *RustFetcher) RepairLayout(versionDir string) error {
	return f.MergeComponents(versionDir)
}

// checkLayout is the goos-parameterised core of CheckLayout so tests can
// assert both the Unix (lib symlink) and Windows (DLL next to the exe)
// expectations on any host.
func (f *RustFetcher) checkLayout(versionDir string, goos string) ([]string, error) {
	var problems []string

	// std must be reachable from both sysroots (MergeComponents step 1), at the
	// same {target}/lib granularity the merge uses: rustc ships its own
	// lib/rustlib/, so that directory existing says nothing about std.
	if srcRustlib := findStdRustlib(versionDir); srcRustlib != "" {
		targets, err := stdRustlibTargets(srcRustlib)
		if err != nil {
			return nil, err
		}
		for _, comp := range stdComponents {
			for _, target := range targets {
				if _, err := os.Stat(stdLibDir(versionDir, comp, target)); err != nil {
					problems = append(problems, fmt.Sprintf("%s/lib/rustlib/%s/lib missing (std not merged)", comp, target))
				}
			}
		}
	}

	// rustc's shared runtime must be reachable from every component that ships
	// commands (MergeComponents step 2).
	_, names, err := rustRuntimeFiles(versionDir, goos)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return problems, nil
	}
	comps, err := componentBinDirs(versionDir)
	if err != nil {
		return nil, err
	}
	for _, comp := range comps {
		dstDir := runtimeTargetDir(versionDir, comp, goos)
		for _, name := range names {
			if _, err := os.Lstat(filepath.Join(dstDir, name)); err != nil {
				problems = append(problems, fmt.Sprintf("%s cannot resolve the rustc runtime (%s missing from %s)", comp, name, dstDir))
				break
			}
		}
	}
	return problems, nil
}

// findStdRustlib returns the first rust-std-{target}/lib/rustlib directory in
// versionDir, or "" when the std component is absent. Only the first match is
// used because MergeComponents copies into a fixed destination, so later
// rust-std dirs (extra target stdlibs) would be skipped anyway.
func findStdRustlib(versionDir string) string {
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "rust-std-") {
			continue
		}
		p := filepath.Join(versionDir, entry.Name(), "lib", "rustlib")
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}

// stdComponents are the components whose sysroot must be able to resolve std.
// rustc is the one that matters (it compiles against
// {sysroot}/lib/rustlib/{target}/lib); cargo is merged too so both sysroots
// match rustup's single merged prefix.
var stdComponents = []string{"cargo", "rustc"}

// stdLibDir returns the directory a component's sysroot resolves the std rlibs
// of one target from.
func stdLibDir(versionDir string, comp string, target string) string {
	return filepath.Join(versionDir, comp, "lib", "rustlib", target, "lib")
}

// mergeStdRustlib makes the std library of every target shipped in srcRustlib
// reachable from each sysroot in stdComponents.
//
// The merge is per {target}/lib, NOT per lib/rustlib: the rustc component ships
// its own lib/rustlib/ (holding etc/ and {target}/bin/), so a "skip when
// lib/rustlib already exists" test never merges anything into rustc/ and every
// compile fails with E0463 "can't find crate for `std`". Only the leaf that is
// actually missing is placed, which also leaves a copy made by an older build
// untouched.
func mergeStdRustlib(versionDir string, srcRustlib string, goos string) error {
	targets, err := stdRustlibTargets(srcRustlib)
	if err != nil {
		return err
	}
	for _, comp := range stdComponents {
		for _, target := range targets {
			src := filepath.Join(srcRustlib, target, "lib")
			if err := placeStdLib(src, stdLibDir(versionDir, comp, target), goos); err != nil {
				return err
			}
		}
	}
	return nil
}

// stdRustlibTargets returns the target triples of srcRustlib that actually ship
// a lib/ directory of rlibs, ignoring the installer metadata files and the
// {target}/bin/ helper dir that sit alongside them.
func stdRustlibTargets(srcRustlib string) ([]string, error) {
	entries, err := os.ReadDir(srcRustlib)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", srcRustlib, err)
	}
	var targets []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(srcRustlib, entry.Name(), "lib")); err != nil || !info.IsDir() {
			continue
		}
		targets = append(targets, entry.Name())
	}
	return targets, nil
}

// placeStdLib makes the std rlibs at src reachable at dst: a relative symlink on
// Unix, a copy on Windows where creating a symlink needs a privilege the app
// does not have. Symlinking keeps one ~140 MB std tree per version instead of
// three, is relative so the version dir stays relocatable, and falls back to a
// copy where symlinks are unavailable. An existing dst is left untouched, so
// repeated runs are no-ops.
func placeStdLib(src string, dst string, goos string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if goos != "windows" {
		if rel, err := filepath.Rel(filepath.Dir(dst), src); err == nil {
			if err := os.Symlink(rel, dst); err == nil {
				return nil
			}
		}
	}
	if err := fsutil.CopyDir(src, dst); err != nil {
		return fmt.Errorf("failed to place std lib at %s: %w", dst, err)
	}
	return nil
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
	_, names, err := rustRuntimeFiles(versionDir, goos)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil // no rustc component, or it ships no shared runtime
	}
	comps, err := componentBinDirs(versionDir)
	if err != nil {
		return err
	}
	for _, comp := range comps {
		for _, name := range names {
			if err := placeRuntimeFile(versionDir, comp, name, goos); err != nil {
				return err
			}
		}
	}
	return nil
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

// rustRuntimeFiles returns the directory that holds rustc's shared runtime and
// the file names sibling components need from it: rustc/bin/*.dll on Windows
// (the loader searches the .exe's own directory), rustc/lib/*.{dylib,so} on
// Unix (resolved through the @loader_path/../lib / $ORIGIN/../lib rpath). A
// missing rustc component yields no names and no error; a runtime dir that
// exists but cannot be read is an error, so a corrupt layout is never reported
// as healthy.
func rustRuntimeFiles(versionDir string, goos string) (string, []string, error) {
	dir := filepath.Join(versionDir, "rustc", "lib")
	if goos == "windows" {
		dir = filepath.Join(versionDir, "rustc", "bin")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Windows also reports "not found" when the path exists but is a
			// regular file, so stat it before treating the component as absent
			// — Unix already fails this read with ENOTDIR.
			if info, statErr := os.Lstat(dir); statErr == nil && !info.IsDir() {
				return dir, nil, fmt.Errorf("%s is not a directory", dir)
			}
			return dir, nil, nil
		}
		return dir, nil, fmt.Errorf("failed to read %s: %w", dir, err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if goos == "windows" {
			if strings.EqualFold(filepath.Ext(name), ".dll") {
				names = append(names, name)
			}
			continue
		}
		if isRustSharedLib(name, goos) {
			names = append(names, name)
		}
	}
	return dir, names, nil
}

// runtimeTargetDir returns where comp expects the runtime files to appear:
// next to the .exe on Windows, in <comp>/lib on Unix (what the rpath names).
func runtimeTargetDir(versionDir string, comp string, goos string) string {
	if goos == "windows" {
		return filepath.Join(versionDir, comp, "bin")
	}
	return filepath.Join(versionDir, comp, "lib")
}

// placeRuntimeFile makes one rustc runtime file reachable from comp: a
// relative symlink into <comp>/lib on Unix (relative so the version dir stays
// relocatable, and per-file so a pre-existing real lib dir such as cargo/lib
// keeps its contents), or a hardlink into <comp>/bin on Windows with a copy
// fallback for volumes without hardlink support. Existing entries are left
// untouched, so repeated runs are no-ops.
func placeRuntimeFile(versionDir string, comp string, name string, goos string) error {
	dstDir := runtimeTargetDir(versionDir, comp, goos)
	dst := filepath.Join(dstDir, name)
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}

	if goos == "windows" {
		src := filepath.Join(versionDir, "rustc", "bin", name)
		if err := os.Link(src, dst); err != nil {
			if err := fsutil.CopyFile(src, dst, 0644); err != nil {
				return fmt.Errorf("failed to place %s into %s: %w", name, dstDir, err)
			}
		}
		return nil
	}

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	target := filepath.Join("..", "..", "rustc", "lib", name)
	if err := os.Symlink(target, dst); err != nil {
		return fmt.Errorf("failed to link %s into %s: %w", name, dstDir, err)
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
