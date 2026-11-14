package installer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"svc/internal/config"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
	"svc/internal/shimmanager"
)

// stubPathManager satisfies pathmgr.PathManager without touching the host PATH,
// shell rc files or the Windows registry. It records the ConfigureSdk calls so
// tests can assert the switch still wired PATH up before the layout heal ran.
type stubPathManager struct {
	configured []string
}

func (m *stubPathManager) ConfigureSdk(sdkType string, versionDir string, binDirs []string, extraEnvVars map[string]string) error {
	m.configured = append(m.configured, sdkType+"@"+versionDir)
	return nil
}

func (m *stubPathManager) RemoveSdk(sdkType string, extraEnvVars map[string]string) error {
	return nil
}

func (m *stubPathManager) GetCurrentConfig() (map[string]string, error) {
	return map[string]string{}, nil
}

func (m *stubPathManager) GetAllPathEntries() ([]pathmgr.PathEntry, error) { return nil, nil }

func (m *stubPathManager) CleanExternalPaths(sdkType string, version string, sourcePath string) {}

func (m *stubPathManager) DetectSystemConflicts(sdkType string, envKeys []string) []string {
	return nil
}

func (m *stubPathManager) SetShimManager(mgr pathmgr.ShimConfigurer) {}

// newHealTestService wires a Service rooted in a temp HOME (never the real
// ~/.svc) with a stub path manager and a real shim manager whose rc file also
// lands in that temp HOME. rt is nil: SwitchVersion emits no events.
func newHealTestService(t *testing.T, pathMgr pathmgr.PathManager) (*Service, *config.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	cfg.SetHomeDir(home)
	svc := New(cfg, sdk.NewRegistry(cfg, nil), nil, pathMgr, shimmanager.New(cfg), nil, nil, nil)
	return svc, cfg
}

// requireSymlinkSupport skips the test when the host cannot create symlinks
// (Windows without the privilege / developer mode); the Unix runtime bridge is
// a symlink. Mirrors the guard in internal/sdk/rust_test.go.
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported on this host: %v", err)
	}
}

// buildBrokenRustVersionDir lays out a Rust version the way an install from
// before the layout fix left it: std still sitting in its own component dir and
// no runtime bridge into rustfmt-preview/, i.e. both `rustc` and `rustfmt` fail
// to start. Returns the name of the runtime file the heal must bridge.
func buildBrokenRustVersionDir(t *testing.T, dir string) string {
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

	stdLib := mkDir("rust-std-x86_64-apple-darwin", "lib", "rustlib", "x86_64-apple-darwin", "lib")
	write(filepath.Join(stdLib, "libstd.rlib"), "std")

	runtimeName := "librustc_driver-19105e2789403a53.dylib"
	if runtime.GOOS == "linux" {
		runtimeName = "librustc_driver-90863c8161c83a53.so"
	}
	runtimeDir := mkDir("rustc", "lib")
	exe := "rustfmt"
	if runtime.GOOS == "windows" {
		runtimeName = "rustc_driver-9c6fabb71b9022ee.dll"
		runtimeDir = mkDir("rustc", "bin")
		exe = "rustfmt.exe"
	}
	write(filepath.Join(runtimeDir, runtimeName), "driver")
	write(filepath.Join(mkDir("rustfmt-preview", "bin"), exe), "rustfmt")
	return runtimeName
}

// rustRuntimeLocation returns where comp expects rustc's runtime file: next to
// the .exe on Windows, in <comp>/lib on Unix (what the rpath names).
func rustRuntimeLocation(dir string, comp string, runtimeName string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, comp, "bin", runtimeName)
	}
	return filepath.Join(dir, comp, "lib", runtimeName)
}

// versionTreeEntries lists every path under dir (relative, slash-separated,
// sorted) so a heal that is supposed to be a no-op can be compared exactly.
// WalkDir does not follow symlinks, so the Unix runtime bridge is listed as the
// link itself.
func versionTreeEntries(t *testing.T, dir string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if rel != "." {
			entries = append(entries, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return entries
}

// TestSwitchVersionSelfHealsRustLayout is the end-to-end contract of the
// post-switch heal: switching to a version whose extracted layout is broken
// activates it AND repairs it in place, so a version installed before the
// layout fix became usable without a reinstall.
func TestSwitchVersionSelfHealsRustLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		requireSymlinkSupport(t)
	}
	pm := &stubPathManager{}
	svc, cfg := newHealTestService(t, pm)

	versionDir := cfg.SdkVersionDir("rust", "1.93.0")
	runtimeName := buildBrokenRustVersionDir(t, versionDir)

	healer, ok := svc.registry.Get(sdk.Rust).(sdk.LayoutHealer)
	if !ok {
		t.Fatal("the rust fetcher must implement sdk.LayoutHealer")
	}
	if problems, err := healer.CheckLayout(versionDir); err != nil || len(problems) == 0 {
		t.Fatalf("fixture should start out broken, got %v, %v", problems, err)
	}

	if err := svc.SwitchVersion("rust", "1.93.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}

	if got := cfg.GetActiveVersion("rust"); got != "1.93.0" {
		t.Errorf("active version = %q; want 1.93.0", got)
	}
	if want := []string{"rust@" + versionDir}; len(pm.configured) != 1 || pm.configured[0] != want[0] {
		t.Errorf("ConfigureSdk calls = %v; want %v", pm.configured, want)
	}

	if problems, err := healer.CheckLayout(versionDir); err != nil || len(problems) != 0 {
		t.Errorf("layout after the switch = %v, %v; want no problems", problems, err)
	}
	bridged := rustRuntimeLocation(versionDir, "rustfmt-preview", runtimeName)
	if _, err := os.Stat(bridged); err != nil {
		t.Errorf("%s must exist after the switch healed the layout: %v", bridged, err)
	}
	for _, comp := range []string{"cargo", "rustc"} {
		p := filepath.Join(versionDir, comp, "lib", "rustlib", "x86_64-apple-darwin", "lib", "libstd.rlib")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must exist after the switch healed the layout: %v", p, err)
		}
	}
}

// TestSwitchVersionHealsHealthyRustLayout covers the common case: every switch
// runs the heal, so a version that is already fine must survive it untouched.
func TestSwitchVersionHealsHealthyRustLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		requireSymlinkSupport(t)
	}
	pm := &stubPathManager{}
	svc, cfg := newHealTestService(t, pm)

	versionDir := cfg.SdkVersionDir("rust", "1.93.0")
	runtimeName := buildBrokenRustVersionDir(t, versionDir)
	fetcher := svc.registry.Get(sdk.Rust)
	if err := fetcher.(*sdk.RustFetcher).MergeComponents(versionDir); err != nil {
		t.Fatalf("MergeComponents: %v", err)
	}
	before := versionTreeEntries(t, versionDir)

	if err := svc.SwitchVersion("rust", "1.93.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}

	if after := versionTreeEntries(t, versionDir); !reflect.DeepEqual(after, before) {
		t.Errorf("healing a healthy version changed the tree:\nbefore %v\nafter  %v", before, after)
	}
	if _, err := os.Stat(rustRuntimeLocation(versionDir, "rustfmt-preview", runtimeName)); err != nil {
		t.Errorf("the runtime bridge disappeared: %v", err)
	}
}

// TestSwitchVersionNonHealerSdk covers fetchers without a layout to heal (Go
// extracts a single tree): the switch must succeed and leave the version dir
// exactly as it was.
func TestSwitchVersionNonHealerSdk(t *testing.T) {
	pm := &stubPathManager{}
	svc, cfg := newHealTestService(t, pm)

	if _, ok := svc.registry.Get(sdk.Golang).(sdk.LayoutHealer); ok {
		t.Fatal("the go fetcher must not implement sdk.LayoutHealer")
	}
	versionDir := cfg.SdkVersionDir("go", "1.25.0")
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := svc.SwitchVersion("go", "1.25.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}
	if got := cfg.GetActiveVersion("go"); got != "1.25.0" {
		t.Errorf("active version = %q; want 1.25.0", got)
	}
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bin" {
		t.Errorf("version dir changed for a non-healer sdk: %v", entries)
	}
}

// stubHealer scripts the sdk.LayoutHealer outcomes so the best-effort wiring in
// healLayout can be asserted without a real SDK archive. It shadows the Ruby
// fetcher, whose layout needs no healing of its own.
type stubHealer struct {
	stubFetcher
	problems          []string
	postRepairProblem []string
	checkErr          error
	repairErr         error
	repaired          bool
	checkCalls        int
	repairCalls       int
}

func (h *stubHealer) Type() sdk.SdkType { return sdk.Ruby }

func (h *stubHealer) CheckLayout(versionDir string) ([]string, error) {
	h.checkCalls++
	if h.checkErr != nil {
		return nil, h.checkErr
	}
	if h.repaired {
		return h.postRepairProblem, nil
	}
	return h.problems, nil
}

func (h *stubHealer) RepairLayout(versionDir string) error {
	h.repairCalls++
	if h.repairErr != nil {
		return h.repairErr
	}
	h.repaired = true
	return nil
}

// TestSwitchVersionHealIsBestEffort pins the rule that the heal can never fail
// a switch: the version is already active and on PATH, so layout problems are
// logged, not returned.
func TestSwitchVersionHealIsBestEffort(t *testing.T) {
	broken := []string{"rustfmt-preview cannot resolve the rustc runtime"}
	tests := []struct {
		name            string
		healer          *stubHealer
		wantCheckCalls  int
		wantRepairCalls int
	}{
		{"healthy layout is left alone", &stubHealer{}, 1, 0},
		{"reported problems are repaired", &stubHealer{problems: broken}, 2, 1},
		{
			"unrepairable problems are logged",
			&stubHealer{problems: broken, postRepairProblem: broken},
			2, 1,
		},
		{"check error skips the repair", &stubHealer{checkErr: errors.New("boom")}, 1, 0},
		{"repair error is swallowed", &stubHealer{problems: broken, repairErr: errors.New("nope")}, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cfg := newHealTestService(t, &stubPathManager{})
			svc.registry.Register(tt.healer)
			versionDir := cfg.SdkVersionDir("ruby", "3.3.0")
			if err := os.MkdirAll(versionDir, 0755); err != nil {
				t.Fatal(err)
			}

			if err := svc.SwitchVersion("ruby", "3.3.0"); err != nil {
				t.Fatalf("SwitchVersion must not fail because of the layout heal: %v", err)
			}
			if got := cfg.GetActiveVersion("ruby"); got != "3.3.0" {
				t.Errorf("active version = %q; want 3.3.0", got)
			}
			if tt.healer.checkCalls != tt.wantCheckCalls {
				t.Errorf("CheckLayout called %d times; want %d", tt.healer.checkCalls, tt.wantCheckCalls)
			}
			if tt.healer.repairCalls != tt.wantRepairCalls {
				t.Errorf("RepairLayout called %d times; want %d", tt.healer.repairCalls, tt.wantRepairCalls)
			}
		})
	}
}
