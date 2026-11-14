package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"svc/internal/config"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
	"svc/internal/shim"
	"svc/internal/shimmanager"
)

// newShimTestService wires a Service with the production path-manager/shim
// wiring (pathmgr delegating to shimmanager, as app.go does) on a temp HOME, so
// a refresh can be asserted end to end in shims.json and in the shims dir. The
// legacy pathmgr fallback writes shell rc files, which is why the shim manager is
// injected here.
func newShimTestService(t *testing.T) (*Service, *config.Config) {
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
	shimMgr := shimmanager.New(cfg)
	pathMgr := pathmgr.NewPathManager(cfg)
	pathMgr.SetShimManager(shimMgr)
	return New(cfg, sdk.NewRegistry(cfg, nil), nil, pathMgr, shimMgr, nil, nil, nil), cfg
}

// installBaseShimBinary puts the base shim binary in place — production does it
// in EnsureSetup — so command shims have something to hardlink to.
func installBaseShimBinary(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := os.MkdirAll(cfg.ShimsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ShimsDir(), baseShimName()), []byte("fake-svc-shim"), 0755); err != nil {
		t.Fatal(err)
	}
}

func baseShimName() string {
	if runtime.GOOS == "windows" {
		return "svc-shim.exe"
	}
	return "svc-shim"
}

// shimFileName returns the on-disk name of a command shim.
func shimFileName(cmd string) string {
	if runtime.GOOS == "windows" {
		return cmd + ".exe"
	}
	return cmd
}

// writeShimsConfig seeds shims.json the way an older build would have left it.
func writeShimsConfig(t *testing.T, cfg *config.Config, sdkTypes map[string]shim.SdkShimEntry, commands map[string]string) {
	t.Helper()
	if err := os.MkdirAll(cfg.SvcDir(), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(shim.ShimConfig{Commands: commands, SdkTypes: sdkTypes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ShimsConfigPath(), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// buildRustBinDirs creates one executable in each of the rust fetcher's bin dirs
// so the shim scan finds a command in every one of them.
func buildRustBinDirs(t *testing.T, versionDir string, binDirs []string) {
	t.Helper()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, binDir := range binDirs {
		p := filepath.Join(versionDir, filepath.FromSlash(binDir))
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		cmd := filepath.Base(binDir) + "-cmd"
		if err := os.WriteFile(filepath.Join(p, cmd+ext), []byte(cmd), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

// staleRustBinDirs are the rust bin dirs recorded by builds that predate the
// clippy / rust-analyzer components: what an existing install actually has in
// shims.json.
var staleRustBinDirs = []string{"cargo/bin", "rustc/bin", "rustfmt-preview/bin", "clippy-preview/bin"}

// TestRefreshStaleShimsEndToEnd reproduces the reported gap: shims.json written
// before rust-analyzer-preview/bin was added to GetBinDirs keeps `rust-analyzer`
// off PATH until a version switch happens to rewrite it. The startup refresh must
// pick the new component up, keep the commands that already worked, and be a
// no-op on a second run.
func TestRefreshStaleShimsEndToEnd(t *testing.T) {
	svc, cfg := newShimTestService(t)
	installBaseShimBinary(t, cfg)

	want := svc.registry.Get(sdk.Rust).GetBinDirs()
	if !slices.Contains(want, "rust-analyzer-preview/bin") || slices.Contains(staleRustBinDirs, "rust-analyzer-preview/bin") {
		t.Fatalf("fixture must be stale relative to GetBinDirs(): stale=%v current=%v", staleRustBinDirs, want)
	}

	versionDir := cfg.SdkVersionDir("rust", "1.93.0")
	buildRustBinDirs(t, versionDir, want)
	if err := cfg.SetActiveVersion("rust", "1.93.0"); err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{}
	for _, binDir := range staleRustBinDirs {
		commands[filepath.Base(binDir)+"-cmd"] = "rust"
	}
	writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{"rust": {BinDirs: staleRustBinDirs}}, commands)

	svc.RefreshStaleShims()

	got := svc.shimMgr.GetShimConfig()
	if !reflect.DeepEqual(got.SdkTypes["rust"].BinDirs, want) {
		t.Errorf("recorded binDirs = %v; want %v", got.SdkTypes["rust"].BinDirs, want)
	}
	// Every bin dir's command is mapped and shimmed, including the new one.
	for _, binDir := range want {
		cmd := filepath.Base(binDir) + "-cmd"
		if got.Commands[cmd] != "rust" {
			t.Errorf("commands[%q] = %q; want rust", cmd, got.Commands[cmd])
		}
		if _, err := os.Stat(filepath.Join(cfg.ShimsDir(), shimFileName(cmd))); err != nil {
			t.Errorf("shim for %s missing: %v", cmd, err)
		}
	}

	// Idempotent: nothing is stale any more, so a second pass changes nothing.
	svc.RefreshStaleShims()
	again := svc.shimMgr.GetShimConfig()
	if !reflect.DeepEqual(again.SdkTypes, got.SdkTypes) || !reflect.DeepEqual(again.Commands, got.Commands) {
		t.Errorf("a second refresh changed shims.json:\n%v\n%v", got, again)
	}
}

func TestRefreshStaleShimsSkips(t *testing.T) {
	rustFetcherBinDirs := (&sdk.RustFetcher{}).GetBinDirs()

	t.Run("already up to date", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"rust": {BinDirs: rustFetcherBinDirs},
		}, nil)
		if err := os.MkdirAll(cfg.SdkVersionDir("rust", "1.93.0"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := cfg.SetActiveVersion("rust", "1.93.0"); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("an up-to-date sdk was reconfigured: %v", pm.configured)
		}
	})

	t.Run("env vars changed", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		// Recorded without GOROOT, which the go fetcher reports today.
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"go": {BinDirs: []string{"go/bin"}},
		}, nil)
		versionDir := cfg.SdkVersionDir("go", "1.25.0")
		if err := os.MkdirAll(versionDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := cfg.SetActiveVersion("go", "1.25.0"); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		want := []string{"go@" + versionDir}
		if !reflect.DeepEqual(pm.configured, want) {
			t.Errorf("ConfigureSdk calls = %v; want %v", pm.configured, want)
		}
	})

	t.Run("no active version", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"rust": {BinDirs: staleRustBinDirs},
		}, nil)

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("ConfigureSdk ran without an active version: %v", pm.configured)
		}
	})

	t.Run("version dir is gone", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"rust": {BinDirs: staleRustBinDirs},
		}, nil)
		if err := cfg.SetActiveVersion("rust", "1.93.0"); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("ConfigureSdk ran for a missing version dir: %v", pm.configured)
		}
	})

	t.Run("base shim binary missing", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		// No installBaseShimBinary: without it createShimFor fails for every
		// command and the config update would record an empty command set.
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"rust": {BinDirs: staleRustBinDirs},
		}, map[string]string{"cargo": "rust"})
		if err := os.MkdirAll(cfg.SdkVersionDir("rust", "1.93.0"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := cfg.SetActiveVersion("rust", "1.93.0"); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("ConfigureSdk ran without the base shim binary: %v", pm.configured)
		}
		if got := svc.shimMgr.GetShimConfig().Commands; len(got) != 1 || got["cargo"] != "rust" {
			t.Errorf("shims.json commands changed = %v; want them left alone", got)
		}
	})

	t.Run("unknown sdk type", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"cobol": {BinDirs: []string{"bin"}},
		}, nil)
		if err := cfg.SetActiveVersion("cobol", "1.0.0"); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("an sdk this build does not know was reconfigured: %v", pm.configured)
		}
	})

	t.Run("invalid recorded identifiers", func(t *testing.T) {
		pm := &stubPathManager{}
		svc, cfg := newHealTestService(t, pm)
		installBaseShimBinary(t, cfg)
		// shims.json and config.json are user-editable: neither the sdk type nor
		// the version may be used as a path segment unchecked.
		writeShimsConfig(t, cfg, map[string]shim.SdkShimEntry{
			"..":   {BinDirs: staleRustBinDirs},
			"rust": {BinDirs: staleRustBinDirs},
		}, nil)
		if err := cfg.SetActiveVersion("rust", ".."); err != nil {
			t.Fatal(err)
		}

		svc.RefreshStaleShims()

		if len(pm.configured) != 0 {
			t.Errorf("ConfigureSdk ran for invalid identifiers: %v", pm.configured)
		}
	})

	t.Run("uninitialized service", func(t *testing.T) {
		New(nil, nil, nil, nil, nil, nil, nil, nil).RefreshStaleShims()
	})
}
