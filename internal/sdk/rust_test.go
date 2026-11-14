package sdk

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestRustLinkComponentRuntime_Windows(t *testing.T) {
	dir := t.TempDir()
	mkDir := func(parts ...string) string {
		p := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}

	rustcBin := mkDir("rustc", "bin")
	for _, name := range []string{"rustc_driver-9c6fabb71b9022ee.dll", "std-505ed9e40954533c.dll"} {
		if err := os.WriteFile(filepath.Join(rustcBin, name), []byte(name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Non-DLL neighbours must not be replicated.
	for _, name := range []string{"rustc.exe", "rustc_driver-9c6fabb71b9022ee.pdb"} {
		if err := os.WriteFile(filepath.Join(rustcBin, name), []byte(name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	for _, comp := range []struct{ dir, bin string }{
		{"cargo", "cargo.exe"},
		{"rustfmt-preview", "rustfmt.exe"},
		{"clippy-preview", "clippy-driver.exe"},
		{"rust-analyzer-preview", "rust-analyzer.exe"},
	} {
		binDir := mkDir(comp.dir, "bin")
		if err := os.WriteFile(filepath.Join(binDir, comp.bin), []byte(comp.bin), 0644); err != nil {
			t.Fatalf("write %s: %v", comp.bin, err)
		}
	}

	f := &RustFetcher{}
	if err := f.LinkComponentRuntime(dir, "windows"); err != nil {
		t.Fatalf("LinkComponentRuntime(windows) failed: %v", err)
	}

	for _, comp := range []string{"cargo", "rustfmt-preview", "clippy-preview", "rust-analyzer-preview"} {
		for _, dll := range []string{"rustc_driver-9c6fabb71b9022ee.dll", "std-505ed9e40954533c.dll"} {
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
