package sdk

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestRustFetcher_VerifyCommand(t *testing.T) {
	f := &RustFetcher{}
	bin, args := f.VerifyCommand()
	if bin != "rustc" {
		t.Errorf("VerifyCommand bin: expected 'rustc', got '%s'", bin)
	}
	want := []string{"--version"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("VerifyCommand args: expected %v, got %v", want, args)
	}
}

func TestRustTarget(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "x86_64-pc-windows-msvc"},
		{"windows", "arm64", "aarch64-pc-windows-msvc"},
		{"linux", "amd64", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "aarch64-unknown-linux-gnu"},
		{"darwin", "amd64", "x86_64-apple-darwin"},
		{"darwin", "arm64", "aarch64-apple-darwin"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"-"+tt.goarch, func(t *testing.T) {
			got := rustTarget(tt.goos, tt.goarch)
			if got != tt.want {
				t.Errorf("rustTarget(%q, %q): expected %q, got %q", tt.goos, tt.goarch, tt.want, got)
			}
		})
	}
}

func TestRustMergeComponents(t *testing.T) {
	dir := t.TempDir()

	// Create per-component layout mimicking the extracted Rust tarball.
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	mkFile := func(dir, name, content string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	cargoBin := mk("cargo", "bin")
	mkFile(cargoBin, "cargo.exe", "fake")

	rustcBin := mk("rustc", "bin")
	mkFile(rustcBin, "rustc.exe", "fake")

	rustfmtBin := mk("rustfmt-preview", "bin")
	mkFile(rustfmtBin, "rustfmt.exe", "fake")

	stdLib := mk("rust-std-x86_64-pc-windows-msvc", "lib", "rustlib", "x86_64-pc-windows-msvc", "lib")
	mkFile(stdLib, "libstd-test.rlib", "std content")
	mkFile(stdLib, "libcore-test.rlib", "core content")

	f := &RustFetcher{}
	if err := f.MergeComponents(dir); err != nil {
		t.Fatalf("MergeComponents failed: %v", err)
	}

	// Verify rustlib was copied to cargo/lib/rustlib and rustc/lib/rustlib.
	for _, comp := range []string{"cargo", "rustc"} {
		dst := filepath.Join(dir, comp, "lib", "rustlib", "x86_64-pc-windows-msvc", "lib", "libstd-test.rlib")
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("expected %s to exist after merge: %v", dst, err)
		}
		coreDst := filepath.Join(dir, comp, "lib", "rustlib", "x86_64-pc-windows-msvc", "lib", "libcore-test.rlib")
		if _, err := os.Stat(coreDst); err != nil {
			t.Errorf("expected %s to exist after merge: %v", coreDst, err)
		}
	}

	// Verify the original rust-std source is still intact.
	srcFile := filepath.Join(dir, "rust-std-x86_64-pc-windows-msvc", "lib", "rustlib", "x86_64-pc-windows-msvc", "lib", "libstd-test.rlib")
	if _, err := os.Stat(srcFile); err != nil {
		t.Errorf("original rust-std source should still exist: %v", err)
	}
}

func TestRustMergeComponentsNoStdDir(t *testing.T) {
	dir := t.TempDir()

	// Create only cargo/ and rustc/, no rust-std-* directory.
	mk := func(parts ...string) {
		p := filepath.Join(append([]string{dir}, parts...)...)
		os.MkdirAll(p, 0755)
	}
	mk("cargo", "bin")
	mk("rustc", "bin")

	f := &RustFetcher{}
	// Should not error when there's no rust-std-* directory.
	if err := f.MergeComponents(dir); err != nil {
		t.Fatalf("MergeComponents should not error without rust-std dir: %v", err)
	}

	// Should not have created lib/rustlib.
	if _, err := os.Stat(filepath.Join(dir, "cargo", "lib", "rustlib")); !os.IsNotExist(err) {
		t.Errorf("cargo/lib/rustlib should not exist when there's no rust-std dir")
	}
}

// --- GetBinDirs: every component bin dir must be registered ---

func TestRustFetcher_GetBinDirs(t *testing.T) {
	f := &RustFetcher{}
	got := f.GetBinDirs()

	want := []string{
		"cargo/bin",
		"rustc/bin",
		"rustfmt-preview/bin",
		"clippy-preview/bin",
		"rust-analyzer-preview/bin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBinDirs() = %v, want %v", got, want)
	}
	if got[0] != "cargo/bin" {
		t.Errorf("cargo/bin must stay first so it wins name conflicts, got %q", got[0])
	}
}

// TestRustFetcher_GetBinDirsCoversClippyAndAnalyzer guards the regression that
// left cargo-clippy / clippy-driver / rust-analyzer without shims: a component
// missing from GetBinDirs is never scanned, so its commands stay off PATH.
func TestRustFetcher_GetBinDirsCoversClippyAndAnalyzer(t *testing.T) {
	dirs := (&RustFetcher{}).GetBinDirs()
	joined := strings.Join(dirs, ",")
	for _, want := range []string{"clippy-preview/bin", "rust-analyzer-preview/bin", "rustfmt-preview/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("GetBinDirs() missing %q: %v", want, dirs)
		}
	}
}

// --- isRustSharedLib: pure filename classification ---

func TestIsRustSharedLib(t *testing.T) {
	tests := []struct {
		name, goos string
		want       bool
	}{
		{"librustc_driver-19105e2789403a53.dylib", "darwin", true},
		{"libserde_derive-52d318a9fd18c0ff.dylib", "darwin", true},
		{"librustc_driver-90863c8161c83a53.so", "linux", true},
		{"libstd-abc.so.1", "linux", true},
		{"librustc_driver-19105e2789403a53.dylib", "linux", false},
		{"librustc_driver-90863c8161c83a53.so", "darwin", false},
		{"rustc_driver-9c6fabb71b9022ee.dll", "windows", false},
		{"rustlib", "darwin", false},
		{"rustc", "linux", false},
		{"libfoo.a", "linux", false},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.name, func(t *testing.T) {
			if got := isRustSharedLib(tt.name, tt.goos); got != tt.want {
				t.Errorf("isRustSharedLib(%q, %q) = %v, want %v", tt.name, tt.goos, got, tt.want)
			}
		})
	}
}

// --- LinkComponentRuntime test scaffolding ---

// requireSymlinkSupport skips the test when the host cannot create symlinks
// (Windows without the privilege / developer mode). Mirrors the guard used by
// TestResolveNodeExternalManagerFollowsSymlinks.
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported on this host: %v", err)
	}
}

// rustSharedLibName returns the runtime library name for the host OS so the
// MergeComponents test can assert on a name the code actually links.
func rustSharedLibName(goos string) string {
	if goos == "darwin" {
		return "librustc_driver-19105e2789403a53.dylib"
	}
	return "librustc_driver-90863c8161c83a53.so"
}

// buildRustVersionDir creates the per-component layout of an extracted Rust
// tarball: rustc/ holds the shared runtime, the tool components hold only
// their binaries (which is what makes them fail to launch unpatched).
func buildRustVersionDir(t *testing.T, dir string, goos string) {
	t.Helper()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	rustcLib := mkDir("rustc", "lib")
	write(filepath.Join(rustcLib, rustSharedLibName(goos)), "driver")
	write(filepath.Join(rustcLib, "libserde_derive-52d318a9.dylib"), "macro")
	write(filepath.Join(rustcLib, "libserde_derive-52d318a9.so"), "macro")
	mkDir("rustc", "lib", "rustlib")
	rustcBin := mkDir("rustc", "bin")
	if err := os.WriteFile(filepath.Join(rustcBin, "rustc"), []byte("rustc"), 0755); err != nil {
		t.Fatalf("write rustc: %v", err)
	}

	for _, comp := range []struct{ dir, bin string }{
		{"cargo", "cargo"},
		{"rustfmt-preview", "rustfmt"},
		{"clippy-preview", "clippy-driver"},
		{"rust-analyzer-preview", "rust-analyzer"},
	} {
		binDir := mkDir(comp.dir, "bin")
		if err := os.WriteFile(filepath.Join(binDir, comp.bin), []byte(comp.bin), 0755); err != nil {
			t.Fatalf("write %s: %v", comp.bin, err)
		}
	}

	// cargo/lib already exists as a real directory (MergeComponents puts
	// rustlib there), so it must be populated in place, not replaced.
	cargoRustlib := mkDir("cargo", "lib", "rustlib", "x86_64-unknown-linux-gnu", "lib")
	write(filepath.Join(cargoRustlib, "libstd.rlib"), "std")

	// Components without a bin/ dir (rust-std, llvm-tools) must be ignored.
	mkDir("rust-std-x86_64-unknown-linux-gnu", "lib")
	mkDir("llvm-tools-preview", "lib")
}

func TestRustLinkComponentRuntime_Unix(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustVersionDir(t, dir, "darwin")

	f := &RustFetcher{}
	if err := f.LinkComponentRuntime(dir, "darwin"); err != nil {
		t.Fatalf("LinkComponentRuntime(darwin) failed: %v", err)
	}

	driver := rustSharedLibName("darwin")
	for _, comp := range []string{"cargo", "rustfmt-preview", "clippy-preview", "rust-analyzer-preview"} {
		link := filepath.Join(dir, comp, "lib", driver)
		target, err := os.Readlink(link)
		if err != nil {
			t.Errorf("%s/lib/%s: expected a symlink: %v", comp, driver, err)
			continue
		}
		if want := filepath.Join("..", "..", "rustc", "lib", driver); target != want {
			t.Errorf("%s/lib/%s target = %q, want %q (relative, keeps the tree relocatable)", comp, driver, target, want)
		}
		if _, err := os.Stat(link); err != nil {
			t.Errorf("%s/lib/%s does not resolve: %v", comp, driver, err)
		}
	}

	// The pre-existing cargo/lib/rustlib tree must survive the bridge.
	if _, err := os.Stat(filepath.Join(dir, "cargo", "lib", "rustlib", "x86_64-unknown-linux-gnu", "lib", "libstd.rlib")); err != nil {
		t.Errorf("cargo/lib/rustlib was clobbered: %v", err)
	}

	// rustc itself needs no bridge: rustc/lib must hold the real file, not a link.
	if target, err := os.Readlink(filepath.Join(dir, "rustc", "lib", driver)); err == nil {
		t.Errorf("rustc/lib/%s should stay a real file, got symlink -> %s", driver, target)
	}

	// Subdirectories (rustlib) are not shared libraries and must not be linked
	// into the freshly created lib dirs.
	for _, comp := range []string{"rustfmt-preview", "clippy-preview", "rust-analyzer-preview"} {
		if _, err := os.Lstat(filepath.Join(dir, comp, "lib", "rustlib")); err == nil {
			t.Errorf("%s/lib/rustlib should not exist", comp)
		}
	}

	// Components without bin/ are left alone.
	if _, err := os.Lstat(filepath.Join(dir, "llvm-tools-preview", "lib", driver)); err == nil {
		t.Error("llvm-tools-preview has no bin/ and must not be bridged")
	}
	if _, err := os.Lstat(filepath.Join(dir, "rust-std-x86_64-unknown-linux-gnu", "lib", driver)); err == nil {
		t.Error("rust-std has no bin/ and must not be bridged")
	}
}

func TestRustLinkComponentRuntime_UnixIdempotent(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustVersionDir(t, dir, "darwin")

	f := &RustFetcher{}
	for i := 0; i < 2; i++ {
		if err := f.LinkComponentRuntime(dir, "darwin"); err != nil {
			t.Fatalf("run %d: LinkComponentRuntime failed: %v", i+1, err)
		}
	}
	link := filepath.Join(dir, "rustfmt-preview", "lib", rustSharedLibName("darwin"))
	if _, err := os.Stat(link); err != nil {
		t.Errorf("after two runs %s should still resolve: %v", link, err)
	}
}

func TestRustLinkComponentRuntime_LinuxUsesSoNames(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustVersionDir(t, dir, "linux")

	f := &RustFetcher{}
	if err := f.LinkComponentRuntime(dir, "linux"); err != nil {
		t.Fatalf("LinkComponentRuntime(linux) failed: %v", err)
	}

	libDir := filepath.Join(dir, "clippy-preview", "lib")
	if _, err := os.Stat(filepath.Join(libDir, rustSharedLibName("linux"))); err != nil {
		t.Errorf("expected the .so runtime to be linked: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(libDir, "libserde_derive-52d318a9.dylib")); err == nil {
		t.Error("a .dylib must not be linked when goos=linux")
	}
	if _, err := os.Lstat(filepath.Join(libDir, "libserde_derive-52d318a9.so")); err != nil {
		t.Errorf("expected the .so macro library to be linked: %v", err)
	}
}

func TestRustLinkComponentRuntime_NoRustcComponent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rustfmt-preview", "bin"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	f := &RustFetcher{}
	if err := f.LinkComponentRuntime(dir, "darwin"); err != nil {
		t.Errorf("missing rustc/lib must be a no-op, got: %v", err)
	}
	if err := f.LinkComponentRuntime(dir, "windows"); err != nil {
		t.Errorf("missing rustc/bin must be a no-op, got: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "rustfmt-preview", "lib")); err == nil {
		t.Error("no lib dir should be created when there is no runtime to bridge")
	}
}

func TestRustLinkComponentRuntime_MissingVersionDir(t *testing.T) {
	f := &RustFetcher{}
	missing := filepath.Join(t.TempDir(), "nope")
	if err := f.LinkComponentRuntime(missing, "darwin"); err != nil {
		t.Errorf("a version dir without rustc/ must not error, got: %v", err)
	}
}

// rustWindowsRuntimeDLLs are the shared runtime files of the Windows Rust
// tarball: the MSVC loader has no rpath, so each component needs them next to
// its own .exe.
var rustWindowsRuntimeDLLs = []string{"rustc_driver-9c6fabb71b9022ee.dll", "std-505ed9e40954533c.dll"}

// buildRustWindowsVersionDir creates the Windows flavour of an extracted Rust
// tarball: rustc/bin holds the runtime DLLs, every other component holds only
// its .exe (which is what makes it fail to launch unpatched).
func buildRustWindowsVersionDir(t *testing.T, dir string) {
	t.Helper()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	rustcBin := mkDir("rustc", "bin")
	for _, name := range rustWindowsRuntimeDLLs {
		write(filepath.Join(rustcBin, name), name)
	}
	// Non-DLL neighbours must not be replicated.
	for _, name := range []string{"rustc.exe", "rustc_driver-9c6fabb71b9022ee.pdb"} {
		write(filepath.Join(rustcBin, name), name)
	}

	for _, comp := range []struct{ dir, bin string }{
		{"cargo", "cargo.exe"},
		{"rustfmt-preview", "rustfmt.exe"},
		{"clippy-preview", "clippy-driver.exe"},
		{"rust-analyzer-preview", "rust-analyzer.exe"},
	} {
		write(filepath.Join(mkDir(comp.dir, "bin"), comp.bin), comp.bin)
	}
}

func TestRustLinkComponentRuntime_Windows(t *testing.T) {
	dir := t.TempDir()
	buildRustWindowsVersionDir(t, dir)

	f := &RustFetcher{}
	if err := f.LinkComponentRuntime(dir, "windows"); err != nil {
		t.Fatalf("LinkComponentRuntime(windows) failed: %v", err)
	}

	for _, comp := range []string{"cargo", "rustfmt-preview", "clippy-preview", "rust-analyzer-preview"} {
		for _, dll := range rustWindowsRuntimeDLLs {
			got, err := os.ReadFile(filepath.Join(dir, comp, "bin", dll))
			if err != nil {
				t.Errorf("%s/bin/%s: expected the runtime DLL next to the exe: %v", comp, dll, err)
				continue
			}
			if string(got) != dll {
				t.Errorf("%s/bin/%s has unexpected content %q", comp, dll, got)
			}
		}
		for _, unwanted := range []string{"rustc.exe", "rustc_driver-9c6fabb71b9022ee.pdb"} {
			if _, err := os.Lstat(filepath.Join(dir, comp, "bin", unwanted)); err == nil {
				t.Errorf("%s/bin/%s should not be replicated", comp, unwanted)
			}
		}
	}

	// Idempotent: a second run must not error or duplicate anything.
	if err := f.LinkComponentRuntime(dir, "windows"); err != nil {
		t.Fatalf("second LinkComponentRuntime(windows) failed: %v", err)
	}
}

func TestRustMergeComponentsBridgesRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("MergeComponents bridges via runtime.GOOS; the Unix symlink path is covered by TestRustLinkComponentRuntime_Unix")
	}
	requireSymlinkSupport(t)
	dir := t.TempDir()

	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}

	stdLib := mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib", "x86_64-apple-darwin", "lib")
	if err := os.WriteFile(filepath.Join(stdLib, "libstd.rlib"), []byte("std"), 0644); err != nil {
		t.Fatalf("write libstd: %v", err)
	}
	rustcLib := mkDir("rustc", "lib")
	driver := rustSharedLibName(runtime.GOOS)
	if err := os.WriteFile(filepath.Join(rustcLib, driver), []byte("driver"), 0644); err != nil {
		t.Fatalf("write driver: %v", err)
	}
	mkDir("rustc", "bin")
	rustfmtBin := mkDir("rustfmt-preview", "bin")
	if err := os.WriteFile(filepath.Join(rustfmtBin, "rustfmt"), []byte("rustfmt"), 0755); err != nil {
		t.Fatalf("write rustfmt: %v", err)
	}
	clippyBin := mkDir("clippy-preview", "bin")
	if err := os.WriteFile(filepath.Join(clippyBin, "clippy-driver"), []byte("clippy"), 0755); err != nil {
		t.Fatalf("write clippy-driver: %v", err)
	}

	f := &RustFetcher{}
	if err := f.MergeComponents(dir); err != nil {
		t.Fatalf("MergeComponents failed: %v", err)
	}

	// Phase 1: std library merged into both sysroots.
	for _, comp := range []string{"cargo", "rustc"} {
		if _, err := os.Stat(filepath.Join(dir, comp, "lib", "rustlib", "x86_64-apple-darwin", "lib", "libstd.rlib")); err != nil {
			t.Errorf("%s/lib/rustlib missing after merge: %v", comp, err)
		}
	}

	// Phase 2: the shared runtime reachable from the tool components.
	for _, comp := range []string{"rustfmt-preview", "clippy-preview"} {
		link := filepath.Join(dir, comp, "lib", driver)
		if _, err := os.Stat(link); err != nil {
			t.Errorf("%s/lib/%s must resolve after MergeComponents: %v", comp, driver, err)
		}
	}
}

// --- LayoutHealer (post-switch self-heal) ---

// SwitchVersion type-asserts sdk.LayoutHealer, so a signature drift here would
// silently disable the self-heal instead of failing to compile.
var _ LayoutHealer = (*RustFetcher)(nil)

// runtimeProblemComps extracts the component names from the "<comp> cannot
// resolve the rustc runtime (...)" problem format. Matching on the prefix
// (rather than a substring search) keeps "rustc" itself out of the results:
// every problem message mentions the rustc runtime.
func runtimeProblemComps(problems []string) []string {
	var comps []string
	for _, p := range problems {
		if comp, _, ok := strings.Cut(p, " cannot resolve the rustc runtime"); ok {
			comps = append(comps, comp)
		}
	}
	sort.Strings(comps)
	return comps
}

// stdProblemComps extracts the component names from the
// "<comp>/lib/rustlib/<target>/lib missing (std not merged)" problem format.
// A component is listed once per target whose std is unreachable.
func stdProblemComps(problems []string) []string {
	var comps []string
	for _, p := range problems {
		rest, _, ok := strings.Cut(p, " missing (std not merged)")
		if !ok {
			continue
		}
		if comp, _, ok := strings.Cut(rest, "/lib/rustlib/"); ok {
			comps = append(comps, comp)
		}
	}
	sort.Strings(comps)
	return comps
}

// rustToolComponents are the components of the fixtures above that ship
// commands, i.e. everything componentBinDirs reports. Sorted, because
// checkLayout walks os.ReadDir order and the helpers sort their result.
var rustToolComponents = []string{"cargo", "clippy-preview", "rust-analyzer-preview", "rustfmt-preview"}

func TestRustCheckLayout_Unix(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustVersionDir(t, dir, "darwin")

	f := &RustFetcher{}
	problems, err := f.checkLayout(dir, "darwin")
	if err != nil {
		t.Fatalf("checkLayout on a fresh extraction failed: %v", err)
	}
	if got := runtimeProblemComps(problems); !reflect.DeepEqual(got, rustToolComponents) {
		t.Errorf("runtime problems for %v; want %v (full: %v)", got, rustToolComponents, problems)
	}
	// The fixture's rust-std-*/lib holds no rustlib/, so nothing to merge.
	if got := stdProblemComps(problems); len(got) != 0 {
		t.Errorf("unexpected std problems %v (full: %v)", got, problems)
	}
	// Checking is read-only: the repair must not have happened as a side effect.
	if _, err := os.Lstat(filepath.Join(dir, "rustfmt-preview", "lib")); err == nil {
		t.Error("checkLayout created rustfmt-preview/lib; the check must not repair")
	}

	if err := f.LinkComponentRuntime(dir, "darwin"); err != nil {
		t.Fatalf("LinkComponentRuntime(darwin) failed: %v", err)
	}
	problems, err = f.checkLayout(dir, "darwin")
	if err != nil {
		t.Fatalf("checkLayout after the bridge failed: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("layout still reported broken after the bridge: %v", problems)
	}
}

func TestRustCheckLayout_LinuxUsesSoNames(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustVersionDir(t, dir, "linux")

	f := &RustFetcher{}
	if problems, err := f.checkLayout(dir, "linux"); err != nil {
		t.Fatalf("checkLayout(linux) failed: %v", err)
	} else if got := runtimeProblemComps(problems); !reflect.DeepEqual(got, rustToolComponents) {
		t.Errorf("runtime problems for %v; want %v", got, rustToolComponents)
	}

	if err := f.LinkComponentRuntime(dir, "linux"); err != nil {
		t.Fatalf("LinkComponentRuntime(linux) failed: %v", err)
	}
	if problems, err := f.checkLayout(dir, "linux"); err != nil || len(problems) != 0 {
		t.Errorf("layout after the bridge = %v, %v; want no problems", problems, err)
	}
}

func TestRustCheckLayout_Windows(t *testing.T) {
	dir := t.TempDir()
	buildRustWindowsVersionDir(t, dir)

	f := &RustFetcher{}
	problems, err := f.checkLayout(dir, "windows")
	if err != nil {
		t.Fatalf("checkLayout(windows) failed: %v", err)
	}
	if got := runtimeProblemComps(problems); !reflect.DeepEqual(got, rustToolComponents) {
		t.Errorf("runtime problems for %v; want %v (full: %v)", got, rustToolComponents, problems)
	}

	if err := f.LinkComponentRuntime(dir, "windows"); err != nil {
		t.Fatalf("LinkComponentRuntime(windows) failed: %v", err)
	}
	if problems, err := f.checkLayout(dir, "windows"); err != nil || len(problems) != 0 {
		t.Errorf("layout after the bridge = %v, %v; want no problems", problems, err)
	}

	// A DLL removed by hand (or by an antivirus) is a problem again.
	if err := os.Remove(filepath.Join(dir, "clippy-preview", "bin", rustWindowsRuntimeDLLs[0])); err != nil {
		t.Fatalf("remove bridged DLL: %v", err)
	}
	problems, err = f.checkLayout(dir, "windows")
	if err != nil {
		t.Fatalf("checkLayout after removing a DLL failed: %v", err)
	}
	if got := runtimeProblemComps(problems); !reflect.DeepEqual(got, []string{"clippy-preview"}) {
		t.Errorf("runtime problems for %v; want [clippy-preview]", got)
	}
}

func TestRustCheckLayout_StdNotMerged(t *testing.T) {
	dir := t.TempDir()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	stdLib := mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib", "x86_64-apple-darwin", "lib")
	if err := os.WriteFile(filepath.Join(stdLib, "libstd.rlib"), []byte("std"), 0644); err != nil {
		t.Fatalf("write libstd: %v", err)
	}
	// No shared runtime in rustc/lib, so only the std merge is under test.
	mkDir("rustc", "bin")
	mkDir("rustfmt-preview", "bin")

	f := &RustFetcher{}
	problems, err := f.checkLayout(dir, "darwin")
	if err != nil {
		t.Fatalf("checkLayout failed: %v", err)
	}
	want := []string{"cargo", "rustc"}
	if got := stdProblemComps(problems); !reflect.DeepEqual(got, want) {
		t.Errorf("std problems for %v; want %v (full: %v)", got, want, problems)
	}
	if got := runtimeProblemComps(problems); len(got) != 0 {
		t.Errorf("unexpected runtime problems %v", got)
	}

	if err := f.MergeComponents(dir); err != nil {
		t.Fatalf("MergeComponents failed: %v", err)
	}
	if problems, err := f.checkLayout(dir, "darwin"); err != nil || len(problems) != 0 {
		t.Errorf("layout after the merge = %v, %v; want no problems", problems, err)
	}
	for _, comp := range want {
		p := filepath.Join(dir, comp, "lib", "rustlib", "x86_64-apple-darwin", "lib", "libstd.rlib")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestRustCheckLayout_NoRuntimeIsClean(t *testing.T) {
	f := &RustFetcher{}

	t.Run("no rustc component", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "rustfmt-preview", "bin"), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for _, goos := range []string{"darwin", "linux", "windows"} {
			problems, err := f.checkLayout(dir, goos)
			if err != nil || len(problems) != 0 {
				t.Errorf("checkLayout(%s) = %v, %v; want no problems", goos, problems, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, "rustfmt-preview", "lib")); err == nil {
			t.Error("checkLayout created a lib dir with nothing to bridge")
		}
	})

	t.Run("missing version dir", func(t *testing.T) {
		// SwitchVersion stats the version dir before healing, so a missing dir
		// reports "nothing to do" rather than an error.
		missing := filepath.Join(t.TempDir(), "nope")
		for _, goos := range []string{"darwin", "windows"} {
			problems, err := f.checkLayout(missing, goos)
			if err != nil || len(problems) != 0 {
				t.Errorf("checkLayout(%s) = %v, %v; want no problems", goos, problems, err)
			}
		}
	})

	t.Run("unreadable runtime dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "rustc"), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// rustc/lib as a regular file: ReadDir fails with ENOTDIR on Unix and
		// with a "not found" error on Windows, which the absent-component
		// short-circuit would otherwise swallow. Either way the check must
		// surface an error instead of claiming a healthy layout it could not
		// inspect.
		if err := os.WriteFile(filepath.Join(dir, "rustc", "lib"), []byte("x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := f.checkLayout(dir, "darwin"); err == nil {
			t.Error("checkLayout must fail when rustc/lib cannot be read")
		}
		// The same guarantee holds for the repair path.
		if err := f.LinkComponentRuntime(dir, "darwin"); err == nil {
			t.Error("LinkComponentRuntime must fail when rustc/lib cannot be read")
		}
	})
}

// TestRustLayoutHealer covers the exported sdk.LayoutHealer pair on the host
// GOOS, which is what SwitchVersion drives: a freshly extracted (i.e. broken)
// version is reported, repaired in place, and then reported clean. RepairLayout
// must also be a no-op on an already healthy version, because SwitchVersion
// heals every switch, not just broken ones.
func TestRustLayoutHealer(t *testing.T) {
	if runtime.GOOS != "windows" {
		requireSymlinkSupport(t)
	}
	dir := t.TempDir()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	write(filepath.Join(mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib", "x86_64-apple-darwin", "lib"), "libstd.rlib"), "std")

	exe := "rustfmt"
	runtimeName := rustSharedLibName(runtime.GOOS)
	runtimeDir := mkDir("rustc", "lib")
	if runtime.GOOS == "windows" {
		exe = "rustfmt.exe"
		runtimeName = rustWindowsRuntimeDLLs[0]
		runtimeDir = mkDir("rustc", "bin")
	}
	write(filepath.Join(runtimeDir, runtimeName), "driver")
	write(filepath.Join(mkDir("rustfmt-preview", "bin"), exe), "rustfmt")

	f := &RustFetcher{}
	problems, err := f.CheckLayout(dir)
	if err != nil {
		t.Fatalf("CheckLayout failed: %v", err)
	}
	if len(stdProblemComps(problems)) == 0 {
		t.Errorf("expected the unmerged std to be reported, got %v", problems)
	}
	if len(runtimeProblemComps(problems)) == 0 {
		t.Errorf("expected the missing runtime bridge to be reported, got %v", problems)
	}

	if err := f.RepairLayout(dir); err != nil {
		t.Fatalf("RepairLayout failed: %v", err)
	}
	if problems, err := f.CheckLayout(dir); err != nil || len(problems) != 0 {
		t.Fatalf("layout after RepairLayout = %v, %v; want no problems", problems, err)
	}

	for _, comp := range []string{"cargo", "rustc"} {
		p := filepath.Join(dir, comp, "lib", "rustlib", "x86_64-apple-darwin", "lib", "libstd.rlib")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bridged := filepath.Join(dir, "rustfmt-preview", "lib", runtimeName)
	if runtime.GOOS == "windows" {
		bridged = filepath.Join(dir, "rustfmt-preview", "bin", runtimeName)
	}
	if _, err := os.Stat(bridged); err != nil {
		t.Errorf("%s must exist after RepairLayout: %v", bridged, err)
	}

	// Idempotent: healing a healthy version changes nothing and stays clean.
	if err := f.RepairLayout(dir); err != nil {
		t.Fatalf("second RepairLayout failed: %v", err)
	}
	if problems, err := f.CheckLayout(dir); err != nil || len(problems) != 0 {
		t.Errorf("layout after a second repair = %v, %v; want no problems", problems, err)
	}
}

// --- std merge granularity ---

// buildRustStdFixture creates the part of an extracted Rust tarball that
// matters for the std merge: the rust-std component holding the rlibs, plus a
// rustc component that already ships its own lib/rustlib/ (etc/ and the
// per-target bin/). That pre-existing directory is what made the old
// whole-directory merge skip rustc entirely, leaving rustc without std.
func buildRustStdFixture(t *testing.T, dir string) {
	t.Helper()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	stdLib := mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib", rustStdTarget, "lib")
	write(filepath.Join(stdLib, "libstd.rlib"), "std")
	// Siblings of {target}/ inside the source rustlib are not targets.
	write(filepath.Join(mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib"), "components"), "rust-std")

	write(filepath.Join(mkDir("rustc", "lib", "rustlib", "etc"), "bash_completions"), "etc")
	write(filepath.Join(mkDir("rustc", "lib", "rustlib", rustStdTarget, "bin"), "rust-lldb"), "lldb")
	mkDir("rustc", "bin")
	mkDir("cargo", "bin")
}

// rustStdTarget is the target triple used by the std fixtures.
const rustStdTarget = "x86_64-apple-darwin"

// TestRustMergeStdRustlibWithPreexistingRustlib is the regression test for the
// E0463 "can't find crate for `std`" failure: rustc ships lib/rustlib/ itself,
// so merging only when that directory is absent never gave rustc its std, and
// every compile of a freshly installed Rust failed. The merge has to happen one
// level down, at {target}/lib.
func TestRustMergeStdRustlibWithPreexistingRustlib(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustStdFixture(t, dir)

	f := &RustFetcher{}
	problems, err := f.checkLayout(dir, "darwin")
	if err != nil {
		t.Fatalf("checkLayout failed: %v", err)
	}
	if got, want := stdProblemComps(problems), []string{"cargo", "rustc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("std problems for %v; want %v (full: %v)", got, want, problems)
	}

	if err := f.mergeComponents(dir, "darwin"); err != nil {
		t.Fatalf("mergeComponents failed: %v", err)
	}

	for _, comp := range stdComponents {
		dst := stdLibDir(dir, comp, rustStdTarget)
		got, err := os.ReadFile(filepath.Join(dst, "libstd.rlib"))
		if err != nil {
			t.Errorf("%s: std must be reachable from the %s sysroot: %v", dst, comp, err)
			continue
		}
		if string(got) != "std" {
			t.Errorf("%s/libstd.rlib = %q; want the std content", dst, got)
		}
		// Unix places a relative symlink, so the 140 MB std tree is not
		// duplicated per sysroot and the version dir stays relocatable.
		target, err := os.Readlink(dst)
		if err != nil {
			t.Errorf("%s: expected a symlink on Unix: %v", dst, err)
			continue
		}
		if filepath.IsAbs(target) {
			t.Errorf("%s: symlink target %q must be relative", dst, target)
		}
		if resolved, err := filepath.EvalSymlinks(dst); err != nil || resolved == "" {
			t.Errorf("%s: symlink does not resolve: %v", dst, err)
		}
	}

	// What rustc already shipped must survive the merge.
	for _, p := range []string{
		filepath.Join(dir, "rustc", "lib", "rustlib", "etc", "bash_completions"),
		filepath.Join(dir, "rustc", "lib", "rustlib", rustStdTarget, "bin", "rust-lldb"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was clobbered by the merge: %v", p, err)
		}
	}
	// The source component is untouched, and non-target entries were ignored.
	if _, err := os.Stat(filepath.Join(dir, "cargo", "lib", "rustlib", "components")); err == nil {
		t.Error("cargo/lib/rustlib/components should not exist: only {target}/lib is merged")
	}

	if problems, err := f.checkLayout(dir, "darwin"); err != nil || len(stdProblemComps(problems)) != 0 {
		t.Errorf("std problems after the merge = %v, %v; want none", problems, err)
	}
}

// TestRustMergeStdRustlibWindowsCopies covers the Windows branch, where creating
// a symlink needs a privilege the app does not have: the std tree is copied
// instead, and rustc's own lib/rustlib/ contents are still preserved.
func TestRustMergeStdRustlibWindowsCopies(t *testing.T) {
	dir := t.TempDir()
	buildRustStdFixture(t, dir)

	f := &RustFetcher{}
	if err := f.mergeComponents(dir, "windows"); err != nil {
		t.Fatalf("mergeComponents(windows) failed: %v", err)
	}

	for _, comp := range stdComponents {
		dst := stdLibDir(dir, comp, rustStdTarget)
		if target, err := os.Readlink(dst); err == nil {
			t.Errorf("%s must be a real directory on Windows, got a symlink -> %s", dst, target)
		}
		got, err := os.ReadFile(filepath.Join(dst, "libstd.rlib"))
		if err != nil {
			t.Errorf("%s: %v", dst, err)
			continue
		}
		if string(got) != "std" {
			t.Errorf("%s/libstd.rlib = %q; want the std content", dst, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "rustc", "lib", "rustlib", rustStdTarget, "bin", "rust-lldb")); err != nil {
		t.Errorf("rustc's own rustlib contents were clobbered: %v", err)
	}
	if problems, err := f.checkLayout(dir, "windows"); err != nil || len(stdProblemComps(problems)) != 0 {
		t.Errorf("std problems after the merge = %v, %v; want none", problems, err)
	}
}

// TestRustMergeStdRustlibIdempotent pins that re-merging (every switch and every
// startup heals) neither replaces nor empties an existing std directory, so a
// real copy made by an older build is kept as-is.
func TestRustMergeStdRustlibIdempotent(t *testing.T) {
	requireSymlinkSupport(t)
	dir := t.TempDir()
	buildRustStdFixture(t, dir)

	// A copy left behind by an older build: a real directory with its own file.
	legacy := stdLibDir(dir, "cargo", rustStdTarget)
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "libstd.rlib"), []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}

	f := &RustFetcher{}
	for i := 0; i < 2; i++ {
		if err := f.mergeComponents(dir, "darwin"); err != nil {
			t.Fatalf("run %d: mergeComponents failed: %v", i+1, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(legacy, "libstd.rlib"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if string(got) != "legacy" {
		t.Errorf("cargo std = %q; an existing std dir must not be replaced", got)
	}
	if _, err := os.Readlink(legacy); err == nil {
		t.Error("cargo std was turned into a symlink; an existing directory must be kept")
	}
	rustcStd := stdLibDir(dir, "rustc", rustStdTarget)
	if _, err := os.Readlink(rustcStd); err != nil {
		t.Errorf("%s should still be a symlink after two runs: %v", rustcStd, err)
	}
	if problems, err := f.checkLayout(dir, "darwin"); err != nil || len(problems) != 0 {
		t.Errorf("problems after two merges = %v, %v; want none", problems, err)
	}
}

// TestRustMergeStdRustlibNoStdComponent guards the no-op path: without a
// rust-std component there is nothing to merge and no rustlib may be created.
func TestRustMergeStdRustlibNoStdComponent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rustc", "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	f := &RustFetcher{}
	for _, goos := range []string{"darwin", "windows"} {
		if err := f.mergeComponents(dir, goos); err != nil {
			t.Errorf("mergeComponents(%s) without rust-std failed: %v", goos, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "rustc", "lib", "rustlib")); err == nil {
		t.Error("rustc/lib/rustlib should not be created without a rust-std component")
	}
}
